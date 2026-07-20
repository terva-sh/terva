package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
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
// A regenerate retracts the whole span after the last USER message and generates a
// fresh one. In a Stage scene that span is not necessarily a model take: directed
// posts land as assistant messages, so a user who narrates a beat and then hits ↻
// would have watched their own writing disappear with the model's — no warning, no
// undo. Retry now refuses, and the refusal names the two things that do work.
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

	err := s.retry(s.agent.TranscriptEpoch())
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

	if err := s.retry(s.agent.TranscriptEpoch()); err != nil {
		t.Fatalf("retry over a plain model take should be allowed: %v", err)
	}
	close(cl.release)
}
