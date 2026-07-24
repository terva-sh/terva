package core

// Archiving a session: the transcript leaves the listing without leaving the disk.
//
// It moves into an `archive/` SUBDIRECTORY of the session dir, gzipped on the
// way. The subdirectory IS the mechanism, and that is the whole reason to prefer
// it: every scan of a sessions directory already skips entries where IsDir() —
// ListSessions, the empty-transcript prune, BuildSessionTree, FindSessionByID —
// so an archived session drops out of every picker, every branch tree and every
// resume without one of them learning a filter. A flag on the meta row would
// have been the opposite bargain: correct only for as long as every future
// listing site remembers to honour it, and a silent leak the day one forgets.
//
// Gzip because a transcript is JSONL: the same keys and role names on every
// line, which is close to the best case DEFLATE has. It costs one decompression
// to read an archived session back, and reading one is by definition not the hot
// path.

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/privfs"
)

// ArchiveDirName is the subdirectory, inside a cwd's session directory, that
// holds archived transcripts. A directory entry, so every existing scan skips
// it for free.
const ArchiveDirName = "archive"

// archivedSuffix is what an archived transcript is called. Deliberately NOT
// ".jsonl": isSessionTranscriptName would otherwise match it if the archive dir
// were ever walked, and a listing site would surface a gzip stream as a session.
const archivedSuffix = ".jsonl.gz"

// archivedErrorSuffix is the archived form of the error sidecar (LogError's
// <session>.errors.jsonl). It travels WITH its transcript: the sidecar is that
// session's data, and archiving one without the other orphans a failure record
// against a session no listing shows.
const archivedErrorSuffix = ".errors.jsonl.gz"

// ArchiveDir returns the archive directory for a cwd's sessions.
func ArchiveDir(root, cwd string) string {
	return filepath.Join(SessionsDir(root, cwd), ArchiveDirName)
}

// ArchivedSession describes one transcript in the archive.
//
// It carries a full SessionSummary — read out of the compressed transcript, so
// the archive browser can show the same title, model and message count the live
// picker shows — plus the two facts that only exist once a session is archived.
type ArchivedSession struct {
	SessionSummary
	// ID is the session id (the filename stem). SessionSummary.Path points at
	// the .jsonl.gz, which is NOT a session file, so callers must address an
	// archived session by this rather than by deriving an id from the path.
	ID string
	// ArchivedAt is when the transcript was archived (the archived file's mtime).
	ArchivedAt time.Time
	// Bytes is the compressed size on disk, and Original the size of the
	// transcript it was made from. Together they are the only honest way to show
	// what archiving bought.
	Bytes    int64
	Original int64
}

// ErrNoSuchSession is returned when the named session has no transcript in the
// sessions directory.
var ErrNoSuchSession = errors.New("no such session")

// ErrSessionNotArchived is returned when the named session is not in the archive.
var ErrSessionNotArchived = errors.New("session is not archived")

// ErrSessionExists is returned when restoring would land on top of a live
// session file. Refused rather than overwritten: the file in the sessions
// directory is the one terva may already have open, and clobbering it would
// destroy a live transcript to recover an old one.
var ErrSessionExists = errors.New("a session with that id is already active")

// ArchiveSession compresses the session's transcript (and its error sidecar, if
// it has one) into the archive directory and removes the originals.
//
// The compressed file is written to a temp name and renamed into place BEFORE
// the source is removed, so an interruption can leave two copies but never zero.
// A leftover archived copy is harmless — nothing lists the archive dir except
// ListArchivedSessions, and a re-archive replaces it atomically.
func ArchiveSession(root, cwd, id string) (ArchivedSession, error) {
	src := filepath.Join(SessionsDir(root, cwd), id+".jsonl")
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return ArchivedSession{}, ErrNoSuchSession
		}
		return ArchivedSession{}, err
	}

	dir := ArchiveDir(root, cwd)
	if err := privfs.MkdirAll(dir); err != nil {
		return ArchivedSession{}, fmt.Errorf("archive: create dir: %w", err)
	}

	dst := filepath.Join(dir, id+archivedSuffix)
	if err := gzipFile(src, dst); err != nil {
		return ArchivedSession{}, fmt.Errorf("archive: compress: %w", err)
	}
	// Only now is the archived copy definitely on disk.
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return ArchivedSession{}, fmt.Errorf("archive: remove original: %w", err)
	}

	// The sidecar rides along, best-effort: most sessions never had one, and a
	// failure to move a failure log must not fail the archive itself.
	if sc := ErrorLogPathFor(src); sc != "" {
		if _, err := os.Stat(sc); err == nil {
			if gzipFile(sc, filepath.Join(dir, id+archivedErrorSuffix)) == nil {
				_ = os.Remove(sc)
			}
		}
	}

	out, err := describeArchived(dst, id)
	if err != nil {
		return ArchivedSession{}, err
	}
	out.Original = info.Size()
	return out, nil
}

