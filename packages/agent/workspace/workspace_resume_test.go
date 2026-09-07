package workspace

import (
	"encoding/json"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// cutShortMsg is an assistant message the provider interrupted: the text that
// landed, carrying the mark runLoop stamps when a turn ends on an error.
func cutShortMsg(text string) provider.Message {
	m := swipeMsg(provider.RoleAssistant, text)
	m.Meta = map[string]string{core.MetaIncomplete: "true"}
	return m
}

// toolRoundMsgs is a completed tool round whose follow-up request never went
// out: the model asked for a tool, the tool answered, and nothing read the
// answer. A realistic pair rather than a text block wearing a tool role,
// because the request builders walk these blocks.
func toolRoundMsgs() []provider.Message {
	call := provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.Content{
			provider.ToolCallBlock{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		},
	}
	result := provider.Message{
		Role: provider.RoleTool,
		Content: []provider.Content{
			provider.ToolResultBlock{CallID: "call-1", Content: []provider.Content{provider.TextBlock{Text: "file body"}}},
		},
	}
	return []provider.Message{swipeMsg(provider.RoleUser, "u0"), call, result}
}

// TestResumeTurnAfterAUserMessageAnswersIt is the reported bug. The transcript
// ends on the words the user typed, and before turn.resume there was no way to
// ask for the reply that never came.
//
// The count assertion is the load-bearing one: resume must not re-send the
// prompt. A fix that called Prompt with the same text would leave the user's
// question in the transcript twice and pay for it twice.
func TestResumeTurnAfterAUserMessageAnswersIt(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	s.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0")})

	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)

	if err := s.resumeTurn(s.agent.TranscriptEpoch()); err != nil {
		t.Fatalf("resumeTurn: %v", err)
	}
	close(cl.release)
	drainUntil(t, sub, "done")

	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "ok"}) {
		t.Errorf("after resume = %v; want [u0 ok] — one prompt, one reply", got)
	}
	users := 0
	for _, m := range s.agent.Messages() {
		if m.Role == provider.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Errorf("user messages = %d; want 1 — resume appends nothing, it only runs the loop", users)
	}
}

// TestResumeTurnAfterToolResultsRunsAnOrdinaryTurn covers the shape that is easy
// to forget: the tool ran, its results are on the transcript, and the request
// that would have read them never went out.
func TestResumeTurnAfterToolResultsRunsAnOrdinaryTurn(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	s.agent.SetMessages(toolRoundMsgs())

	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)

	if err := s.resumeTurn(s.agent.TranscriptEpoch()); err != nil {
		t.Fatalf("resumeTurn: %v", err)
	}
	close(cl.release)
	drainUntil(t, sub, "done")

	msgs := s.agent.Messages()
	if len(msgs) != 4 {
		t.Fatalf("message count = %d; want 4 (prompt, tool call, tool result, reply)", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || reviseText(last) != "ok" {
		t.Errorf("last message = %s/%q; want an assistant reply reading the tool results", last.Role, reviseText(last))
	}
}

// TestResumeTurnExtendsACutShortReply drives the third shape end to end: the
// reply the provider interrupted grows in place rather than becoming a second
// message, and the merge survives a reload as a replace amend.
func TestResumeTurnExtendsACutShortReply(t *testing.T) {
	cl := &prefillGatedClient{release: make(chan struct{}), cont: " and vanished into the trees."}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{swipeMsg(provider.RoleUser, "u0"), cutShortMsg("The knight rode on,")}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)

	if err := s.resumeTurn(s.agent.TranscriptEpoch()); err != nil {
		t.Fatalf("resumeTurn: %v", err)
	}
	close(cl.release)
	drainUntil(t, sub, "done")
	// persistContinue runs in launchTurn's afterTurn, after the agent's own
	// "done" but before the authoritative turn-end snapshot. Wait for that
	// snapshot so the reopen below does not race the persist.
	drainUntil(t, sub, ctrlproto.EventSnapshot)

	const want = "The knight rode on, and vanished into the trees."
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", want}) {
		t.Errorf("after resume = %v; want [u0 %q] — the reply grows in place", got, want)
	}
	if _, reloaded, err := core.OpenSession(s.sess.Path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else if got := reviseTexts(reloaded); !reflect.DeepEqual(got, []string{"u0", want}) {
		t.Errorf("reloaded = %v; want [u0 %q] — the merge did not persist", got, want)
	}
}

