package dialogs

// The side-chat dialog drives an asker, not a provider client. These pin the
// contract the /btw rewrite depends on: the dialog carries its own conversation
// and replays it, cancels an in-flight ask on esc, and releases the snapshot on
// close — all without ever holding a transcript or a client.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// scriptedAsker records what it was asked and answers from a queue. A nil entry
// blocks until released, so a test can hold an ask in flight.
type scriptedAsker struct {
	// entered counts Ask CALLS, bumped on entry and before the mutex, so it
	// stays meaningful even if the recording below somehow isn't. It exists to
	// tell "Ask never ran" apart from "Ask ran and its record went missing" —
	// see askerState.
	entered   atomic.Int64
	mu        sync.Mutex
	priors    [][]SideChatExchange
	questions []string
	reply     string
	closed    int
	block     chan struct{} // non-nil: Ask waits on it (and on ctx)
}

func (a *scriptedAsker) Ask(ctx context.Context, prior []SideChatExchange, question string) (string, error) {
	a.entered.Add(1)
	a.mu.Lock()
	a.priors = append(a.priors, prior)
	a.questions = append(a.questions, question)
	block := a.block
	reply := a.reply
	a.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if reply == "" {
		reply = "answer to " + question
	}
	return reply, nil
}

func (a *scriptedAsker) Close() {
	a.mu.Lock()
	a.closed++
	a.mu.Unlock()
}

func (a *scriptedAsker) snapshot() ([][]SideChatExchange, []string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.priors, append([]string(nil), a.questions...), a.closed
}

// askerState is what a failure in here has to print to be diagnosable at all.
//
// A Windows CI run once failed the seeded-ask test with "asked []" — and that
// message cannot be acted on, because fmt renders both []string{} and
// []string{""} as exactly "[]". "No question was asked" and "an empty question
// was asked" are different bugs with different causes, and the output could not
// tell them apart. So: quote the questions, and report the Ask entry count
// separately from what Ask recorded. The three failures then read distinctly —
// entered=0 (the answer text appeared without an ask at all), entered=1 with an
// empty question (the seed was lost before the ask), and entered=1 with the
// question recorded but not visible here (a synchronisation claim this test
// relies on is false).
func askerState(a *scriptedAsker, d *BtwDialog, width int) string {
	_, questions, closed := a.snapshot()
	return fmt.Sprintf("Ask entered %d time(s); recorded %d question(s) %q; closed=%d\nrendered pane:\n%s",
		a.entered.Load(), len(questions), questions, closed,
		strings.Join(d.Render(tui.Dark, width), "\n"))
}

// waitUntil polls pred to a deadline. An optional diag is rendered into the
// timeout message: a timeout here says only "it never happened", which is the
// least informative thing a failure can say.
func waitUntil(t *testing.T, what string, pred func() bool, diag ...func() string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	detail := ""
	if len(diag) > 0 && diag[0] != nil {
		detail = "\n" + diag[0]()
	}
	t.Fatalf("timed out waiting for %s%s", what, detail)
}

// Reply sentinels for the tests that wait on a reply APPEARING IN THE PANE.
//
// They have to be strings the dialog's own chrome can never contain, and that is
// not a stylistic preference — it is the root cause of a flake that was open for
// two weeks and cost a release go-live. The old sentinel was "the answer"; the
// spinner picks a phrase at random from tui.Theme.SpinnerMessages, one of which
// is "googling the answer (not really)". Whenever that phrase came up, the wait
// predicate matched the SPINNER, returned before the ask goroutine had run, and
// the next assertion reported the seed as never asked — with a rendered pane
// showing a spinner and no reply, which is exactly what the failure looked like.
//
// It is a ~1-in-22 coin flip that then needs to win a scheduling race, which is
// why 300 local iterations under -race came back clean and it only ever surfaced
// on loaded CI.
//
// TestReplySentinelsCannotCollideWithChrome keeps this true as phrases are added.
const (
	sentinelReply      = "ZZREPLYZZ"
	sentinelFirstReply = "ZZFIRSTREPLYZZ"
)

// The sentinels above are only load-bearing while nothing the dialog renders on
// its own contains them. A new spinner phrase is the way that quietly stops
// being true, so this enrols from the real list rather than a copy.
func TestReplySentinelsCannotCollideWithChrome(t *testing.T) {
	phrases := tui.Dark.SpinnerMessages
	if len(phrases) == 0 {
		t.Fatal("no spinner phrases; this guard would pass vacuously")
	}
	for _, s := range []string{sentinelReply, sentinelFirstReply} {
		for _, p := range phrases {
			if strings.Contains(p, s) {
				t.Errorf("spinner phrase %q contains the reply sentinel %q — a wait predicate "+
					"looking for that sentinel will match the spinner and return before any reply exists", p, s)
			}
		}
	}
}

