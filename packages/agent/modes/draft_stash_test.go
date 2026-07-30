package modes

// The draft stash (ctrl+s): parking a half-written reply to answer the
// question the agent just asked, and getting it back afterwards.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// Ctrl+S parks the draft (visible in the "set aside:" row, editor
// emptied) and a second Ctrl+S brings it back. Driven as the raw 0x13
// byte a terminal actually sends.
func TestCtrlSParksAndRestoresDraft(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("hello there agent")
	h.waitText("hello there agent")

	h.term.Type("\x13")
	h.waitText("set aside:")
	h.waitScreen("editor emptied, draft only in the chip row", func(s *tuitest.Screen) bool {
		return strings.Count(s.Text(), "hello there agent") == 1
	})

	h.term.Type("\x13")
	h.waitGone("set aside:")
	h.waitText("hello there agent")
}

// The parked draft returns to the editor on its own once the message
// that displaced it is sent — the whole point of the feature.
func TestStashAutoReturnsAfterSend(t *testing.T) {
	fc := newFakeCarrier()
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	h.term.Type("my half written reply")
	h.waitText("my half written reply")
	h.term.Type("\x13")
	h.waitText("set aside:")

	h.term.Type("yes, use the second option\r")
	h.waitGone("set aside:")
	h.waitText("my half written reply")
}

// The nudge appears only for a draft typed while a turn is in flight,
// survives the turn's end (that is the moment the agent's question
// lands), and hands over to the "set aside:" row once the stash is
// used. Typing while idle never shows it.
func TestStashHintArmsOnlyWhileBusy(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	// Idle typing: no nudge, however much is typed.
	h.term.Type("an idle draft with plenty of text")
	h.waitText("an idle draft")
	if strings.Contains(h.term.Screen().Text(), "set this draft aside") {
		t.Fatalf("hint showed for an idle draft; screen:\n%s", h.term.Screen().Text())
	}

	// Same draft, but now a turn is in flight: the next key arms it.
	if !h.i.turns.claimSlot(func() {}) {
		t.Fatal("could not claim the turn slot")
	}
	h.term.Type("!") // any rune; refresh runs per key
	h.waitText("set this draft aside")

	// The turn ends — the hint must survive; this is when the question
	// the user needs to answer is actually on screen.
	h.i.turns.releaseSlot()
	h.term.Type("?")
	h.waitText("set this draft aside")

	// Stashing swaps the nudge for the parked-draft row.
	h.term.Type("\x13")
	h.waitGone("set this draft aside")
	h.waitText("set aside:")
}

// With a draft on both sides, ctrl+s swaps them — and the pending
// clipboard images swap with their drafts, so neither side's
// attachments can be dropped by the other side's submit.
func TestCtrlSSwapsDraftsAndImages(t *testing.T) {
	i := &Interactive{ed: tui.NewEditor("> ")}
	i.keymap = i.buildGlobalKeymap()

	imgA := clipboardImageAttachment{marker: "[clipboard image #1]", image: provider.ImageBlock{MimeType: "image/png"}}
	i.ed.SetValue("draft A [clipboard image #1]")
	i.clipboardImages = []clipboardImageAttachment{imgA}

	press := func() {
		if handled, _ := i.dispatchGlobalKey(t.Context(), tui.Key{Kind: tui.KeyCtrlS}); !handled {
			t.Fatal("ctrl+s was not claimed by the keymap")
		}
	}

	press() // park A
	if !i.ed.IsEmpty() || i.stash == nil || len(i.clipboardImages) != 0 {
		t.Fatalf("park: editor=%q stash=%v pending=%d", i.ed.Value(), i.stash != nil, len(i.clipboardImages))
	}

	imgB := clipboardImageAttachment{marker: "[clipboard image #2]", image: provider.ImageBlock{MimeType: "image/png"}}
	i.ed.SetValue("draft B [clipboard image #2]")
	i.clipboardImages = []clipboardImageAttachment{imgB}

	press() // swap: A back, B parked
	if got := i.ed.Value(); got != "draft A [clipboard image #1]" {
		t.Fatalf("editor after swap = %q, want draft A", got)
	}
	if len(i.clipboardImages) != 1 || i.clipboardImages[0].marker != imgA.marker {
		t.Fatalf("pending images after swap = %+v, want A's", i.clipboardImages)
	}
	if i.stash == nil || i.stash.ed.Value() != "draft B [clipboard image #2]" {
		t.Fatalf("stash after swap = %v, want draft B", i.stash)
	}
	if len(i.stash.images) != 1 || i.stash.images[0].marker != imgB.marker {
		t.Fatalf("stashed images after swap = %+v, want B's", i.stash.images)
	}
}

// A stashed draft's images sit out the interposed send: the answer's
// submit reconciles only its own pending list, and popStashedDraft
// returns the parked attachments intact.
func TestStashedImagesSurviveInterposedSend(t *testing.T) {
	i := &Interactive{ed: tui.NewEditor("> ")}
	i.keymap = i.buildGlobalKeymap()

	img := clipboardImageAttachment{marker: "[clipboard image #1]", image: provider.ImageBlock{MimeType: "image/png"}}
	i.ed.SetValue("look at [clipboard image #1] please")
	i.clipboardImages = []clipboardImageAttachment{img}

	if handled, _ := i.dispatchGlobalKey(t.Context(), tui.Key{Kind: tui.KeyCtrlS}); !handled {
		t.Fatal("ctrl+s was not claimed by the keymap")
	}

	// The interposed answer submits with an empty pending list, so the
	// reconcile attaches nothing and drops nothing of the parked draft's.
	text, imgs := preparePromptWithClipboardImages("just the answer", i.clipboardImages)
	if text != "just the answer" || len(imgs) != 0 {
		t.Fatalf("interposed submit = %q +%d images, want it untouched", text, len(imgs))
	}

	i.popStashedDraft()
	if got := i.ed.Value(); got != "look at [clipboard image #1] please" {
		t.Fatalf("restored draft = %q", got)
	}
	if len(i.clipboardImages) != 1 || i.clipboardImages[0].marker != img.marker {
		t.Fatalf("restored images = %+v, want the parked one", i.clipboardImages)
	}
}

// Marker numbering must not restart while a stash still holds numbered
// attachments: two live "[clipboard image #1]" markers are
// indistinguishable at submit time and the second image would silently
// drop. The counter does rewind once nothing holds a marker.
func TestClipboardMarkerNumberingSkipsStashedDrafts(t *testing.T) {
	i := &Interactive{ed: tui.NewEditor("> ")}

	if got := i.nextClipboardMarker(); got != "[clipboard image #1]" {
		t.Fatalf("first marker = %q", got)
	}
	i.clipboardImages = []clipboardImageAttachment{{marker: "[clipboard image #1]"}}

	// Draft (and its image) parked: the pending list empties, but #1 is
	// still alive inside the stash — the next paste must not reuse it.
	i.stash = &draftStash{ed: i.ed.State(), images: i.clipboardImages}
	i.clipboardImages = nil
	if got := i.nextClipboardMarker(); got != "[clipboard image #2]" {
		t.Fatalf("marker while stash holds #1 = %q, want #2", got)
	}

	// Everything released: numbering rewinds like it always has.
	i.stash = nil
	i.clipboardImages = nil
	if got := i.nextClipboardMarker(); got != "[clipboard image #1]" {
		t.Fatalf("marker after all released = %q, want #1", got)
	}
}
