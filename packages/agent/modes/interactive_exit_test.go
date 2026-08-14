package modes

// What terva owes the shell it hands the terminal back to. terva renders on
// the main screen rather than the alternate one, so on exit the conversation
// stays where it was printed and the shell's next prompt prints wherever the
// cursor was left. Teardown therefore has to erase the live band AND leave the
// cursor below everything still painted — the two halves are one contract, and
// a sequence emitted after the parking can quietly undo it.

import (
	"strings"
	"testing"
)

// screenState reads the emulated screen as the returning shell would see it:
// every row, the joined text, and the row of the last thing still painted.
func screenState(h *harness) (text string, lastContent int) {
	rows := h.term.Screen().Rows()
	lastContent = -1
	for i, row := range rows {
		if strings.TrimSpace(row) != "" {
			lastContent = i
		}
	}
	return strings.Join(rows, "\n"), lastContent
}

// TestExitParksTheCursorBelowTheTranscript drives a real double-ctrl+c exit
// through Run and reads the emulated screen the way the returning shell sees
// it. The failure this pins was invisible to every renderer-level test:
// TeardownLog parked the cursor correctly and the mode-reset write that
// followed included DECSTBM (\x1b[r), which homes the cursor as a side effect
// of setting the scrolling region. The prompt then printed at row 0, on top of
// the conversation, with the rest of the frame still painted below it.
func TestExitParksTheCursorBelowTheTranscript(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
	})
	h.waitText("type /help")
	// The band must be ON screen before we tear it down, or "the band is
	// gone" below passes without ever having been true.
	h.waitText("(test) test-model")

	h.term.Type("\x03")
	h.waitText("press ctrl+c again to exit")
	h.term.Type("\x03")
	h.waitExit()

	text, lastContent := screenState(h)
	if !strings.Contains(text, "type /help") {
		t.Fatalf("teardown wiped the conversation; the transcript is the one thing "+
			"it must leave alone. screen:\n%s", text)
	}
	if strings.Contains(text, "(test) test-model") {
		t.Fatalf("teardown left the live band on screen; screen:\n%s", text)
	}
	if _, y := h.term.Screen().Cursor(); y <= lastContent {
		t.Fatalf("cursor parked at row %d but the frame still ends at row %d: the "+
			"shell prompt would print over the conversation. screen:\n%s",
			y, lastContent, text)
	}
}

// TestExitWhileResumingParksTheCursorToo is the flow the bug was reported
// from: /sessions, pick a session, ctrl+c before the resume lands. Worth its
// own case rather than folding into the one above, because it exits through a
// different door — keyCtrlC treats a loading session as a bail-out, so a
// single press quits instead of arming the usual double-tap, and Run returns
// with the resume still in flight.
func TestExitWhileResumingParksTheCursorToo(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	loading := make(chan struct{})
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.OpenSessionsOnBoot = true
		cfg.ListSessions = bootPickerSummaries
		cfg.LoadSession = func(string) error {
			close(loading)
			<-release
			return nil
		}
	})
	h.waitText("resume-me-title")
	h.term.Type("\r")
	// Held inside LoadSession: the ctrl+c below lands while the resume is
	// genuinely mid-flight, which is what makes one press enough. (If the
	// selection had not taken, the dialog would still be open and ctrl+c would
	// close it instead of exiting — waitExit would then time out.)
	<-loading
	h.term.Type("\x03")
	h.waitExit()

	text, lastContent := screenState(h)
	if _, y := h.term.Screen().Cursor(); y <= lastContent {
		t.Fatalf("cursor parked at row %d but the frame still ends at row %d. screen:\n%s",
			y, lastContent, text)
	}
}
