package modes

// The persisted composer draft — stage 5 of
// docs/proposals/session-state-sidecar.md.
//
// Two things are being held here. First the POLICY: a draft the user wrote
// comes back as text, a line the model offered comes back as an offer, and
// neither ever overwrites what the user has typed since. Second the GOROUTINE:
// the restore arrives on a pump goroutine and the editor is main-loop-only
// state, so the write must be marshalled with runOnMain.
//
// The header of input_ghost_switch_test.go documents why -race cannot guard the
// second one and why the order is asserted instead. The same reasoning applies
// verbatim here, and so does its other trap: events are delivered with
// handleCarrierEvent rather than through fc.stream, because the fake carrier's
// channel cannot survive a session switch.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// draftCarrier is a fakeCarrier that also serves the two state verbs, and
// records every write with the session it named — the session is half of what
// the switch guard checks.
type draftCarrier struct {
	*fakeCarrier
	dmu    sync.Mutex
	stored ctrlproto.ComposerDraft
	saves  []savedDraft
	setErr error
	read   chan struct{} // one token per SessionState call
}

type savedDraft struct {
	sess  string
	draft ctrlproto.ComposerDraft
}

func newDraftCarrier() *draftCarrier {
	return &draftCarrier{fakeCarrier: newFakeCarrier(), read: make(chan struct{}, 8)}
}

func (d *draftCarrier) SessionState(_ context.Context, _ string) (ctrlproto.SessionStateResult, error) {
	d.dmu.Lock()
	stored := d.stored
	d.dmu.Unlock()
	select {
	case d.read <- struct{}{}:
	default:
	}
	if stored.Text == "" {
		return ctrlproto.SessionStateResult{}, nil
	}
	return ctrlproto.SessionStateResult{Composer: &stored}, nil
}

func (d *draftCarrier) SetComposerDraft(_ context.Context, sess string, p ctrlproto.ComposerDraft) error {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	d.saves = append(d.saves, savedDraft{sess: sess, draft: p})
	if d.setErr != nil {
		return d.setErr
	}
	d.stored = p
	return nil
}

func (d *draftCarrier) hold(draft ctrlproto.ComposerDraft) {
	d.dmu.Lock()
	d.stored = draft
	d.dmu.Unlock()
}

func (d *draftCarrier) written() []savedDraft {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	return append([]savedDraft(nil), d.saves...)
}

// typeInto puts text in the composer from the main loop, where the editor
// lives, and refreshes the mirror the way a keystroke would.
func typeInto(h *harness, text string) {
	done := make(chan struct{})
	h.i.runOnMain(func() {
		h.i.ed.SetValue(text)
		h.i.noteComposerDraft()
		close(done)
	})
	<-done
}

