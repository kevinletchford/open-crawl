package logs

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/robots"
)

const (
	gptUA    = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.4; +https://openai.com/gptbot"
	appleUA  = "Mozilla/5.0 (Macintosh) Version/17.4 Safari/605.1.15 (Applebot/0.1; +http://www.apple.com/go/applebot)"
	claudeUA = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)"
)

func TestParseLineFormats(t *testing.T) {
	tests := []struct {
		name, line string
		want       Request
	}{
		{"combined",
			`20.125.66.81 - - [10/Oct/2026:13:55:36 +0000] "GET /docs/a?x=1 HTTP/1.1" 403 512 "-" "` + gptUA + `"`,
			Request{IP: netip.MustParseAddr("20.125.66.81"), Method: "GET", Path: "/docs/a?x=1", Status: 403, UserAgent: gptUA,
				Time: time.Date(2026, 10, 10, 13, 55, 36, 0, time.UTC)}},
		{"common without UA",
			`::ffff:10.0.0.1 - - [10/Oct/2026:13:55:36 +0000] "GET http://example.com/abs HTTP/1.1" 200 1`,
			Request{IP: netip.MustParseAddr("10.0.0.1"), Method: "GET", Path: "/abs", Status: 200,
				Time: time.Date(2026, 10, 10, 13, 55, 36, 0, time.UTC)}},
		{"cloudflare logpush",
			`{"ClientIP":"20.125.66.81","ClientRequestMethod":"GET","ClientRequestURI":"/p?q=1","EdgeResponseStatus":200,"ClientRequestUserAgent":"` + gptUA + `","EdgeStartTimestamp":1791640536000000000}`,
			Request{IP: netip.MustParseAddr("20.125.66.81"), Method: "GET", Path: "/p?q=1", Status: 200, UserAgent: gptUA,
				Time: time.Unix(0, 1791640536000000000).UTC()}},
		{"vercel drain",
			`{"timestamp":1791640536000,"proxy":{"clientIp":"17.0.0.5","method":"GET","path":"/","statusCode":304,"userAgent":["` + appleUA + `"]}}`,
			Request{IP: netip.MustParseAddr("17.0.0.5"), Method: "GET", Path: "/", Status: 304, UserAgent: appleUA,
				Time: time.UnixMilli(1791640536000).UTC()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseLine(tt.line, FormatAuto)
			if !ok {
				t.Fatal("not parsed")
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
	for _, bad := range []string{"", "garbage", `{"msg":"no fields"}`} {
		if _, ok := ParseLine(bad, FormatAuto); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestMatchAgent(t *testing.T) {
	list := agents.All()
	tests := map[string]string{
		gptUA:    "GPTBot",
		claudeUA: "ClaudeBot",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)": "Googlebot",
		"Googlebot-Image/1.0":                     "",
		"meta-externalagent/1.1 (+https://x)":     "Meta-ExternalAgent",
		"Mozilla/5.0 (Macintosh) Safari/605.1.15": "",
		"Claude-User/1.0":                         "Claude-User",
	}
	for ua, want := range tests {
		got, ok := MatchAgent(list, ua)
		if ok != (want != "") || got.Name != want {
			t.Errorf("%q matched %q (ok %v), want %q", ua, got.Name, ok, want)
		}
	}
}

func TestParseRanges(t *testing.T) {
	p, err := ParseRanges([]byte(`{"creationTime":"x","prefixes":[{"ipv4Prefix":"20.125.66.80/28"},{"ipv6Prefix":"2600:1f28:365:8000::/56"},{"ipv4Prefix":"junk"}]}`))
	if err != nil || len(p) != 2 || !p[0].Contains(netip.MustParseAddr("20.125.66.81")) {
		t.Errorf("ranges = %v, %v", p, err)
	}
	if _, err := ParseRanges([]byte(`<html>`)); err == nil {
		t.Error("expected an error for non-JSON")
	}
}

type fakeResolver struct {
	ptr  map[string][]string
	host map[string][]string
}

func (f fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if names, ok := f.ptr[addr]; ok {
		return names, nil
	}
	return nil, &net.DNSError{Err: "not found", Name: addr, IsNotFound: true}
}

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if addrs, ok := f.host[host]; ok {
		return addrs, nil
	}
	return nil, errors.New("no such host")
}

func testVerifier() *Verifier {
	return NewStaticVerifier(
		map[string][]netip.Prefix{"GPTBot": {netip.MustParsePrefix("20.125.66.80/28")}},
		map[string]string{"Applebot": ".applebot.apple.com"},
		fakeResolver{
			ptr: map[string][]string{
				"17.0.0.5": {"17-0-0-5.applebot.apple.com."},
				"6.6.6.6":  {"evil.applebot.apple.com.attacker.net."},
				"7.7.7.7":  {"7.applebot.apple.com."}, // forward lookup won't confirm
			},
			host: map[string][]string{
				"17-0-0-5.applebot.apple.com": {"17.0.0.5"},
				"7.applebot.apple.com":        {"8.8.8.8"},
			},
		})
}

func TestVerify(t *testing.T) {
	v := testVerifier()
	ctx := context.Background()
	tests := []struct {
		agent, ip string
		want      Verdict
	}{
		{"GPTBot", "20.125.66.81", Verified},
		{"GPTBot", "1.2.3.4", Unverified},
		{"Applebot", "17.0.0.5", Verified},
		{"Applebot", "6.6.6.6", Unverified}, // PTR doesn't end in the suffix
		{"Applebot", "7.7.7.7", Unverified}, // forward lookup doesn't confirm
		{"Applebot", "9.9.9.9", Unverified}, // no PTR record
		{"Bytespider", "1.2.3.4", Unknown},  // nothing published
	}
	for _, tt := range tests {
		if got := v.Verify(ctx, tt.agent, netip.MustParseAddr(tt.ip)); got != tt.want {
			t.Errorf("%s from %s = %s, want %s", tt.agent, tt.ip, got, tt.want)
		}
	}
}

func TestAnalyze(t *testing.T) {
	policy := robots.FromResponse(200, []byte("User-agent: GPTBot\nDisallow: /private\n"), nil)
	an := NewAnalyzer(agents.All(), testVerifier(), &policy)
	ctx := context.Background()
	line := func(ip, path string, status int, ua string) string {
		return ip + ` - - [10/Oct/2026:13:55:36 +0000] "GET ` + path + ` HTTP/1.1" ` + strconv.Itoa(status) + ` 10 "-" "` + ua + `"`
	}
	for _, l := range []string{
		line("20.125.66.81", "/", 200, gptUA),
		line("20.125.66.81", "/docs", 403, gptUA),
		line("20.125.66.82", "/docs", 403, gptUA),
		line("20.125.66.81", "/private/x", 200, gptUA),
		line("1.2.3.4", "/", 200, gptUA),        // spoofed
		line("1.2.3.4", "/admin", 403, gptUA),   // spoofed, must not count as refused
		line("5.5.5.5", "/", 200, "Bytespider"), // unverifiable
		line("9.9.9.9", "/", 200, "Mozilla/5.0 regular browser"),
		"not a log line",
	} {
		an.Line(ctx, l, FormatAuto)
	}
	r := an.Report()

	if r.Lines != 9 || r.Parsed != 8 {
		t.Errorf("lines/parsed = %d/%d", r.Lines, r.Parsed)
	}
	var gpt *AgentStats
	for i := range r.Agents {
		if r.Agents[i].Agent == "GPTBot" {
			gpt = &r.Agents[i]
		}
	}
	if gpt == nil {
		t.Fatal("GPTBot missing")
	}
	if gpt.Requests != 6 || gpt.Verified != 4 || gpt.Unverified != 2 || gpt.SpoofIPs != 1 {
		t.Errorf("GPTBot counts = %+v", gpt)
	}
	if gpt.Refused != 2 || gpt.Disallowed != 1 || gpt.Status["2xx"] != 2 || gpt.Status["4xx"] != 2 {
		t.Errorf("GPTBot outcomes = %+v", gpt)
	}

	ids := map[string]bool{}
	for _, f := range r.Findings {
		ids[f.ID] = true
	}
	for _, want := range []string{"logs_refused_gptbot", "logs_spoofed_gptbot", "logs_disallowed_gptbot", "logs_not_seen"} {
		if !ids[want] {
			t.Errorf("missing finding %s; have %v", want, ids)
		}
	}
	for _, f := range r.Findings {
		if f.ID == "logs_refused_gptbot" && !strings.Contains(f.Detail, "verified by IP") {
			t.Errorf("refusal finding should say the requests were verified: %q", f.Detail)
		}
		if f.ID == "logs_not_seen" && slices.Contains(f.Agents, "GPTBot") {
			t.Error("GPTBot was seen")
		}
	}
	for _, s := range r.Agents {
		if s.Agent == "Bytespider" && (s.Unknown != 1 || s.VerificationNotes == "") {
			t.Errorf("Bytespider = %+v", s)
		}
	}
}
