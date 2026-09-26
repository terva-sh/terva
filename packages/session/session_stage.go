package session

import (
	"encoding/json"
	"time"
)

// The Stage state of a session is stored as its own rows rather than as fields
// of the last-wins meta row.
//
// It used to ride SessionMeta, which put a character card, a cast and a bound
// user persona into the metadata of every session, coding or not, and made the
// session package describe a product it should only store for (decision 0004
// recorded that as an accepted leak; decision 0012 gives the state to
// terva-stage). Moving the fields to their own type closes the leak in Go: an
// engine reader of SessionMeta can no longer see a card. Giving them their own
// row closes it on disk: a meta row written at format version 5 says nothing
// about Stage.
//
// The rows live in the session file, not in a sidecar, for the reason the lore
// rows do (see the header of session_lore.go): branch, export, import, archive
// and delete already carry the transcript, and a separate file is one more
// thing each of those paths has to remember, which is the bug #470 closed.
//
// Unlike lore, a stage row is a whole snapshot. The state is a dozen short
// strings and two small maps, and it changes a handful of times per session, so
// the diff machinery lore needed would buy nothing here.
const recordStage = "stage"

// Stage is a session's immersive state: how it was created, what is bound to
// it, and which World it belongs to. Zero for an ordinary coding session.
//
// The JSON tags are the ones these fields had on the meta row, so a stage row
// and a pre-v5 meta row spell the state the same way.
type Stage struct {
	// Experience, Card, Cast, and Greeting persist how an immersive session was
	// created — its --chat/--play mode, character card, declared cast, and
	// selected opening — so a restart rebuilds the same session rather than the
	// workspace default.
	Experience string            `json:"experience,omitempty"` // "chat" | "play"
	Card       string            `json:"card,omitempty"`       // card ref (library id or path)
	Cast       map[string]string `json:"cast,omitempty"`       // actor name -> persona|card ref
	// CastModels pins specific cast members to a provider+model (Phase 7); an actor
	// with no entry inherits the session/host route. Parallel to Cast (keyed by the
	// same actor name) so the ref map stays a plain name->ref.
	CastModels map[string]CastRoute `json:"cast_models,omitempty"`
	Greeting   int                  `json:"greeting,omitempty"` // selected greeting index
	// Background is the scene backdrop bound to this session (a backgrounds-library
	// id, served over /media/backgrounds/<id>). Unlike the creation spec above it
	// is mutable mid-session (SetBackground), so it is presentation metadata the
	// client renders, not a build input.
	Background string `json:"background,omitempty"`
	// UserName and UserDescription are the session's bound user persona — who the
	// user is *in the story* (distinct from SessionMeta.Persona, which is who the
	// agent is). The DESCRIPTION rides the uncached per-turn tail, so a change
	// takes effect next turn for free. The NAME is the card {{user}} macro, baked
	// into the cached prefix at build (threaded into build Args.As on materialize),
	// so changing it mid-session is a deliberate prefix rebuild.
	UserName        string `json:"user_name,omitempty"`
	UserDescription string `json:"user_description,omitempty"`
	// UserGender and UserPronouns are the persona's stated identity (free-form
	// strings — the client offers an inclusive dropdown with an "Other" text
	// escape, so these are never an enum). When set, the per-turn user-persona
	// frame tells the model to use them; when unset, it steers the model away from
	// inventing them.
	UserGender   string `json:"user_gender,omitempty"`
	UserPronouns string `json:"user_pronouns,omitempty"`
	// World is the saved World this session belongs to (a worlds-library id,
	// W5) — stamped when the session is created in a World or when its
	// embedded World is promoted (worlds.save). Grouping metadata: the
	// session's own Cast, lorebook and Coordination remain its working copy;
	// nothing here syncs live.
	World string `json:"world,omitempty"`
}

// IsZero reports whether s holds no Stage state at all.
func (s Stage) IsZero() bool {
	return s.Experience == "" && s.Card == "" && len(s.Cast) == 0 && len(s.CastModels) == 0 &&
		s.Greeting == 0 && s.Background == "" && s.UserName == "" && s.UserDescription == "" &&
		s.UserGender == "" && s.UserPronouns == "" && s.World == ""
}

// foldMetaStage applies a meta row's contribution to a folded Stage.
//
// Below v5 the row IS the Stage snapshot, so absent fields mean cleared. At v5
// and up the state lives in stage rows and the meta row has nothing to say about
// it; reading its silence as a clear would unbind the card on the next note edit.
//
// Shared by every reader that folds the state, as foldMetaLore is, so the rule
// is written once.
func foldMetaStage(stage Stage, m SessionMeta) Stage {
	if m.FormatVersion < sessionFormatVersionStage {
		return m.legacyStage
	}
	return stage
}

// sessionMetaFields is SessionMeta without its JSON methods, so they can marshal
// the struct without recursing into themselves.
type sessionMetaFields SessionMeta

// sessionMetaJSON is a meta row as it sits on disk: the meta fields with the
// legacy Stage fields flattened beside them, where every pre-v5 row put them.
type sessionMetaJSON struct {
	sessionMetaFields
	Stage
}

// MarshalJSON writes the meta fields, and the legacy Stage fields when the row
// carries them (a row below v5, or an old row being re-emitted by export).
func (m SessionMeta) MarshalJSON() ([]byte, error) {
	return json.Marshal(sessionMetaJSON{sessionMetaFields(m), m.legacyStage})
}

// UnmarshalJSON reads a meta row, keeping any legacy Stage fields aside for
// foldMetaStage.
func (m *SessionMeta) UnmarshalJSON(b []byte) error {
	var row sessionMetaJSON
	if err := json.Unmarshal(b, &row); err != nil {
		return err
	}
	*m = SessionMeta(row.sessionMetaFields)
	m.legacyStage = row.Stage
	return nil
}

// writeStageLocked makes next the session's Stage state and records it; the
// caller must hold writeMu.
//
// Below v5 a meta row is itself a Stage snapshot, so an empty Stage needs no
// row of its own and no version bump: the meta row says it. That keeps a coding
// session, which never holds Stage state, from claiming a version it does not
// use. Anything else declares v5 first, with a meta row, so no reader meets a
// stage row before the version that says stage rows exist; the bump also stops
// writeMeta carrying the legacy fields.
func (s *Session) writeStageLocked(next Stage) error {
	s.Stage = next
	if s.Meta.FormatVersion < sessionFormatVersionStage {
		if next.IsZero() {
			return s.writeMetaLocked()
		}
		s.Meta.FormatVersion = sessionFormatVersionStage
		if err := s.writeMetaLocked(); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	return s.writeLineLocked(sessionLine{Type: recordStage, Stage: &next, At: &now})
}

// updateStage applies edit to a copy of the session's Stage and records the
// result, holding writeMu across the read and the write.
func (s *Session) updateStage(edit func(*Stage)) error {
	if s == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	next := s.Stage
	edit(&next)
	return s.writeStageLocked(next)
}
