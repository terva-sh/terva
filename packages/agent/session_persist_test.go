package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// onePassClient is a minimal provider.Client whose every turn streams a
// fixed text reply and completes.
type onePassClient struct{ reply string }

func (c *onePassClient) Name() string { return "fake" }

func (c *onePassClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventTextDelta{Delta: c.reply}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.reply}},
		}}
	}()
	return out, nil
}

// TestWireHeadlessSessionPersistWritesTurns is the regression test for
// bot mode's silent transcript loss: the daemon opened a session, told
// extensions about it, and never appended a single turn — every restart
// forgot the whole DM conversation while claiming to persist it. The
// helper under test is what botcmd (and ACP) now wire: per-message
// durable persistence plus the session identity terva_status reports.
func TestWireHeadlessSessionPersistWritesTurns(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "prov", "fake-model", "test")
	if err != nil {
		t.Fatal(err)
	}

	ag := core.NewAgent(&onePassClient{reply: "hello from the daemon"}, "fake-model", "", core.Registry{})
	build.WireHeadlessSessionPersist(ag, sess)

	if id, path := ag.SessionIdentity(); id == "" || path != sess.Path {
		t.Fatalf("helper did not adopt session identity: id=%q path=%q", id, path)
	}

	if err := ag.Prompt(context.Background(), "are you keeping notes?", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "are you keeping notes?") {
		t.Errorf("session file missing the user prompt:\n%s", got)
	}
	if !strings.Contains(got, "hello from the daemon") {
		t.Errorf("session file missing the assistant reply:\n%s", got)
	}
}

// twoTurnToolClient streams a tool call on its first turn and a final text
// reply on its second, so a caller can observe the transcript from INSIDE the
// task — during the tool's Execute, after turn one has been persisted but well
// before Prompt returns.
type twoTurnToolClient struct {
	turn  int
	reply string
}

func (c *twoTurnToolClient) Name() string { return "fake" }

func (c *twoTurnToolClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.turn++
	out := make(chan provider.Event, 4)
	first := c.turn == 1
	go func() {
		defer close(out)
		if first {
			call := provider.ToolCallBlock{ID: "T1", Name: "peek", Arguments: []byte(`{}`)}
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "calling the tool"}, call},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.reply}},
		}}
	}()
	return out, nil
}

// peekTool reads the session file the moment it runs — i.e. mid-task.
type peekTool struct {
	path string
	seen string
}

func (t *peekTool) Name() string            { return "peek" }
func (t *peekTool) Description() string     { return "reads the live session file" }
func (t *peekTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *peekTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	b, _ := os.ReadFile(t.path)
	t.seen = string(b)
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// TestSwarmChildTranscriptIsReadableMidTask pins the property the swarm child
// was missing: its transcript must be on disk while the task is still running,
// not written in one batch after it ends.
//
// A swarm sub-agent is a long-lived headless front end like ACP, rpc --session,
// and the workspace daemon, all of which wire WireHeadlessSessionPersist. The
// child instead did `start := len(ag.Messages())` … `WriteNewTranscript` after
// PromptWithPolicy, so a coordinator inspecting a working sub-agent found a file
// holding only its meta row and had no way to see in until the task finished.
//
// The assertion is made from INSIDE the task — a tool that reads the session
// file as it executes — because that is the only place the distinction shows.
// Both strategies produce an identical file once Prompt returns.
func TestSwarmChildTranscriptIsReadableMidTask(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "prov", "fake-model", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	peek := &peekTool{path: sess.Path}
	ag := core.NewAgent(&twoTurnToolClient{reply: "done"}, "fake-model", "", core.Registry{"peek": peek})
	build.WireHeadlessSessionPersist(ag, sess)

	if err := ag.Prompt(context.Background(), "investigate the thing", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if peek.seen == "" {
		t.Fatal("the tool never ran, so nothing was observed mid-task")
	}
	if !strings.Contains(peek.seen, "investigate the thing") {
		t.Errorf("the task's own prompt was not on disk mid-task:\n%s", peek.seen)
	}
	if !strings.Contains(peek.seen, "calling the tool") {
		t.Errorf("turn one's assistant message was not on disk mid-task:\n%s", peek.seen)
	}
	// The final reply comes after the peek, so its absence mid-task is the
	// control: this test would pass trivially if it were reading the finished
	// file rather than a live one.
	if strings.Contains(peek.seen, "done") {
		t.Errorf("mid-task read saw the FINAL reply; the peek is not observing a live file:\n%s", peek.seen)
	}
}
