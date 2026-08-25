package browserrun

import "encoding/json"

// CommonParams are shared by every quick-action endpoint.
type CommonParams struct {
	URL  string `json:"url"`
	HTML string `json:"html"`

	GotoOptions         *GotoOptions          `json:"gotoOptions"`
	WaitForSelector     *WaitForSelectorParams `json:"waitForSelector"`
	WaitForTimeout      *int                   `json:"waitForTimeout"` // ms; max 60000
	ActionTimeout       *int                   `json:"actionTimeout"`  // ms; max 300000
	Viewport            *ViewportParams        `json:"viewport"`
	UserAgent           string                 `json:"userAgent"`
	Authenticate        *AuthParams            `json:"authenticate"`
	Cookies             []CookieParam          `json:"cookies"`
	SetExtraHTTPHeaders map[string]string      `json:"setExtraHTTPHeaders"`

	RejectResourceTypes  []string `json:"rejectResourceTypes"`
	RejectRequestPattern []string `json:"rejectRequestPattern"`
	AllowResourceTypes   []string `json:"allowResourceTypes"`
	AllowRequestPattern  []string `json:"allowRequestPattern"`

	AddScriptTag         []ScriptTag `json:"addScriptTag"`
	AddStyleTag          []StyleTag  `json:"addStyleTag"`
	SetJavaScriptEnabled *bool       `json:"setJavaScriptEnabled"`
}

type GotoOptions struct {
	// "load" | "domcontentloaded" | "networkidle0" | "networkidle2"
	WaitUntil string `json:"waitUntil"`
	Timeout   int    `json:"timeout"` // ms
}

type WaitForSelectorParams struct {
	Selector string `json:"selector"`
	Timeout  int    `json:"timeout"` // ms; 0 = 30000
	Visible  bool   `json:"visible"` // wait until visible (not just present)
}

type ViewportParams struct {
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor"`
	IsMobile          bool    `json:"isMobile"`
}

type AuthParams struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type CookieParam struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HttpOnly bool   `json:"httpOnly"`
}

type ScriptTag struct {
	URL     string `json:"url"`
	Content string `json:"content"`
}

type StyleTag struct {
	URL     string `json:"url"`
	Content string `json:"content"`
}

// --- Screenshot ---

type ScreenshotRequest struct {
	CommonParams
	ScreenshotOptions *ScreenshotOptions `json:"screenshotOptions"`
	Selector          string             `json:"selector"`
}

type ScreenshotOptions struct {
	FullPage              bool    `json:"fullPage"`
	OmitBackground        bool    `json:"omitBackground"`
	Quality               *int    `json:"quality"`
	Type                  string  `json:"type"` // "png" | "jpeg"
	Clip                  *Clip   `json:"clip"`
	CaptureBeyondViewport bool    `json:"captureBeyondViewport"`
}

type Clip struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// --- PDF ---

type PDFRequest struct {
	CommonParams
	PDFOptions *PDFOptions `json:"pdfOptions"`
}

type PDFOptions struct {
	Format              string     `json:"format"`
	Margin              *PDFMargin `json:"margin"`
	Scale               float64    `json:"scale"`
	PrintBackground     bool       `json:"printBackground"`
	Landscape           bool       `json:"landscape"`
	PageRanges          string     `json:"pageRanges"`
	PreferCSSPageSize   bool       `json:"preferCSSPageSize"`
	DisplayHeaderFooter bool       `json:"displayHeaderFooter"`
	HeaderTemplate      string     `json:"headerTemplate"`
	FooterTemplate      string     `json:"footerTemplate"`
	Timeout             int        `json:"timeout"` // ms; default 30000, max 300000
}

type PDFMargin struct {
	Top    string `json:"top"`
	Right  string `json:"right"`
	Bottom string `json:"bottom"`
	Left   string `json:"left"`
}

// --- Scrape ---

type ScrapeRequest struct {
	CommonParams
	Elements []ScrapeElement `json:"elements"`
}

type ScrapeElement struct {
	Selector string `json:"selector"`
}

type ScrapeResult struct {
	Selector string         `json:"selector"`
	Results  []ScrapedNode  `json:"results"`
}

type ScrapedNode struct {
	Text       string            `json:"text"`
	HTML       string            `json:"html"`
	Attributes map[string]string `json:"attributes"`
	Width      float64           `json:"width"`
	Height     float64           `json:"height"`
	Top        float64           `json:"top"`
	Left       float64           `json:"left"`
}

// --- Links ---

type LinksRequest struct {
	CommonParams
	VisibleLinksOnly    bool `json:"visibleLinksOnly"`
	ExcludeExternalLinks bool `json:"excludeExternalLinks"`
}

// --- Snapshot ---

type SnapshotRequest struct {
	CommonParams
	ScreenshotOptions *ScreenshotOptions `json:"screenshotOptions"`
	Selector          string             `json:"selector"`
}

type SnapshotResult struct {
	Content    string `json:"content"`
	Screenshot string `json:"screenshot"` // base64
}

// --- JSON / AI ---

type JSONRequest struct {
	CommonParams
	Prompt         string           `json:"prompt"`
	ResponseFormat *ResponseFormat  `json:"response_format"`
	CustomAI       []CustomAIConfig `json:"custom_ai"`
}

type ResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"schema"`
}

type CustomAIConfig struct {
	Model         string `json:"model"`
	Authorization string `json:"authorization"`
}

// JSONOptions is used by the crawl endpoint when "json" is in formats.
type JSONOptions struct {
	Prompt         string           `json:"prompt"`
	ResponseFormat *ResponseFormat  `json:"response_format"`
	CustomAI       []CustomAIConfig `json:"custom_ai"`
}

// --- Crawl ---

type CrawlOptions struct {
	IncludePatterns      []string `json:"includePatterns"`
	ExcludePatterns      []string `json:"excludePatterns"`
	IncludeExternalLinks bool     `json:"includeExternalLinks"`
	IncludeSubdomains    bool     `json:"includeSubdomains"`
}

type CrawlRequest struct {
	URL           string       `json:"url"`
	Limit         int          `json:"limit"`
	Depth         int          `json:"depth"`
	Render        *bool        `json:"render"`
	Formats       []string     `json:"formats"`
	Source        string       `json:"source"` // "all" | "sitemaps" | "links"
	Options       CrawlOptions `json:"options"`
	JSONOptions   *JSONOptions `json:"jsonOptions"`   // required when "json" in formats
	MaxAge        int          `json:"maxAge"`        // cache max age seconds
	ModifiedSince int64        `json:"modifiedSince"` // unix timestamp filter
	// Local-only extensions
	MaxConcurrency int `json:"maxConcurrency"`
	DelayMs        int `json:"delayMs"`
}

// --- API envelope ---

type APIResponse struct {
	Success bool        `json:"success"`
	Result  interface{} `json:"result,omitempty"`
	Errors  []APIError  `json:"errors,omitempty"`
}

type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func successResponse(result interface{}) APIResponse {
	return APIResponse{Success: true, Result: result}
}

func errResponse(code int, msg string) APIResponse {
	return APIResponse{
		Success: false,
		Errors:  []APIError{{Code: code, Message: msg}},
	}
}
