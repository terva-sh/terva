package workspace

// Directed authorship — the daemon half of Stage's "post" verb (Phase 6).
//
// The suggest surface drafts a line and hands it back for the user to approve
// and edit without ever touching the transcript. PostLine is the other half:
// once the user approves a character's or the narrator's line, it commits that
// line INTO the transcript as an attributed assistant message. It is not a model
// turn — nothing is generated here — so it just appends, persists, and
// broadcasts, exactly as a normal turn's message lands.

import (
	"context"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

var _ ctrlproto.DirectController = (*Workspace)(nil)

// PostLine commits an approved directed line to a chat or play session.
func (w *Workspace) PostLine(_ context.Context, sess string, p ctrlproto.PostLineParams) error {
	s, err := w.directSession(sess)
	if err != nil {
		return err
	}
	return s.postDirected(p.Actor, p.Text)
}

// DirectTurn runs one turn steered by an out-of-character direction (Phase 6b).
func (w *Workspace) DirectTurn(_ context.Context, sess string, p ctrlproto.DirectTurnParams) error {
	s, err := w.directSession(sess)
	if err != nil {
		return err
	}
	return s.directTurn(p.Text)
}

// AdvanceTurn runs one turn on the transcript as it stands — Stage's "▶ Advance".
func (w *Workspace) AdvanceTurn(_ context.Context, sess string) error {
	s, err := w.directSession(sess)
	if err != nil {
		return err
	}
	return s.advance()
}

// directSession resolves sess and gates it to an immersive (chat or play)
// session: directed authorship is a Stage concept, not a plain agent transcript.
func (w *Workspace) directSession(sess string) (*wsSession, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	if s.sess == nil || s.sess.Meta.Experience == "" {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("directed authorship is only available in a chat or play session"))
	}
	return s, nil
}

// directTurn runs one turn steered by an out-of-character direction — the
// "apply-as-direction" disposition of directed authorship (Phase 6b). Unlike
// postDirected (which commits an approved line and never runs the model), this
// hands the model a [Direction] steer and lets it write the next beat: the
// direction rides the transcript as a [Direction] user message (the convention
// cast.speak established), so it reuses the whole turn + attribution path. ErrBusy
// while a turn is in flight (beginTurn).
func (s *wsSession) directTurn(text string) error {
	body := strings.TrimSpace(text)
	if body == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("direct: nothing to direct"))
	}
	return s.promptBlocks(directionDirective(body), nil)
}

// advance runs one turn against the transcript as it stands — no user message,
// no direction, nothing appended but the model's own reply. It is the fourth
// corner of Stage's turn grid: the composer writes the user's line, suggest drafts
// the user's line, postDirected authors the character's line, and this asks the
// model for the character's line.
//
// Deliberately NOT promptBlocks: that persists a synthetic user message (the
// convention cast.speak and directTurn share), which would litter the scene with a
// steer the user never wrote. The stage cue instead rides the ephemeral tail for
// exactly this turn (core.Agent.Advance) — which is also what keeps the request
// from ending on a bare assistant message after a run of directed lines. ErrBusy
// while a turn is in flight (beginTurn).
//
// In a World that routes (W3b), advance IS the meta-narrator's moment: with no
// new player message to answer, "who does the next beat belong to" is the whole
// question, so the router picks and the pick voices — the bound character's
// pick staying exactly the agent.Advance above. With one character (or
// coordination off) there is nothing to decide, and advance is unchanged.
func (s *wsSession) advance() error {
	if len(s.agent.Messages()) == 0 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("advance: there is no scene to continue yet"))
	}
	turnCtx, err := s.beginTurn()
	if err != nil {
		return err
	}
	// The advance response is a fresh tail with no alternatives, exactly as a
	// prompt's is — the prior span (a greeting, a swipe choice, a directed line)
	// is now history.
	s.clearTail()
	routes := s.worldRoutes()
	s.launchTurn(turnCtx, func(ctx context.Context) error {
		if routes {
			if pick := s.pickSpeaker(ctx, ""); !pick.bound {
				return s.voiceLine(ctx, pick, "")
			}
		}
		return s.agent.Advance(ctx, nil)
	}, nil)
	return nil
}

// directionMark opens an out-of-character steer. It rides the transcript text
// rather than the message meta because the model has to READ it — meta never
// reaches the provider — which is also why recognizing one again is string work.
const directionMark = "[Direction]"

// directionDirective wraps an out-of-character direction in the [Direction]
// marker the model reads as a steer (not the player's dialogue) and the Stage
// client renders as a de-emphasized 🎬 note. Shared with cast.speak so both
// directed turns carry one convention.
func directionDirective(text string) string {
	return directionMark + " " + text
}

// directionBody is directionDirective read back: the steer without its marker,
// and whether the message was one at all.
//
// It exists because until now this convention had a WRITER in Go and a reader
// only in TypeScript (the Stage store's directionBody), so every Go surface that
// walked a transcript saw a steer as ordinary player dialogue and had no way not
// to. The story export is the first one where that was visibly wrong; it will
// not be the last. Same name as the TS function on purpose — if the convention
// ever changes, both ends should be findable by one grep.
func directionBody(text string) (string, bool) {
	if !strings.HasPrefix(text, directionMark) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, directionMark)), true
}

// postDirected appends a user-directed line into the live transcript as an
// assistant message attributed to actor (empty = the narrator), persists it, and
// broadcasts. The text was drafted and approved by the user through suggest, so
// this is NOT a model turn: it mirrors how a normal turn's message lands.
//
// AppendMessage flushes any deferred greeting before writing its own row, so a
// post before the first real turn promotes the session exactly as a first user
// message would — leaving disk and the live transcript aligned (the invariant a
// reload replays: live == replayed). The posted line becomes a fresh,
// non-swipeable tail. Rejected while a turn is in flight so it never races the
// turn's own appends.
func (s *wsSession) postDirected(actor, text string) error {
	body := strings.TrimSpace(text)
	if body == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("post: nothing to post"))
	}
	s.mu.Lock()
	busy := s.turnCancel != nil
	s.mu.Unlock()
	if busy {
		return ctrlproto.ErrBusy
	}

	meta := map[string]string{core.MetaSource: core.MetaDirected}
	if a := strings.TrimSpace(actor); a != "" {
		meta[core.MetaActor] = a
	}
	m := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: body}},
		Time:    time.Now(),
		Meta:    meta,
	}
	if err := s.sess.AppendMessage(m); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "post: %v", err)
	}
	s.agent.SetMessages(append(s.agent.Messages(), m))
	s.clearTail() // the post is a fresh tail, not a swipe over the prior span
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}
