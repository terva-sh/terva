package workspace

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// saturatedTurnClient serves one normal turn whose usage lands near the
// context ceiling, then answers the follow-up compaction request with a
// canned summary.
type saturatedTurnClient struct{ calls int32 }

func (c *saturatedTurnClient) Name() string { return "saturated-fake" }

func (c *saturatedTurnClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		if call == 1 {
			// 95% of claude-sonnet-4-5's 200k window.
			out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 190_000}}
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "ok"}},
			}}
			return
		}
		out <- provider.EventTextDelta{Delta: "summary"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "summary"}},
		}}
	}()
	return out, nil
}

// TestWorkspacePostTurnAutoCompact: the ctrlproto workspace host must
// mirror the legacy TUI engine's post-turn policy — a turn that ends past
// the auto-compact threshold condenses while idle, so the NEXT prompt
// doesn't pay the summarization latency inside its pre-turn check. This
// was a migration gap: PromptWithPolicy only checks before a prompt, so
// the default TUI pipeline never compacted after a saturated turn.
func TestWorkspacePostTurnAutoCompact(t *testing.T) {
	cl := &saturatedTurnClient{}
	tmp := testsupport.TempDir(t)
	sess, err := core.NewSession(tmp, tmp, "p", "claude-sonnet-4-5", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	s := &wsSession{
		id:    "autocompact",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		sess:  sess,
		agent: core.NewAgent(cl, "claude-sonnet-4-5", "", core.Registry{}),
		title: "titled",
	}
	s.agent.AddEventObserver(func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
	})
	// A transcript comfortably beyond keep-tail so CanCompact is true.
	seed := make([]provider.Message, 0, 8)
	for range 4 {
		seed = append(seed,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q"}}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a"}}},
		)
	}
	s.agent.SetMessages(seed)

	sub := s.hub.add(nil, true)
	if err := s.prompt("hi", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	drainUntil(t, sub, "done")
	// After the sealed-turn snapshot the host notices the saturated gauge,
	// announces, and compacts: notice → (summary request) → snapshot.
	notice, _ := drainUntil(t, sub, ctrlproto.EventNotice)
	if notice.Notice == nil || !strings.Contains(notice.Notice.Text, "compacting") {
		t.Fatalf("want auto-compact notice, got %+v", notice.Notice)
	}
	drainUntil(t, sub, ctrlproto.EventNotice) // "Compacted the conversation."

	if got := atomic.LoadInt32(&cl.calls); got != 2 {
		t.Fatalf("Stream calls = %d; want 2 (turn + compaction summary)", got)
	}
	msgs := s.agent.Messages()
	if len(msgs) == 0 || msgs[0].Meta["compaction"] != "true" {
		t.Fatalf("transcript head is not a compaction summary; %d messages", len(msgs))
	}
}

// saturatedThenFailingClient saturates the gauge on the turn, then refuses the
// compaction the way an overloaded provider does.
type saturatedThenFailingClient struct{ calls int32 }

func (c *saturatedThenFailingClient) Name() string { return "saturated-failing" }

func (c *saturatedThenFailingClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		if call == 1 {
			out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 190_000}}
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "ok"}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewAPIError(
			"openai-codex", "Our servers are currently overloaded. Please try again later.", true)}
	}()
	return out, nil
}

// Announce, then say how it ended. This host told clients a compaction had
// started and then discarded the error, so a failure was indistinguishable from
// one still running — on the path that is also its PRIMARY auto-compaction.
// Success and the nothing-to-do case were both already announced; only failure
// was mute, which is the one a user actually needs to act on.
func TestWorkspacePostTurnAutoCompactAnnouncesFailure(t *testing.T) {
	cl := &saturatedThenFailingClient{}
	tmp := testsupport.TempDir(t)
	sess, err := core.NewSession(tmp, tmp, "p", "claude-sonnet-4-5", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	agent := core.NewAgent(cl, "claude-sonnet-4-5", "", core.Registry{})
	agent.MaxRetries = 0 // the ladder is proven elsewhere; this is about the report
	s := &wsSession{
		id:    "autocompact-fail",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		sess:  sess,
		agent: agent,
		title: "titled",
	}
	s.agent.AddEventObserver(func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
	})
	seed := make([]provider.Message, 0, 8)
	for range 4 {
		seed = append(seed,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q"}}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a"}}},
		)
	}
	s.agent.SetMessages(seed)

	sub := s.hub.add(nil, true)
	if err := s.prompt("hi", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	drainUntil(t, sub, "done")

	start, _ := drainUntil(t, sub, ctrlproto.EventNotice)
	if start.Notice == nil || !strings.Contains(start.Notice.Text, "compacting") {
		t.Fatalf("want the auto-compact announcement, got %+v", start.Notice)
	}
	end, _ := drainUntil(t, sub, ctrlproto.EventNotice)
	if end.Notice == nil {
		t.Fatal("the announcement was never followed by an outcome — the failure is still mute")
	}
	if end.Notice.Level != "error" {
		t.Errorf("outcome notice level = %q; want %q so clients can surface it as a failure", end.Notice.Level, "error")
	}
	if !strings.Contains(end.Notice.Text, "overloaded") {
		t.Errorf("outcome notice = %q; it must quote the provider's reason, not just say something went wrong", end.Notice.Text)
	}

	// The transcript is untouched: a failed compaction summarized nothing.
	if msgs := s.agent.Messages(); len(msgs) > 0 && msgs[0].Meta["compaction"] == "true" {
		t.Error("a failed compaction still replaced the transcript")
	}
}