// TestResumeTurnGuards proves each refusal happens BEFORE a turn starts. A guard
// that refused after claiming the turn slot would wedge the session, which is a
// worse failure than the one this whole verb exists to fix.
func TestResumeTurnGuards(t *testing.T) {
	// A healthy session: the last turn produced its reply, so there is nothing
	// to resume.
	idle := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	idle.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")})
	if err := idle.resumeTurn(idle.agent.TranscriptEpoch()); err == nil {
		t.Error("resume on a session that is not stuck should be refused")
	}

	// A stale epoch means the client is acting on a transcript that moved. This
	// matters more for resume than for the other revision verbs, because the
	// client may have read the transcript before a restart.
	stale := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	stale.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0")})
	if err := stale.resumeTurn(stale.agent.TranscriptEpoch() + 999); err == nil {
		t.Error("resume with a stale epoch should be refused")
	}

	// The cut-short shape needs a provider that continues a prefill. This one
	// does not, so it is refused by name rather than by disabling the verb.
	nocap := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	nocap.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0"), cutShortMsg("half a")})
	if err := nocap.resumeTurn(nocap.agent.TranscriptEpoch()); err == nil {
		t.Error("the cut-short shape on a non-prefill provider should be refused")
	}

	if idle.busy() || stale.busy() || nocap.busy() {
		t.Error("a refused resume must not leave a turn running")
	}
}

// TestResumeTurnEpochZeroSkipsTheStalenessCheck pins a deliberate relaxation.
// The TUI tracks no transcript epoch, and resume names no index, so 0 means "do
// not check" here exactly as it does for conversation.history. The neighbouring
// guards are what keep that safe, so this test also proves they still fire at
// epoch 0 rather than being skipped along with the check.
func TestResumeTurnEpochZeroSkipsTheStalenessCheck(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	s.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0")})
	if s.agent.TranscriptEpoch() == 0 {
		t.Fatal("precondition: the live epoch must be non-zero, or this proves nothing")
	}

	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)
	if err := s.resumeTurn(0); err != nil {
		t.Fatalf("resume at epoch 0 should skip the check, not fail it: %v", err)
	}
	close(cl.release)
	drainUntil(t, sub, "done")

	// And the not-stuck guard still applies at epoch 0: the relaxation is about
	// staleness alone.
	idle := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	idle.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")})
	if err := idle.resumeTurn(0); err == nil {
		t.Error("epoch 0 must not skip the not-stuck refusal")
	}
}

// TestResumeTurnStillWorksWhereContinueRefuses is the point of adding a verb
// instead of widening turn.retry or turn.continue. On the same transcript, the
// two older verbs have nothing to act on and resume does.
func TestResumeTurnStillWorksWhereContinueRefuses(t *testing.T) {
	msgs := []provider.Message{swipeMsg(provider.RoleUser, "u0")}

	cont := newTurnTestSession(t, &prefillGatedClient{release: make(chan struct{}), cont: "x"})
	cont.agent.SetMessages(msgs)
	if err := cont.continueTurn(cont.agent.TranscriptEpoch()); err == nil {
		t.Error("continue has no trailing assistant to extend here; it must refuse")
	}

	retry := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	retry.agent.SetMessages(msgs)
	if err := retry.retry(ctrlproto.TurnRetryParams{Epoch: retry.agent.TranscriptEpoch()}); err == nil {
		t.Error("retry has no model take to regenerate here; it must refuse")
	}

	res := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	res.agent.SetMessages(msgs)
	if err := res.resumeTurn(res.agent.TranscriptEpoch()); err != nil {
		t.Errorf("resume is the verb for this transcript and it refused: %v", err)
	}
}
