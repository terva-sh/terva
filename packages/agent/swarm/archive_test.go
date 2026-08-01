package swarm

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func archiveSwarm(t *testing.T) *Swarm {
	t.Helper()
	root := testsupport.TempDir(t)
	return New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
}

// spawnStopped returns a finished agent with a transcript on disk.
func spawnStopped(t *testing.T, f *Swarm, task string) *Agent {
	t.Helper()
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: task, SessionID: "sess-A"})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Stop(a.ID)
	a.Wait()
	// Give the record something worth compressing.
	log := filepath.Join(f.agentStateDir(a.ID), "events.jsonl")
	body := strings.Repeat(`{"type":"stdout","data":{"text":"hello world"}}`+"\n", 400)
	if err := os.WriteFile(log, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return a
}

func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The contract in one test: the live record is gone, the compressed one is
// readable, and the agent has left the swarm.
func TestArchiveCompressesAndMovesTheRecord(t *testing.T) {
	f := archiveSwarm(t)
	a := spawnStopped(t, f, "archive me")
	src := f.agentStateDir(a.ID)
	before, err := os.ReadFile(filepath.Join(src, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Archive(a.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Live state gone, and gone from the swarm.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("live state dir survived the archive: %v", err)
	}
	if f.Get(a.ID) != nil {
		t.Error("archived agent is still in the swarm")
	}
	if got := snapshotIDs(f.SnapshotAll()); len(got) != 0 {
		t.Errorf("archived agent still listed: %v", got)
	}

	// Record recoverable, byte for byte.
	dst := f.agentArchiveDir(a.ID)
	if got := gunzip(t, filepath.Join(dst, "events.jsonl.gz")); got != string(before) {
		t.Errorf("archived transcript does not round-trip (%d bytes vs %d)", len(got), len(before))
	}
	// meta.json stays readable without decompressing. With nothing in terva
	// listing the archive, this is the only way a human finds the record they
	// want without gunzipping every one of them — `grep -l` IS the recovery
	// story now.
	meta, err := readAgentMeta(dst)
	if err != nil {
		t.Fatalf("read archived meta: %v", err)
	}
	if meta.ID != a.ID || meta.Task != "archive me" {
		t.Errorf("archived meta = %+v; want the agent's identity", meta)
	}
	if meta.SessionID != "sess-A" {
		t.Errorf("archived meta lost the session stamp: %q", meta.SessionID)
	}
}

// The reason the feature exists: Reload reads every live agent's log at every
// launch, so an archived one must not be walked.
func TestAnArchivedAgentIsNotReloaded(t *testing.T) {
	root := testsupport.TempDir(t)
	mk := func() *Swarm {
		return New(Config{
			Root: root, RepoRoot: root,
			NewRunner: func(a *Agent) Runner {
				return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
			},
		})
	}
	f := mk()
	kept := spawnStopped(t, f, "keep me")
	gone := spawnStopped(t, f, "archive me")
	if err := f.Archive(gone.ID); err != nil {
		t.Fatal(err)
	}

	g := mk()
	loaded, errs := g.Reload()
	if len(errs) > 0 {
		t.Fatalf("reload errs: %v", errs)
	}
	if loaded != 1 {
		t.Errorf("reload loaded %d agents; want 1 (the archived one must not be walked)", loaded)
	}
	if g.Get(gone.ID) != nil {
		t.Error("the archived agent came back on reload")
	}
	if g.Get(kept.ID) == nil {
		t.Error("the live agent did not survive reload")
	}
}

// Archiving a running agent would compress a transcript still being written.
func TestArchiveRefusesARunningAgent(t *testing.T) {
	f := archiveSwarm(t)
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Stop(a.ID); a.Wait() }()

	if err := f.Archive(a.ID); err == nil {
		t.Error("archived a running agent; want a refusal")
	}
	if _, err := os.Stat(f.agentStateDir(a.ID)); err != nil {
		t.Errorf("a refused archive disturbed the live state: %v", err)
	}
}

// One-way, and idempotence is NOT the contract: a second archive of the same id
// must fail loudly rather than silently overwrite a record it cannot recover.
func TestArchiveTwiceIsAnError(t *testing.T) {
	f := archiveSwarm(t)
	a := spawnStopped(t, f, "once")
	if err := f.Archive(a.ID); err != nil {
		t.Fatal(err)
	}
	// The agent is gone from the swarm, so the second call cannot even find it —
	// but the archive must still be there afterwards.
	if err := f.Archive(a.ID); err == nil {
		t.Error("second archive succeeded; want an error")
	}
	if _, err := os.Stat(filepath.Join(f.agentArchiveDir(a.ID), "events.jsonl.gz")); err != nil {
		t.Errorf("the archived record did not survive a second attempt: %v", err)
	}
}

// The ordering that matters: nothing is deleted until the archive is durable.
// Simulated by making the archive root a FILE, so the write cannot succeed.
func TestAFailedArchiveLeavesTheAgentAlone(t *testing.T) {
	f := archiveSwarm(t)
	a := spawnStopped(t, f, "unarchivable")

	// Block the archive root with a regular file.
	if err := os.WriteFile(f.archiveRoot(), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.Archive(a.ID); err == nil {
		t.Fatal("archive succeeded despite an unwritable root")
	}
	// The transcript is the thing that cannot be recreated. It must still be
	// here, and the agent must still be usable.
	if _, err := os.Stat(filepath.Join(f.agentStateDir(a.ID), "events.jsonl")); err != nil {
		t.Errorf("a failed archive destroyed the live transcript: %v", err)
	}
	if f.Get(a.ID) == nil {
		t.Error("a failed archive dropped the agent from the swarm")
	}
}

// A .partial left by an interrupted run is debris, not a record, and must not
// be counted or block a retry.
func TestAPartialArchiveIsDebrisNotARecord(t *testing.T) {
	f := archiveSwarm(t)
	a := spawnStopped(t, f, "retry me")

	stale := f.agentArchiveDir(a.ID) + ".partial"
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "events.jsonl.gz"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Archive(a.ID); err != nil {
		t.Fatalf("archive did not clear stale debris: %v", err)
	}
	if got := gunzip(t, filepath.Join(f.agentArchiveDir(a.ID), "events.jsonl.gz")); !strings.Contains(got, "hello world") {
		t.Error("the retried archive kept the junk instead of the real transcript")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf(".partial debris survived a successful archive: %v", err)
	}
}

// Compression has to actually pay, or the whole exercise is a rename.
func TestTheArchiveIsSubstantiallySmaller(t *testing.T) {
	f := archiveSwarm(t)
	a := spawnStopped(t, f, "compress me")
	raw, err := os.Stat(filepath.Join(f.agentStateDir(a.ID), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Archive(a.ID); err != nil {
		t.Fatal(err)
	}
	gz, err := os.Stat(filepath.Join(f.agentArchiveDir(a.ID), "events.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if gz.Size() >= raw.Size() {
		t.Errorf("archived log is %d bytes vs %d raw; compression bought nothing", gz.Size(), raw.Size())
	}
	t.Logf("events.jsonl %d -> %d bytes (%.0f%%)", raw.Size(), gz.Size(),
		100*float64(gz.Size())/float64(raw.Size()))
}

// A new agent must never be minted onto an archived id. Nothing would notice:
// the archive is unreachable from terva, so the collision only surfaces when
// archiving the new agent either clobbers a record that cannot be recovered or
// dead-ends with "already archived" and no way out.
func TestANewAgentCannotClaimAnArchivedID(t *testing.T) {
	// The id is slug + now.UnixNano()%1e6, so two spawns normally differ by the
	// clock alone and this test would pass without the guard it is here to
	// check. Pin Now so both spawns mint the SAME base id and only the
	// archive-collision check can separate them.
	root := testsupport.TempDir(t)
	frozen := time.Unix(1767225600, 0)
	f := New(Config{
		Root: root, RepoRoot: root,
		Now: func() time.Time { return frozen },
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	a := spawnStopped(t, f, "collide with me")
	id := a.ID
	if err := f.Archive(id); err != nil {
		t.Fatal(err)
	}

	// Same source text, same frozen clock: the mint produces the same base and
	// must skip it rather than reuse it.
	b, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "collide with me"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Stop(b.ID); b.Wait() }()

	if b.ID == id {
		t.Fatalf("new agent claimed the archived id %q", id)
	}
	// And the archived record is untouched.
	if got := gunzip(t, filepath.Join(f.agentArchiveDir(id), "events.jsonl.gz")); !strings.Contains(got, "hello world") {
		t.Error("the archived record was disturbed by a later spawn")
	}
}
