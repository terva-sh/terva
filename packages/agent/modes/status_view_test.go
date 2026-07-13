package modes

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// stripANSI flattens themed rows so assertions read the text, not the codes.
func stripANSI(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		inEsc := false
		for _, c := range r {
			switch {
			case c == '\x1b':
				inEsc = true
			case inEsc:
				if c == 'm' {
					inEsc = false
				}
			default:
				b.WriteRune(c)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestStatusRowsFullFacts(t *testing.T) {
	got := stripANSI(statusRows(tui.Theme{Muted: 8, Accent: 4}, statusFacts{
		Version:       "0.120.1 (8f5bd80, 2026-07-12T06:30:32Z)",
		Uptime:        2*time.Hour + 3*time.Minute,
		Provider:      "anthropic",
		Model:         "claude-fable-5",
		Auth:          "subscription (oauth)",
		Reasoning:     "high",
		CWD:           "/work/terva",
		Trusted:       true,
		SessionID:     "sess-42",
		SessPath:      "/home/x/.terva/sessions/sess-42.jsonl",
		ContextTokens: 50_000,
		Window:        200_000,
		Cumulative:    core.WireUsage{Input: 900_000, CacheRead: 300_000, Output: 45_600, CostUSD: 12.3456},
		Windows:       []ctrlproto.UsageWindowInfo{{Label: "5h window", UsedPercent: 34}},
	}))
	for _, want := range []string{
		"v0.120.1 (8f5bd80, 2026-07-12T06:30:32Z)",
		"2h3m0s",
		"anthropic / claude-fable-5",
		"subscription (oauth)",
		"high",
		"/work/terva (trusted)",
		"sess-42",
		"/home/x/.terva/sessions/sess-42.jsonl",
		"50k / 200k tokens (25.0% of window)",
		"1.2M in / 46k out, $12.3456",
		"5h window",
		"34% used",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status block missing %q:\n%s", want, got)
		}
	}
}

func TestStatusRowsDegradesGracefully(t *testing.T) {
	// A zero facts struct (embedder/test: no version stamped, no carrier,
	// no session) must still render header, version-unknown, and uptime —
	// never panic or emit empty labels.
	got := stripANSI(statusRows(tui.Theme{Muted: 8, Accent: 4}, statusFacts{}))
	for _, want := range []string{"terva status", "version", "unknown", "uptime", "none (live-only conversation; not persisted)"} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded status block missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "model") || strings.Contains(got, "context") {
		t.Errorf("degraded status block should omit model/context rows:\n%s", got)
	}
}
