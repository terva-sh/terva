package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestParseClaudeVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2.1.267 (Claude Code)", "2.1.267", true},
		{"2.1.267 (Claude Code)\n", "2.1.267", true},
		{"claude version 3.0.1", "3.0.1", true},
		{"10.20.30", "10.20.30", true},
		{"", "", false},
		{"garbage", "", false},
		{"2.1", "", false},
		{"v2", "", false},
		{"v22.1.0", "", false},                  // no word boundary after the v prefix, so no match
		{"needs 1.2.3 or 4.5.6", "1.2.3", true}, // first triple wins; pick still floors it
	}
	for _, c := range cases {
		got, ok := parseClaudeVersion(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseClaudeVersion(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestVersionTripleNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.1.268", "2.1.267", true},
		{"2.1.267", "2.1.267", false}, // equal is not newer
		{"2.1.266", "2.1.267", false},
		{"2.2.0", "2.1.999", true},
		{"3.0.0", "2.9.9", true},
		{"2.1.67", "2.1.267", false}, // numeric, not lexical
		{"2.10.0", "2.9.0", true},    // same trap on the middle part
	}
	for _, c := range cases {
		if got := versionTripleNewer(c.a, c.b); got != c.want {
			t.Errorf("versionTripleNewer(%q, %q) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPickClaudeCodeVersionFloorsAtBaseline(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"newer install wins", "99.0.0 (Claude Code)", "99.0.0"},
		{"older install floors", "0.1.0 (Claude Code)", claudeCodeVersion},
		{"equal install floors", claudeCodeVersion + " (Claude Code)", claudeCodeVersion},
		{"garbage floors", "command not found", claudeCodeVersion},
		{"empty floors", "", claudeCodeVersion},
	}
	for _, c := range cases {
		if got := pickClaudeCodeVersion(c.out); got != c.want {
			t.Errorf("%s: pickClaudeCodeVersion(%q) = %q; want %q", c.name, c.out, got, c.want)
		}
	}
}

// The OAuth user-agent claims the host's installed version only when it is
// newer than the baseline, and the baseline when the host gives none.
func TestTheOAuthUserAgentClaimsTheInstalledVersionAboveTheFloor(t *testing.T) {
	for _, c := range []struct {
		name      string
		installed func() string
		want      string
	}{
		{"no host version", nil, claudeCodeVersion},
		{"not known yet", func() string { return "" }, claudeCodeVersion},
		{"older install", func() string { return "0.1.0" }, claudeCodeVersion},
		{"newer install", func() string { return "99.0.0" }, "99.0.0"},
	} {
		var opts []ClientOption
		if c.installed != nil {
			opts = append(opts, WithClaudeCodeVersion(c.installed))
		}
		var ua string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua = r.Header.Get("user-agent")
			w.Header().Set("content-type", "text/event-stream")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}))
		ch, err := NewAnthropicOAuthSource(StaticCredential("t"), srv.URL, opts...).Stream(context.Background(), Request{
			Model:    "claude-sonnet-4.5",
			Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		for range ch { //nolint:revive // drain
		}
		srv.Close()
		if ua != "claude-cli/"+c.want {
			t.Errorf("%s: user-agent %q, want claude-cli/%s", c.name, ua, c.want)
		}
	}
}

// The baseline itself must stay a parseable triple, or the floor comparison
// in pickClaudeCodeVersion silently degrades.
func TestBaselineClaudeCodeVersionIsATriple(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(claudeCodeVersion) {
		t.Fatalf("claudeCodeVersion = %q is not a dotted version triple", claudeCodeVersion)
	}
}

// knownClaudeCodeFloors are the lowest Claude Code versions Anthropic accepts
// for a model, where one has been observed. A request that claims less fails
// with http 400 claude_code_version_too_old. Machines without a newer local
// install claim the baseline, so the baseline must meet every floor here.
var knownClaudeCodeFloors = map[string]string{
	"claude-opus-5-5": "2.1.280", // reported 2026-09-23, TKT-01M37S86TR
}

func TestTheBaselineMeetsEveryKnownModelFloor(t *testing.T) {
	for model, floor := range knownClaudeCodeFloors {
		if versionTripleNewer(floor, claudeCodeVersion) {
			t.Errorf("claudeCodeVersion %s is below %s, which %s requires; raise the baseline", claudeCodeVersion, floor, model)
		}
	}
}