// A seeded open asks immediately, and the reply lands on the turn.
func TestBtwDialogSeededAskCompletes(t *testing.T) {
	a := &scriptedAsker{reply: sentinelReply}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "why is the sky blue", func() {})

	waitUntil(t, "the reply to render", func() bool {
		return strings.Contains(strings.Join(d.Render(tui.Dark, 80), "\n"), sentinelReply)
	}, func() string { return askerState(a, d, 80) })

	_, questions, _ := a.snapshot()
	if len(questions) != 1 || questions[0] != "why is the sky blue" {
		t.Fatalf("want the seed asked exactly once.\n%s", askerState(a, d, 80))
	}
}

// The dialog replays its OWN prior exchanges on the second ask; the frozen main
// transcript is the asker's, never the dialog's.
func TestBtwDialogReplaysItsOwnPriorTurns(t *testing.T) {
	a := &scriptedAsker{reply: sentinelFirstReply}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "first question", func() {})
	waitUntil(t, "the first reply", func() bool {
		return strings.Contains(strings.Join(d.Render(tui.Dark, 80), "\n"), sentinelFirstReply)
	})

	// Second question, typed.
	for _, r := range "second question" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r}, func() {})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}, func() {})

	waitUntil(t, "the second ask", func() bool {
		_, questions, _ := a.snapshot()
		return len(questions) == 2
	})

	priors, questions, _ := a.snapshot()
	if questions[1] != "second question" {
		t.Fatalf("second question = %q", questions[1])
	}
	// The first ask carried no prior; the second carried exactly the first
	// exchange, with its reply.
	if len(priors[0]) != 0 {
		t.Fatalf("first ask carried prior turns: %v", priors[0])
	}
	if len(priors[1]) != 1 || priors[1][0].User != "first question" || priors[1][0].Assistant != sentinelFirstReply {
		t.Fatalf("second ask's prior = %v, want the first exchange", priors[1])
	}
}

// The reported cursor must land on the editor row even after a turn
// exists. PadDialogFrame only inserts its post-header blank when the
// first body row isn't already blank (the empty state); with a turn the
// first body row is blank, so CursorPos must not count that pad row.
// Before the fix it did, dropping the cursor one row below the editor.
func TestBtwDialogCursorLandsOnEditorAfterTurn(t *testing.T) {
	const width = 80
	a := &scriptedAsker{reply: sentinelReply}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "a question", func() {})
	waitUntil(t, "the reply", func() bool {
		return strings.Contains(strings.Join(d.Render(tui.Dark, width), "\n"), sentinelReply)
	})

	// Type a marker into the editor without submitting it.
	const marker = "ZZMARKERZZ"
	for _, r := range marker {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r}, func() {})
	}

	padded := PadDialogFrame(d.Render(tui.Dark, width))
	row, _ := d.CursorPos(width)
	if row < 0 || row >= len(padded) {
		t.Fatalf("cursor row %d out of range [0,%d)", row, len(padded))
	}
	if !strings.Contains(padded[row], marker) {
		t.Fatalf("cursor row %d does not land on the editor line.\nrow content: %q", row, padded[row])
	}
}

// esc during an in-flight ask cancels it (ctx fires) and does NOT close the
// dialog; a second esc closes and releases the snapshot exactly once.
func TestBtwDialogEscCancelsThenCloses(t *testing.T) {
	a := &scriptedAsker{block: make(chan struct{})}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "hang please", func() {})

	waitUntil(t, "the ask to be in flight", func() bool { return d.Loading() })

	// First esc: cancels the in-flight ask, dialog stays open.
	if closed := d.HandleKey(tui.Key{Kind: tui.KeyEsc}, func() {}); closed {
		t.Fatal("first esc closed the dialog instead of cancelling the ask")
	}
	waitUntil(t, "the ask to unwind after cancel", func() bool { return !d.Loading() })
	if !d.Active() {
		t.Fatal("first esc closed the dialog; it should only cancel")
	}

	// Second esc: closes and releases the snapshot.
	if closed := d.HandleKey(tui.Key{Kind: tui.KeyEsc}, func() {}); !closed {
		t.Fatal("second esc did not close the dialog")
	}
	if d.Active() {
		t.Fatal("dialog still active after the closing esc")
	}
	waitUntil(t, "the snapshot to be released", func() bool {
		_, _, closed := a.snapshot()
		return closed == 1
	})
}

// Close releases the snapshot even with no ask ever sent — an open the user
// immediately dismissed must not leak the daemon-side freeze.
func TestBtwDialogCloseReleasesUnusedSnapshot(t *testing.T) {
	a := &scriptedAsker{}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "", func() {})
	d.Close()
	if _, _, closed := a.snapshot(); closed != 1 {
		t.Fatalf("Close released the snapshot %d times, want 1", closed)
	}
}
