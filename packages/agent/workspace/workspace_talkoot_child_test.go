package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/provider"
)

// A swarm child of work that no person began (TKT-01M7R5QJ).
//
// 🔑 The child in these tests is a real process. The daemon's own runner
// starts it, tells it of its mark in the environment, and serves its review
// socket. The process is this test binary, which stands in for terva: it
// builds the gate that a child builds, and it sends each line of its task to
// that gate as a bash call. So the daemon's half and the child's half meet
// over the real socket, and no line of the test tells either half what the
// other one decided.

// childStandIn makes the test binary work as a swarm child. The daemon hands
// its environment to each child, so a test that sets it gets a stand-in.
const childStandIn = "TERVA_TEST_SWARM_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childStandIn) != "" && slices.Contains(os.Args, "--swarm-agent") {
		standInChild()
		return
	}
	os.Exit(m.Run())
}

// standInChild is the child's side: the gate of runSwarmAgentMode with no
// model behind it. Each line of the task is one bash call, and the report
// says what the gate did with it.
func standInChild() {
	var inbox, cwd string
	args := os.Args[1:]
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--swarm-agent":
			inbox = args[i+1]
		case "--cwd":
			cwd = args[i+1]
		}
	}
	task := args[len(args)-1]
	enc := json.NewEncoder(os.Stdout)
	emit := func(typ string, data map[string]any) {
		data["type"], data["time"] = typ, time.Now().Format(time.RFC3339Nano)
		_ = enc.Encode(data)
	}
	ln, err := swarm.Listen(inbox)
	if err != nil {
		emit("error", map[string]any{"message": err.Error()})
		os.Exit(1)
	}
	defer ln.Close()
	// As swarmChildRungs in package agent reads it.
	var rungs []build.Rung
	marked := os.Getenv("TERVA_"+swarm.UnattendedEnv) != ""
	if marked {
		rungs = UnattendedChildRungs(cwd, inbox)
	}
	emit("agent_ready", map[string]any{"cwd": cwd, "unattended": marked})
	gate := build.BuildToolGate(nil, nil, nil, rungs...)
	var report []string
	for _, line := range strings.Split(task, "\n") {
		raw, _ := json.Marshal(map[string]string{"command": line})
		ok, why, _ := gate.CheckTool(context.Background(), provider.ToolCallBlock{ID: "call", Name: "bash", Arguments: raw}, nil)
		if ok {
			why = "ran"
		}
		report = append(report, line+" => "+why)
	}
	emit("assistant_message", map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Join(report, "\n")}}})
	emit("task_end", map[string]any{"step": 1})
	// The supervisor ends the child. A test that failed does not.
	select {
	case <-ln.Lines():
	case <-time.After(30 * time.Second):
	}
	emit("agent_stopped", map[string]any{"reason": "shutdown"})
}

// childConfig turns sub-agents on, with this routines key.
func childConfig(t *testing.T, routines string) {
	t.Helper()
	cfg := `{"talkoot_enabled": true, "auto_swarm_enabled": true, "talkoot_routines": ` + routines + `}`
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// childReport waits for the one sub-agent of the daemon to report, and
// returns it with each line of its report.
func childReport(t *testing.T, w *Workspace) (*swarm.Agent, []string) {
	t.Helper()
	var a *swarm.Agent
	var text string
	waitFor(t, "the report of the sub-agent", func() bool {
		if all := w.swarm.List(); len(all) == 1 {
			a = all[0]
			text = a.Snapshot().Findings()
		}
		return strings.Contains(text, " => ")
	}, func() string {
		if a == nil {
			return "the daemon has no sub-agent"
		}
		evs, _ := swarm.ReadEventLog(a.EventLogPath)
		var types []string
		for _, ev := range evs {
			types = append(types, ev.Type)
		}
		return "status " + string(a.Status()) + ", events " + strings.Join(types, " ")
	})
	return a, strings.Split(text, "\n")
}

// spawnCall is a run that hands these commands to a sub-agent.
func spawnCall(t *testing.T, commands ...string) *runScript {
	t.Helper()
	return &runScript{reply: "The sub-agent has the work.", callName: "swarm_spawn", callArgs: spawnArgs(t, commands...)}
}

// spawnArgs are the arguments of a swarm_spawn call with these commands as
// its task. The test's directory is not a trusted one, and the call says
// that it knows.
func spawnArgs(t *testing.T, commands ...string) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"task": strings.Join(commands, "\n"), "allow_untrusted": true})
	if err != nil {
		t.Fatal(err)
	}
	return string(args)
}

