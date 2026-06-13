package hooks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	stubOnce sync.Once
	stubPath string
	stubErr  error
)

// buildStub compiles testdata/cmd/hookstub once per test run.
func buildStub(t *testing.T) string {
	t.Helper()
	stubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hookstub")
		if err != nil {
			stubErr = err
			return
		}
		out := filepath.Join(dir, "hookstub")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, "./testdata/cmd/hookstub")
		if b, err := cmd.CombinedOutput(); err != nil {
			stubErr = err
			_ = b
			return
		}
		stubPath = out
	})
	if stubErr != nil {
		t.Fatalf("build hookstub: %v", stubErr)
	}
	return stubPath
}

func engineWith(t *testing.T, pre []Spec, post []Spec) *Engine {
	t.Helper()
	e := NewEngine(Config{PreToolUse: pre, PostToolUse: post}, t.TempDir(), t.Logf)
	if e == nil {
		t.Fatal("engine should not be nil with hooks configured")
	}
	return e
}

func bashArgs(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func TestNewEngineNilWhenEmpty(t *testing.T) {
	if NewEngine(Config{}, ".", nil) != nil {
		t.Fatal("empty config should produce nil engine")
	}
	var e *Engine
	if r := e.RunPre(context.Background(), "bash", nil); r.Decision != "" {
		t.Fatal("nil engine must be inert")
	}
	e.Observe("tool_call", "x", "bash", nil, false) // must not panic
}

func TestRunPreDecisions(t *testing.T) {
	stub := buildStub(t)
	cases := []struct {
		mode     string
		decision string
	}{
		{"allow", DecisionAllow},
		{"deny", DecisionDeny},
		{"ask", DecisionAsk},
		{"silent", ""},
		{"garbage", ""}, // bad JSON = no opinion, never a block
		{"fail", ""},    // exit 1 = no opinion
	}
	for _, c := range cases {
		e := engineWith(t, []Spec{{Command: stub, Args: []string{c.mode}}}, nil)
		r := e.RunPre(context.Background(), "bash", bashArgs("ls"))
		if r.Decision != c.decision {
			t.Errorf("mode %s: decision %q, want %q", c.mode, r.Decision, c.decision)
		}
	}
}

func TestRunPreExit2IsDenyWithStderrReason(t *testing.T) {
	stub := buildStub(t)
	e := engineWith(t, []Spec{{Command: stub, Args: []string{"exit2"}}}, nil)
	r := e.RunPre(context.Background(), "bash", bashArgs("rm -rf /"))
	if r.Decision != DecisionDeny {
		t.Fatalf("exit 2 should deny, got %q", r.Decision)
	}
	if !strings.Contains(r.Reason, "blocked by exit2 stub") {
		t.Errorf("deny reason should carry stderr, got %q", r.Reason)
	}
}

func TestRunPreRewriteAccumulatesAndContinues(t *testing.T) {
	stub := buildStub(t)
	e := engineWith(t, []Spec{
		{Command: stub, Args: []string{"rewrite"}},
		{Command: stub, Args: []string{"allow"}},
	}, nil)
	r := e.RunPre(context.Background(), "bash", bashArgs("ls"))
	if r.Decision != DecisionAllow {
		t.Fatalf("second hook should allow, got %q", r.Decision)
	}
	var got map[string]string
	if err := json.Unmarshal(r.UpdatedArgs, &got); err != nil || got["command"] != "echo rewritten" {
		t.Errorf("rewrite lost: %s", r.UpdatedArgs)
	}
}

func TestRunPreFirstDecisiveWins(t *testing.T) {
	stub := buildStub(t)
	e := engineWith(t, []Spec{
		{Command: stub, Args: []string{"deny"}},
		{Command: stub, Args: []string{"allow"}},
	}, nil)
	if r := e.RunPre(context.Background(), "bash", bashArgs("ls")); r.Decision != DecisionDeny {
		t.Fatalf("first decisive hook should win, got %q", r.Decision)
	}
}

func TestRunPreToolFilter(t *testing.T) {
	stub := buildStub(t)
	e := engineWith(t, []Spec{
		{Command: stub, Args: []string{"deny"}, Tools: "bash"},
		{Command: stub, Args: []string{"deny"}, Tools: "mcp_*"},
	}, nil)
	if r := e.RunPre(context.Background(), "read", bashArgs("x")); r.Decision != "" {
		t.Error("filtered hooks must not fire for unmatched tools")
	}
	if r := e.RunPre(context.Background(), "mcp_github_x", nil); r.Decision != DecisionDeny {
		t.Error("glob filter should match")
	}
}

func TestRunPreTimeoutIsNoOpinion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep stub uses the go binary; timing on windows CI is unreliable")
	}
	// `go run` of a sleeping program would be slow to build; reuse the
	// stub in "silent" mode under an absurdly short timeout instead —
	// the point is only that a deadline produces no-opinion, not a
	// deny.
	stub := buildStub(t)
	e := engineWith(t, []Spec{{Command: stub, Args: []string{"silent"}, TimeoutMS: 1}}, nil)
	r := e.RunPre(context.Background(), "bash", bashArgs("ls"))
	if r.Decision != "" {
		t.Fatalf("timeout should be no opinion, got %q", r.Decision)
	}
}

func TestObserveFiresPostHookWithCorrelatedCall(t *testing.T) {
	stub := buildStub(t)
	outFile := filepath.Join(t.TempDir(), "post.jsonl")
	e := engineWith(t, nil, []Spec{{Command: stub, Args: []string{"echo", outFile}}})

	args := bashArgs("ls -la")
	e.Observe("tool_call", "call_1", "bash", args, false)
	e.Observe("tool_result", "call_1", "", nil, true)

	deadline := time.Now().Add(5 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		content, _ = os.ReadFile(outFile)
		if len(content) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(content) == 0 {
		t.Fatal("post hook never wrote its payload")
	}
	var ev struct {
		Event   string          `json:"event"`
		Tool    string          `json:"tool"`
		Args    json.RawMessage `json:"args"`
		IsError bool            `json:"is_error"`
	}
	if err := json.Unmarshal(content[:len(content)-1], &ev); err != nil {
		t.Fatalf("payload not JSON: %v in %q", err, content)
	}
	if ev.Event != "post_tool_use" || ev.Tool != "bash" || !ev.IsError {
		t.Errorf("payload = %+v", ev)
	}
	if !strings.Contains(string(ev.Args), "ls -la") {
		t.Errorf("post hook lost the correlated args: %s", ev.Args)
	}
}

func TestObserveUnknownResultIsIgnored(t *testing.T) {
	stub := buildStub(t)
	e := engineWith(t, nil, []Spec{{Command: stub, Args: []string{"echo", filepath.Join(t.TempDir(), "x")}}})
	e.Observe("tool_result", "never_seen", "", nil, false) // must not panic or spawn
}
