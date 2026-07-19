package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// TestTranslateTervaIsNearIdentity pins the dogfood translator: the rpc stream
// IS core.WireEvent, so events pass through untouched and only the two rpc
// PROTOCOL frame kinds are special — `response` acks vanish, `done` becomes the
// task_end the swarm's OnTurnEnd keys on. Almost no mapping is the point: a
// conformance failure then indicts the contract, not this function.
func TestTranslateTervaIsNearIdentity(t *testing.T) {
	cases := []struct {
		line string
		want string // event type, or "" for dropped
	}{
		{`{"type":"response","command":"prompt","success":true,"data":{"started":true}}`, ""},
		{`{"type":"response","command":"hello","success":true}`, ""},
		{`{"type":"turn_start"}`, "turn_start"},
		{`{"type":"assistant_message","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`, "assistant_message"},
		{`{"type":"tool_call","name":"read","input":{}}`, "tool_call"},
		{`{"type":"tool_result","name":"read"}`, "tool_result"},
		{`{"type":"error","error":"boom"}`, "error"},
		{`{"type":"usage","usage":{"cost_usd":0.001},"cumulative":{"cost_usd":0.004}}`, "usage"}, // cost rides this
		{`{"type":"done"}`, "task_end"},                                                          // synthesized: no task_end on the rpc wire
		{`not json`, ""},
		{`{"no":"type"}`, ""},
	}
	for _, c := range cases {
		evs := translateTerva([]byte(c.line))
		if c.want == "" {
			if len(evs) != 0 {
				t.Errorf("%s → %v, want dropped", c.line, evs)
			}
			continue
		}
		if len(evs) != 1 || evs[0].Type != c.want {
			t.Errorf("%s → %v, want one %q", c.line, evs, c.want)
			continue
		}
		// Pass-through events must not carry a duplicate "type" inside Data (the
		// swarm event re-adds it at the top level).
		if _, dup := evs[0].Data["type"]; dup {
			t.Errorf("%s: Data still carries a redundant type key: %v", c.line, evs[0].Data)
		}
	}

	// The pass-through must preserve the payload the sink reads — here the
	// assistant message's content blocks.
	evs := translateTerva([]byte(`{"type":"assistant_message","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))
	msg, _ := evs[0].Data["message"].(map[string]any)
	if msg == nil || msg["content"] == nil {
		t.Errorf("assistant_message lost its payload: %v", evs[0].Data)
	}

	// The usage event is the terva backend's cost carrier: cumulative.cost_usd
	// must survive the pass-through untouched, because swarm.IngestEvent reads it
	// there to light up a terva worker's live spend (there is no cost on `done`).
	evs = translateTerva([]byte(`{"type":"usage","usage":{"cost_usd":0.001},"cumulative":{"cost_usd":0.004,"input":120}}`))
	cum, _ := evs[0].Data["cumulative"].(map[string]any)
	if cost, _ := cum["cost_usd"].(float64); cost != 0.004 {
		t.Errorf("usage event lost cumulative.cost_usd: got %v from %v", cost, evs[0].Data)
	}
}

// TestTervaCommandSpeaksTheDogfoodWire pins the argv: `terva rpc` in the lease,
// the model selection / approval / persona as FLAGS, no rendered prompt on the
// command line, and — critically — no rpc auth token in the child env, which is
// what lets the first prompt work without a hello handshake.
func TestTervaCommandSpeaksTheDogfoodWire(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, Task{Mission: "do the thing"}, Workspace{Path: "/lease/wt-9"})
	b.Route = Route{Provider: "anthropic", Model: "claude-opus-4-8", Effort: "high"}
	b.Policy = Policy{Posture: "workspace"}
	b.Identity.Name = "Mieli"

	cmd, err := tervaCommand(Dispatch{Briefing: b, Dir: "/lease/wt-9"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")

	for _, want := range []string{"rpc", "--rpc-approvals", "--cwd /lease/wt-9", "--provider anthropic",
		"--model claude-opus-4-8", "--reasoning high", "--approval workspace", "--persona Mieli"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	// The task rides stdin, never argv.
	if strings.Contains(argv, "do the thing") {
		t.Errorf("the task must not be on the command line: %s", argv)
	}
	// cmd.Dir is the lease.
	if cmd.Dir != "/lease/wt-9" {
		t.Errorf("cmd.Dir = %q, want the lease", cmd.Dir)
	}
	// No rpc token in the child env, or it would demand a hello we never send.
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "TERVACORE_RPC_TOKEN=") || strings.HasPrefix(kv, "ZOTCORE_RPC_TOKEN=") { // rename:keep — embedder compat
			t.Errorf("child env still carries an rpc auth token: %q", kv)
		}
	}
}

// TestTervaCommandsWireSessionForResume: both terva backends pass the per-agent
// session file as `--session <path>` so `terva rpc` persists and reopens the
// worker's transcript across a revival — terva's resume mechanism (a session file
// it owns), the analog of claude's minted --session-id. Absent when the runner
// gives no path, so a session-less dispatch stays in-memory only.
func TestTervaCommandsWireSessionForResume(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, Task{Mission: "do the thing"}, Workspace{Path: "/lease/wt-9"})
	const sessPath = "/state/agents/alpha-1/session.json"

	for _, tc := range []struct {
		name string
		cmd  func(Dispatch) (*exec.Cmd, error)
	}{
		{"terva", tervaCommand},
		{"terva:portable", tervaPortableCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withSess, err := tc.cmd(Dispatch{Briefing: b, Dir: "/lease/wt-9", SessionPath: sessPath})
			if err != nil {
				t.Fatal(err)
			}
			if v := flagValue(withSess.Args, "--session"); v != sessPath {
				t.Errorf("--session = %q, want the per-agent session path %q", v, sessPath)
			}

			bare, err := tc.cmd(Dispatch{Briefing: b, Dir: "/lease/wt-9"})
			if err != nil {
				t.Fatal(err)
			}
			if v := flagValue(bare.Args, "--session"); v != "" {
				t.Errorf("no SessionPath must mean no --session flag, got %q", v)
			}
		})
	}
}

// TestWithoutRPCTokenStrips proves the env filter drops both token vars and
// leaves everything else — a mangled env would break the child outright.
func TestWithoutRPCTokenStrips(t *testing.T) {
	in := []string{"PATH=/bin", "TERVACORE_RPC_TOKEN=secret", "HOME=/root", "ZOTCORE_RPC_TOKEN=old"} // rename:keep — embedder compat
	out := withoutRPCToken(in)
	got := strings.Join(out, " ")
	if strings.Contains(got, "RPC_TOKEN") {
		t.Errorf("token survived: %v", out)
	}
	for _, keep := range []string{"PATH=/bin", "HOME=/root"} {
		if !strings.Contains(got, keep) {
			t.Errorf("dropped a non-token var %q: %v", keep, out)
		}
	}
}

// TestSelfAssemblingWorkerRunsThroughTheSupervisor drives the REAL tervaBackend
// (translate/steer/opening) through the real runner and a real Swarm against a
// fake `terva rpc` — proving the dogfood bridge end to end without a model:
// spawn, the response-ack drop, the WireEvent pass-through, a steered follow-up,
// and done→task_end.
func TestSelfAssemblingWorkerRunsThroughTheSupervisor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets (the inbox) are not supported on windows")
	}
	if testing.Short() {
		t.Skip("skips the process-spawning runner test in -short mode")
	}

	fakeBin := buildFakeTerva(t)
	resolved := loadedRepo(t)

	backend := tervaBackend()
	backend.Name = "faketerva"
	backend.Command = func(d Dispatch) (*exec.Cmd, error) {
		cmd := exec.Command(fakeBin)
		cmd.Dir = d.Dir
		return cmd, nil
	}

	f := swarm.New(swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: testsupport.TempDir(t),
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return NewRunner(a, backend, resolved, nil)
		},
	})
	defer f.StopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a, err := f.Spawn(ctx, "make the tests deterministic")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// The opening turn is the multi-line selfAssembledTask markdown, so "echo:"
	// and the mission land on different transcript lines — match the mission
	// text, which is in the transcript only because it was echoed back.
	waitForTranscript(t, a, "make the tests deterministic")

	if err := retrySend(t, f, a.ID, "also update the changelog", 2*time.Second); err != nil {
		t.Fatalf("steer: %v", err)
	}
	waitForTranscript(t, a, "echo: also update the changelog")

	if err := f.Stop(a.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	a.Wait()
	if got := a.Status(); got != swarm.StatusDone && got != swarm.StatusKilled {
		t.Fatalf("final status = %s; want done or killed", got)
	}

	evs, err := swarm.ReadEventLog(a.EventLogPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	// done → task_end reached the durable log; the response acks did NOT (they
	// are rpc protocol, dropped by translate — but kept raw below).
	if _, ok := findEvent(evs, "task_end"); !ok {
		t.Error("no task_end synthesized from the rpc `done` frame")
	}
	for _, e := range evs {
		if e.Type == "response" {
			t.Errorf("an rpc protocol `response` frame leaked into the translated log: %v", e.Data)
		}
	}
	// The raw stream keeps everything, including the dropped acks.
	rawPath := filepath.Join(filepath.Dir(a.EventLogPath), "raw.jsonl")
	if raw, err := os.ReadFile(rawPath); err != nil {
		t.Errorf("raw stream not retained: %v", err)
	} else if !strings.Contains(string(raw), `"type":"response"`) {
		t.Errorf("raw.jsonl should retain the dropped protocol frames; got:\n%s", raw)
	}

	// The terva cost route, end to end and model-free: the fake emitted a per-turn
	// usage event with a CLIMBING cumulative (0.0025 then 0.005), and the
	// supervisor recorded it onto the snapshot. Two turns ran, so last-wins gives
	// ~0.005 — a sum-of-cumulatives bug would give 0.0075, so the upper bound is
	// the real assertion here.
	if cost := a.Snapshot().CostUSD; cost <= 0.004 || cost >= 0.006 {
		t.Errorf("worker cost = %v, want ~0.005 (last-wins cumulative over two turns off the usage wire)", cost)
	}
}

func buildFakeTerva(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "faketerva")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/cmd/faketerva")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build faketerva: %v\n%s", err, b)
	}
	return out
}
