package swarm

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestSwarmStateIsPrivate pins the R5 Batch-B fix for swarm state: the
// per-agent state dir, meta.json, and events.jsonl carry session-transcript-
// grade data (full tool output), so they must be 0700/0600 like the child's
// session file — not the historical 0755/0644.
func TestSwarmStateIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-mode assertions do not apply on Windows")
	}
	root := testsupport.TempDir(t)
	release := make(chan struct{})
	f := New(Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error {
				<-release
				return nil
			})
		},
	})
	defer f.StopAll()
	defer close(release)

	a, err := f.Spawn(context.Background(), "mode check task")
	if err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Dir(a.EventLogPath)
	assertPrivateMode(t, stateDir, 0o700)
	assertPrivateMode(t, filepath.Join(stateDir, "meta.json"), 0o600)

	// The event log is runner-owned; open it the way the runner does.
	log, err := OpenEventLog(a.EventLogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	assertPrivateMode(t, a.EventLogPath, 0o600)
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}
