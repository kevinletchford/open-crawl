// Package agents is the registry of AI crawlers, fetchers and robots.txt
// tokens that an audit checks a site against.
//
// The data lives in agents.json. Every field states what the operator has
// documented, not what we assume: where a fact is undocumented the value is
// "unknown", and user agents we have only seen in the wild are marked
// "observed". Review entries against each docs_url before a release.
package agents

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// Kind says what an agent's fetches are used for.
type Kind string

const (
	// KindTraining crawls to collect model training data.
	KindTraining Kind = "training"
	// KindSearchIndex crawls to build an index that answers are drawn from.
	KindSearchIndex Kind = "search_index"
	// KindUserFetch fetches a page because a user asked about it.
	KindUserFetch Kind = "user_fetch"
	// KindTokenOnly is a robots.txt token with no crawler of its own; another
	// agent (FetchedBy) does the fetching.
	KindTokenOnly Kind = "token_only"
)

// Support is an operator's documented stance on a control.
type Support string

const (
	Honours      Support = "honours"
	MayIgnore    Support = "may_ignore"
	NotSupported Support = "not_supported"
	Unknown      Support = "unknown"
)

// Agent is one entry in the registry.
type Agent struct {
	Name     string `json:"name"`
	Token    string `json:"token"`
	Operator string `json:"operator"`
	Kind     Kind   `json:"kind"`
	// FetchedBy names the agent that fetches on behalf of a token-only entry.
	FetchedBy string `json:"fetched_by,omitempty"`
	// UserAgent is empty for token-only entries.
	UserAgent string `json:"user_agent"`
	// UserAgentSource is "documented" or "observed".
	UserAgentSource string  `json:"user_agent_source,omitempty"`
	RendersJS       bool    `json:"renders_js"`
	RobotsTxt       Support `json:"robots_txt"`
	RobotsTxtNote   string  `json:"robots_txt_note,omitempty"`
	MetaRobots      Support `json:"meta_robots"`
	// Verification is how the operator lets sites verify real traffic: an IP
	// range URL or an rDNS rule. Empty when nothing is published.
	Verification string `json:"verification,omitempty"`
	DocsURL      string `json:"docs_url,omitempty"`
	Purpose      string `json:"purpose"`
}

// Fetches reports whether the agent sends requests of its own.
func (a Agent) Fetches() bool { return a.UserAgent != "" }

//go:embed agents.json
var registryJSON []byte

var registry = mustLoad(registryJSON)

func mustLoad(data []byte) []Agent {
	var list []Agent
	if err := json.Unmarshal(data, &list); err != nil {
		panic(fmt.Sprintf("agents: bad agents.json: %v", err))
	}
	return list
}

// All returns every agent in the registry, in registry order.
func All() []Agent {
	return append([]Agent(nil), registry...)
}

// Select returns the agents named in names, matched case-insensitively
// against Name or Token. An empty list selects every agent.
func Select(names []string) ([]Agent, error) {
	if len(names) == 0 {
		return All(), nil
	}
	var out []Agent
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		a, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown agent %q (run `opencrawl agents` for the list)", name)
		}
		out = append(out, a)
	}
	return out, nil
}

// Lookup finds one agent by Name or Token, ignoring case.
func Lookup(name string) (Agent, bool) {
	for _, a := range registry {
		if strings.EqualFold(a.Name, name) || strings.EqualFold(a.Token, name) {
			return a, true
		}
	}
	return Agent{}, false
}
