package swarm

import (
	"strings"
	"testing"
)

// TestSwarmAgentArgs locks in the exact flag set the subprocess
// runner uses to start a swarm agent in daemon mode. Past
// regressions in this area:
//
//   - "--no-sess" instead of "--no-session" (old print-mode runner):
//     every spawned agent died with "unknown flag" before it could
//     talk to the model.
//
//   - Forgetting --cwd: the child resolved tools against the parent
//     terva's working directory, defeating the whole point of the
//     worktree isolation.
//
//   - Forgetting --session: a daemon-mode agent without a session
//     file would lose context between follow-up turns, making
//     "send another message" mostly useless.
//
// The test asserts the load-bearing pieces are present in plausible
// positions. If a flag is renamed, update both the runner and this
// test so we notice immediately.
func TestSwarmAgentArgs(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe:         "/path/to/terva",
		Dir:         "/tmp/worktree",
		SessionPath: "/tmp/state/session.json",
		InboxPath:   "/tmp/state/in.sock",
		Task:        "do the thing",
	})
	if len(args) < 7 {
		t.Fatalf("argv unexpectedly short: %v", args)
	}
	if args[0] != "/path/to/terva" {
		t.Fatalf("argv[0] = %q; want the binary path", args[0])
	}
	// The task must come last so anything that looks flag-like in
	// the task body doesn't get interpreted as a flag.
	if args[len(args)-1] != "do the thing" {
		t.Fatalf("task should be last positional; got %v", args)
	}

	mustHave := map[string]string{
		"--swarm-agent": "/tmp/state/in.sock",
		"--session":     "/tmp/state/session.json",
		"--cwd":         "/tmp/worktree",
	}
	for flag, value := range mustHave {
		i := indexOf(args, flag)
		if i < 0 {
			t.Errorf("argv missing %q: %v", flag, args)
			continue
		}
		if i+1 >= len(args) || args[i+1] != value {
			t.Errorf("argv %q value = %q; want %q", flag, safeAt(args, i+1), value)
		}
	}

	// Reject prior bad flags explicitly so a future revert is caught.
	joined := strings.Join(args, " ")
	for _, bad := range []string{"--print", "--no-sess ", "--no-session"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("argv contains stale/wrong flag %q: %s", bad, joined)
		}
	}
}

// TestSwarmAgentArgsPersona pins persona dispatch: a non-empty Persona
// becomes a --persona flag BEFORE the positional task; an empty one omits
// the flag so the child falls back to its default identity.
func TestSwarmAgentArgsPersona(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
		Persona: "vartija", Task: "audit the auth path",
	})
	pi := indexOf(args, "--persona")
	if pi < 0 || safeAt(args, pi+1) != "vartija" {
		t.Fatalf("argv missing --persona vartija: %v", args)
	}
	if args[len(args)-1] != "audit the auth path" {
		t.Fatalf("task should be the last positional; got %v", args)
	}
	if pi+1 >= len(args)-1 {
		t.Fatalf("--persona must precede the positional task: %v", args)
	}
	none := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock", Task: "t",
	})
	if indexOf(none, "--persona") >= 0 {
		t.Fatalf("empty persona should omit --persona: %v", none)
	}
}

// TestSwarmAgentArgsEmptyTaskOmitsPositional makes sure that when the
// agent is being adopted (no fresh task) we don't pass an empty
// positional which the arg parser would treat as a real prompt.
func TestSwarmAgentArgsEmptyTaskOmitsPositional(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
	})
	for _, a := range args {
		if a == "" {
			t.Fatalf("argv contains an empty positional: %v", args)
		}
	}
	// last arg should be a real flag value, not a stray positional
	if a := args[len(args)-1]; strings.HasPrefix(a, "--") {
		t.Fatalf("argv ends on a flag with no value: %v", args)
	}
}

