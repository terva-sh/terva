package build

// The detector's nudge must be PERSISTED, not just performed — the same lesson
// as image-exclusion and escalation next door. Rung 1 of the stuck-loop hatch
// nudges a repeating model, but the nudge only rides the ephemeral tail, so
// nothing in the session log says it fired. WireHeadlessSessionPersist now
// registers a stall observer that records it.
//
// This drives the same spinning turn the escalation wiring test uses (spinClient
// / spinTool, defined in wiring_escalation_test.go) and asserts the stall row
// lands. Deleting the registration in WireHeadlessSessionPersist makes it fail.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/stall"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

func TestWiredPersistenceRecordsStall(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := session.NewSessionAtPath(path, "/ws", "openai-compatible", "gemma-4-26b", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Detection on, but NO escalation — a stall row must land on its own, for a
	// plain user who never configured a swap target.
	ag, _ := coretest.NewAgentWithStall(&spinClient{stopAfter: 5}, "gemma-4-26b", "system", core.Registry{"spin": spinTool{}}, core.WithMaxSteps(20))

	WireHeadlessSessionPersist(ag, sess)

	if err := ag.Prompt(context.Background(), "update models.json", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// Two rows: the loop runs past the escalate watermark, and with no escalation
	// target rung 2 speaks in rung 3's place. Both are the detector's own work and
	// both must land, for a plain user who never configured a swap target.
	rows := readStallRows(t, path)
	if len(rows) != 2 {
		t.Fatalf("want a rung-1 and a rung-2 stall row on disk, got %d — the observer was never joined to the session", len(rows))
	}
	// The rung is what makes "nudged and ignored" readable from the log later.
	// Rung 1 omits it, so absent reads as 1.
	if rows[0].Rung != 0 {
		t.Errorf("the first nudge should omit rung (absent means 1), got %d", rows[0].Rung)
	}
	if rows[1].Rung != 2 {
		t.Errorf("the hold-off should record rung 2, got %d", rows[1].Rung)
	}
	r := rows[0]
	// The spin tool repeats identical args AND an identical error; churn is
	// preferred when both fire.
	if r.Axis != "churn" {
		t.Errorf("axis = %q, want churn", r.Axis)
	}
	if r.Tool != "spin" {
		t.Errorf("tool = %q, want the looping tool", r.Tool)
	}
	if !strings.Contains(r.Detail, "boom") {
		t.Errorf("detail = %q, want the repeated error slice", r.Detail)
	}
}

type persistedStall struct {
	Axis   string `json:"axis"`
	Tool   string `json:"tool"`
	Detail string `json:"detail"`
	Rung   int    `json:"rung"`
}

func readStallRows(t *testing.T, path string) []persistedStall {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	var out []persistedStall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			Type  string          `json:"type"`
			Stall *persistedStall `json:"stall"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == "stall" && row.Stall != nil {
			out = append(out, *row.Stall)
		}
	}
	return out
}

// countingGate stands in for the host's ladder: it allows every call and
// counts the calls it was asked about.
type countingGate struct{ asked atomic.Int32 }

func (g *countingGate) CheckTool(context.Context, provider.ToolCallBlock, core.Tool) (bool, string, json.RawMessage) {
	g.asked.Add(1)
	return true, "", nil
}

// capturingClient records the requests it forwards to next.
type capturingClient struct {
	next provider.Client
	mu   sync.Mutex
	reqs []provider.Request
}

func (c *capturingClient) Name() string { return c.next.Name() }

func (c *capturingClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return c.next.Stream(ctx, req)
}

// terva's NewAgent connects the stuck-loop detector in all four places, for
// every host at once: the step gate hears the batches, the assembler carries
// the note and spends it on delivery, and the refusal wraps the host's ladder
// outermost. A wedged model shows each one: both notes ride exactly one
// request each, the refused calls never reach the host's gate, and the turn
// ends.
func TestNewAgentWiresTheStuckLoopDetector(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r.ToolRegistry["spin"] = spinTool{}
	ladder := &countingGate{}
	ag := r.NewAgent(ladder)
	client := &capturingClient{next: &spinClient{stopAfter: 40}}
	ag.SetClientAndModel(client, "", "gemma-4-26b")

	var got []core.StallRecord
	if err := ag.Prompt(context.Background(), "go", nil, func(ev core.AgentEvent) {
		if s, ok := ev.(core.EvStall); ok {
			got = append(got, s.StallRecord)
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	rungs := map[int]int{}
	for _, s := range got {
		rungs[s.Rung]++
	}
	// Seven identical results earn the block, three refusals end the turn
	// (stall.stallRefuseAt and stallRefuseMax, unexported there).
	if rungs[1] != 1 || rungs[2] != 1 || rungs[3] != 3 || rungs[4] != 1 {
		t.Errorf("rungs = %v, want one nudge, one hold-off, three refusals and the end of the turn", rungs)
	}
	if n := ladder.asked.Load(); n != 7 {
		t.Errorf("the host's gate was asked %d times, want 7: a refused call reached it", n)
	}
	notes := map[string]int{}
	for _, req := range client.reqs {
		if strings.Contains(req.EphemeralContext, "[loop check]") {
			notes[req.EphemeralContext]++
		}
	}
	if len(notes) != 2 {
		t.Errorf("%d distinct notes reached the model, want the nudge and the hold-off", len(notes))
	}
	for text, n := range notes {
		if n != 1 {
			t.Errorf("a note rode %d requests, want 1: delivery did not spend it\n%s", n, text)
		}
	}
}

// The host's escalation channel and its auto policy reach the detector, so a
// loop past the nudge swaps without asking.
func TestNewAgentBindsTheEscalator(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r.ToolRegistry["spin"] = spinTool{}
	r.SetEscalator(escalatorToTarget{target: stall.EscalationTarget{Provider: "openai-codex", Model: "gpt-5.6-sol"}})
	r.EscalateAuto = true
	ag := r.NewAgent(core.AllowAll)
	ag.SetClientAndModel(&spinClient{stopAfter: 5}, "", "gemma-4-26b")

	var got []core.EscalationRecord
	if err := ag.Prompt(context.Background(), "go", nil, func(ev core.AgentEvent) {
		if e, ok := ev.(core.EvEscalation); ok {
			got = append(got, e.EscalationRecord)
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(got) != 1 || got[0].Disposition != core.EscalationSwitched || !got[0].Auto {
		t.Errorf("want one automatic switch, got %+v", got)
	}
}
