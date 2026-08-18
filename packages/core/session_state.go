package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"terva.sh/terva/packages/privfs"
)

// The session state sidecar: durable per-session CLIENT state, kept beside the
// transcript at <session>.state.json. See
// docs/proposals/session-state-sidecar.md.
//
// It is general-purpose by design. The composer draft is its first tenant, not
// its purpose — a second tenant must not need a second file, so the file is a
// map of named tenants and this type never assumes it knows all of them.
//
// It is a CONVENIENCE, and the transcript is the data. That asymmetry decides
// every error decision below: a state file that is missing, truncated,
// hand-edited, or written by a newer binary means "no state", and never an
// error that stops a session from opening. Losing a draft is a disappointment;
// refusing to open the session it belonged to is a catastrophe.

// sessionStateVersion is the format version this binary writes.
const sessionStateVersion = 1

// MaxSessionStateBytes caps the encoded sidecar. The composer draft persists
// Editor.SubmitValue(), which expands paste placeholders into their bodies, so
// a pasted logfile would otherwise land here whole and be re-read on every
// session bind.
//
// Over the cap we refuse to persist and say so, rather than storing a prefix:
// half a draft looks like a whole one, and the user would discover the loss
// only by reading carefully. 64 KiB is far above any hand-typed message and far
// below a pasted logfile.
const MaxSessionStateBytes = 64 << 10

// ErrSessionStateTooLarge is returned by SaveSessionState when the encoded
// state exceeds MaxSessionStateBytes. Callers surface it; nothing is written.
var ErrSessionStateTooLarge = errors.New("session state exceeds the size cap")

// Composer draft provenance. A draft the user typed and a line the model
// offered are not the same thing, and the difference has to survive a restart:
// a stored suggestion comes back as a GHOST to accept, never as text already
// sitting in the composer as though the user wrote it. See
// docs/proposals/idle-suggestions.md on why handing the machine's words back as
// the user's own is the failure to avoid.
const (
	ComposerSourceUser       = "user"
	ComposerSourceSuggestion = "suggestion"
)

// composerTenantKey is the state file's key for the composer tenant.
const composerTenantKey = "composer"

// ComposerDraft is the composer tenant: one unsent message and where it came
// from.
type ComposerDraft struct {
	// Text is the draft as plain text, with paste/file/dir placeholder bodies
	// already expanded (Editor.SubmitValue). Storing the un-expanded buffer
	// would persist markers pointing at a map that no longer exists.
	Text string `json:"text"`
	// Source is ComposerSourceUser or ComposerSourceSuggestion.
	Source string `json:"source"`
	// UpdatedAt is when the draft was last written, for a client that wants to
	// tell the user how old the thing it just restored is.
	UpdatedAt time.Time `json:"updated_at"`
}

// IsSuggestion reports whether this draft is the machine's words rather than
// the user's, and therefore must be restored as an offer.
func (d ComposerDraft) IsSuggestion() bool { return d.Source == ComposerSourceSuggestion }

// SessionState is the decoded sidecar. Tenants this binary does not know about
// are carried verbatim, so an older binary cannot silently delete a newer one's
// state by round-tripping the file.
type SessionState struct {
	// raw holds every top-level key, known tenants included. Keeping the whole
	// document rather than a struct is what makes the round trip lossless.
	raw map[string]json.RawMessage
}

// SessionStatePathFor derives the state sidecar's path from a transcript path,
// for callers holding a path rather than an open session. Empty in, empty out.
func SessionStatePathFor(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	return strings.TrimSuffix(transcriptPath, ".jsonl") + stateSidecarSuffix
}

// StatePath returns this session's state sidecar path.
func (s *Session) StatePath() string {
	if s == nil {
		return ""
	}
	return SessionStatePathFor(s.Path)
}

// LoadSessionState reads the sidecar at path.
//
// It DELIBERATELY returns no error. Missing, unreadable, truncated,
// hand-edited, or written by a future version all mean the same thing to every
// caller — no state — and an error return would invite a caller to propagate it
// and fail a session open over a lost draft. ListArchivedSessions takes the
// same line for the same reason.
func LoadSessionState(path string) SessionState {
	if path == "" {
		return SessionState{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return SessionState{}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return SessionState{}
	}
	return SessionState{raw: raw}
}

// SaveSessionState writes state to path atomically at 0600 (privfs.WriteFile:
// temp file, chmod, rename — the treatment config.json gets, because this holds
// the user's own prose).
//
// Returns ErrSessionStateTooLarge without writing when the encoded form exceeds
// MaxSessionStateBytes, so the caller can tell the user their draft was not
// kept rather than silently storing part of it.
//
// Writing an empty state removes the file: an empty sidecar and no sidecar mean
// the same thing, and leaving one behind litters the sessions directory with
// files that say nothing.
func SaveSessionState(path string, state SessionState) error {
	if path == "" {
		return errors.New("session state: empty path")
	}
	if state.isEmpty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("session state: remove: %w", err)
		}
		return nil
	}
	raw := make(map[string]json.RawMessage, len(state.raw)+1)
	for k, v := range state.raw {
		raw[k] = v
	}
	ver, err := json.Marshal(sessionStateVersion)
	if err != nil {
		return fmt.Errorf("session state: encode version: %w", err)
	}
	raw["version"] = ver

	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("session state: encode: %w", err)
	}
	if len(b) > MaxSessionStateBytes {
		return fmt.Errorf("%w: %d bytes, cap %d", ErrSessionStateTooLarge, len(b), MaxSessionStateBytes)
	}
	if err := privfs.WriteFile(path, b); err != nil {
		return fmt.Errorf("session state: write: %w", err)
	}
	return nil
}

// isEmpty reports whether the state carries nothing worth a file. A document
// holding only the version stamp counts as empty: the stamp describes the file
// rather than any tenant's state.
func (s SessionState) isEmpty() bool {
	for k := range s.raw {
		if k != "version" {
			return false
		}
	}
	return true
}

// Composer returns the composer tenant, and whether one is present.
//
// A tenant that fails to decode is reported absent rather than surfaced as an
// error: per-tenant tolerance is the same rule the file-level load follows, so
// one malformed tenant cannot cost the others.
func (s SessionState) Composer() (ComposerDraft, bool) {
	b, ok := s.raw[composerTenantKey]
	if !ok {
		return ComposerDraft{}, false
	}
	var d ComposerDraft
	if err := json.Unmarshal(b, &d); err != nil {
		return ComposerDraft{}, false
	}
	if d.Text == "" {
		// An empty draft is not a draft. Storing one would make "restore" put
		// nothing into a composer and call it a restore.
		return ComposerDraft{}, false
	}
	return d, true
}

// SetComposer installs the composer tenant, leaving every other tenant alone.
// An empty Text clears it, because an empty draft is not a draft.
func (s *SessionState) SetComposer(d ComposerDraft) error {
	if strings.TrimSpace(d.Text) == "" {
		s.ClearComposer()
		return nil
	}
	if d.Source == "" {
		d.Source = ComposerSourceUser
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now().UTC()
	}
	b, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("session state: encode composer: %w", err)
	}
	if s.raw == nil {
		s.raw = map[string]json.RawMessage{}
	}
	s.raw[composerTenantKey] = b
	return nil
}

// ClearComposer drops the composer tenant, leaving every other tenant alone.
func (s *SessionState) ClearComposer() {
	delete(s.raw, composerTenantKey)
}
