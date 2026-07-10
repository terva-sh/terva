package workspace

// The side-chat surface — the daemon half of /btw.
//
// A side chat is an ephemeral, tool-less completion alongside a live session:
// the user asks a question against a frozen view of the conversation, reads the
// reply, and closes the overlay, leaving the session's transcript untouched.
// The model call, the credential, and the transcript are all daemon-side, so
// the surface belongs here — it was the last thing the TUI reached a live
// *core.Agent for (AgentFor, plan 4.1). The TUI now opens a snapshot, streams
// asks against it, and closes it, holding no agent at all.
//
// Frozen deliberately: a turn landing on the session mid-dialogue (a queued
// prompt, a paired chat DM, another device) must not shift the ground under a
// side chat already in progress. Open captures the system prompt, the
// transcript, and the client once; every ask runs against that capture plus the
// side chat's own prior exchanges, which the client carries.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// maxOpenSideChats bounds how many snapshots one session may hold at once. The
// TUI opens one per /btw overlay and closes it on dismiss; the cap only catches
// a client that leaks opens (a crash between open and close), so it is generous.
const maxOpenSideChats = 8

// sideChatSnapshot is a frozen view of a session, captured at open time. It
// holds a client rather than reaching back through the session, so an ask is
// unaffected by a model switch or a logout that lands on the live session while
// the overlay is up.
type sideChatSnapshot struct {
	system string
	msgs   []provider.Message
	client provider.Client
	model  string
}

// SideChatOpen freezes sess and returns an id naming the snapshot.
func (w *Workspace) SideChatOpen(ctx context.Context, sess string) (string, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return "", err
	}
	ag := s.agent
	if ag == nil || ag.Client == nil {
		// No credential resolved for this session yet (a credential-less boot
		// before /login). Nothing to complete against.
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "not logged in")
	}

	_, model := s.currentModel()
	snap := &sideChatSnapshot{
		system: ag.System,
		msgs:   ag.Messages(), // Messages returns a copy; the freeze is real
		client: ag.Client,
		model:  model,
	}

	s.sidechatMu.Lock()
	defer s.sidechatMu.Unlock()
	if len(s.sidechats) >= maxOpenSideChats {
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest,
			"too many open side chats (%d); close one first", len(s.sidechats))
	}
	if s.sidechats == nil {
		s.sidechats = map[string]*sideChatSnapshot{}
	}
	s.sidechatSeq++
	id := fmt.Sprintf("sc-%d", s.sidechatSeq)
	s.sidechats[id] = snap
	return id, nil
}

// SideChatAsk runs one question against the snapshot, preceded by the side
// chat's own prior exchanges. Blocks on the model; records nothing.
func (w *Workspace) SideChatAsk(ctx context.Context, sess, id string, prior []ctrlproto.SideChatTurn, question string) (string, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return "", err
	}
	s.sidechatMu.Lock()
	snap := s.sidechats[id]
	s.sidechatMu.Unlock()
	if snap == nil {
		return "", ctrlproto.Errorf(ctrlproto.CodeNotFound, "unknown side chat %q (reopen it)", id)
	}
	if strings.TrimSpace(question) == "" {
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "empty side-chat question")
	}

	// frozen system + frozen transcript + prior side-chat turns + this question.
	msgs := append([]provider.Message(nil), snap.msgs...)
	for _, t := range prior {
		if strings.TrimSpace(t.User) != "" {
			msgs = append(msgs, sideChatMessage(provider.RoleUser, t.User))
		}
		if strings.TrimSpace(t.Assistant) != "" {
			msgs = append(msgs, sideChatMessage(provider.RoleAssistant, t.Assistant))
		}
	}
	msgs = append(msgs, sideChatMessage(provider.RoleUser, question))

	req := provider.Request{
		Model:    snap.model,
		System:   snap.system,
		Messages: msgs,
		// No tools: a side chat is conversational, not agentic.
	}
	return streamText(ctx, snap.client, req)
}

// SideChatClose releases the snapshot. Unknown id is a no-op.
func (w *Workspace) SideChatClose(ctx context.Context, sess, id string) error {
	s := w.existing(sess)
	if s == nil {
		return nil
	}
	s.sidechatMu.Lock()
	delete(s.sidechats, id)
	s.sidechatMu.Unlock()
	return nil
}

func sideChatMessage(role provider.Role, text string) provider.Message {
	return provider.Message{
		Role:    role,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Time:    time.Now(),
	}
}

// streamText drains a one-off completion to its final text, honouring ctx
// cancellation. The daemon twin of the BtwDialog goroutine that used to run in
// the TUI against the crutch agent's client.
func streamText(ctx context.Context, cl provider.Client, req provider.Request) (string, error) {
	stream, err := cl.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventTextDelta:
			sb.WriteString(e.Delta)
		case provider.EventDone:
			if e.Err != nil {
				return "", e.Err
			}
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return sb.String(), nil
}
