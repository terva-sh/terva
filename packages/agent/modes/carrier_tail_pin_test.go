package modes

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func wireMessages(n int) []core.WireMessage {
	out := make([]core.WireMessage, 0, n)
	for range n {
		out = append(out, core.MessageToWire(provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "x"}},
		}))
	}
	return out
}

// A binding's first snapshot decides the first-paint tail cap, and the render
// pass consumes it exactly once. The cap exists so resuming a multi-thousand-
// message session doesn't block the first paint on every prior turn; before
// the pump owned the transcript it was computed at construction off the crutch
// agent, which no longer has the messages.
func TestCarrierTailPinCapsLongFirstSnapshot(t *testing.T) {
	i := &Interactive{}
	i.armCarrierBind()

	if _, ok := i.takeCarrierTailLimit(); ok {
		t.Fatal("armed but un-resolved pin handed out a limit")
	}

	i.setCarrierTranscript(wireMessages(initialResumeTailLimit + 1))

	limit, ok := i.takeCarrierTailLimit()
	if !ok || limit != initialResumeTailLimit {
		t.Fatalf("limit = %d,%v; want %d,true", limit, ok, initialResumeTailLimit)
	}
	if _, ok := i.takeCarrierTailLimit(); ok {
		t.Fatal("tail limit handed out twice; the render pass must consume it once")
	}
}

// A short first snapshot resolves to "no cap" rather than leaving the pin
// armed — otherwise a session that starts empty and grows past the limit would
// be capped mid-conversation.
func TestCarrierTailPinShortFirstSnapshotUncaps(t *testing.T) {
	i := &Interactive{}
	i.armCarrierBind()
	i.setCarrierTranscript(wireMessages(3))

	limit, ok := i.takeCarrierTailLimit()
	if !ok || limit != 0 {
		t.Fatalf("limit = %d,%v; want 0,true", limit, ok)
	}

	// Growing past the limit later must not re-cap: the pin is spent.
	i.setCarrierTranscript(wireMessages(initialResumeTailLimit + 1))
	if _, ok := i.takeCarrierTailLimit(); ok {
		t.Fatal("a later snapshot re-armed the cap")
	}
}

// Snapshots also ride compaction, clear, and every resubscribe. Re-capping
// there would collapse a transcript the user had scrolled open.
func TestCarrierTailPinIgnoresLaterSnapshots(t *testing.T) {
	i := &Interactive{}
	i.armCarrierBind()
	i.setCarrierTranscript(wireMessages(initialResumeTailLimit + 5))
	if _, ok := i.takeCarrierTailLimit(); !ok {
		t.Fatal("first snapshot did not resolve the pin")
	}

	// A reconnect's resubscribe replays a big snapshot. No new cap.
	i.setCarrierTranscript(wireMessages(initialResumeTailLimit + 500))
	if _, ok := i.takeCarrierTailLimit(); ok {
		t.Fatal("resubscribe snapshot re-capped the view")
	}

	// Only a fresh binding re-arms it.
	i.armCarrierBind()
	i.setCarrierTranscript(wireMessages(initialResumeTailLimit + 1))
	if limit, ok := i.takeCarrierTailLimit(); !ok || limit != initialResumeTailLimit {
		t.Fatalf("after re-bind: limit = %d,%v; want %d,true", limit, ok, initialResumeTailLimit)
	}
}
