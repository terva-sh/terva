package workspace

import (
	"context"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// prefillGatedClient advertises ContinuesAssistantPrefill and streams a fixed
// continuation once released — the turn-test twin of gatedTurnClient for the
// continue path.
type prefillGatedClient struct {
	release chan struct{}
	cont    string
}

func (c *prefillGatedClient) Name() string { return "prefill-gated-fake" }
func (c *prefillGatedClient) Capabilities() provider.ClientCapabilities {
	return provider.ClientCapabilities{ContinuesAssistantPrefill: true}
}
func (c *prefillGatedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		<-c.release
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.cont}},
		}}
	}()
	return out, nil
}

// TestContinueTurnMergesAndPersists drives turn.continue through a real
// prefill-continuation: the streamed text merges onto the trailing assistant
// message in place (no new message), and the merge persists as a replace amend
// that a reload reconstructs.
func TestContinueTurnMergesAndPersists(t *testing.T) {
	cl := &prefillGatedClient{release: make(chan struct{}), cont: " and vanished into the trees."}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	base := []provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "The knight rode on,")}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)

	if err := s.continueTurn(s.agent.TranscriptEpoch()); err != nil {
		t.Fatalf("continueTurn: %v", err)
	}
	close(cl.release)
	drainUntil(t, sub, "done")
	// The durable replace amend is written in launchTurn's afterTurn
	// (persistContinue), which runs after the agent's own "done" but before the
	// authoritative turn-end snapshot. Wait for that snapshot (as the retry test
	// does) so the reopen below does not race the persist.
	drainUntil(t, sub, ctrlproto.EventSnapshot)

	// Merged IN PLACE: still two messages, the last one extended.
	const want = "The knight rode on, and vanished into the trees."
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", want}) {
		t.Errorf("after continue = %v, want [u0 %q]", got, want)
	}
	// The extension persisted as a replace amend — a reload reconstructs it.
	if _, reloaded, err := core.OpenSession(s.sess.Path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else if got := reviseTexts(reloaded); !reflect.DeepEqual(got, []string{"u0", want}) {
		t.Errorf("reloaded = %v, want [u0 %q]", got, want)
	}
}

// TestContinueTurnGuards proves continue refuses the cases that have nothing to
// extend, a stale view, or a provider that can't continue a prefill — before
// starting any turn.
func TestContinueTurnGuards(t *testing.T) {
	// A prefill-capable provider, but no trailing assistant to continue.
	cap := &prefillGatedClient{release: make(chan struct{}), cont: "x"}
	s := newTurnTestSession(t, cap)
	s.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0")})
	if err := s.continueTurn(s.agent.TranscriptEpoch()); err == nil {
		t.Error("continue with no trailing assistant should error")
	}
	// A stale epoch is refused.
	s.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")})
	if err := s.continueTurn(s.agent.TranscriptEpoch() + 999); err == nil {
		t.Error("continue with a stale epoch should be refused")
	}
	// A provider that cannot continue a prefill is a bad request, even with a
	// trailing assistant present.
	nocap := newTurnTestSession(t, &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})})
	nocap.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")})
	if err := nocap.continueTurn(nocap.agent.TranscriptEpoch()); err == nil {
		t.Error("continue on a non-prefill provider should be refused")
	}
	if s.busy() || nocap.busy() {
		t.Error("a refused continue must not leave a turn running")
	}
}