// 🚨 The gap of the ticket. A run that reads "this chain never merges" hands
// the merge to a sub-agent. The sub-agent's own gate refuses each route, and
// its review requests count against the run's routine.
func TestASubAgentOfAnUnattendedRunHoldsTheRunsRules(t *testing.T) {
	t.Setenv(childStandIn, "1")
	// The daemon hands its environment to the child. Only the daemon's own
	// mark may reach it.
	t.Setenv("TERVA_"+swarm.UnattendedEnv, "")
	p := spawnCall(t,
		"just pr-merge 12",
		"git push origin HEAD:trunk",
		"just pr-review 12",
		"just pr-review 13",
		"just pr-review 14",
		"just pr-status 14",
	)
	w, seat, clock, _ := mergeCrew(t, p, "workspace")
	childConfig(t, reviewPattern[:len(reviewPattern)-1]+`}`)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)

	a, report := childReport(t, w)
	// The daemon wrote the mark, from the run's own chain.
	roots := runRoots(t)
	if len(roots) != 1 {
		t.Fatalf("want one run in the room, got %d", len(roots))
	}
	chain := roots[0].Envelope.ID
	if a.Unattended == nil || *a.Unattended != (swarm.Unattended{Team: "crew", Member: "helm", Chain: chain}) {
		t.Fatalf("the sub-agent's mark is %+v, want the crew, helm and the chain %s", a.Unattended, chain)
	}
	want := []struct{ line, has string }{
		{"just pr-merge 12", mergeDenied},
		{"git push origin HEAD:trunk", mergeDenied},
		{"just pr-review 12", "=> ran"},
		{"just pr-review 13", "=> ran"},
		{"just pr-review 14", "its chains asked for 2 today, and its limit is 2 a day"},
		{"just pr-status 14", "=> ran"},
	}
	if len(report) != len(want) {
		t.Fatalf("the sub-agent reported %d lines, want %d:\n%s", len(report), len(want), strings.Join(report, "\n"))
	}
	for i, c := range want {
		if !strings.HasPrefix(report[i], c.line+" => ") || !strings.Contains(report[i], c.has) {
			t.Errorf("the gate's answer for %q is %q, want it to hold %q", c.line, report[i], c.has)
		}
	}
	// The count is the routine's own: the room holds each request under the
	// routine, as a request of the member whose run started the sub-agent.
	lines := reviewLines(t)
	if len(lines) != 2 {
		t.Fatalf("the room holds %d review lines, want 2: %+v", len(lines), lines)
	}
	for _, l := range lines {
		if l.Member != "helm" || l.Count != 1 || l.Ref != talkoot.RoutinePrefix+rid || l.Chain != chain {
			t.Fatalf("a review line of a sub-agent must name the member, the routine and the chain: %+v", l)
		}
	}
	if _, r := routineView(t, w); r.ReviewsToday != 2 {
		t.Fatalf("the list of routines shows %d review requests today, want 2", r.ReviewsToday)
	}
}

// The control. A person asks helm for the same sub-agent. It gets no mark,
// and its gate refuses nothing and counts nothing.
func TestASubAgentOfAPersonsTurnHasNoMark(t *testing.T) {
	t.Setenv(childStandIn, "1")
	t.Setenv("TERVA_"+swarm.UnattendedEnv, "")
	cwd := talkootHome(t)
	newForge(t, cwd)
	childConfig(t, reviewPattern[:len(reviewPattern)-1]+`}`)
	p := &runScript{memberTool: "swarm_spawn", memberBash: spawnArgs(t, "just pr-merge 12", "just pr-review 12"), memberBashOn: "Hand the merge to a sub-agent."}
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, p.serve)
	text := strings.Replace(string(crewText(cwd)), "role: coordinator\n", "role: coordinator\n    posture: workspace\n", 1)
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(text)); err != nil {
		t.Fatal(err)
	}
	helmAfterOneTurn(t, w)
	tell(t, w, "Hand the merge to a sub-agent.")

	a, report := childReport(t, w)
	if a.Unattended != nil {
		t.Fatalf("a sub-agent of a person's turn got the mark %+v", a.Unattended)
	}
	for _, line := range report {
		if !strings.HasSuffix(line, "=> ran") {
			t.Errorf("the gate of a sub-agent with no mark refused a call: %q", line)
		}
	}
	if lines := reviewLines(t); len(lines) != 0 {
		t.Fatalf("a sub-agent of a person's turn counted a review: %+v", lines)
	}
}
