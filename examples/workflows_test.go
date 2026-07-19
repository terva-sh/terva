package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/workflow"
	"terva.sh/terva/packages/testsupport"
)

// Every script in workflows/ is executed here against a stub engine, so a
// shipped example is a *running* example: the meta parses, the script
// completes under the determinism rules, and its return value has the shape
// the file's comments promise. The table is deliberately exhaustive — an
// example with no entry fails the test, which is what keeps this directory
// and CI in lockstep.
var exampleRuns = map[string]struct {
	opts  workflow.Options
	check func(t *testing.T, res workflow.Result)
}{
	"01-hello-fanout.js": {
		check: func(t *testing.T, res workflow.Result) {
			v := exportValue[struct {
				Topics  []string `json:"topics"`
				Reports []string `json:"reports"`
			}](t, res)
			if len(v.Topics) != 3 || len(v.Reports) != 3 {
				t.Errorf("want 3 topics / 3 reports, got %d / %d", len(v.Topics), len(v.Reports))
			}
		},
	},
	"02-review-verify.js": {
		check: func(t *testing.T, res workflow.Result) {
			v := exportValue[struct {
				Dimensions int              `json:"dimensions"`
				Confirmed  []map[string]any `json:"confirmed"`
			}](t, res)
			// The stub's schema deliverables zero-fill (real=false), so the
			// refutation pass confirms nothing — the point is the pipeline
			// and both schema'd verifies ran to completion.
			if v.Dimensions != 2 || len(v.Confirmed) != 0 {
				t.Errorf("want 2 dimensions / 0 confirmed under the stub, got %d / %d", v.Dimensions, len(v.Confirmed))
			}
			if res.Agents != 4 {
				t.Errorf("want 4 agents (2 review + 2 verify), got %d", res.Agents)
			}
		},
	},
	"03-args-budget.js": {
		opts: workflow.Options{
			Args:      map[string]any{"paths": []any{"packages/a", "packages/b", "packages/c", "packages/d"}},
			BudgetUSD: 0.27, // stub agents cost $0.01 — the ceiling must stop the sweep early
		},
		check: func(t *testing.T, res workflow.Result) {
			v := exportValue[struct {
				Audited   int `json:"audited"`
				Requested int `json:"requested"`
			}](t, res)
			if v.Requested != 4 {
				t.Errorf("want 4 requested, got %d", v.Requested)
			}
			if v.Audited == 0 || v.Audited >= v.Requested {
				t.Errorf("the budget ceiling should stop the sweep partway: audited %d of %d", v.Audited, v.Requested)
			}
		},
	},
}

func TestEveryExampleWorkflowRuns(t *testing.T) {
	entries, err := os.ReadDir("workflows")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".js" {
			continue
		}
		seen++
		run, ok := exampleRuns[e.Name()]
		if !ok {
			t.Errorf("examples/workflows/%s has no entry in exampleRuns — every shipped example must be executed here", e.Name())
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("workflows", e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			opts := run.opts
			opts.Root = testsupport.TempDir(t)
			res, err := workflow.Run(context.Background(), &stubEngine{}, src, opts)
			if err != nil {
				t.Fatalf("example did not run: %v", err)
			}
			if res.Value == nil {
				t.Fatal("example returned no value")
			}
			run.check(t, res)
		})
	}
	if seen == 0 {
		t.Fatal("no example scripts found — wrong working directory?")
	}
}

// A resumed, unchanged example must replay entirely from the journal: the
// resume story the docs tell, proven on the shipped script.
func TestExampleResumeReplaysFromJournal(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("workflows", "01-hello-fanout.js"))
	if err != nil {
		t.Fatal(err)
	}
	root := testsupport.TempDir(t)
	first, err := workflow.Run(context.Background(), &stubEngine{}, src, workflow.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := workflow.Run(context.Background(), &stubEngine{}, src, workflow.Options{Root: root, ResumeID: first.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if second.Agents != 0 || second.CachedAgents != first.Agents {
		t.Errorf("resume spawned %d agents and replayed %d; want 0 spawned, %d replayed",
			second.Agents, second.CachedAgents, first.Agents)
	}
}

// exportValue round-trips a script's returned value through JSON into a
// typed shape, the same way a CLI consumer would read the stdout JSON.
func exportValue[T any](t *testing.T, res workflow.Result) T {
	t.Helper()
	raw, err := json.Marshal(res.Value)
	if err != nil {
		t.Fatalf("result value does not marshal: %v", err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("result value has the wrong shape: %v\nvalue: %s", err, raw)
	}
	return v
}

// stubEngine satisfies workflow.Engine with canned, deterministic agents:
// schema-less spawns report a findings string, schema'd spawns report a
// zero-valued object matching the schema's declared property types. Each
// agent costs a fixed $0.01 so the budget example's ceiling math is exact.
type stubEngine struct {
	mu sync.Mutex
	n  int
}

func (e *stubEngine) AllowBackend(name string) error {
	if name == "" {
		return nil
	}
	return fmt.Errorf("stub engine runs native agents only")
}

func (e *stubEngine) Spawn(_ context.Context, req swarm.SpawnRequest) (workflow.Handle, error) {
	e.mu.Lock()
	e.n++
	id := fmt.Sprintf("stub-%d", e.n)
	e.mu.Unlock()
	var result json.RawMessage
	if len(req.Schema) > 0 {
		result = zeroDeliverable(req.Schema)
	} else {
		result, _ = json.Marshal("stub findings for: " + req.Task)
	}
	return stubHandle{id: id, result: result}, nil
}

type stubHandle struct {
	id     string
	result json.RawMessage
}

func (h stubHandle) ID() string { return h.id }
func (h stubHandle) AwaitTask(context.Context) workflow.Outcome {
	return workflow.Outcome{AgentID: h.id, Result: h.result, CostUSD: 0.01}
}

// zeroDeliverable builds an object of zero values for the schema's declared
// properties — enough to satisfy any required-fields contract while staying
// deterministic.
func zeroDeliverable(schema json.RawMessage) json.RawMessage {
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	obj := map[string]any{}
	for name, p := range s.Properties {
		switch p.Type {
		case "string":
			obj[name] = ""
		case "boolean":
			obj[name] = false
		case "number", "integer":
			obj[name] = 0
		case "array":
			obj[name] = []any{}
		default:
			obj[name] = map[string]any{}
		}
	}
	raw, _ := json.Marshal(obj)
	return raw
}
