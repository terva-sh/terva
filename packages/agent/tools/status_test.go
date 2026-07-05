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

// TestStatusSetProviderRebindsAfterCrossProviderSwap reproduces the web /
// ctrlproto bug: a cross-provider model swap (SetClientAndModel) updates
// the live agent's model but keeps the registry, so terva_status kept
// naming the OLD provider — and because FindModel(oldProvider, newModel)
// misses, the context window read as unknown. SetProvider re-binds the
// identity so both are correct again.
func TestStatusSetProviderRebindsAfterCrossProviderSwap(t *testing.T) {
	// Live agent now runs a real anthropic model (post-swap), but the tool
	// was built for the previous provider ("openai" + oauth) and never
	// rebuilt — the stale state the bug leaves behind.
	ag := &core.Agent{Model: "claude-sonnet-4-5"}
	ag.SeedLastTurnUsage(provider.Usage{InputTokens: 10000})
	st := &StatusTool{Provider: "openai", AuthMethod: "oauth", Agent: ag}

	before := statusText(mustExec(t, st))
	if !strings.Contains(before, "provider: openai") {
		t.Errorf("precondition: expected the stale provider; got:\n%s", before)
	}
	if !strings.Contains(before, "window size unknown") {
		t.Errorf("precondition: stale provider should break the window lookup; got:\n%s", before)
	}

	// The swap re-binds the identity to the model's real provider.
	st.SetProvider("anthropic", "apikey", "")

	after := statusText(mustExec(t, st))
	for _, want := range []string{
		"provider: anthropic",
		"auth: api key",
		"% of window", // window now resolves for anthropic/claude-sonnet-4-5
	} {
		if !strings.Contains(after, want) {
			t.Errorf("after SetProvider, output missing %q\n--- output ---\n%s", want, after)
		}
	}
	if strings.Contains(after, "provider: openai") {
		t.Errorf("SetProvider did not clear the stale provider\n--- output ---\n%s", after)
	}
	if d, ok := mustExec(t, st).Details.(map[string]any); ok && d["provider"] != "anthropic" {
		t.Errorf("Details provider still stale: %v", d["provider"])
	}
}

func mustExec(t *testing.T, st *StatusTool) core.ToolResult {
	t.Helper()
	res, err := st.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

func TestFmtTokens(t *testing.T) {
	cases := map[int]string{0: "0", 850: "850", 12300: "12.3k", 1_500_000: "1.5M"}
	for in, want := range cases {
		if got := fmtTokens(in); got != want {
			t.Errorf("fmtTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestStatusReportsCallingAgentFromContext: several live agents can
// share one StatusTool through a shared registry (bot mode mints an
// agent per chat); the bound Agent field then points at whichever was
// built LAST. The dispatch context must win, so each conversation gets
// its own numbers.
func TestStatusReportsCallingAgentFromContext(t *testing.T) {
	stale := &core.Agent{Model: "stale-model"}
	caller := &core.Agent{Model: "caller-model", Reasoning: "low"}

	st := &StatusTool{Provider: "anthropic", Agent: stale}

	res, err := st.Execute(core.ContextWithAgent(context.Background(), caller), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := statusText(res)
	if !strings.Contains(text, "model: caller-model") {
		t.Errorf("status should report the CALLING agent's model\n--- output ---\n%s", text)
	}
	if strings.Contains(text, "stale-model") {
		t.Errorf("status leaked the stale bound agent's model\n--- output ---\n%s", text)
	}

	// Without a dispatch context the bound field is the fallback.
	res, err = st.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if text := statusText(res); !strings.Contains(text, "model: stale-model") {
		t.Errorf("direct call should fall back to the bound agent\n--- output ---\n%s", text)
	}
}

// TestStatusReportsSessionIdentity: a persisted conversation reports its
// session id (the --resume key) and transcript path; a live-only agent
// says so explicitly instead of staying silent.
func TestStatusReportsSessionIdentity(t *testing.T) {
	dir := t.TempDir()
	sess, err := core.NewSession(dir, dir, "prov", "m", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ag := core.NewAgent(nil, "m", "", nil)
	ag.AdoptSessionIdentity(sess)
	st := &StatusTool{Provider: "anthropic", Agent: ag}

	res, err := st.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := statusText(res)
	wantID, _ := ag.SessionIdentity()
	if !strings.Contains(text, "session: "+wantID) {
		t.Errorf("missing session id line\n--- output ---\n%s", text)
	}
	if !strings.Contains(text, "session file: "+sess.Path) {
		t.Errorf("missing session file line\n--- output ---\n%s", text)
	}
	d, ok := res.Details.(map[string]any)
	if !ok || d["session_id"] != wantID || d["session_path"] != sess.Path {
		t.Errorf("details missing session identity: %#v", res.Details)
	}

	// Live-only agent (bot-mode group chats, --no-session).
	res, err = (&StatusTool{Provider: "anthropic", Agent: core.NewAgent(nil, "m", "", nil)}).Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if text := statusText(res); !strings.Contains(text, "session: none (live-only conversation; not persisted)") {
		t.Errorf("live-only agent should say the conversation is not persisted\n--- output ---\n%s", text)
	}
}

// TestStatusReportsProjectKey: the project id (what project-scoped
// extension state and swarm coordination key on) rides the report.
func TestStatusReportsProjectKey(t *testing.T) {
	st := &StatusTool{Provider: "anthropic", CWD: "/tmp/proj"}
	res, err := st.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "project: " + core.ProjectKey("/tmp/proj")
	if text := statusText(res); !strings.Contains(text, want) {
		t.Errorf("status output missing %q\n--- output ---\n%s", want, text)
	}
	d, ok := res.Details.(map[string]any)
	if !ok || d["project_id"] != core.ProjectKey("/tmp/proj") {
		t.Errorf("details missing project_id: %#v", res.Details)
	}
}
