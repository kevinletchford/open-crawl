// Package logs reads web server and CDN access logs and reports what the
// real AI agents requested and what they were served.
//
// This is the ground truth an outside crawl can't get: a probe with GPTBot's
// user agent shows how the site treats that string, but only the logs show
// how it treated GPTBot. Requests are verified against each operator's
// published IP ranges (or reverse DNS), so a spoofed user agent isn't
// mistaken for the real agent.
package logs

import (
	"encoding/json"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Request is one log line, reduced to what the report needs.
type Request struct {
	Time      time.Time
	IP        netip.Addr
	Method    string
	Path      string // path and query
	Status    int
	UserAgent string
}

// Format names a log format.
type Format string

const (
	FormatAuto     Format = "auto"
	FormatCombined Format = "combined" // nginx/Apache combined (and common + UA)
	FormatJSON     Format = "json"     // one JSON object per line: Cloudflare Logpush, Vercel, generic
)

// combinedRE matches the combined log format:
// 203.0.113.9 - - [10/Oct/2026:13:55:36 +0000] "GET /a?b=1 HTTP/1.1" 200 2326 "ref" "UA"
var combinedRE = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "(\S+) (\S+)[^"]*" (\d{3}) \S+(?: "[^"]*" "([^"]*)")?`)

// ParseLine parses one line. It returns ok=false for blank lines and lines
// it doesn't recognise.
func ParseLine(line string, f Format) (Request, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Request{}, false
	}
	if f == FormatAuto {
		f = FormatCombined
		if line[0] == '{' {
			f = FormatJSON
		}
	}
	if f == FormatJSON {
		return parseJSON(line)
	}
	return parseCombined(line)
}

func parseCombined(line string) (Request, bool) {
	m := combinedRE.FindStringSubmatch(line)
	if m == nil {
		return Request{}, false
	}
	ip, err := netip.ParseAddr(m[1])
	if err != nil {
		return Request{}, false
	}
	status, _ := strconv.Atoi(m[5])
	t, _ := time.Parse("02/Jan/2006:15:04:05 -0700", m[2])
	return Request{Time: t.UTC(), IP: ip.Unmap(), Method: m[3], Path: pathOf(m[4]), Status: status, UserAgent: m[6]}, true
}

// jsonFields lists, for each value, the field names used by common log
// sources: Cloudflare Logpush first, then Vercel log drains (nested under
// "proxy"), then generic names.
var jsonFields = map[string][]string{
	"ip":     {"ClientIP", "proxy.clientIp", "client_ip", "remote_addr", "ip"},
	"path":   {"ClientRequestURI", "ClientRequestPath", "proxy.path", "path", "request_uri", "url"},
	"method": {"ClientRequestMethod", "proxy.method", "method", "request_method"},
	"status": {"EdgeResponseStatus", "OriginResponseStatus", "proxy.statusCode", "status", "status_code"},
	"ua":     {"ClientRequestUserAgent", "proxy.userAgent", "user_agent", "http_user_agent", "userAgent"},
	"time":   {"EdgeStartTimestamp", "timestamp", "proxy.timestamp", "time", "@timestamp"},
}

func parseJSON(line string) (Request, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return Request{}, false
	}
	get := func(key string) any {
		for _, name := range jsonFields[key] {
			if v, ok := lookup(obj, name); ok && v != nil {
				return v
			}
		}
		return nil
	}

	var r Request
	ipStr, _ := get("ip").(string)
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return Request{}, false
	}
	r.IP = ip.Unmap()
	r.Path = pathOf(asString(get("path")))
	r.Method = asString(get("method"))
	r.Status = asInt(get("status"))
	switch ua := get("ua").(type) {
	case string:
		r.UserAgent = ua
	case []any: // Vercel sends the user agent as a list
		if len(ua) > 0 {
			r.UserAgent = asString(ua[0])
		}
	}
	r.Time = asTime(get("time"))
	if r.Path == "" || r.Status == 0 {
		return Request{}, false
	}
	return r, true
}

// lookup finds a dotted key such as "proxy.path" in nested objects.
func lookup(obj map[string]any, dotted string) (any, bool) {
	cur := any(obj)
	for _, part := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

// asTime accepts RFC 3339 strings and Unix times in seconds, milliseconds or
// nanoseconds (Cloudflare's default is nanoseconds).
func asTime(v any) time.Time {
	switch x := v.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t.UTC()
		}
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return unixGuess(n)
		}
	case float64:
		return unixGuess(int64(x))
	}
	return time.Time{}
}

func unixGuess(n int64) time.Time {
	switch {
	case n > 1e17:
		return time.Unix(0, n).UTC()
	case n > 1e11:
		return time.UnixMilli(n).UTC()
	default:
		return time.Unix(n, 0).UTC()
	}
}

// pathOf reduces a request target, which may be an absolute URL, to its
// path and query.
func pathOf(target string) string {
	if strings.HasPrefix(target, "/") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil || u.Path == "" {
		return target
	}
	if u.RawQuery != "" {
		return u.EscapedPath() + "?" + u.RawQuery
	}
	return u.EscapedPath()
}
