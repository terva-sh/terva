package dialogs

// The side-chat dialog drives an asker, not a provider client. These pin the
// contract the /btw rewrite depends on: the dialog carries its own conversation
// and replays it, cancels an in-flight ask on esc, and releases the snapshot on
// close — all without ever holding a transcript or a client.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// scriptedAsker records what it was asked and answers from a queue. A nil entry
// blocks until released, so a test can hold an ask in flight.
type scriptedAsker struct {
	mu        sync.Mutex
	priors    [][]SideChatExchange
	questions []string
	reply     string
	closed    int
	block     chan struct{} // non-nil: Ask waits on it (and on ctx)
}

func (a *scriptedAsker) Ask(ctx context.Context, prior []SideChatExchange, question string) (string, error) {
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

func waitUntil(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A seeded open asks immediately, and the reply lands on the turn.
func TestBtwDialogSeededAskCompletes(t *testing.T) {
	a := &scriptedAsker{reply: "the answer"}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "why is the sky blue", func() {})

	waitUntil(t, "the reply to render", func() bool {
		return strings.Contains(strings.Join(d.Render(tui.Dark, 80), "\n"), "the answer")
	})

	_, questions, _ := a.snapshot()
	if len(questions) != 1 || questions[0] != "why is the sky blue" {
		t.Fatalf("asked %v, want the seed once", questions)
	}
}

// The dialog replays its OWN prior exchanges on the second ask; the frozen main
// transcript is the asker's, never the dialog's.
func TestBtwDialogReplaysItsOwnPriorTurns(t *testing.T) {
	a := &scriptedAsker{reply: "first reply"}
	d := NewBtwDialog()
	d.Open(tui.Dark, a, testsupport.TempDir(t), "first question", func() {})
	waitUntil(t, "the first reply", func() bool {
		return strings.Contains(strings.Join(d.Render(tui.Dark, 80), "\n"), "first reply")
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
	if len(priors[1]) != 1 || priors[1][0].User != "first question" || priors[1][0].Assistant != "first reply" {
		t.Fatalf("second ask's prior = %v, want the first exchange", priors[1])
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
