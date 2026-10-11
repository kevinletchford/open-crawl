package agents

import "testing"

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range All() {
		if a.Name == "" || a.Token == "" || a.Operator == "" || a.Purpose == "" {
			t.Errorf("%q: name, token, operator and purpose are required", a.Name)
		}
		if seen[a.Token] {
			t.Errorf("%q: duplicate token", a.Token)
		}
		seen[a.Token] = true

		switch a.Kind {
		case KindTokenOnly:
			if a.UserAgent != "" {
				t.Errorf("%q: token-only agents send no requests, so no user agent", a.Name)
			}
			if _, ok := Lookup(a.FetchedBy); !ok {
				t.Errorf("%q: fetched_by %q is not in the registry", a.Name, a.FetchedBy)
			}
		case KindTraining, KindSearchIndex, KindUserFetch:
			if a.UserAgent == "" {
				t.Errorf("%q: crawling agents need a user agent", a.Name)
			}
			if a.UserAgentSource != "documented" && a.UserAgentSource != "observed" {
				t.Errorf("%q: user_agent_source must be documented or observed", a.Name)
			}
		default:
			t.Errorf("%q: unknown kind %q", a.Name, a.Kind)
		}

		for _, s := range []Support{a.RobotsTxt, a.MetaRobots} {
			switch s {
			case Honours, MayIgnore, NotSupported, Unknown:
			default:
				t.Errorf("%q: unknown support value %q", a.Name, s)
			}
		}
	}
}

func TestSelect(t *testing.T) {
	got, err := Select([]string{"gptbot", "Meta-ExternalAgent", "meta-externalagent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "GPTBot" || got[1].Token != "meta-externalagent" {
		t.Fatalf("unexpected selection: %+v", got)
	}
	if _, err := Select([]string{"NoSuchBot"}); err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
	all, _ := Select(nil)
	if len(all) != len(All()) {
		t.Fatal("empty selection should return every agent")
	}
}
