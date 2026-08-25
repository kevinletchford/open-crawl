package browserrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleJSON(w http.ResponseWriter, r *http.Request) {
	var req JSONRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "invalid request body: "+err.Error()))
		return
	}
	if req.Prompt == "" && req.ResponseFormat == nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "either prompt or response_format is required"))
		return
	}

	bCtx, release, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errResponse(503, err.Error()))
		return
	}

	start := time.Now()

	// Render page to markdown
	content, err := pageToMarkdown(bCtx, req.CommonParams)
	release() // release browser before the (potentially slow) LLM call
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	// Extract structured data with LLM
	result, err := s.llm.Extract(r.Context(), content, req.Prompt, req.ResponseFormat, req.CustomAI)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, "llm error: "+err.Error()))
		return
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/json", req.URL, http.StatusOK, elapsed)

	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	writeJSON(w, http.StatusOK, successResponse(result))
}

// LLMClient handles extraction via local Ollama or a remote provider.
type LLMClient struct {
	cfg AIConfig
	hc  *http.Client
}

func newLLMClient(cfg AIConfig) *LLMClient {
	return &LLMClient{cfg: cfg, hc: &http.Client{Timeout: 120 * time.Second}}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model          string           `json:"model"`
	Messages       []openAIMessage  `json:"messages"`
	ResponseFormat *json.RawMessage `json:"response_format,omitempty"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *LLMClient) Extract(ctx context.Context, content, prompt string, rf *ResponseFormat, customAI []CustomAIConfig) (json.RawMessage, error) {
	var sb strings.Builder
	sb.WriteString("Below is the content of a web page in Markdown format:\n\n")
	if len(content) > 50000 {
		content = content[:50000] + "\n...[truncated]"
	}
	sb.WriteString(content)
	sb.WriteString("\n\n")
	if prompt != "" {
		sb.WriteString("Task: ")
		sb.WriteString(prompt)
	} else {
		sb.WriteString("Extract structured data according to the provided JSON schema.")
	}
	sb.WriteString("\n\nRespond with valid JSON only, no explanation.")
	userMsg := sb.String()

	// Try each custom AI config in order (fallback chain)
	if len(customAI) > 0 {
		var lastErr error
		for _, ai := range customAI {
			result, err := c.callCustomAI(ctx, ai, userMsg, rf)
			if err == nil {
				return result, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}

	// Default provider
	switch c.cfg.DefaultProvider {
	case "openai":
		if c.cfg.OpenAIAPIKey != "" {
			return c.callOpenAICompat(ctx, "https://api.openai.com", "gpt-4o", "Bearer "+c.cfg.OpenAIAPIKey, userMsg, rf)
		}
	case "anthropic":
		if c.cfg.AnthropicAPIKey != "" {
			return c.callAnthropic(ctx, "claude-sonnet-4-6", userMsg, rf)
		}
	}

	// Default: Ollama
	return c.callOllama(ctx, userMsg, rf)
}

// callCustomAI dispatches to the right client based on the "provider/model" format.
func (c *LLMClient) callCustomAI(ctx context.Context, ai CustomAIConfig, userMsg string, rf *ResponseFormat) (json.RawMessage, error) {
	provider, model := parseProviderModel(ai.Model)
	auth := ai.Authorization

	switch provider {
	case "anthropic":
		return c.callAnthropic(ctx, model, userMsg, rf)
	case "workers-ai":
		// Route workers-ai through Ollama-compat base URL if configured,
		// otherwise fall through to OpenAI-compat with no base URL set.
		baseURL := c.cfg.OllamaBaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return c.callOpenAICompat(ctx, baseURL, model, auth, userMsg, rf)
	default:
		// openai, mistral, cohere, etc. — all OpenAI-compatible
		baseURL := openAIBaseURL(provider)
		return c.callOpenAICompat(ctx, baseURL, model, auth, userMsg, rf)
	}
}

// parseProviderModel splits "provider/model" into its parts.
// If no slash is present, provider defaults to "openai".
func parseProviderModel(s string) (provider, model string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 {
		return strings.ToLower(parts[0]), parts[1]
	}
	return "openai", s
}

// openAIBaseURL returns the base URL for known providers.
func openAIBaseURL(provider string) string {
	switch provider {
	case "openai":
		return "https://api.openai.com"
	default:
		// Unknown provider: try as an OpenAI-compatible endpoint using the model name as-is.
		return "https://api.openai.com"
	}
}

func (c *LLMClient) callOllama(ctx context.Context, userMsg string, rf *ResponseFormat) (json.RawMessage, error) {
	baseURL := c.cfg.OllamaBaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return c.callOpenAICompat(ctx, baseURL, c.cfg.OllamaModel, "", userMsg, rf)
}

func (c *LLMClient) callOpenAICompat(ctx context.Context, baseURL, model, auth, userMsg string, rf *ResponseFormat) (json.RawMessage, error) {
	reqBody := openAIRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: "You are a data extraction assistant. Always respond with valid JSON."},
			{Role: "user", Content: userMsg},
		},
	}

	if rf != nil {
		raw := json.RawMessage(fmt.Sprintf(`{"type":%q}`, rf.Type))
		reqBody.ResponseFormat = &raw
	}

	b, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if auth != "" {
		httpReq.Header.Set("Authorization", auth)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("llm decode: %w", err)
	}
	if oaiResp.Error != nil {
		return nil, fmt.Errorf("llm: %s", oaiResp.Error.Message)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("llm: no choices in response")
	}

	raw := oaiResp.Choices[0].Message.Content
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw), nil
	}
	quoted, _ := json.Marshal(raw)
	return quoted, nil
}

type anthropicRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []openAIMessage `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *LLMClient) callAnthropic(ctx context.Context, model, userMsg string, rf *ResponseFormat) (json.RawMessage, error) {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages:  []openAIMessage{{Role: "user", Content: userMsg}},
	}
	b, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.cfg.AnthropicAPIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ar anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	if ar.Error != nil {
		return nil, fmt.Errorf("anthropic: %s", ar.Error.Message)
	}
	if len(ar.Content) == 0 {
		return nil, fmt.Errorf("anthropic: empty response")
	}

	raw := ar.Content[0].Text
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw), nil
	}
	quoted, _ := json.Marshal(raw)
	return quoted, nil
}
