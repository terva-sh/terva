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
// side chat already in progress. Open captures the system prompt, the per-turn
// ephemeral tail, the transcript, and the client once; every ask runs against
// that capture plus the side chat's own prior exchanges, which the client
// carries.
//
// The tail is captured because it is NOT part of the system prompt — the agent
// assembles it per request, and it is where the bound user persona lives. A side
// chat that froze only the system prompt was the one surface in the session that
// did not know who it was talking to.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
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
	// ephemeral is the session's per-turn tail as it stood at open time: the
	// bound user persona (who you are, your stated gender and pronouns), the
	// author's note, the World lore in play. It is NOT part of the system prompt — the
	// agent assembles it per request — so freezing the system prompt alone left a
	// side chat as the one surface that did not know who it was talking to. It
	// would say "he" about a persona whose pronouns every other prompt had right.
	//
	// Rendered ONCE here rather than per ask, which is the same freeze the rest of
	// this struct gets: a note edit or a lore change landing mid-dialogue must not
	// shift the ground under an overlay already open.
	ephemeral string
}

// SideChatOpen freezes sess and returns an id naming the snapshot.
func (w *Workspace) SideChatOpen(ctx context.Context, sess string) (string, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return "", err
	}
	ag := s.agent
	if ag == nil || ag.Client() == nil {
		// No credential resolved for this session yet (a credential-less boot
		// before /login). Nothing to complete against.
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not logged in"))
	}

	_, model := s.currentModel()
	// FramePreview assembles with AssemblePeek, the side-effect-free twin. A real
	// turn's assembly records which lore entries fired, and opening an overlay
	// must not write that state on the session's behalf.
	frame := ag.FramePreview()
	snap := &sideChatSnapshot{
		system:    frame.SystemText(),
		msgs:      ag.Messages(), // Messages returns a copy; the freeze is real
		client:    ag.Client(),
		model:     model,
		ephemeral: frame.VolatileText(),
	}

	s.sidechatMu.Lock()
	defer s.sidechatMu.Unlock()
	if len(s.sidechats) >= maxOpenSideChats {
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest,
			"%s", i18n.T("too many open side chats (%d); close one first", len(s.sidechats)))
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
		return "", ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("unknown side chat %q (reopen it)", id))
	}
	if strings.TrimSpace(question) == "" {
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("empty side-chat question"))
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
		// The frozen per-turn tail, so the side chat knows the same things about
		// the scene and the player that the conversation it is asking about does.
		EphemeralContext: snap.ephemeral,
		// No tools: a side chat is conversational, not agentic.
	}
	out, usage, err := streamText(ctx, snap.client, req)
	s.recordSideChannelUsage(sideChannelSideChat, usage)
	return out, err
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

// The side-channel sources, as they land on the usage row on disk. Each names
// the surface that spent the tokens, in the vocabulary a reader of the session
// file sees, so the list is a format the way a row type is: rename one and
// every session already written stops answering for it. Add a constant here
// when a new one-off completion appears rather than passing a literal, so the
// set stays greppable from the one place that owns it.
const (
	sideChannelNextStep      = "next_step"      // the idle composer's suggestion, and /nextstep
	sideChannelSideChat      = "side_chat"      // a side chat turn
	sideChannelSuggest       = "suggest"        // the Stage reply drafter
	sideChannelRoute         = "route"          // the meta-narrator's speaker pick
	sideChannelVoice         = "voice"          // the line the picked speaker voices; it lands in the transcript, but it is billed outside Run, so the ledger sees it here
	sideChannelCardDoctor    = "card_doctor"    // the card doctor's read
	sideChannelSessionDoctor = "session_doctor" // the session doctor's read
	sideChannelNextScene     = "next_scene"     // the next-scene proposal
	sideChannelRealize       = "realize"        // realizing a proposed scene
	sideChannelTitle         = "title"          // the generated session title
)

// recordSideChannelUsage books a one-off completion's spend against this
// session's agent, under the source that spent it. Nil-safe on every hop: a
// cold session has no agent, and a stream that failed before its usage event
// yields a zero Usage the agent discards, so callers can hand back whatever
// streamText returned without guarding first.
func (s *wsSession) recordSideChannelUsage(source string, u provider.Usage) {
	if s == nil || s.agent == nil {
		return
	}
	s.agent.RecordSideChannelUsage(source, u)
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
//
// It returns the request's usage as well, and every caller is expected to book
// it (core.Agent.RecordSideChannelUsage). It used to drop EventUsage on the
// floor, which made every side-channel call — the router's pick, the voiced
// line, suggest, side chat — free as far as the session file was concerned.
// Returning it rather than exposing an opt-in variant is the point: a caller now
// has to look at the usage to ignore it.
func streamText(ctx context.Context, cl provider.Client, req provider.Request) (string, provider.Usage, error) {
	stream, err := cl.Stream(ctx, req)
	if err != nil {
		return "", provider.Usage{}, err
	}
	var sb strings.Builder
	var usage provider.Usage
	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventTextDelta:
			sb.WriteString(e.Delta)
		case provider.EventUsage:
			// Assign, don't accumulate: a provider emits exactly one EventUsage
			// per request (it folds its own cumulative refreshes internally).
			usage = e.Usage
		case provider.EventDone:
			if e.Err != nil {
				// The spend is real even when the turn errored out, so hand the
				// usage back rather than swallowing it with the error.
				return "", usage, e.Err
			}
		}
	}
	if ctx.Err() != nil {
		return "", usage, ctx.Err()
	}
	return sb.String(), usage, nil
}
