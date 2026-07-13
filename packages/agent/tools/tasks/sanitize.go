package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

// CleanOneLine collapses a value to a single safe display line: newlines, tabs,
// and other control characters (including ANSI escapes) are dropped or turned
// into spaces, runs of whitespace are collapsed, the result is trimmed, and it
// is truncated to max runes with an ellipsis. This defuses display-injection via
// task fields and the host-supplied session title, and bounds output size.
// max <= 0 means no length limit.
func CleanOneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1 // drop other control chars (e.g. ESC)
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 {
		r := []rune(s)
		if len(r) > max {
			return strings.TrimSpace(string(r[:max])) + "…"
		}
	}
	return s
}

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
