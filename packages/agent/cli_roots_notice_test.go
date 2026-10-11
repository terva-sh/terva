package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

func TestPeerNoticePreservesJailPosture(t *testing.T) {
	primary, peer := testsupport.TempDir(t), testsupport.TempDir(t)
	sb := tools.NewSandbox(primary)
	if err := sb.AddRoot(peer); err != nil {
		t.Fatal(err)
	}
	// The notice names the canonical root that AddRoot stored.
	peer, err := filepath.EvalSymlinks(peer)
	if err != nil {
		t.Fatal(err)
	}
	posture := "fixture posture notice"
	if got := peerRootJailNotice(posture, sb); got != posture {
		t.Fatal("unlocked sandbox acquired a confined-roots notice", got)
	}
	sb.Lock()
	if got := peerRootJailNotice(posture, sb); !strings.HasPrefix(got, posture) || !strings.Contains(got, peer) {
		t.Fatal("locked peer notice lost posture or roots", got)
	}
	if got := peerRootJailNotice(posture, nil); got != posture {
		t.Fatal("absent sandbox lost its posture notice", got)
	}
}
