package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/deliverable"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/provider"
)

// DeliverResultTool records a swarm child's structured deliverable — the
// tool half of the structured-deliverable contract (docs/plans/
// jsengine-code-execution-and-workflows.md, workstream B). It is
// registered only in a swarm child whose spawn carried a schema, and the
// SPAWN SCHEMA IS ITS ARGUMENT SCHEMA: the provider enforces the shape at
// call time, this tool re-validates (providers vary in strictness), and a
// mismatch returns as a tool error the model fixes and retries in-turn —
// the retry-at-the-tool-layer mechanism. The validated document lands in
// the agent's state dir, where the supervisor reads and re-validates it
// at task end (swarm.captureDeliverable).
//
// It writes ONLY the swarm state dir under the terva home — never the
// workspace — so like the task tools it classifies read-only and stays
// available in plan mode (a plan-mode reviewer still delivers findings).
type DeliverResultTool struct {
	// ArgSchema is the spawn's deliverable schema, advertised verbatim as
	// this tool's argument schema (top-level object; swarm_spawn enforces
	// that at dispatch).
	ArgSchema json.RawMessage
	// Dir overrides state-dir resolution for tests. Empty derives it from
	// TERVA_SWARM_EVENT_LOG, which the swarm runner exports into every
	// child.
	Dir string
}

func (t *DeliverResultTool) Name() string { return "deliver_result" }
func (t *DeliverResultTool) Description() string {
	return "Record your structured deliverable for the dispatcher that spawned you. Call exactly once, near the end of your task, with your COMPLETE findings as the arguments — they must match this tool's schema. If the call reports a validation error, fix the arguments and call again. This does not end your turn: after a successful call, finish with a short prose summary for humans."
}
func (t *DeliverResultTool) Schema() json.RawMessage { return t.ArgSchema }

func (t *DeliverResultTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	if err := deliverable.Validate(t.ArgSchema, raw); err != nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("deliverable rejected: %v — fix the arguments to match the schema and call deliver_result again", err)}},
			IsError: true,
		}, nil
	}
	dir := t.Dir
	if dir == "" {
		if logPath := envcompat.Get("SWARM_EVENT_LOG"); logPath != "" {
			dir = filepath.Dir(logPath)
		}
	}
	if dir == "" {
		return core.ToolResult{}, fmt.Errorf("deliver_result: no swarm state dir (this tool only works inside a swarm child)")
	}
	path := filepath.Join(dir, deliverable.FileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return core.ToolResult{}, fmt.Errorf("deliver_result: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return core.ToolResult{}, fmt.Errorf("deliver_result: write: %w", err)
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "Deliverable recorded and validated. Finish your turn with a short prose summary for the humans reading the transcript."}},
		Details: map[string]any{"bytes": len(raw)},
	}, nil
}
