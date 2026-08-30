package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A refusal has to name an actor the MODEL can appeal to. /unjail is a user
// slash command, so "use /unjail to disable" reads to the model as an
// instruction it can follow and cannot. The recorded failure: a refused write
// to /tmp came back one turn later as a `cat > /tmp/... <<'PY'` heredoc, the
// same bytes with the review surface stripped off.
func TestWriteRefusalNamesTheUserNotTheModel(t *testing.T) {
	root := testsupport.TempDir(t)
	outside := testsupport.TempDir(t)
	sb := NewSandbox(root)
	sb.Lock()

	err := sb.CheckPath(filepath.Join(outside, "a.txt"))
	if err == nil {
		t.Fatal("expected an out-of-root write to be refused")
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "ask the user to run /unjail") {
		t.Errorf("refusal does not name the user as the actor: %q", msg)
	}
	// The bare imperative is what made the model treat /unjail as its own move.
	for _, bad := range []string{"use /unjail to disable", "use /unjail to lift"} {
		if strings.Contains(strings.ToLower(msg), bad) {
			t.Errorf("refusal still tells the model to run /unjail itself (%q): %q", bad, msg)
		}
	}
}

// The same rule on the bash side. These refusals are the ones a model meets
// while it probes the jail edge, which is exactly when a wrong actor sends it
// looking for a way around instead of a way through.
func TestCommandRefusalsNameTheUser(t *testing.T) {
	sb := NewSandbox(testsupport.TempDir(t))
	sb.Lock()

	for _, cmd := range []string{
		"sudo apt-get install foo",
		"rm -rf /",
		"cd /etc && ls",
	} {
		err := sb.CheckCommand(cmd)
		if err == nil {
			t.Fatalf("expected %q to be refused", cmd)
		}
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "ask the user to run /unjail") {
			t.Errorf("refusal for %q does not name the user: %q", cmd, err)
		}
		if strings.Contains(msg, "use /unjail to disable") {
			t.Errorf("refusal for %q still reads as a model-runnable command: %q", cmd, err)
		}
	}
}

// A refusal that only says "no" leaves the model to invent a destination, and
// the one it invents is /tmp through a shell redirect. Naming the sanctioned
// scratch dir is what keeps the next step a reviewable tool call.
func TestWriteRefusalNamesTheScratchDir(t *testing.T) {
	root := testsupport.TempDir(t)
	home := testsupport.TempDir(t)
	scratch := filepath.Join(home, "scratch")

	sb := NewSandbox(root)
	sb.AddWritableRoot(scratch)
	sb.ScratchDir = scratch
	sb.Lock()

	// The grant itself holds before the directory exists.
	if err := sb.CheckPath(filepath.Join(scratch, "make-mochi-scenarios.py")); err != nil {
		t.Fatalf("write into the scratch grant was refused: %v", err)
	}
	// Containment elsewhere is untouched, and the refusal points at scratch.
	err := sb.CheckPath(filepath.Join(testsupport.TempDir(t), "make-mochi-scenarios.py"))
	if err == nil {
		t.Fatal("the scratch grant must not open the rest of the filesystem")
	}
	if !strings.Contains(err.Error(), scratch) {
		t.Errorf("refusal does not name the scratch dir, so the model has nowhere to go: %v", err)
	}
}

// With no scratch dir configured the refusal must not advertise one. An
// embedder that never calls allowScratchWrites would otherwise send the model
// to a path that does not exist.
func TestWriteRefusalWithoutScratchDirAdvertisesNothing(t *testing.T) {
	sb := NewSandbox(testsupport.TempDir(t))
	sb.Lock()

	err := sb.CheckPath(filepath.Join(testsupport.TempDir(t), "a.txt"))
	if err == nil {
		t.Fatal("expected an out-of-root write to be refused")
	}
	// Match the SENTENCE, not the bare word: the temp dir carries this test's
	// own name, so a substring check for "scratch" matches the path itself.
	if strings.Contains(strings.ToLower(err.Error()), "put scratch files under") {
		t.Errorf("refusal advertises a scratch dir that was never granted: %v", err)
	}
}

// ScratchDir is DISPLAY ONLY: it names a path in the refusal and grants
// nothing. The grant is the AddWritableRoot entry. Setting one without the
// other would point the model at a path that refuses it again, so this pins
// which of the two actually carries the permission.
func TestScratchDirGrantsNothingByItself(t *testing.T) {
	root := testsupport.TempDir(t)
	scratch := filepath.Join(testsupport.TempDir(t), "scratch")

	sb := NewSandbox(root)
	sb.ScratchDir = scratch // deliberately no AddWritableRoot
	sb.Lock()

	if err := sb.CheckPath(filepath.Join(scratch, "a.txt")); err == nil {
		t.Error("ScratchDir must not grant write access on its own")
	}
}
