package browserrun

import (
	"testing"
)

func TestBuildActions_RequiresURLOrHTML(t *testing.T) {
	_, err := buildActions(CommonParams{})
	if err == nil {
		t.Fatal("expected error when neither url nor html provided")
	}
}

func TestBuildActions_WithURL(t *testing.T) {
	actions, err := buildActions(CommonParams{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions) == 0 {
		t.Fatal("expected at least one action")
	}
}

func TestBuildActions_WithHTML(t *testing.T) {
	actions, err := buildActions(CommonParams{HTML: "<html><body>hi</body></html>"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions) == 0 {
		t.Fatal("expected at least one action")
	}
}

func TestBuildActions_ViewportDefaults(t *testing.T) {
	_, err := buildActions(CommonParams{URL: "https://example.com", Viewport: nil})
	if err != nil {
		t.Fatalf("nil viewport should use defaults: %v", err)
	}
}

func TestBuildActions_InjectStyleAndScript(t *testing.T) {
	jsEnabled := true
	actions, err := buildActions(CommonParams{
		URL:                  "https://example.com",
		SetJavaScriptEnabled: &jsEnabled,
		AddStyleTag:          []StyleTag{{Content: "body{color:red}"}},
		AddScriptTag:         []ScriptTag{{Content: "console.log('ok')"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// viewport + navigate + wait + style + script = at least 5 actions
	if len(actions) < 5 {
		t.Fatalf("expected >=5 actions, got %d", len(actions))
	}
}

func TestBuildActions_UserAgent(t *testing.T) {
	base, _ := buildActions(CommonParams{URL: "https://example.com"})
	withUA, err := buildActions(CommonParams{URL: "https://example.com", UserAgent: "TestBot/1.0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// userAgent adds one extra action
	if len(withUA) <= len(base) {
		t.Errorf("expected more actions with UserAgent, base=%d withUA=%d", len(base), len(withUA))
	}
}

func TestBuildActions_WaitForSelector(t *testing.T) {
	visible := true
	base, _ := buildActions(CommonParams{URL: "https://example.com"})
	withSel, err := buildActions(CommonParams{
		URL: "https://example.com",
		WaitForSelector: &WaitForSelectorParams{
			Selector: "#main",
			Visible:  visible,
			Timeout:  5000,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withSel) <= len(base) {
		t.Errorf("WaitForSelector should add an action: base=%d, withSel=%d", len(base), len(withSel))
	}
}

func TestBuildActions_WaitForTimeout(t *testing.T) {
	ms := 100
	base, _ := buildActions(CommonParams{URL: "https://example.com"})
	withTimeout, err := buildActions(CommonParams{
		URL:            "https://example.com",
		WaitForTimeout: &ms,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withTimeout) <= len(base) {
		t.Errorf("WaitForTimeout should add an action: base=%d, withTimeout=%d", len(base), len(withTimeout))
	}
}

func TestBuildActions_StyleTagURL(t *testing.T) {
	base, _ := buildActions(CommonParams{URL: "https://example.com"})
	withTag, err := buildActions(CommonParams{
		URL:         "https://example.com",
		AddStyleTag: []StyleTag{{URL: "https://cdn.example.com/style.css"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withTag) <= len(base) {
		t.Errorf("URL-based style tag should add an action")
	}
}

func TestBuildActions_ScriptTagURL(t *testing.T) {
	base, _ := buildActions(CommonParams{URL: "https://example.com"})
	withTag, err := buildActions(CommonParams{
		URL:          "https://example.com",
		AddScriptTag: []ScriptTag{{URL: "https://cdn.example.com/script.js"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withTag) <= len(base) {
		t.Errorf("URL-based script tag should add an action")
	}
}

func TestBuildActions_AllowResourceTypes(t *testing.T) {
	base, _ := buildActions(CommonParams{URL: "https://example.com"})
	withAllow, err := buildActions(CommonParams{
		URL:                "https://example.com",
		AllowResourceTypes: []string{"document", "script"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withAllow) <= len(base) {
		t.Errorf("AllowResourceTypes should add fetch-intercept action")
	}
}

// --- cssToInches ---

func TestCSSToInches(t *testing.T) {
	cases := []struct {
		input string
		want  float64
		delta float64
	}{
		{"1in", 1.0, 0.001},
		{"2.54cm", 1.0, 0.001},
		{"25.4mm", 1.0, 0.001},
		{"96px", 1.0, 0.001},
		{"0", 0.0, 0.001},
	}
	for _, c := range cases {
		got := cssToInches(c.input)
		if diff := got - c.want; diff > c.delta || diff < -c.delta {
			t.Errorf("cssToInches(%q) = %.6f, want %.6f (±%.3f)", c.input, got, c.want, c.delta)
		}
	}
}

// --- paperFormatInches ---

func TestPaperFormatInches(t *testing.T) {
	cases := []struct {
		format string
		w, h   float64
	}{
		{"Letter", 8.5, 11},
		{"A4", 8.27, 11.69},
		{"A3", 11.69, 16.54},
		{"A5", 5.83, 8.27},
		{"Legal", 8.5, 14},
		{"Tabloid", 11, 17},
		{"unknown", 8.5, 11},
	}
	for _, c := range cases {
		w, h := paperFormatInches(c.format)
		if w != c.w || h != c.h {
			t.Errorf("paperFormatInches(%q) = (%.2f, %.2f), want (%.2f, %.2f)", c.format, w, h, c.w, c.h)
		}
	}
}

// --- resourceTypePatterns ---

func TestResourceTypePatterns(t *testing.T) {
	pats := resourceTypePatterns([]string{"image", "stylesheet"})
	if len(pats) == 0 {
		t.Fatal("expected patterns for image and stylesheet")
	}
	found := false
	for _, p := range pats {
		if p == "*.css" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected *.css in patterns, got: %v", pats)
	}
}

func TestResourceTypePatterns_UnknownType(t *testing.T) {
	pats := resourceTypePatterns([]string{"notarealtype"})
	if len(pats) != 0 {
		t.Errorf("expected no patterns for unknown type, got: %v", pats)
	}
}

// --- normaliseResourceTypes ---

func TestNormaliseResourceTypes(t *testing.T) {
	got := normaliseResourceTypes([]string{"Image", "SCRIPT", "stylesheet"})
	want := []string{"image", "script", "stylesheet"}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("normaliseResourceTypes[%d] = %q, want %q", i, got[i], w)
		}
	}
}
