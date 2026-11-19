package modes

// What a shared-file card holds reaches the SCREEN on an idle session.
//
// Two caches sit between a share's late data and the terminal. The View caches
// each rendered message, and tui's own tests pin that one. The other is here:
// redraw keeps the whole built chat while the session is idle, keyed on the
// transcript revision. A path or a preview arrives through the carrier after
// the card is drawn and moves no revision, so that key has to count it, or the
// card stays as first painted until a resize happens to evict it.
//
// A busy turn hides the problem, because redraw rebuilds every frame while one
// runs. The session that shows it is the resumed one: the transcript loads, the
// first idle frame draws the cards and asks for their data, and the answer
// lands with nothing else left to change.
//
// So these tests go through redraw and read the terminal. A test that called
// View.Build would rebuild the chat by construction and pass either way.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// idleShareSession is a TUI painting to a fake terminal, idle, with one share
// already in its transcript, as a resumed session has.
func idleShareSession(t *testing.T, c *shareCarrier, share core.SharedFile) (*Interactive, *tuitest.FakeTerm) {
	t.Helper()
	t.Setenv("TERM_PROGRAM", "")
	term := tuitest.NewFakeTerm(120, 40)
	noImages := false
	// Run() is not started: redraw is called on the test's own goroutine, so
	// the only concurrency is the fetch under test.
	i := NewInteractive(InteractiveConfig{
		Terminal:            term,
		Theme:               tui.Dark,
		Model:               "test-model",
		Provider:            "test",
		CWD:                 testsupport.TempDir(t),
		TervaHome:           testsupport.TempDir(t),
		Version:             "v0.0.0-test",
		InlineImagesEnabled: &noImages,
	})
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"
	i.cfg.CarrierLocal = true
	cols, rows := term.Size()
	i.rend.Resize(cols, rows)

	i.mu.Lock()
	i.appendCarrierToolResultLocked(toolResultWithShares(share.CallID, share))
	i.mu.Unlock()
	return i, term
}

func TestAPathThatLandsOnAnIdleSessionReachesTheScreen(t *testing.T) {
	const path = "/store/s1/shr_a-deliverable.pdf"
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "deliverable.pdf", path)}
	c.listRelease = make(chan struct{})
	i, term := idleShareSession(t, c, core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "deliverable.pdf", Kind: "document", Size: 2048,
	})

	// The first idle frame: the card is drawn, and the listing is asked for
	// and held open.
	i.redraw()
	first := term.Screen().Text()
	if !strings.Contains(first, "deliverable.pdf") {
		t.Fatalf("the card never reached the screen, so this test cannot see a path arrive:\n%s", first)
	}
	if strings.Contains(first, path) {
		t.Fatalf("the path is on screen before the listing answered:\n%s", first)
	}

	close(c.listRelease)
	waitForPath(t, i, "shr_a")
	i.redraw()

	if screen := term.Screen().Text(); !strings.Contains(screen, path) {
		t.Errorf("the path landed and the idle frame did not repaint the card:\n%s", screen)
	}
}

// The same cache, for the bytes. The metadata line is asserted because it is
// what the card draws from fetched bytes on a terminal with no image protocol,
// which is the terminal this fixture is.
func TestAPreviewThatLandsOnAnIdleSessionReachesTheScreen(t *testing.T) {
	const previewOnly = "image - image/png"
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("\x89PNG\r\n\x1a\nnot a real png, only bytes")})
	c.release = make(chan struct{})
	i, term := idleShareSession(t, c, core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "chart.png", Kind: "image", Mime: "image/png", Size: 32,
	})

	i.redraw()
	first := term.Screen().Text()
	if !strings.Contains(first, "chart.png") {
		t.Fatalf("the card never reached the screen:\n%s", first)
	}
	if strings.Contains(first, previewOnly) {
		t.Fatalf("the preview is on screen before the fetch answered:\n%s", first)
	}

	close(c.release)
	waitForPreview(t, i, "shr_a")
	i.redraw()

	if screen := term.Screen().Text(); !strings.Contains(screen, previewOnly) {
		t.Errorf("the bytes landed and the idle frame did not repaint the card:\n%s", screen)
	}
}
