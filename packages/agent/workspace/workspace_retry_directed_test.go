package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// directedMsg is a line the USER authored into the scene — what post.line writes:
// an assistant message tagged with the directed source.
func directedMsg(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Meta:    map[string]string{core.MetaSource: core.MetaDirected},
	}
}

// TestRetryRefusesToDiscardDirectedLines guards a footgun that silently destroyed
// authored work.
//
// A regenerate retracts a span and generates a fresh one. In a Stage scene that
// span is not necessarily a model take: directed posts land as assistant messages,
// so a user who narrates a beat and then hits ↻ would have watched their own
// writing disappear with the model's — no warning, no undo.
//
// The protection is now the ANCHOR rather than a refusal (lastResponseStart stops
// at authored lines), so this fixture — a scene ending on two directed beats — has
// no model output after the boundary at all. It is still refused, and still
// without mutating anything; what changed is that the refusal now says what is
// actually true, instead of describing damage it was about to do.
func TestRetryRefusesToDiscardDirectedLines(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	// A completed exchange, then two lines the user wrote into the scene — the
	// exact shape a session lands in after narrating with post.line.
	base := []provider.Message{
		swipeMsg(provider.RoleUser, "u0"),
		swipeMsg(provider.RoleAssistant, "a0"),
		directedMsg("*Mistress Elira reads the note.*"),
		directedMsg("*The fitting passed in a blur.*"),
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()})
	if err == nil {
		t.Fatal("retry regenerated over user-authored lines: both directed beats would have been " +
			"destroyed with no warning. It must refuse instead.")
	}
	if !strings.Contains(err.Error(), "you wrote") {
		t.Errorf("refusal does not explain what it protected: %v", err)
	}

	// Refusing must not be a partial mutation: the transcript is untouched.
	if got := len(s.agent.Messages()); got != len(base) {
		t.Errorf("transcript has %d messages after a refused retry, want %d — a refusal must not mutate", got, len(base))
	}
	if got := reviseTexts(s.agent.Messages()); got[len(got)-1] != "*The fitting passed in a blur.*" {
		t.Errorf("the last authored line did not survive the refusal: %v", got)
	}
}

// TestRetryStillRegeneratesAPlainModelTake — the guard is scoped to authored
// content. An ordinary response (no directed lines in the span) still regenerates,
// so the footgun fix does not cost the feature.
func TestRetryStillRegeneratesAPlainModelTake(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()}); err != nil {
		t.Fatalf("retry over a plain model take should be allowed: %v", err)
	}
	close(cl.release)
}

// TestRetryAfterAdvanceFromAColdOpen: SD5 opens a scene on a COLD OPEN, which is
// an assistant message tagged directed — so a scene you advance into with ▶
// instead of typing holds no user message at all.
//
// Anchoring on the last USER message meant there was no anchor, and ↻ answered
// "nothing to retry" with a perfectly real model take on screen. That is the
// shape every next-scene session starts in, so the button read as broken exactly
// where the feature was newest.
func TestRetryAfterAdvanceFromAColdOpen(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{
		directedMsg("*Dawn finds the shop cold.*"), // the cold open
		swipeMsg(provider.RoleAssistant, "*She sets the kettle on.*"),
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()}); err != nil {
		t.Fatalf("retry after an advance from a cold open was refused: %v", err)
	}

	// Assert BEFORE releasing the gated client. The retract completes inside
	// retry(); releasing lets the replacement turn append its own take, so an
	// assertion after the release is racing that append and reads two messages
	// whenever the turn goroutine wins. Seen once under full-suite load, as
	// "[*Dawn finds the shop cold.* ok]" — the retract had worked perfectly and
	// the reply had simply already landed.
	if got := s.agent.Messages(); len(got) != 1 || reviseText(got[0]) != "*Dawn finds the shop cold.*" {
		t.Errorf("the cold open did not survive the retract: %v", reviseTexts(s.agent.Messages()))
	}
	close(cl.release)
}

// TestRetryRegeneratesTheReplyToADirectedLine is the case the old anchor got
// backwards. A scene where you narrate a beat and the model answers it ends
// "[directed, model reply]" — the reply IS a take, and the only reason retry used
// to refuse was that the span it computed swept your line in with it. Protecting
// authored work by declining to work is not protecting it.
func TestRetryRegeneratesTheReplyToADirectedLine(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{
		swipeMsg(provider.RoleUser, "u0"),
		swipeMsg(provider.RoleAssistant, "a0"),
		directedMsg("*Mistress Elira reads the note.*"),
		swipeMsg(provider.RoleAssistant, "*Her jaw tightens.*"),
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()}); err != nil {
		t.Fatalf("the model's reply to a directed line should be retryable: %v", err)
	}
	// Before the release, for the reason given above: the replacement turn
	// appends a fourth message the moment it is unblocked, and this assertion
	// counts messages.
	got := reviseTexts(s.agent.Messages())
	if len(got) != 3 {
		t.Fatalf("retract took the wrong span: %v", got)
	}
	if got[2] != "*Mistress Elira reads the note.*" {
		t.Errorf("the authored line did not survive: %v", got)
	}
	close(cl.release)
}

// A scene ending on your own line has no take under the button, and saying
// "nothing to retry" there reads as a malfunction when a reply is plainly on
// screen. It should say which kind of line it is looking at.
func TestRetryExplainsAnAuthoredEnding(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{
		swipeMsg(provider.RoleUser, "u0"),
		swipeMsg(provider.RoleAssistant, "a0"),
		directedMsg("*She closes the ledger.*"),
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()})
	if err == nil {
		t.Fatal("a scene ending on an authored line has no model take to regenerate")
	}
	if !strings.Contains(err.Error(), "you wrote") {
		t.Errorf("the refusal does not say what it is looking at: %v", err)
	}
}

// The greeting stays out of reach. A card greeting (not directed, no user message
// before it) is card content with its own swipe, so a transcript that is nothing
// but a greeting — or a greeting with advances stacked on it — is not a retry
// target, and must not be, or ↻ would eat the character's opening.
func TestRetryLeavesTheGreetingAlone(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{
		swipeMsg(provider.RoleAssistant, "*She looks up.* \"Oh — hello.\""),
		swipeMsg(provider.RoleAssistant, "*The shop settles.*"),
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()}); err == nil {
		t.Error("a greeting-rooted transcript must not be retractable — the greeting would go with it")
	}
	if got := len(s.agent.Messages()); got != 2 {
		t.Errorf("a refused retry mutated the transcript: %d messages", got)
	}
}
