package build

// A harness-driven model swap must be PERSISTED, not just performed — the same
// lesson as image-exclusion recovery next door (wiring_image_exclusion_test.go).
//
// Rung 3 of the stuck-loop hatch swaps a stalled model for a stronger one. The
// swap itself writes only a "meta" row via UpdateModel — byte-identical to a
// user /model switch — so nothing in the log says the change was the harness
// escalating rather than the user choosing. WireHeadlessSessionPersist now
// registers an escalation observer that records the "why" beside that meta row.
//
// This drives a real spinning turn with a bound Escalator and asserts the
// escalation row lands on disk with its provenance. Deleting the registration in
// WireHeadlessSessionPersist makes it fail.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// spinClient dispatches the same failing tool call until the transcript already
// holds stopAfter assistant turns, then answers normally — long enough to cross
// the escalate watermark (stallThreshold + stallEscalateAfterNudge = 5 identical
// results). Counting assistant messages (not raw Stream calls) keeps the index
// stable across retries, which re-send the same transcript.
type spinClient struct{ stopAfter int }

func (c *spinClient) Name() string { return "spin-fake" }

func (c *spinClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	n := 0
	for _, m := range req.Messages {
		if m.Role == provider.RoleAssistant {
			n++
		}
	}
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}}
		if n >= c.stopAfter {
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "done"}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID: fmt.Sprintf("call-%d", n), Name: "spin", Arguments: json.RawMessage(`{"x":1}`),
			}},
		}}
	}()
	return out, nil
}

// spinTool always fails identically, so the churn axis of the detector trips.
type spinTool struct{}

func (spinTool) Name() string            { return "spin" }
func (spinTool) Description() string     { return "always fails identically" }
func (spinTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (spinTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "boom: the same failure again"}},
		IsError: true,
	}, nil
}

// escalatorToTarget swaps (in name only — the client is not really rebuilt here;
// the turn ends on its own) to a fixed strong target.
type escalatorToTarget struct{ target core.EscalationTarget }

func (e escalatorToTarget) Target() (core.EscalationTarget, bool) { return e.target, true }
func (e escalatorToTarget) Escalate(context.Context, core.EscalationRequest) (core.EscalationOutcome, error) {
	return core.EscalationOutcome{Switched: true, ToProvider: e.target.Provider, ToModel: e.target.Model}, nil
}

func TestWiredPersistenceRecordsEscalation(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/ws", "openai-compatible", "gemma-4-26b", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ag := core.NewAgent(&spinClient{stopAfter: 5}, "gemma-4-26b", "system", core.Registry{"spin": spinTool{}})
	ag.MaxSteps = 20
	ag.SetStallDetection(true)
	ag.SetStuckLoopEscalation(true)
	ag.SetEscalateAuto(true) // auto: no Asker to drive in a headless test
	ag.Escalator = escalatorToTarget{target: core.EscalationTarget{Provider: "openai-codex", Model: "gpt-5.6-sol"}}

	WireHeadlessSessionPersist(ag, sess)

	if err := ag.Prompt(context.Background(), "update models.json", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	rows := readEscalationRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("want exactly one escalation row on disk, got %d — the observer was never joined to the session", len(rows))
	}
	r := rows[0]
	if r.Disposition != "switched" {
		t.Errorf("disposition = %q, want switched", r.Disposition)
	}
	if r.FromModel != "gemma-4-26b" || r.ToProvider != "openai-codex" || r.ToModel != "gpt-5.6-sol" {
		t.Errorf("escalation provenance not recorded: %+v", r)
	}
	if !r.Auto {
		t.Error("an auto swap must persist Auto=true")
	}
	if r.Tool != "spin" {
		t.Errorf("tool = %q, want the looping tool", r.Tool)
	}
}

type persistedEscalation struct {
	Disposition string `json:"disposition"`
	Tool        string `json:"tool"`
	FromModel   string `json:"from_model"`
	ToProvider  string `json:"to_provider"`
	ToModel     string `json:"to_model"`
	Auto        bool   `json:"auto"`
}

func readEscalationRows(t *testing.T, path string) []persistedEscalation {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	var out []persistedEscalation
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			Type       string               `json:"type"`
			Escalation *persistedEscalation `json:"escalation"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == "escalation" && row.Escalation != nil {
			out = append(out, *row.Escalation)
		}
	}
	return out
}
