package swarm

// Archiving a finished agent: compress its record, move it out of the live
// tree, and forget it.
//
// The problem it solves is not clutter. Reload() reads the tail of every
// agent's events.jsonl at every launch, so a swarm that only ever grows makes
// every terva start slower — eleven agents from one day of work were already
// 10.8 MB of replay, one of them 2.1 MB. Remove() was the only cleanup and it
// deletes the transcript outright. The whole feature is the gap between "shows
// up in every list forever" and "gone".
//
// ONE-WAY, and terva cannot reach an archived record AT ALL: no Unarchive, no
// listing, no count, nothing that reads this tree to put a number on a screen.
// Archiving is the record leaving the program for good.
//
// That is stronger than it needs to be for correctness, and deliberately so. A
// read path is how an archive becomes a second live tree — first a count, then
// a list, then "open it just to look", and the growth problem is back with two
// places for an agent id to exist. Terva writes here, names the destination
// once, and never looks again.
//
// Not lost, though: it is a plain directory of plain formats, so the record is
// recovered with gunzip and nothing else. Being unreachable FROM TERVA is the
// design; being unreachable is not.
//
// Layout, a sibling of agents/ so Reload never walks it:
//
//	<root>/archive/<id>/meta.json         (plain)
//	<root>/archive/<id>/events.jsonl.gz
//	<root>/archive/<id>/session.json.gz
//
// meta.json stays UNCOMPRESSED, alone among the three, and this matters MORE
// now that nothing in terva lists the archive rather than less. It is ~1 KB of
// identity — id, task, when, which model, which session — and with no listing
// anywhere in the program it is the only way to find the record you want
// without decompressing every one of them. `grep -l` across the archive is the
// recovery story. The bulk is the other two files; compressing the small one
// would buy nothing and spend the only cheap read a human has.

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"terva.sh/terva/packages/privfs"
)

// archiveDirName is the archive root under the swarm root. A sibling of
// "agents", never inside it: Reload walks agents/*/meta.json, so anything that
// lands there is read at every launch, which is the cost being reclaimed.
const archiveDirName = "archive"

// gzippedInArchive are the files compressed on the way in. Everything else in
// an agent's state dir is either identity (meta.json, copied verbatim) or a
// runtime artifact (in.sock) that means nothing once the process is gone.
var gzippedInArchive = []string{"events.jsonl", "session.json"}

// archiveRoot is the directory archived agents are written to. Unexported:
// nothing outside this package has business locating the archive, and an
// exported accessor is the first step in growing a read path back.
func (f *Swarm) archiveRoot() string { return filepath.Join(f.cfg.Root, archiveDirName) }

func (f *Swarm) agentArchiveDir(id string) string {
	return filepath.Join(f.archiveRoot(), id)
}

// Archive compresses a finished agent's record into the archive tree, deletes
// its live state, and drops it from the swarm. One-way.
//
// The ordering is the only interesting part: the archive is built into a temp
// directory, renamed into place, and ONLY THEN is the source removed. Remove()
// can swallow its cleanup errors because the user asked for the data to be
// gone; here an error before the rename must leave the agent exactly as it was,
// because the alternative is deleting a transcript on behalf of an operation
// that promised to keep it.
func (f *Swarm) Archive(id string) error {
	a := f.Get(id)
	if a == nil {
		return fmt.Errorf("no such agent %q", id)
	}
	a.mu.Lock()
	st := a.status
	a.mu.Unlock()
	// Same guard as Remove: a running agent is still writing the files being
	// compressed, so the archive would capture a torn transcript.
	if st == StatusRunning || st == StatusPending {
		return fmt.Errorf("agent %s still %s", a.ID, st)
	}
	// Release the worktree lease, as Remove does. Idempotent for an agent that
	// terminated through the runner; a no-op for a detached one.
	a.finish()

	src := f.agentStateDir(a.ID)
	dst := f.agentArchiveDir(a.ID)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("agent %s is already archived", a.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("archive %s: %w", a.ID, err)
	}
	if err := writeArchive(src, dst); err != nil {
		return fmt.Errorf("archive %s: %w", a.ID, err)
	}

	// Durable now. The live copy can go, and a failure here is cosmetic — the
	// record survives either way, and a stale state dir is reloaded as a
	// detached agent rather than lost.
	_ = os.RemoveAll(src)

	f.mu.Lock()
	delete(f.agents, a.ID)
	for i, k := range f.order {
		if k == a.ID {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	f.mu.Unlock()
	return nil
}

// writeArchive builds the compressed record for one agent at dst, atomically.
//
// It stages into a sibling temp directory and renames, so an interrupted
// archive leaves either nothing or a complete record — never a half-written one
// that the caller would then delete the source for. The temp name is derived
// from the id rather than randomised: two concurrent archives of the SAME agent
// are the collision worth losing, and the second one failing on a non-empty
// temp dir is the right outcome.
func writeArchive(src, dst string) error {
	if err := privfs.MkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}
	tmp := dst + ".partial"
	// A leftover .partial is debris from an interrupted run, not data: the
	// source it was built from is still live (nothing is deleted before the
	// rename), so clearing it can lose nothing.
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := privfs.MkdirAll(tmp); err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // no-op once the rename has moved it

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	wrote := false
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			// in.sock is a socket and every other non-regular entry is a
			// runtime artifact; neither means anything without the process.
			continue
		}
		name := e.Name()
		if slices.Contains(gzippedInArchive, name) {
			if err := gzipFile(filepath.Join(src, name), filepath.Join(tmp, name+".gz")); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(filepath.Join(src, name))
			if err != nil {
				return err
			}
			if err := privfs.WriteFile(filepath.Join(tmp, name), data); err != nil {
				return err
			}
		}
		wrote = true
	}
	if !wrote {
		return fmt.Errorf("nothing to archive in %s", src)
	}
	return os.Rename(tmp, dst)
}

// gzipFile streams src through gzip into dst. Streamed rather than read-all
// because an events log is the biggest thing here by an order of magnitude and
// is exactly what a long-running agent makes bigger.
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A session that never wrote a transcript has no session.json. An
			// absent file is not a failed archive.
			return nil
		}
		return err
	}
	defer in.Close()

	out, err := privfs.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// archivedIDTaken reports whether an id already names an archived record.
//
// The ONE read of the archive tree, and it exists to protect the archive rather
// than to reach it: without it a freshly minted id could collide with an
// archived one, and archiving the new agent would either clobber a record that
// cannot be recovered or dead-end with "already archived" and no way out.
// Nothing user-visible comes back from here — only "pick a different id".
func (f *Swarm) archivedIDTaken(id string) bool {
	_, err := os.Stat(f.agentArchiveDir(id))
	return err == nil
}
