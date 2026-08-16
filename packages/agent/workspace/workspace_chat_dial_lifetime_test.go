package workspace

import (
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
)

// A dial is work that outlives the call that started it, and stopping the
// BRIDGES does not stop it: a dial past its handshake goes on to rebuild the
// bound session's tools, which installs terva's docs tree into TERVA_HOME. Left
// unjoined it keeps writing after the workspace that started it is gone.
//
// This was found as an intermittent "TempDir RemoveAll cleanup: directory not
// empty" — roughly one run in three under -race — and only once a test pinned
// TERVA_HOME at a directory it then removed. In production the home outlives the
// process, so the same leak is silent.
//
// Deterministic rather than a repeat-count: the gate below holds the dial open,
// so the join is observed BLOCKING and then observed RETURNING. A test that only
// ran the suite until it stopped flaking would pass just as well on a build
// where nothing joins anything.
func TestCloseWaitsForAnInFlightDial(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	conn := &gatedChatConn{fakeChatConn: newFakeChatConn(chat.Capabilities{}), release: make(chan struct{})}
	registerConnService(t, "parked-dial", conn)

	if err := w.chatConnect("s1", "parked-dial"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	joined := make(chan struct{})
	go func() {
		w.chatWaitDials()
		close(joined)
	}()

	// The dial is parked inside Connect, so the join MUST still be blocked.
	// Without this half the test would pass on a chatWaitDials that returns
	// immediately — which is exactly the build being ruled out.
	select {
	case <-joined:
		t.Fatal("the join returned while a dial was still parked in its handshake")
	case <-time.After(100 * time.Millisecond):
	}

	close(conn.release)

	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("the join never returned after the dial was released")
	}
}
