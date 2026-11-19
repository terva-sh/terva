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

// 🚨 TKT-01M7R5QJ. A sub-agent of a chain that no person began holds the
// chain's rules on its own gate. An external worker has no terva gate, so
// such a chain cannot start one. The host's mark reaches the spawn of a
// native sub-agent, and no argument of the tool can set it or clear it.
func TestAnUnattendedTurnMarksItsSubAgentAndStartsNoExternalWorker(t *testing.T) {
	type spawned struct {
		backend string
		mark    *swarm.Unattended
	}
	newTool := func(mark func() *swarm.Unattended) (*SwarmSpawnTool, *[]spawned) {
		var got []spawned
		f := swarm.New(swarm.Config{
			Root:     testsupport.TempDir(t),
			RepoRoot: testsupport.TempDir(t),
			NewRunner: func(a *swarm.Agent) swarm.Runner {
				got = append(got, spawned{a.Backend, a.Unattended})
				return swarm.RunnerFunc(func(context.Context, swarm.Sink) error { return nil })
			},
		})
		t.Cleanup(f.StopAll)
		return &SwarmSpawnTool{
			Swarm: f, Enabled: func() bool { return true }, HostProvider: "p", HostModel: "m",
			AllowBackend: func(string) error { return nil },
			Unattended:   mark,
		}, &got
	}
	run := func(tool *SwarmSpawnTool, args map[string]any) core.ToolResult {
		raw, _ := json.Marshal(args)
		res, err := tool.Execute(context.Background(), raw, nil)
		if err != nil {
			return toolErr(err.Error())
		}
		return res
	}
	chain := swarm.Unattended{Team: "crew", Member: "helm", Chain: "run-1"}
	lone := func() *swarm.Unattended { c := chain; return &c }

	t.Run("an external worker is refused", func(t *testing.T) {
		tool, got := newTool(lone)
		res := run(tool, map[string]any{"task": "merge it", "backend": "claude"})
		if !res.IsError || !strings.Contains(resultText(res), "no person began this chain") {
			t.Fatalf("want a refusal that names the chain, got: %s", resultText(res))
		}
		if len(*got) != 0 {
			t.Fatalf("the refused call started %+v", *got)
		}
	})
	t.Run("a native sub-agent gets the host's mark", func(t *testing.T) {
		tool, got := newTool(lone)
		// Words that a model might try. The tool reads none of them.
		res := run(tool, map[string]any{"task": "merge it", "unattended": false, "Unattended": nil, "attended": true})
		if res.IsError {
			t.Fatalf("a native spawn must start: %s", resultText(res))
		}
		if len(*got) != 1 || (*got)[0].mark == nil || *(*got)[0].mark != chain {
			t.Fatalf("the sub-agent started as %+v, want the mark %+v", *got, chain)
		}
	})
	t.Run("a process that is itself unattended starts no external worker", func(t *testing.T) {
		// The host gives no mark, as in a sub-agent's own process. The
		// supervisor's variable is the mark there.
		t.Setenv("TERVA_"+swarm.UnattendedEnv, "1")
		tool, got := newTool(nil)
		res := run(tool, map[string]any{"task": "merge it", "backend": "claude"})
		if !res.IsError || !strings.Contains(resultText(res), "no person began this chain") {
			t.Fatalf("want a refusal that names the chain, got: %s", resultText(res))
		}
		if len(*got) != 0 {
			t.Fatalf("the refused call started %+v", *got)
		}
		if res := run(tool, map[string]any{"task": "search it"}); res.IsError {
			t.Fatalf("a native spawn must start: %s", resultText(res))
		}
		if len(*got) != 1 || (*got)[0].mark == nil {
			t.Fatalf("the sub-agent of an unattended process started as %+v, want a mark", *got)
		}
	})
	t.Run("a turn that a person began marks nothing", func(t *testing.T) {
		t.Setenv("TERVA_"+swarm.UnattendedEnv, "")
		for _, mark := range []func() *swarm.Unattended{nil, func() *swarm.Unattended { return nil }} {
			tool, got := newTool(mark)
			if res := run(tool, map[string]any{"task": "merge it", "backend": "claude", "unattended": true}); res.IsError {
				t.Fatalf("an attended spawn on a backend must start: %s", resultText(res))
			}
			if len(*got) != 1 || (*got)[0].mark != nil || (*got)[0].backend != "claude" {
				t.Fatalf("the sub-agent started as %+v, want the backend and no mark", *got)
			}
		}
	})
}