// RestoreSession decompresses an archived transcript back into the sessions
// directory, where every listing sees it again.
//
// Refuses rather than overwrites when a live session already holds the id: the
// file there may be open right now, and losing a live transcript to recover an
// old one is not a trade anyone asked for.
func RestoreSession(root, cwd, id string) (string, error) {
	dir := ArchiveDir(root, cwd)
	src := filepath.Join(dir, id+archivedSuffix)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return "", ErrSessionNotArchived
		}
		return "", err
	}
	dst := filepath.Join(SessionsDir(root, cwd), id+".jsonl")
	if _, err := os.Stat(dst); err == nil {
		return "", ErrSessionExists
	}
	if err := privfs.MkdirAll(filepath.Dir(dst)); err != nil {
		return "", fmt.Errorf("restore: create dir: %w", err)
	}
	if err := gunzipFile(src, dst); err != nil {
		return "", fmt.Errorf("restore: decompress: %w", err)
	}
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("restore: remove archived copy: %w", err)
	}
	// And the sidecar, the same way it was archived.
	if scSrc := filepath.Join(dir, id+archivedErrorSuffix); fileExists(scSrc) {
		if sc := ErrorLogPathFor(dst); sc != "" {
			if gunzipFile(scSrc, sc) == nil {
				_ = os.Remove(scSrc)
			}
		}
	}
	return dst, nil
}

// ListArchivedSessions describes every archived transcript for a cwd, newest
// archived first. Never an error: an unreadable or absent archive directory is
// an empty archive, which is what a browser should show rather than a failure.
func ListArchivedSessions(root, cwd string) []ArchivedSession {
	dir := ArchiveDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ArchivedSession
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, archivedSuffix) {
			continue
		}
		// The sidecar shares the .jsonl.gz ending and is not a session — the
		// same trap isSessionTranscriptName exists to close on the live side.
		if strings.HasSuffix(name, archivedErrorSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, archivedSuffix)
		a, err := describeArchived(filepath.Join(dir, name), id)
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	sortArchivedNewestFirst(out)
	return out
}

// IsArchived reports whether the id names an archived session.
func IsArchived(root, cwd, id string) bool {
	return fileExists(filepath.Join(ArchiveDir(root, cwd), id+archivedSuffix))
}

func sortArchivedNewestFirst(a []ArchivedSession) {
	// Insertion order from ReadDir is lexical; archived-at descending is what a
	// browser wants, with the id as the tiebreak so the order is total.
	for i := 1; i < len(a); i++ {
		for j := i; j > 0; j-- {
			l, r := a[j-1], a[j]
			if l.ArchivedAt.After(r.ArchivedAt) || (l.ArchivedAt.Equal(r.ArchivedAt) && l.ID <= r.ID) {
				break
			}
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// describeArchived reads a compressed transcript into the same SessionSummary
// the live picker renders, so an archived row is not a mystery id.
func describeArchived(path, id string) (ArchivedSession, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ArchivedSession{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return ArchivedSession{}, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return ArchivedSession{}, fmt.Errorf("read archived session %s: %w", id, err)
	}
	defer zr.Close()
	return ArchivedSession{
		SessionSummary: describeSessionFrom(path, zr),
		ID:             id,
		ArchivedAt:     info.ModTime(),
		Bytes:          info.Size(),
	}, nil
}

// OpenArchivedSession returns a reader over an archived transcript's decompressed
// bytes. The caller closes it. This is the seam a future archive viewer reads
// through — nothing else needs to know the archive is compressed at all.
func OpenArchivedSession(root, cwd, id string) (io.ReadCloser, error) {
	path := filepath.Join(ArchiveDir(root, cwd), id+archivedSuffix)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotArchived
		}
		return nil, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read archived session %s: %w", id, err)
	}
	return &archiveReader{zr: zr, f: f}, nil
}

type archiveReader struct {
	zr *gzip.Reader
	f  *os.File
}

func (a *archiveReader) Read(p []byte) (int, error) { return a.zr.Read(p) }
func (a *archiveReader) Close() error {
	err := a.zr.Close()
	if cerr := a.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// gzipFile compresses src to dst through a temp file in dst's directory, so dst
// only ever appears complete. Best compression: a transcript is written once and
// read rarely, and the CPU is spent on a file that is about to sit still.
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := privfs.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// gunzipFile is gzipFile's inverse, with the same temp-then-rename discipline:
// a half-written transcript in the sessions directory would be listed, opened,
// and parsed as a real session.
func gunzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer zr.Close()

	tmp := dst + ".tmp"
	out, err := privfs.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