// awaitDraft waits for a save that satisfies want, and reports what it saw.
func awaitDraft(t *testing.T, dc *draftCarrier, why string, want func(savedDraft) bool) savedDraft {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range dc.written() {
			if want(s) {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no draft write matching %s within the deadline; saw %+v", why, dc.written())
	return savedDraft{}
}

// awaitComposer waits until the composer satisfies want.
func awaitComposer(t *testing.T, h *harness, why string, want func(composerState) bool) composerState {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if c := composer(h); want(c) {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := composer(h)
	t.Fatalf("composer never became %s: value=%q ghost=%q", why, got.value, got.ghost)
	return composerState{}
}

func draftHarness(t *testing.T, dc *draftCarrier) *harness {
	t.Helper()
	return startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = dc
		cfg.CarrierSession = "s1"
	})
}

// The restore must be QUEUED for the main loop, not written on the goroutine
// that fetched it. Same guard shape as TestTheRetractIsMarshalledOntoTheMainLoop
// and for the same reason: the editor has no lock, so an inline write is a data
// race against Editor.Render that -race demonstrably does not catch.
//
// The main loop is parked, a probe is queued behind the parking function, and
// only then is the snapshot delivered. The fetch is allowed to complete (the
// carrier hands back a token when it is read), so what the probe sees is not
// "the answer had not arrived yet" — it is "the answer arrived and waited its
// turn". A marshalled restore queues BEHIND the probe; an inline one has
// already written the composer.
func TestTheDraftRestoreIsMarshalledOntoTheMainLoop(t *testing.T) {
	dc := newDraftCarrier()
	dc.hold(ctrlproto.ComposerDraft{Text: "the parked draft", Source: ctrlproto.ComposerSourceUser})
	h := draftHarness(t, dc)

	parked, release := make(chan struct{}), make(chan struct{})
	h.i.runOnMain(func() { close(parked); <-release })
	<-parked

	var seen string
	probed := make(chan struct{})
	h.i.runOnMain(func() { seen = h.i.ed.Value(); close(probed) })

	h.i.handleCarrierEvent(snapshotFor("s1"))

	// Wait until the draft has actually been READ, so an inline write would
	// have had its chance. The short settle after it covers the few
	// instructions between the read returning and the write that follows it.
	select {
	case <-dc.read:
	case <-time.After(4 * time.Second):
		t.Fatal("the bind never read the session state")
	}
	time.Sleep(50 * time.Millisecond)

	close(release)
	<-probed

	if seen != "" {
		t.Fatalf("the composer already held %q when the main loop resumed;\n"+
			"the restore ran on the fetching goroutine instead of being marshalled with "+
			"runOnMain, which is a data race against Editor.Render", seen)
	}

	// And it does still restore, once the loop drains.
	awaitComposer(t, h, "restored", func(c composerState) bool { return c.value == "the parked draft" })
}

// The user's own words come back as TEXT, ready to keep editing or send.
func TestARestoredUserDraftLandsAsText(t *testing.T) {
	dc := newDraftCarrier()
	dc.hold(ctrlproto.ComposerDraft{Text: "half a question about", Source: ctrlproto.ComposerSourceUser})
	h := draftHarness(t, dc)

	h.i.handleCarrierEvent(snapshotFor("s1"))

	got := awaitComposer(t, h, "the restored draft", func(c composerState) bool { return c.value != "" })
	if got.value != "half a question about" {
		t.Errorf("value = %q", got.value)
	}
	if got.ghost != "" {
		t.Errorf("ghost = %q, want the user's own draft as text rather than an offer", got.ghost)
	}
}

// The model's line comes back as an OFFER. Restoring it as text would hand the
// machine's words back as the user's own prose, which is the one thing this
// feature must never do.
func TestARestoredSuggestionLandsAsAnOffer(t *testing.T) {
	dc := newDraftCarrier()
	dc.hold(ctrlproto.ComposerDraft{Text: "shall I run the tests?", Source: ctrlproto.ComposerSourceSuggestion})
	h := draftHarness(t, dc)

	h.i.handleCarrierEvent(snapshotFor("s1"))

	got := awaitComposer(t, h, "the restored offer", func(c composerState) bool { return c.ghost != "" })
	if got.ghost != "shall I run the tests?" {
		t.Errorf("ghost = %q", got.ghost)
	}
	if got.value != "" {
		t.Errorf("value = %q, want a suggestion restored as an offer and not as typed text", got.value)
	}
}

// A restore takes a round trip, and whatever the user typed meanwhile outranks
// what they typed before. It arrives as a refusable offer rather than
// overwriting live keystrokes.
func TestARestoreNeverOverwritesATypedComposer(t *testing.T) {
	dc := newDraftCarrier()
	dc.hold(ctrlproto.ComposerDraft{Text: "the older draft", Source: ctrlproto.ComposerSourceUser})
	h := draftHarness(t, dc)

	typeInto(h, "what I am typing now")
	h.i.handleCarrierEvent(snapshotFor("s1"))

	got := awaitComposer(t, h, "offered the older draft", func(c composerState) bool { return c.ghost != "" })
	if got.value != "what I am typing now" {
		t.Errorf("value = %q, want the live keystrokes untouched", got.value)
	}
	if got.ghost != "the older draft" {
		t.Errorf("ghost = %q, want the restored draft offered rather than planted", got.ghost)
	}
}

// Typing produces one write, and only once the composer settles.
//
// The delay is half the feature: the animation tick runs every 120ms, so
// without a debounce a paragraph would be a few hundred whole-file round trips
// through the daemon.
func TestTheDraftIsSavedOnceTheComposerSettles(t *testing.T) {
	dc := newDraftCarrier()
	h := draftHarness(t, dc)

	typeInto(h, "a message I have not sent")

	// Well inside the debounce, and several ticks wide: nothing may have gone
	// out yet.
	time.Sleep(composerDraftDebounce / 2)
	if w := dc.written(); len(w) != 0 {
		t.Errorf("wrote %+v before the composer settled — the debounce is not holding", w)
	}

	got := awaitDraft(t, dc, "the typed draft", func(s savedDraft) bool {
		return s.draft.Text == "a message I have not sent"
	})
	if got.sess != "s1" {
		t.Errorf("saved under session %q, want the bound one", got.sess)
	}
	if got.draft.Source != ctrlproto.ComposerSourceUser {
		t.Errorf("source = %q, want the user's own", got.draft.Source)
	}
}

// A standing offer is saved too, TAGGED — which is what lets the restore put it
// back as an offer instead of as the user's writing.
func TestAnUnacceptedOfferIsSavedAsASuggestion(t *testing.T) {
	dc := newDraftCarrier()
	h := draftHarness(t, dc)

	done := make(chan struct{})
	h.i.runOnMain(func() {
		h.i.ed.SetGhost("shall I run the tests?")
		h.i.noteComposerDraft()
		close(done)
	})
	<-done

	got := awaitDraft(t, dc, "the standing offer", func(s savedDraft) bool {
		return s.draft.Text == "shall I run the tests?"
	})
	if got.draft.Source != ctrlproto.ComposerSourceSuggestion {
		t.Errorf("source = %q, want it tagged as the machine's line", got.draft.Source)
	}
}

// Leaving a session writes its draft under the session being LEFT. Saving it
// under the new binding would file one conversation's unsent words in another.
func TestSwitchingSessionsSavesTheDraftUnderTheOldSession(t *testing.T) {
	dc := newDraftCarrier()
	h := draftHarness(t, dc)

	typeInto(h, "unsent, and about s1")
	if err := h.i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("switch session: %v", err)
	}

	got := awaitDraft(t, dc, "the draft of the session being left", func(s savedDraft) bool {
		return s.draft.Text == "unsent, and about s1"
	})
	if got.sess != "s1" {
		t.Errorf("saved under %q, want s1 — the session it was typed in", got.sess)
	}
}

// The over-cap refusal is told to the user, and NOT retried: it can never
// succeed, and a retry every debounce would be a stutter they cannot stop.
func TestAnOverCapRefusalIsShownAndNotRetried(t *testing.T) {
	dc := newDraftCarrier()
	dc.setErr = &ctrlproto.Error{Code: ctrlproto.CodeBadRequest, Message: "the draft is too large to keep"}
	h := draftHarness(t, dc)

	typeInto(h, "far too much prose")
	awaitDraft(t, dc, "the refused write", func(s savedDraft) bool {
		return s.draft.Text == "far too much prose"
	})

	// The user is told.
	deadline := time.Now().Add(4 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		done := make(chan struct{})
		h.i.runOnMain(func() { status = h.i.statusErr; close(done) })
		<-done
		if strings.Contains(status, "draft too long") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(status, "draft too long") {
		t.Errorf("statusErr = %q, want the user told their draft was not kept", status)
	}

	// And it is not tried again, over several debounce windows.
	before := len(dc.written())
	time.Sleep(2 * composerDraftDebounce)
	if after := len(dc.written()); after != before {
		t.Errorf("writes went from %d to %d — an over-cap draft is being retried", before, after)
	}
}
