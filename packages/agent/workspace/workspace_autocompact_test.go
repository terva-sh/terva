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
	if err := s.prompt("hi", nil); err != nil {
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
