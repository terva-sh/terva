package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"terva.sh/terva/packages/core"
)

// Field-length and count caps. Display fields are normalized to one line and
// truncated to these bounds at ingress so neither persisted state nor tool/panel
// output can be flooded by oversized model input.
const (
	MaxTitleLen        = 200
	MaxActiveFormLen   = 200
	MaxNoteLen         = 300
	MaxEvidenceLen     = 500
	MaxSessionTitle    = 80
	MaxLabelLen        = 80
	MaxBatch           = 100
	MaxTasksPerSession = 500
	// MaxGenerations bounds how many archived task lists a session file retains.
	// The file is loaded whole on every session open, so archives can't grow
	// without limit; the oldest generations are dropped FIFO past this cap.
	MaxGenerations = 50
)

// CleanOneLine is core.CleanOneLine, kept as a name in this package because
// every call site here reads as a task-field sanitizer. The implementation moved
// to core when memory needed the same one: two copies of a sanitizer means a fix
// to one leaves the other accepting what it was meant to reject.
func CleanOneLine(s string, max int) string { return core.CleanOneLine(s, max) }

// safeSessionID reports whether id can be used verbatim in a filename: a
// non-empty run of [A-Za-z0-9._-] (<=128) with no "..", and not "." or "..".
func safeSessionID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 128 {
		return false
	}
	if strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// sessionFileName maps a session id to a traversal-safe file name. Path-safe ids
// (the documented UUID contract) map to the readable tasks-<id>.json; anything
// else is hashed so a hostile id can never escape the data dir.
func sessionFileName(id string) string {
	if safeSessionID(id) {
		return "tasks-" + id + ".json"
	}
	sum := sha256.Sum256([]byte(id))
	return "tasks-" + hex.EncodeToString(sum[:8]) + ".json"
}
