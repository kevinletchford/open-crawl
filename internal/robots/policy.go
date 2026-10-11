package robots

import "fmt"

// Mode is how a robots.txt fetch outcome is interpreted.
type Mode string

const (
	// ModeParsed means robots.txt was fetched and its rules apply.
	ModeParsed Mode = "parsed"
	// ModeAllowAll means robots.txt was "unavailable" (a 4xx): crawlers may
	// fetch everything.
	ModeAllowAll Mode = "allow_all"
	// ModeDisallowAll means robots.txt was "unreachable" (a 5xx, 429 or a
	// network error): crawlers must assume everything is disallowed.
	ModeDisallowAll Mode = "disallow_all"
)

// Policy is a robots.txt fetch outcome, ready to answer Check.
type Policy struct {
	Mode   Mode  `json:"mode"`
	Status int   `json:"status,omitempty"`
	File   *File `json:"file,omitempty"`
	// Error is the network error, if the fetch failed outright.
	Error string `json:"error,omitempty"`
}

// FromResponse applies RFC 9309 section 2.3.1 to a fetch of /robots.txt.
// Pass the final status after redirects (crawlers follow at least five), the
// body, and any transport error.
//
// 429 is treated as unreachable, as Google does, because a rate-limited
// robots.txt says nothing about what the site allows.
func FromResponse(status int, body []byte, err error) Policy {
	switch {
	case err != nil:
		return Policy{Mode: ModeDisallowAll, Error: err.Error()}
	case status >= 200 && status < 300:
		return Policy{Mode: ModeParsed, Status: status, File: Parse(body)}
	case status == 429 || status >= 500:
		return Policy{Mode: ModeDisallowAll, Status: status}
	default:
		// 4xx, and 3xx left over after too many redirects.
		return Policy{Mode: ModeAllowAll, Status: status}
	}
}

// Check decides whether the agent with product token may fetch path.
func (p Policy) Check(token, path string) Verdict {
	switch p.Mode {
	case ModeParsed:
		return p.File.Check(token, path)
	case ModeAllowAll:
		return Verdict{Allowed: true, Reason: fmt.Sprintf("robots.txt returned %d, so everything is allowed", p.Status)}
	default:
		if path == "/robots.txt" {
			return Verdict{Allowed: true, Reason: "/robots.txt is always allowed"}
		}
		if p.Error != "" {
			return Verdict{Allowed: false, Reason: "robots.txt could not be fetched, so everything is disallowed"}
		}
		return Verdict{Allowed: false, Reason: fmt.Sprintf("robots.txt returned %d, so everything is disallowed", p.Status)}
	}
}
