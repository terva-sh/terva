package modes

// A carrier can decline to serve /shared in two different ways, and until this
// pass only one of them produced a sensible message.
//
// In-process, a carrier that does not implement SharedFilesController fails the
// type assertion in sharedCarrier() and the panel says "not available in this
// mode". Over a socket that assertion always succeeds — the client side is one
// concrete type that forwards whatever it is asked — so the same refusal comes
// back as a CodeUnsupported frame instead. That is the shape a replay carrier
// and a daemon with no share store both produce, and it was reported as
// "shared files: not supported" and "save failed: not supported": a fault the
// user would go looking for, rather than a mode that does not carry the
// feature.
//
// The other half of these tests is the line that must NOT move. CodeNotFound
// from a fetch means the sweeper already took the file, or the id belongs to
// another session. The panel is still listing a row that promises the file is
// there, so that has to keep reading as a failure. The sibling panels fold
// NotFound in with Unsupported because their surfaces use it for "switched off
// for this session"; here it means something else, and these tests are what
// stops someone copying that pattern over.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// A daemon with no share store answers the listing verb with CodeUnsupported.
// That is the same answer as a carrier that never served the verb at all, so it
// has to arrive as the same error.
func TestRefreshTranslatesUnsupportedIntoUnavailable(t *testing.T) {
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this host has no share store")
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"

	err := i.refreshSharedFiles()

	if !errors.Is(err, errSharedUnavailable) {
		t.Fatalf("a CodeUnsupported listing should read as unavailable, got %v", err)
	}
}

// The message the user actually sees when they open the panel on a carrier that
// does not serve it. Asserted at the dialog level, not just on the error value,
// because the translation only matters if it reaches the screen.
func TestOpeningSharedOnAnUnsupportedCarrierSaysItIsTheMode(t *testing.T) {
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this host has no share store")
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"

	i.openSharedDialog()

	if !strings.Contains(i.statusErr, "not available in this mode") {
		t.Errorf("opening /shared on an unsupported carrier should name the mode, got %q", i.statusErr)
	}
	// The raw wire text leaking through is the bug this replaced: it reads as a
	// malfunction and sends the user hunting for one.
	if strings.Contains(i.statusErr, "no share store") {
		t.Errorf("the wire message should not reach the status line, got %q", i.statusErr)
	}
	if i.sharedDialog.Active() {
		t.Error("the panel opened over a carrier that cannot fill it")
	}
}

// A real listing failure keeps its own message. Collapsing everything into
// "not available in this mode" would hide a broken daemon behind what looks
// like an intentional limitation.
func TestRefreshKeepsARealListingFailureVisible(t *testing.T) {
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeInternal, "the store is on fire")
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"

	err := i.refreshSharedFiles()

	if err == nil {
		t.Fatal("a failing listing should return an error")
	}
	if errors.Is(err, errSharedUnavailable) {
		t.Fatalf("an internal failure must not masquerade as an unsupported carrier: %v", err)
	}
	if !strings.Contains(err.Error(), "on fire") {
		t.Errorf("the real cause should survive, got %v", err)
	}
}

// Saving from a carrier with no share store never reached the disk, so it did
// not fail — there was nothing to fetch. "save failed" would send the user
// looking at permissions or free space for a problem that is not there.
func TestSaveOnAnUnsupportedCarrierDoesNotClaimTheSaveFailed(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this host has no share store")
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	})

	i.saveSharedFile("shr_a")

	notice := dialogNotice(i)
	if !strings.Contains(notice, "not available in this mode") {
		t.Errorf("save on an unsupported carrier should name the mode:\n%s", notice)
	}
	if strings.Contains(notice, "save failed") {
		t.Errorf("nothing was attempted, so nothing failed:\n%s", notice)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("an unsupported save left %d files behind", len(entries))
	}
}

// The line that must not move. A swept file also comes back as an error from
// the same call, but it IS a failure: the panel is still showing a row that
// says the file is there. Folding NotFound in with Unsupported — which is what
// /tasks and /workflows do, for their own good reasons — would turn a missing
// deliverable into "this mode does not do that".
func TestSaveStillReportsASweptFileAsAFailure(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeNotFound, "no such shared file")
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	})

	i.saveSharedFile("shr_a")

	notice := dialogNotice(i)
	if !strings.Contains(notice, "save failed") {
		t.Errorf("a swept file is a real failure and must read as one:\n%s", notice)
	}
	if strings.Contains(notice, "not available in this mode") {
		t.Errorf("a missing file is not a missing capability:\n%s", notice)
	}
}

// sharedUnsupported is what draws the line, so it is worth pinning directly:
// the codes either side of it, a bare error, and nil.
func TestSharedUnsupportedMatchesOnlyTheCapabilityAnswer(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unsupported", ctrlproto.Errorf(ctrlproto.CodeUnsupported, "no store"), true},
		{"not found stays a real failure", ctrlproto.Errorf(ctrlproto.CodeNotFound, "swept"), false},
		{"internal stays a real failure", ctrlproto.Errorf(ctrlproto.CodeInternal, "boom"), false},
		{"bad request stays a real failure", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "too large"), false},
		{"a plain error is not a wire answer", errors.New("unsupported"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sharedUnsupported(tc.err); got != tc.want {
				t.Errorf("sharedUnsupported(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
