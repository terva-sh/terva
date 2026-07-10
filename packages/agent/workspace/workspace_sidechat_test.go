package workspace

// The side-chat surface: an ephemeral completion frozen against a session,
// leaving its transcript untouched. The daemon half of /btw, and the seam that
// let plan 4.1 delete the last AgentFor reader.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// echoClient answers each request by echoing the last user message, and records
// every request it saw so a test can inspect what was actually sent.
type echoClient struct {
	mu   sync.Mutex
	reqs []provider.Request
}

func (c *echoClient) Name() string { return "echo" }

func (c *echoClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()

	last := ""
	if n := len(req.Messages); n > 0 {
		last = messageText(req.Messages[n-1])
	}
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "echo", Model: req.Model}
		out <- provider.EventTextDelta{Delta: "echo: " + last}
		out <- provider.EventDone{Stop: provider.StopEnd}
	}()
	return out, nil
}

func (c *echoClient) lastRequest(t *testing.T) provider.Request {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("no request reached the client")
	}
	return c.reqs[len(c.reqs)-1]
}

func messageText(m provider.Message) string {
	var sb strings.Builder
	for _, b := range m.Content {
		if tb, ok := b.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// sidechatTestSession builds a workspace with one live session whose agent has a
// system prompt, a seeded transcript, and the echo client.
func sidechatTestSession(t *testing.T, id string) (*Workspace, *wsSession, *echoClient) {
	t.Helper()
	w, s, _ := chatTestWorkspace(t, id)
	cl := &echoClient{}
	ag := core.NewAgent(cl, "fake-model", "the frozen system prompt", core.Registry{})
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "the original question"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "the original answer"}}},
	})
	s.agent = ag
	return w, s, cl
}

// Open freezes; Ask runs the completion with the frozen system + transcript +
// prior + question, in that order; the transcript is untouched throughout.
func TestSideChatAskUsesTheFrozenContext(t *testing.T) {
	w, s, cl := sidechatTestSession(t, "s1")
	before := len(s.agent.Messages())

	id, err := w.SideChatOpen(context.Background(), "s1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	prior := []ctrlproto.SideChatTurn{{User: "side q1", Assistant: "side a1"}}
	reply, err := w.SideChatAsk(context.Background(), "s1", id, prior, "the side question")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if reply != "echo: the side question" {
		t.Fatalf("reply = %q", reply)
	}

	req := cl.lastRequest(t)
	if req.System != "the frozen system prompt" {
		t.Fatalf("system = %q, want the frozen prompt", req.System)
	}
	// frozen transcript (2) + prior (2) + question (1).
	if len(req.Messages) != 5 {
		t.Fatalf("request carried %d messages, want 5 (2 frozen + 2 prior + question)", len(req.Messages))
	}
	got := []string{
		messageText(req.Messages[0]), messageText(req.Messages[1]),
		messageText(req.Messages[2]), messageText(req.Messages[3]),
		messageText(req.Messages[4]),
	}
	want := []string{"the original question", "the original answer", "side q1", "side a1", "the side question"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message[%d] = %q, want %q (order: frozen, then prior, then question)", i, got[i], want[i])
		}
	}
	// No tools on a side chat.
	if len(req.Tools) != 0 {
		t.Fatalf("side chat sent %d tools, want none", len(req.Tools))
	}

	// The session's own transcript never grew.
	if after := len(s.agent.Messages()); after != before {
		t.Fatalf("the session transcript changed: %d -> %d", before, after)
	}
}

// The snapshot is frozen at open: a turn appended to the session afterward does
// not reach a side chat already open.
func TestSideChatIsFrozenAtOpen(t *testing.T) {
	w, s, cl := sidechatTestSession(t, "s1")

	id, err := w.SideChatOpen(context.Background(), "s1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// A turn lands on the live session after the freeze.
	s.agent.SetMessages(append(s.agent.Messages(), provider.Message{
		Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "a later prompt"}},
	}))

	if _, err := w.SideChatAsk(context.Background(), "s1", id, nil, "q"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	req := cl.lastRequest(t)
	// frozen 2 + question 1 = 3; the later prompt must NOT appear.
	if len(req.Messages) != 3 {
		t.Fatalf("frozen snapshot leaked a later turn: %d messages, want 3", len(req.Messages))
	}
	for _, m := range req.Messages {
		if messageText(m) == "a later prompt" {
			t.Fatal("a turn appended after open reached a frozen side chat")
		}
	}
}

// A stale id — closed, or never issued — is a CodeNotFound error, not a panic
// and not a silent empty reply.
func TestSideChatAskUnknownID(t *testing.T) {
	w, _, _ := sidechatTestSession(t, "s1")
	id, _ := w.SideChatOpen(context.Background(), "s1")

	if err := w.SideChatClose(context.Background(), "s1", id); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := w.SideChatAsk(context.Background(), "s1", id, nil, "q")
	if err == nil {
		t.Fatal("asking a closed side chat succeeded")
	}
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v", err)
	}
}

// Closing the session releases every snapshot it holds.
func TestSideChatReleasedOnSessionClose(t *testing.T) {
	w, s, _ := sidechatTestSession(t, "s1")
	id, err := w.SideChatOpen(context.Background(), "s1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	s.close()

	// The registry is gone; asking must fail rather than complete against a
	// snapshot that outlived its session.
	if _, err := w.SideChatAsk(context.Background(), "s1", id, nil, "q"); err == nil {
		t.Fatal("a side chat survived the session close")
	}
}

// The open cap is enforced, so a client that leaks opens cannot pin unbounded
// frozen transcripts.
func TestSideChatOpenCap(t *testing.T) {
	w, _, _ := sidechatTestSession(t, "s1")
	for i := range maxOpenSideChats {
		if _, err := w.SideChatOpen(context.Background(), "s1"); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	if _, err := w.SideChatOpen(context.Background(), "s1"); err == nil {
		t.Fatalf("open past the cap of %d succeeded", maxOpenSideChats)
	}
}
