package workspace

import (
	"slices"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
)

// Both vocabularies of tool activity read as the same starts and ends.
func TestWorkerToolActivityReadsBothVocabularies(t *testing.T) {
	msg := func(role string, blocks ...map[string]any) map[string]any {
		content := make([]any, len(blocks))
		for i, b := range blocks {
			content[i] = b
		}
		return map[string]any{"message": map[string]any{"role": role, "content": content}}
	}
	cases := []struct {
		name string
		ev   swarm.Event
		want []toolMark
	}{
		{"terva call", swarm.NewEvent("tool_call", map[string]any{"id": "c1", "name": "bash"}), []toolMark{{"c1", "bash"}}},
		{"terva result", swarm.NewEvent("tool_result", map[string]any{"id": "c1"}), []toolMark{{id: "c1"}}},
		{"claude tool_use", swarm.NewEvent("assistant_message", msg("assistant",
			map[string]any{"type": "text", "text": "Looking."},
			map[string]any{"type": "tool_use", "id": "t1", "name": "Bash"},
			map[string]any{"type": "tool_use", "id": "t2", "name": "Read"})),
			[]toolMark{{"t1", "Bash"}, {"t2", "Read"}}},
		{"claude tool_result", swarm.NewEvent("user_message", msg("user",
			map[string]any{"type": "tool_result", "tool_use_id": "t1"})), []toolMark{{id: "t1"}}},
		{"text only", swarm.NewEvent("assistant_message", msg("assistant", map[string]any{"type": "text", "text": "Done."})), nil},
		{"call without a name", swarm.NewEvent("tool_call", map[string]any{"id": "c1"}), nil},
		{"other", swarm.NewEvent("turn_start", map[string]any{}), nil},
	}
	for _, c := range cases {
		if got := workerToolActivity(c.ev); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The swarm's event hook hands a worker's tool activity to its events.
func TestTheWorkerHookReportsTools(t *testing.T) {
	var got []toolMark
	hooks, _ := swarmWorkers{}.hook(workerEvents{tool: func(id, name string) { got = append(got, toolMark{id, name}) }})
	hooks.OnEvent(swarm.NewEvent("tool_call", map[string]any{"id": "c1", "name": "bash"}))
	hooks.OnEvent(swarm.NewEvent("tool_result", map[string]any{"id": "c1"}))
	if want := []toolMark{{"c1", "bash"}, {id: "c1"}}; !slices.Equal(got, want) {
		t.Errorf("reported %v, want %v", got, want)
	}
}

func jevTool(t *testing.T, w *Workspace) string {
	t.Helper()
	return memberView(t, w, "crew", "jev").Status.Tool
}

// A worker member's tool shows in its status while it runs, and goes when
// it ends. A stale process shows nothing.
func TestAWorkerMemberShowsItsTool(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	waitTalkoot(t, "the turn", func() bool { return jevWorking(t, w) })
	var mu sync.Mutex
	var pushed []string
	stop := w.talkootWatch("crew", func(e talkootEvent) {
		if e.Kind != "status" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, st := range e.Status {
			if st.Member == "jev" {
				pushed = append(pushed, st.Tool)
			}
		}
	})
	defer stop()
	ev := fw.ev(t, "agent-1")
	ev.tool("c1", "bash")
	if got := jevTool(t, w); got != "bash" {
		t.Fatalf("tool = %q, want bash", got)
	}
	// The web view hears it as a status event, with no read.
	waitTalkoot(t, "the status event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(pushed, "bash")
	})
	ev.tool("c1", "")
	if got := jevTool(t, w); got != "" {
		t.Fatalf("tool = %q after it ended", got)
	}
	// A report from a process the member no longer runs draws nothing.
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	w.nextWorkerRun(run, "jev")
	ev.tool("c2", "read")
	if got := jevTool(t, w); got != "" {
		t.Errorf("a stale process drew the tool %q", got)
	}
}

// A busy run skips a worker's tool report rather than wait: the report runs
// on the worker's event path, and the router clears the tool at the turn end.
func TestABusyRunSkipsAToolReport(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	ev := fw.ev(t, "agent-1")
	run.mu.Lock()
	done := make(chan struct{})
	go func() {
		ev.tool("c1", "bash")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		run.mu.Unlock()
		t.Fatal("a tool report waited on a busy run")
	}
	run.mu.Unlock()
}

// A tool report does not wait for a flush in progress: the flush waits for
// the watchers, and the report runs on the worker's event path.
func TestAToolReportDoesNotWaitForAFlush(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	ev := fw.ev(t, "agent-1")
	run.flushMu.Lock()
	done := make(chan struct{})
	go func() {
		ev.tool("c1", "bash")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		run.flushMu.Unlock()
		t.Fatal("a tool report waited for a flush")
	}
	run.flushMu.Unlock()
}
