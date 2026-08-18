package chat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// Nothing owned a Message's staged files unless the message reached the QUEUE.
//
// connhost moves every inbound non-image attachment to disk while dispatching
// the frame — before any policy runs — so by the time the gate says no, the
// files already exist. cleanupFiles was reachable only from the three paths a
// queued message takes, and Run's switch has no default, so every message the
// gate handled itself left its files behind forever. There is no sweeper
// anywhere in the tree: "incoming" appears in connhost and in tests, nowhere
// else. A bot sitting in a busy guild it was never approved for staged every
// upload it could see and deleted none of them.
//
// Each case below is a real gate rejection, driven through the real Run loop.
func TestAGateRejectedMessageLeavesNoStagedFiles(t *testing.T) {
	cases := []struct {
		name  string
		msg   func(path string) Message
		setup func(t *testing.T) (*fakeConnector, *Loop)
	}{
		{
			name: "unapproved group: silent-by-default is the security boundary",
			msg: func(path string) Message {
				m := Message{ID: "g1", ChatID: "g100", ChatKind: "group", ChatTitle: "ops",
					UserID: "9", Username: "stranger", Text: "here is a file"}
				m.Files = []FileAttachment{{Path: path, Kind: "document", MimeType: "application/pdf", Size: 3}}
				return m
			},
			setup: func(t *testing.T) (*fakeConnector, *Loop) {
				conn := newFakeConnector(Capabilities{})
				return conn, startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
			},
		},
		{
			name: "a DM from someone who is not the paired owner",
			msg: func(path string) Message {
				m := msgFrom("9", "not the owner")
				m.ID = "d1"
				m.Files = []FileAttachment{{Path: path, Kind: "document", MimeType: "application/pdf", Size: 3}}
				return m
			},
			setup: func(t *testing.T) (*fakeConnector, *Loop) {
				conn := newFakeConnector(Capabilities{})
				return conn, startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := tc.setup(t)

			dir := filepath.Join(testsupport.TempDir(t), "incoming", "msg")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			staged := filepath.Join(dir, "upload.pdf")
			if err := os.WriteFile(staged, []byte("PDF"), 0o600); err != nil {
				t.Fatal(err)
			}

			conn.inbound <- tc.msg(staged)

			// The handler cleans on the delivery goroutine, so poll rather than
			// assume an ordering with the fake connector's send loop. Both the
			// file and its per-message directory must go: cleanupFiles removes
			// them in two passes, and asserting only the file would pass while
			// the directories accumulated one per rejected message.
			waitGone(t, staged, dir)
		})
	}
}

// An ask answer consumes the message's TEXT. Its files are not consumed by
// anything, so they must not survive either — takeTextAnswer returning true was
// the other early return with no cleanup behind it.
func TestAMessageSwallowedByATextAskLeavesNoStagedFiles(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	dir := filepath.Join(testsupport.TempDir(t), "incoming", "ask")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "answer.pdf")
	if err := os.WriteFile(staged, []byte("PDF"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Park a text-fallback ask so the next message in that chat is claimed as
	// its answer rather than flowing on as a prompt.
	answers := make(chan Answer, 1)
	l.mu.Lock()
	l.textAsk = &pendingTextAsk{
		ask:     Ask{ChatID: "100", Text: "which?", Options: []AskOption{{Key: "one", Label: "one"}}},
		answers: answers,
	}
	l.mu.Unlock()

	m := msgFrom("7", "1")
	m.ID = "ans1"
	m.Files = []FileAttachment{{Path: staged, Kind: "document", MimeType: "application/pdf", Size: 3}}
	conn.inbound <- m

	select {
	case <-answers:
	case <-time.After(2 * time.Second):
		t.Fatal("the ask never received its answer; this test is not exercising the path it claims to")
	}
	waitGone(t, staged, dir)
}

// The complement, and the reason the fix is a deferral rather than a cleanup
// call in every branch: a message that DOES reach the queue must keep its files
// until the turn is done with them. A defer that fired unconditionally would
// delete the attachment out from under the prompt.
func TestAQueuedMessageKeepsItsFilesUntilTheTurnIsDone(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	// A gated client parks the turn so the assertion below runs while the
	// message is queued and its files are still needed.
	release := make(chan struct{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok", gate: release}, pairedWith("7"))
	_ = l

	dir := filepath.Join(testsupport.TempDir(t), "incoming", "queued")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "keep.pdf")
	if err := os.WriteFile(staged, []byte("PDF"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := msgFrom("7", "read this")
	m.ID = "q1"
	m.Files = []FileAttachment{{Path: staged, Kind: "document", MimeType: "application/pdf", Size: 3}}
	conn.inbound <- m

	// While the turn is parked, the file must still be there. Give the handler
	// a moment to have run its deferral if it were going to.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("a queued message's attachment was deleted before its turn ran: %v", err)
	}
	close(release)
	waitGone(t, staged, dir)
}

// waitGone polls until every path is absent, or fails naming the one that
// stayed. Polling rather than a single check because cleanupFiles removes files
// in one pass and their now-empty directories in a second, so there is a real
// window where the file is gone and the directory is not — a no-tolerance
// assertion passes on an idle machine and fails on a loaded runner.
func waitGone(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var left string
		for _, p := range paths {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				left = p
				break
			}
		}
		if left == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s survived: a message that never reached the queue leaked its staged files", left)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