// TestSwarmAgentArgsPostureIsDeliberate pins the two flags the child argv
// does NOT carry, because their absence is the security posture:
//
//   - no --approval: the child resolves ModeSwarmAgent's default, which is
//     yolo with no gate object at all (build's mode-posture table pins the
//     resolver side). Forwarding the parent's approval here would park child
//     tool calls on prompts nobody is watching.
//   - no --ext: the child still runs full default extension discovery and the
//     user's MCP config (swarm_agent.go → the shared non-interactive setup) —
//     absence means "no EXTRA paths", not "no extensions". Anyone tightening
//     or loosening this pair is changing what a trusted project's code can do
//     unattended at yolo; docs/architecture/06-extensibility.md §1.5 is the
//     matrix that states the whole picture, and
//     docs/proposals/multiplexed-extension-access.md the open proposal to
//     change the extension half.
//
// The other terva-child argv builder (worker/terva.go) deliberately DOES
// forward --approval — a supervised foreign worker keeps the caller's posture
// (worker/posture_test.go). These two builders differing is intended; this
// test exists so a well-meaning "consistency" edit can't silently align them.
func TestSwarmAgentArgsPostureIsDeliberate(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
		Persona: "vartija", Task: "audit the auth path",
	})
	for _, banned := range []string{"--approval", "--ext", "--no-ext", "--no-mcp"} {
		if i := indexOf(args, banned); i >= 0 {
			t.Errorf("child argv carries %q — this posture flag's ABSENCE is load-bearing; read this test's comment before adding it: %v", banned, args)
		}
	}
}

// TestDefaultChildArgsSpawnIncludesTask pins the spawn shape: a
// fresh (non-resuming) Agent produces argv that ends with the
// original task as a positional, so the child runs it as the
// initial user turn.
func TestDefaultChildArgsSpawnIncludesTask(t *testing.T) {
	a := &Agent{Dir: "/wt", Task: "do thing"}
	args := defaultChildArgs("/terva", a, "/s.json", "/in.sock")
	if got := args[len(args)-1]; got != "do thing" {
		t.Fatalf("spawn argv last = %q; want %q\n%v", got, "do thing", args)
	}
}

// TestDefaultChildArgsResumeOmitsTask is the regression for the
// "agent busy; send 'cancel' first" error: when an Agent is being
// resumed (Resuming==true), the child argv MUST NOT include the
// original Task as a positional. Otherwise the child fires the task
// as a fresh user turn on every resume, racing with whatever the
// user types next via the inbox.
func TestDefaultChildArgsResumeOmitsTask(t *testing.T) {
	a := &Agent{Dir: "/wt", Task: "do thing", Resuming: true}
	args := defaultChildArgs("/terva", a, "/s.json", "/in.sock")
	for _, v := range args {
		if v == "do thing" {
			t.Fatalf("resume argv contains the task; it would re-fire as a duplicate turn\n%v", args)
		}
	}
	// And no trailing positional at all: the last arg should be a
	// flag value, not a stray empty string.
	if got := args[len(args)-1]; got == "" {
		t.Fatalf("resume argv ends with an empty positional: %v", args)
	}
	if strings.HasPrefix(args[len(args)-1], "--") {
		t.Fatalf("resume argv ends on a bare flag (no value): %v", args)
	}
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}

func safeAt(xs []string, i int) string {
	if i < 0 || i >= len(xs) {
		return ""
	}
	return xs[i]
}

// A tier can be a cheaper model, a smaller amount of thinking, or both — on a
// provider that ships one good model the effort is the only lever there is.
// The child only obeys one if the flag actually reaches it.
func TestSwarmAgentArgsReasoning(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
		Model: "k3", Provider: "kimi", Reasoning: "off", Task: "triage this",
	})
	ri := indexOf(args, "--reasoning")
	if ri < 0 || safeAt(args, ri+1) != "off" {
		t.Fatalf("argv missing --reasoning off: %v", args)
	}
	if ri+1 >= len(args)-1 {
		t.Fatalf("--reasoning must precede the positional task: %v", args)
	}
	// Untiered spawns have always let the child resolve its own effort, and
	// a flag pinning it to nothing would change that for every one of them.
	none := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock", Task: "t",
	})
	if indexOf(none, "--reasoning") >= 0 {
		t.Fatalf("empty reasoning should omit --reasoning: %v", none)
	}
}

// The effort has to survive a restart the same way the model does: a resumed
// agent that silently reverted to full thinking would quietly double what a
// weak-tier delegation costs.
func TestDefaultChildArgsCarriesReasoning(t *testing.T) {
	a := &Agent{ID: "x", Task: "t", Dir: "/wt", Model: "k3", Provider: "kimi", Reasoning: "low"}
	args := defaultChildArgs("/terva", a, "/s.json", "/in.sock")
	ri := indexOf(args, "--reasoning")
	if ri < 0 || safeAt(args, ri+1) != "low" {
		t.Fatalf("resumed argv missing --reasoning low: %v", args)
	}
}
