package core

import (
	"path/filepath"
	"strings"
)

// Sidecars: the files that belong to a transcript and must travel with it.
//
// A sidecar is that session's data. Every lifecycle operation on a transcript
// has to carry it: delete removes it, the empty-session prune drops it,
// archiving compresses it alongside, and restoring brings it back. Miss one and
// the failure is silent — an orphaned file no listing shows, or a file that
// outlives the session it described.
//
// That knowledge used to be hand-written at each site, once per sidecar. One
// sidecar made that survivable; a second makes it a drift hazard, because the
// lists are far apart and nothing fails when one of them is short. This table
// is the single place a sidecar is declared, and every site consults it.
//
// To add a sidecar: add one row here. The lifecycle sites need no edit, and the
// table-driven guards in session_sidecar_test.go extend to it automatically —
// that automatic extension is the point of the table, so keep the guards
// ranging over sessionSidecars rather than naming a suffix.

// sessionSidecar is one file that lives beside a transcript and belongs to it.
type sessionSidecar struct {
	// suffix is the live form: it replaces ".jsonl" on the transcript path.
	// It deliberately keeps the transcript's base name so the two sort together
	// in a directory listing.
	suffix string
	// archivedSuffix is the compressed form, stored beside the ".jsonl.gz" in
	// the archive directory. Empty means the sidecar is not archived — it is
	// dropped instead, which is a deliberate choice per sidecar, not a default.
	archivedSuffix string
}

// The error log's two forms (LogError). Named because ErrorLogPathFor is public
// API and derives the live path from the same string — one definition, so the
// table and the path helper cannot disagree.
const (
	errorSidecarSuffix         = ".errors.jsonl"
	errorSidecarArchivedSuffix = ".errors.jsonl.gz"
)

// The state sidecar's two forms (see session_state.go and
// docs/proposals/session-state-sidecar.md). Same reason for naming them:
// SessionStatePathFor derives the live path from this string.
const (
	stateSidecarSuffix         = ".state.json"
	stateSidecarArchivedSuffix = ".state.json.gz"
)

// sessionSidecars declares every file that travels with a transcript.
var sessionSidecars = []sessionSidecar{
	// The error log (LogError). Archived with its transcript: archiving one
	// without the other orphans a failure record against a session no listing
	// shows.
	{suffix: errorSidecarSuffix, archivedSuffix: errorSidecarArchivedSuffix},
	// Client state, the composer draft first among it. Archived deliberately
	// rather than dropped: the draft is the user's own prose, the same class of
	// thing as the transcript, and someone who archives a session and restores
	// it later should find the half-written message they left in it. Compressing
	// a small JSON buys nothing in bytes; it buys one uniform archive path.
	{suffix: stateSidecarSuffix, archivedSuffix: stateSidecarArchivedSuffix},
}

// SessionSidecarPaths returns the paths of every sidecar belonging to the
// transcript at transcriptPath, whether or not each exists. Callers removing a
// transcript must remove these too; a missing file is not an error.
//
// Empty in, empty out.
func SessionSidecarPaths(transcriptPath string) []string {
	if transcriptPath == "" {
		return nil
	}
	stem := strings.TrimSuffix(transcriptPath, ".jsonl")
	out := make([]string, 0, len(sessionSidecars))
	for _, sc := range sessionSidecars {
		out = append(out, stem+sc.suffix)
	}
	return out
}

// sessionSidecarPair is one sidecar's live and archived location, for moving it
// in either direction.
type sessionSidecarPair struct {
	Live     string
	Archived string
}

// sessionSidecarPairs pairs each archivable sidecar's live path (derived from
// transcriptPath) with its archived path in archiveDir under the session id.
// Sidecars with no archived form are omitted, so a caller that ranges over this
// carries exactly the ones meant to survive archiving.
func sessionSidecarPairs(transcriptPath, archiveDir, id string) []sessionSidecarPair {
	if transcriptPath == "" || archiveDir == "" || id == "" {
		return nil
	}
	stem := strings.TrimSuffix(transcriptPath, ".jsonl")
	out := make([]sessionSidecarPair, 0, len(sessionSidecars))
	for _, sc := range sessionSidecars {
		if sc.archivedSuffix == "" {
			continue
		}
		out = append(out, sessionSidecarPair{
			Live:     stem + sc.suffix,
			Archived: filepath.Join(archiveDir, id+sc.archivedSuffix),
		})
	}
	return out
}

// isSessionSidecarName reports whether a directory entry name is a sidecar
// rather than a transcript. Sidecars share the transcript's base name (and some
// share its extension) so they sort together, which is exactly why a scan
// cannot tell them apart by extension alone.
func isSessionSidecarName(name string) bool {
	for _, sc := range sessionSidecars {
		if strings.HasSuffix(name, sc.suffix) {
			return true
		}
	}
	return false
}

// isArchivedSessionSidecarName is isSessionSidecarName for the archive
// directory. The archived forms share the .jsonl.gz ending with archived
// transcripts, so a scan there needs the same filter the live side needs — the
// trap is identical and so is the fix.
func isArchivedSessionSidecarName(name string) bool {
	for _, sc := range sessionSidecars {
		if sc.archivedSuffix != "" && strings.HasSuffix(name, sc.archivedSuffix) {
			return true
		}
	}
	return false
}
