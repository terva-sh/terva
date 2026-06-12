package tools

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func statusText(r core.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

func TestStatusToolReportsStaticAndLiveFacts(t *testing.T) {
	// A bare Agent is enough: the tool only reads Model/Reasoning and
	// the usage snapshots, all safe on a zero-value agent.
	ag := &core.Agent{Model: "claude-sonnet-4-5", Reasoning: "high"}
	ag.SeedLastTurnUsage(provider.Usage{InputTokens: 10000, CacheReadTokens: 2000})
	ag.SeedCost(provider.Usage{InputTokens: 12000, OutputTokens: 3000, CostUSD: 0.05})

	st := &StatusTool{Provider: "anthropic", CWD: "/tmp/proj", AuthMethod: "oauth", Agent: ag}
	res, err := st.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := statusText(res)

	for _, want := range []string{
		"provider: anthropic",
		"model: claude-sonnet-4-5",
		"auth: subscription",
		"reasoning effort: high",
		"cwd: /tmp/proj",
		"% of window", // claude-sonnet-4-5 has a known catalog context window
		"session totals:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status output missing %q\n--- output ---\n%s", want, text)
		}
	}

	d, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details is %T, want map[string]any", res.Details)
	}
	if d["provider"] != "anthropic" || d["model"] != "claude-sonnet-4-5" {
		t.Errorf("Details provider/model = %v/%v", d["provider"], d["model"])
	}
	if cw, _ := d["context_window"].(int); cw <= 0 {
		t.Errorf("context_window not resolved from catalog: %v", d["context_window"])
	}
}

func TestStatusToolNilAgentDegradesGracefully(t *testing.T) {
	st := &StatusTool{Provider: "openai", CWD: "/x"}
	res, err := st.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := statusText(res)
	if !strings.Contains(text, "provider: openai") {
		t.Errorf("missing static provider; got:\n%s", text)
	}
	if !strings.Contains(text, "live usage unavailable") {
		t.Errorf("expected unavailable note when no agent is bound; got:\n%s", text)
	}
}

func TestFmtTokens(t *testing.T) {
	cases := map[int]string{0: "0", 850: "850", 12300: "12.3k", 1_500_000: "1.5M"}
	for in, want := range cases {
		if got := fmtTokens(in); got != want {
			t.Errorf("fmtTokens(%d) = %q, want %q", in, got, want)
		}
	}
}
