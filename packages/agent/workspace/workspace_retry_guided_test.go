package workspace

import (
	"context"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// capturingTurnClient records the request a turn actually sent. The gated fake
// next door only signals that a turn started; a guided retry's whole point is
// WHAT reaches the model, so this keeps the request itself.
type capturingTurnClient struct {
	mu      sync.Mutex
	req     provider.Request
	started chan struct{}
}

func (c *capturingTurnClient) Name() string { return "capturing-fake" }

func (c *capturingTurnClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.req = req
	c.mu.Unlock()
	select {
	case c.started <- struct{}{}:
	default:
	}
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "regenerated"}},
		}}
	}()
	return out, nil
}

// lastEphemeral returns the request's ephemeral tail — the trailing, cache-free
// block a stage cue rides on.
func (c *capturingTurnClient) lastEphemeral() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.req.EphemeralContext
}

// runGuidedRetry drives one retry over a completed exchange and returns the
// session plus the client that captured the resulting request.
func runGuidedRetry(t *testing.T, p ctrlproto.TurnRetryParams, take string) (*wsSession, *capturingTurnClient) {
	t.Helper()
	cl := &capturingTurnClient{started: make(chan struct{}, 1)}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{
		swipeMsg(provider.RoleUser, "What do you make of the letter?"),
		swipeMsg(provider.RoleAssistant, take),
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	p.Epoch = s.agent.TranscriptEpoch()
	if err := s.retry(p); err != nil {
		t.Fatalf("guided retry rejected: %v", err)
	}
	<-cl.started
	return s, cl
}

// TestGuidedRetryCarriesGuidanceAndPriorTake: the guidance reaches the model, and
// so does the take it is asking to improve on.
//
// The prior take is the load-bearing half. Regenerate guidance is almost always
// relative — "shorter", "less of that" — so a model that cannot see what it is
// being asked to change is guessing at the referent, and the retry comes back
// varying something the user never mentioned.
func TestGuidedRetryCarriesGuidanceAndPriorTake(t *testing.T) {
	const take = "*She turns the letter over, and over, and over, saying nothing for a long while.*"
	_, cl := runGuidedRetry(t, ctrlproto.TurnRetryParams{Guidance: "make her answer out loud"}, take)

	tail := cl.lastEphemeral()
	if !strings.Contains(tail, "make her answer out loud") {
		t.Errorf("the guidance never reached the model; ephemeral tail was:\n%s", tail)
	}
	if !strings.Contains(tail, take) {
		t.Errorf("the withdrawn take was not shown, so relative guidance has nothing to refer to; tail was:\n%s", tail)
	}
}

// TestGuidedRetryIgnorePriorWithholdsTheTake — the opt-out. When the last attempt
// went somewhere the user wants no trace of, showing it back would anchor the
// retry to exactly what they are trying to escape.
func TestGuidedRetryIgnorePriorWithholdsTheTake(t *testing.T) {
	const take = "*A wholly wrong turn the user wants gone.*"
	_, cl := runGuidedRetry(t, ctrlproto.TurnRetryParams{
		Guidance:    "start the scene somewhere else entirely",
		IgnorePrior: true,
	}, take)

	tail := cl.lastEphemeral()
	if !strings.Contains(tail, "somewhere else entirely") {
		t.Errorf("the guidance never reached the model; ephemeral tail was:\n%s", tail)
	}
	if strings.Contains(tail, take) {
		t.Errorf("ignore_prior still showed the withdrawn take, anchoring the retry to it; tail was:\n%s", tail)
	}
}

// TestPlainRetrySendsNoCue: an unguided regenerate must stay byte-for-byte what it
// always was — an independent sample from the same prefix. A cue leaking into it
// would silently change every existing client's ↻.
func TestPlainRetrySendsNoCue(t *testing.T) {
	_, cl := runGuidedRetry(t, ctrlproto.TurnRetryParams{}, "*The original take.*")

	tail := cl.lastEphemeral()
	if strings.Contains(tail, "[Retry]") {
		t.Errorf("a plain regenerate sent a retry cue; ephemeral tail was:\n%s", tail)
	}
	if strings.Contains(tail, "The original take") {
		t.Errorf("a plain regenerate quoted the take it is replacing, so it is no longer an independent sample:\n%s", tail)
	}
}

// TestGuidedRetryDoesNotPersistGuidance is the correctness claim behind keeping
// the cue request-scoped.
//
// A regenerate's takes all share the transcript prefix they were generated from.
// Persisting the guidance into that prefix would put it in front of takes that
// never saw it — swipe back one and the scene would show an instruction that did
// not apply to the beat under it. So the transcript after a guided retry must
// look exactly like the transcript after a plain one.
func TestGuidedRetryDoesNotPersistGuidance(t *testing.T) {
	const guidance = "make her answer out loud"
	s, _ := runGuidedRetry(t, ctrlproto.TurnRetryParams{Guidance: guidance}, "*Silence.*")

	for _, got := range reviseTexts(s.agent.Messages()) {
		if strings.Contains(got, guidance) {
			t.Fatalf("the guidance was written into the transcript (%q). Takes that never saw it would "+
				"render underneath it after a swipe.", got)
		}
	}
}

// TestRetryCueShape covers the composition directly — cheaper than a turn per
// branch, and it pins the one rule a reader is most likely to get backwards:
// ignore_prior is meaningless without guidance, because a plain regenerate never
// quotes the prior take in the first place.
func TestRetryCueShape(t *testing.T) {
	span := []provider.Message{swipeMsg(provider.RoleAssistant, "the withdrawn take")}

	if got := retryCue(ctrlproto.TurnRetryParams{}, span); got != "" {
		t.Errorf("no guidance must mean no cue, got %q", got)
	}
	if got := retryCue(ctrlproto.TurnRetryParams{IgnorePrior: true}, span); got != "" {
		t.Errorf("ignore_prior alone must not conjure a cue, got %q", got)
	}
	if got := retryCue(ctrlproto.TurnRetryParams{Guidance: "   "}, span); got != "" {
		t.Errorf("whitespace-only guidance is no guidance, got %q", got)
	}

	// A span with no assistant prose (a turn that produced only tool traffic)
	// still yields a usable cue — the guidance alone, with no empty quote block.
	got := retryCue(ctrlproto.TurnRetryParams{Guidance: "shorter"},
		[]provider.Message{swipeMsg(provider.RoleUser, "not a take")})
	if !strings.Contains(got, "shorter") {
		t.Errorf("guidance lost when the span had no prose to quote: %q", got)
	}
	if strings.Contains(got, "withdrawn attempt was") {
		t.Errorf("cue quoted an empty prior take: %q", got)
	}
}

// TestPriorTakeTextTruncates guards the token cost of a pathological span. Stage
// prose never reaches the cap (a take is bounded by its own max_tokens), but an
// agent turn that spent itself on tool traffic could quote back more than the
// regeneration costs.
func TestPriorTakeTextTruncates(t *testing.T) {
	huge := strings.Repeat("x", retryPriorLimit*2)
	got := priorTakeText([]provider.Message{swipeMsg(provider.RoleAssistant, huge)})
	if len(got) >= len(huge) {
		t.Errorf("prior take was not truncated: %d chars", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation was silent, so a reader cannot tell the quote is partial: %q", got[len(got)-40:])
	}
}
