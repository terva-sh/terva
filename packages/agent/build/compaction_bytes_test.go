package build

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

var updateCompactionGolden = flag.Bool("update-compaction-golden", false, "rewrite testdata/compaction_requests.golden from the current code")

// summaryClient answers a turn with "ok" and a summarization request with a
// summary, stopping at the length limit when cut is set, so the truncation
// notice is written. It records every request.
type summaryClient struct {
	cut bool

	mu   sync.Mutex
	reqs []provider.Request
}

func (c *summaryClient) Name() string { return "openai" }

func (c *summaryClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	text, stop := "ok", provider.StopEnd
	if len(c.reqs) > 1 {
		text = "## Goal\nthe summary"
		if c.cut {
			stop = provider.StopLength
		}
	}
	out := make(chan provider.Event, 2)
	out <- provider.EventTextDelta{Delta: text}
	out <- provider.EventDone{Stop: stop, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: text}},
	}}
	close(out)
	return out, nil
}

// compactionAgent is a terva agent over a transcript that holds a failed
// write, so the executed-actions ledger has a line to render.
func compactionAgent(t *testing.T, cwd string, client provider.Client) *core.Agent {
	t.Helper()
	args := Args{CWD: cwd, Provider: "openai", Model: "gpt-5"}
	r, err := Resolve(args, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent(core.AllowAll)
	ag.SetClientAndModel(client, ag.Model())
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "write the file"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.ToolCallBlock{ID: "c1", Name: "write", Arguments: json.RawMessage(`{"path":"a.txt","content":"x"}`)}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.ToolResultBlock{CallID: "c1", IsError: true, Content: []provider.Content{provider.TextBlock{Text: "disk full"}}}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "it failed"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "try again later"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "will do"}}},
	})
	return ag
}

func renderRequest(b *strings.Builder, title string, req provider.Request) {
	fmt.Fprintf(b, "===== %s: system =====\n%s\n", title, req.System)
	for i, m := range req.Messages {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				fmt.Fprintf(b, "===== %s: message %d (%s) =====\n%s\n", title, i, m.Role, tb.Text)
			}
		}
	}
	if req.EphemeralContext != "" {
		fmt.Fprintf(b, "===== %s: tail =====\n%s\n", title, req.EphemeralContext)
	}
}

// The compaction text terva sends, pinned byte for byte as it is assembled:
// the cold and the warm summarization requests, the checkpoint a cut summary
// leaves (with its truncation notice and executed-actions ledger), and the
// context-pressure note at every band with automatic compaction on and off.
// Captured before the text moved out of the engine (TKT-01M35WJZS). The i18n
// catalog pins each string by its key; this pins how they are put together.
// Regenerate with -update-compaction-golden only for a change meant to alter it.
func TestTervasCompactionTextIsUnchanged(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	var b strings.Builder

	for _, warm := range []bool{false, true} {
		c := &summaryClient{cut: !warm}
		ag := compactionAgent(t, cwd, c)
		f, _ := EngineFeatureByID("cache_aware_compaction")
		f.Apply(ag, warm)
		if err := ag.Prompt(context.Background(), "and now?", nil, func(core.AgentEvent) {}); err != nil {
			t.Fatal(err)
		}
		if _, err := ag.Compact(context.Background(), 2, nil); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		name := map[bool]string{false: "cold, cut short", true: "warm"}[warm]
		renderRequest(&b, name+" summary request", c.reqs[len(c.reqs)-1])
		for i, m := range ag.Messages() {
			for _, cn := range m.Content {
				if tb, ok := cn.(provider.TextBlock); ok {
					fmt.Fprintf(&b, "===== %s checkpoint, message %d (%s) =====\n%s\n", name, i, m.Role, tb.Text)
				}
			}
		}
	}

	m, err := modelreg.FindModel("openai", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	window := m.EffectiveContextWindow()
	for _, mode := range []string{"steps", "off"} {
		if err := config.MutateConfig(func(c *config.Config) { c.AutoCompact = mode }); err != nil {
			t.Fatal(err)
		}
		for _, frac := range []float64{0.72, 0.80, 0.86, 0.93} {
			c := &summaryClient{}
			ag := compactionAgent(t, cwd, c)
			ag.SeedLastTurnUsage(provider.Usage{InputTokens: int(frac * float64(window))})
			if err := ag.Prompt(context.Background(), "go on", nil, func(core.AgentEvent) {}); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "===== pressure note, auto_compact %s, %.0f%% =====\n%s\n", mode, frac*100, c.reqs[0].EphemeralContext)
		}
	}

	got := b.String()
	got = strings.ReplaceAll(got, home, "<TERVA_HOME>")
	got = strings.ReplaceAll(got, cwd, "<CWD>")
	got = normalizeGoldenPaths(got)
	got = dropTaggedGroups(t, got)
	got = regexp.MustCompile(`Current date: \d{4}-\d{2}-\d{2}`).ReplaceAllString(got, "Current date: <DATE>")
	golden := filepath.Join("testdata", "compaction_requests.golden")
	if *updateCompactionGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("terva's compaction text changed. Diff it against %s; rerun with -update-compaction-golden only if the change is meant:\n%s", golden, got)
	}
}
