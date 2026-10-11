package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/kevinletchford/open-crawl/internal/agents"
	"github.com/kevinletchford/open-crawl/internal/fetch"
)

// Verdict says whether a request really came from the agent its user agent
// names.
type Verdict string

const (
	// Verified: the IP is in the operator's published ranges, or passes the
	// operator's reverse-DNS check.
	Verified Verdict = "verified"
	// Unverified: the operator publishes a way to verify, and this IP fails
	// it. The user agent is probably spoofed.
	Unverified Verdict = "unverified"
	// Unknown: the operator publishes no way to verify, or the published
	// list couldn't be loaded.
	Unknown Verdict = "unknown"
)

// Resolver does the DNS lookups for reverse-DNS verification. net.Resolver
// satisfies it.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Verifier checks request IPs against each agent's published verification.
type Verifier struct {
	// ranges maps agent name to published prefixes. An agent with an entry
	// but no prefixes failed to load.
	ranges map[string][]netip.Prefix
	// rdns maps agent name to the hostname suffix its reverse DNS must end in.
	rdns     map[string]string
	resolver Resolver
	// LoadErrors records lists that couldn't be loaded, by agent.
	LoadErrors map[string]string

	mu    sync.Mutex
	cache map[string]Verdict // agent|ip → verdict
}

// NewVerifier loads the published IP range lists for the given agents.
// Agents whose verification is "rDNS <suffix>" are checked with resolver.
// A list that fails to load leaves its agent's requests Unknown.
func NewVerifier(ctx context.Context, client *fetch.Client, list []agents.Agent, resolver Resolver) *Verifier {
	v := &Verifier{
		ranges: map[string][]netip.Prefix{}, rdns: map[string]string{},
		resolver: resolver, LoadErrors: map[string]string{}, cache: map[string]Verdict{},
	}
	loaded := map[string][]netip.Prefix{} // URL → prefixes, as agents share lists
	failed := map[string]string{}
	for _, a := range list {
		switch {
		case strings.HasPrefix(a.Verification, "rDNS "):
			v.rdns[a.Name] = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(a.Verification, "rDNS ")), "*")
		case strings.HasPrefix(a.Verification, "https://"):
			u := a.Verification
			if _, ok := loaded[u]; !ok && failed[u] == "" {
				res := client.Get(ctx, u, "")
				var err error
				if !res.OK() {
					err = fmt.Errorf("HTTP %d %v", res.Status, res.Err)
				} else {
					loaded[u], err = ParseRanges(res.Body)
				}
				if err != nil {
					failed[u] = err.Error()
				}
			}
			if msg := failed[u]; msg != "" {
				v.LoadErrors[a.Name] = u + ": " + msg
				continue
			}
			v.ranges[a.Name] = loaded[u]
		}
	}
	return v
}

// NewStaticVerifier builds a Verifier from known ranges and rDNS suffixes,
// for tests and offline use.
func NewStaticVerifier(ranges map[string][]netip.Prefix, rdns map[string]string, resolver Resolver) *Verifier {
	if ranges == nil {
		ranges = map[string][]netip.Prefix{}
	}
	if rdns == nil {
		rdns = map[string]string{}
	}
	return &Verifier{ranges: ranges, rdns: rdns, resolver: resolver, LoadErrors: map[string]string{}, cache: map[string]Verdict{}}
}

// ParseRanges reads a published IP range list in the
// {"prefixes":[{"ipv4Prefix":"…"},{"ipv6Prefix":"…"}]} format that Google,
// Bing, OpenAI, Anthropic, Perplexity and Common Crawl all use.
func ParseRanges(data []byte) ([]netip.Prefix, error) {
	var doc struct {
		Prefixes []struct {
			IPv4 string `json:"ipv4Prefix"`
			IPv6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse IP ranges: %w", err)
	}
	var out []netip.Prefix
	for _, p := range doc.Prefixes {
		for _, s := range []string{p.IPv4, p.IPv6} {
			if s == "" {
				continue
			}
			if pfx, err := netip.ParsePrefix(s); err == nil {
				out = append(out, pfx.Masked())
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parse IP ranges: no prefixes found")
	}
	return out, nil
}

// Verify decides whether ip really belongs to the named agent.
func (v *Verifier) Verify(ctx context.Context, agent string, ip netip.Addr) Verdict {
	key := agent + "|" + ip.String()
	v.mu.Lock()
	if verdict, ok := v.cache[key]; ok {
		v.mu.Unlock()
		return verdict
	}
	v.mu.Unlock()

	verdict := Unknown
	if prefixes, ok := v.ranges[agent]; ok {
		verdict = Unverified
		for _, p := range prefixes {
			if p.Contains(ip) {
				verdict = Verified
				break
			}
		}
	} else if suffix, ok := v.rdns[agent]; ok && v.resolver != nil {
		verdict = v.verifyRDNS(ctx, ip, suffix)
	}

	v.mu.Lock()
	v.cache[key] = verdict
	v.mu.Unlock()
	return verdict
}

// verifyRDNS is forward-confirmed reverse DNS: the IP's PTR name must end in
// suffix and resolve back to the same IP.
func (v *Verifier) verifyRDNS(ctx context.Context, ip netip.Addr, suffix string) Verdict {
	names, err := v.resolver.LookupAddr(ctx, ip.String())
	if err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			return Unverified
		}
		return Unknown
	}
	for _, name := range names {
		name = strings.TrimSuffix(name, ".")
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		addrs, err := v.resolver.LookupHost(ctx, name)
		if err != nil {
			return Unknown
		}
		for _, a := range addrs {
			if parsed, err := netip.ParseAddr(a); err == nil && parsed.Unmap() == ip {
				return Verified
			}
		}
	}
	return Unverified
}
