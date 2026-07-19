package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestSwarmSpawnBackendGate covers the `backend` param end to end: the tool
// hands the requested backend to the injected AllowBackend gate, and only a name
// the gate accepts reaches the SpawnRequest. A rejected or unwired backend never
// spawns — the model gets a tool error instead. This is the enforcement point
// for "a new foreign worker only spawns when external workers are enabled";
// NewRunner downstream is deliberately unconditional (it must revive persisted
// workers), so if this gate leaked, nothing else would stop it.
func TestSwarmSpawnBackendGate(t *testing.T) {
	// build a tool whose swarm records the Backend that reached the spawn.
	newTool := func(allow func(string) error) (*SwarmSpawnTool, *string) {
		captured := new(string)
		*captured = "<unspawned>"
		f := swarm.New(swarm.Config{
			Root:     testsupport.TempDir(t),
			RepoRoot: testsupport.TempDir(t),
			NewRunner: func(a *swarm.Agent) swarm.Runner {
				*captured = a.Backend
				return swarm.RunnerFunc(func(context.Context, swarm.Sink) error { return nil })
			},
		})
		t.Cleanup(f.StopAll)
		return &SwarmSpawnTool{
			Swarm:        f,
			Enabled:      func() bool { return true },
			HostProvider: "p",
			HostModel:    "m",
			AllowBackend: allow,
		}, captured
	}

	run := func(t *SwarmSpawnTool, backend string) core.ToolResult {
		args := map[string]any{"task": "do a thing"}
		if backend != "" {
			args["backend"] = backend
		}
		raw, _ := json.Marshal(args)
		res, err := t.Execute(context.Background(), raw, nil)
		if err != nil {
			return toolErr(err.Error())
		}
		return res
	}

	accept := func(string) error { return nil }
	reject := func(name string) error { return errText("external workers are disabled") }

	t.Run("accepted backend reaches the spawn", func(t *testing.T) {
		tool, captured := newTool(accept)
		res := run(tool, "claude")
		if res.IsError {
			t.Fatalf("a permitted backend should spawn, got error: %s", resultText(res))
		}
		if *captured != "claude" {
			t.Errorf("SpawnRequest.Backend = %q, want claude", *captured)
		}
	})

	t.Run("rejected backend never spawns", func(t *testing.T) {
		tool, captured := newTool(reject)
		res := run(tool, "claude")
		if !res.IsError {
			t.Fatal("a rejected backend must return a tool error")
		}
		if !strings.Contains(resultText(res), "disabled") {
			t.Errorf("error should carry the gate's reason, got: %s", resultText(res))
		}
		if *captured != "<unspawned>" {
			t.Errorf("a rejected backend must not spawn; captured %q", *captured)
		}
	})

	t.Run("backend with no gate wired is refused", func(t *testing.T) {
		tool, captured := newTool(nil)
		res := run(tool, "claude")
		if !res.IsError {
			t.Fatal("a backend request with no AllowBackend must be refused")
		}
		if *captured != "<unspawned>" {
			t.Errorf("must not spawn; captured %q", *captured)
		}
	})

	t.Run("no backend spawns a native sub-agent", func(t *testing.T) {
		// The gate rejects everything, yet an omitted backend must sail past it:
		// the whole check is skipped when backend is empty.
		tool, captured := newTool(reject)
		res := run(tool, "")
		if res.IsError {
			t.Fatalf("a native spawn must not touch the backend gate, got: %s", resultText(res))
		}
		if *captured != "" {
			t.Errorf("native spawn must carry an empty Backend, got %q", *captured)
		}
	})
}

type errText string

func (e errText) Error() string { return string(e) }

func resultText(r core.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}
