//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// This is the cross-package end-to-end for F3
// (docs/reviews/2026-08-29-local-model-harness-friction-review.md): a loop that
// lives entirely inside a script, invisible to the model's own call.
//
// packages/core proves the plumbing with a fake tool, because it cannot import
// the concrete tools it dispatches. Nothing there can prove that the REAL
// code_execution reports its inner calls — delete the core.ReportInnerCall line
// from dispatchHostTool and every core test still passes. This one fails.

// stallScriptedClient is the smallest provider.Client that can drive a turn:
// it replays canned events per request and keeps the requests for inspection.
// The stall nudge rides Request.EphemeralContext, so the recorded requests are
// also where the assertion looks.
type stallScriptedClient struct {
	script func(n int) []provider.Event

	mu   sync.Mutex
	reqs []provider.Request
}

func (c *stallScriptedClient) Name() string { return "scripted" }

func (c *stallScriptedClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	n := len(c.reqs)
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()

	evs := c.script(n)
	out := make(chan provider.Event, len(evs))
	go func() {
		defer close(out)
		for _, e := range evs {
			out <- e
		}
	}()
	return out, nil
}

func (c *stallScriptedClient) calls() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.reqs...)
}

// scriptCallTurn is one assistant turn calling code_execution. The script text
// differs every time (so the dispatch key never repeats) and the program prints
// a different line every time (so the result fingerprint never repeats) — while
// the read underneath fails identically. That is the recorded shape, and it is
// invisible to both outer axes by construction.
func scriptCallTurn(n int) []provider.Event {
	script := fmt.Sprintf(
		`try { read("missing.txt"); } catch (e) { print("attempt %d could not load the page"); }`, n)
	args, _ := json.Marshal(codeExecArgs{Script: script})
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID:        fmt.Sprintf("call-%d", n),
				Name:      "code_execution",
				Arguments: json.RawMessage(args),
			}},
		}},
	}
}

func finishedTurn() []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventTextDelta{Delta: "done"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}},
	}
}

// loopingScriptAgent wires a real agent over the real code_execution and read
// tools, with the script binding dispatching into the same registry the model
// uses — the production shape, minus the approval gate.
func loopingScriptAgent(t *testing.T, turns int) (*core.Agent, *stallScriptedClient) {
	t.Helper()
	dir := testsupport.TempDir(t) // deliberately empty: missing.txt never exists

	rt := &ReadTool{CWD: dir}
	ce := &CodeExecutionTool{}
	ce.HostCall = realToolHost(map[string]core.Tool{"read": rt})

	client := &stallScriptedClient{script: func(n int) []provider.Event {
		if n >= turns {
			return finishedTurn()
		}
		return scriptCallTurn(n)
	}}

	a := core.NewAgent(client, "m", "you are terva",
		core.Registry{"code_execution": ce, "read": rt})
	a.MaxSteps = 20
	a.SetStallDetection(true)
	return a, client
}

// nudges returns the loop-check notes the harness put on the ephemeral tail.
func nudges(reqs []provider.Request) []string {
	var out []string
	for _, r := range reqs {
		if strings.Contains(r.EphemeralContext, "[loop check]") {
			out = append(out, r.EphemeralContext)
		}
	}
	return out
}

// TestCodeExecutionInnerLoopReachesTheStallDetector: five scripts, each one a
// different program printing a different line, each one failing on the same
// read. The model's own calls never repeat and never return an error, so before
// the inner-call reporting the detector saw nothing at all and the loop ran
// unremarked.
func TestCodeExecutionInnerLoopReachesTheStallDetector(t *testing.T) {
	a, client := loopingScriptAgent(t, 5)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	got := nudges(client.calls())
	if len(got) == 0 {
		t.Fatal("a script looping on the same failing host call must reach the stall detector")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "code_execution") {
		t.Errorf("the nudge should name the tool the model actually called:\n%s", joined)
	}
	// The detail names the inner tool, or the model reads an error it never saw
	// against a call it did not make.
	if !strings.Contains(joined, "read") {
		t.Errorf("the nudge should name the inner tool that kept failing:\n%s", joined)
	}
}

// The control. The same five scripts, the same everything, with the detector
// off: nothing is nudged. It pins the nudge above to the detector rather than
// to some other note that happens to ride the same tail.
func TestCodeExecutionInnerLoopSilentWhenDetectionIsOff(t *testing.T) {
	a, client := loopingScriptAgent(t, 5)
	a.SetStallDetection(false)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := nudges(client.calls()); len(got) != 0 {
		t.Fatalf("detection is off; nothing should be nudged:\n%s", strings.Join(got, "\n"))
	}
}

// A script whose host calls SUCCEED is working, not stalling, however many
// times the model runs one. This is the false positive the inner axis must
// never manufacture: same tool, same shape, no failure underneath.
func TestCodeExecutionSucceedingScriptsNeverNudge(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte("<html>ok</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &ReadTool{CWD: dir}
	ce := &CodeExecutionTool{}
	ce.HostCall = realToolHost(map[string]core.Tool{"read": rt})

	client := &stallScriptedClient{script: func(n int) []provider.Event {
		if n >= 5 {
			return finishedTurn()
		}
		script := fmt.Sprintf(`print("run %d: " + read("page.html").length);`, n)
		args, _ := json.Marshal(codeExecArgs{Script: script})
		return []provider.Event{
			provider.EventStart{Provider: "scripted"},
			provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}},
			provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role: provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{
					ID:        fmt.Sprintf("call-%d", n),
					Name:      "code_execution",
					Arguments: json.RawMessage(args),
				}},
			}},
		}
	}}
	a := core.NewAgent(client, "m", "you are terva",
		core.Registry{"code_execution": ce, "read": rt})
	a.MaxSteps = 20
	a.SetStallDetection(true)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := nudges(client.calls()); len(got) != 0 {
		t.Fatalf("scripts whose host calls succeed must not be nudged:\n%s", strings.Join(got, "\n"))
	}
}
