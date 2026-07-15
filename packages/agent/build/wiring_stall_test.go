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
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func TestWiredPersistenceRecordsStall(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/ws", "openai-compatible", "gemma-4-26b", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Detection on, but NO escalation — a stall row must land on its own, for a
	// plain user who never configured a swap target.
	ag := core.NewAgent(&spinClient{stopAfter: 5}, "gemma-4-26b", "system", core.Registry{"spin": spinTool{}})
	ag.MaxSteps = 20
	ag.SetStallDetection(true)

	WireHeadlessSessionPersist(ag, sess)

	if err := ag.Prompt(context.Background(), "update models.json", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	rows := readStallRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("want exactly one stall row on disk, got %d — the observer was never joined to the session", len(rows))
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
