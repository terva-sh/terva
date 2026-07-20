package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
)

// The saved-World library (Worlds W5). A World is the same object whether
// embedded in a session or saved here — promotion is a MOVE up a scope, never
// a rewrite (the proposal's pillar 2): the doc below is exactly the session's
// World state (roster + pins + lore incl. the audience/learned axes +
// coordination), lifted verbatim. Saved Worlds are listed beside sessions and
// group the sessions created in them (SessionMeta.World).
//
// Sync semantics are EXPLICIT, not live: a session created in a World copies
// the World in; play mutates the session's own copy; worlds.save writes a
// member session's current state back (last-wins). Deliberate — multi-session
// write-through would need conflict rules nothing here has earned yet.
//
// Layout: $TERVA_HOME/worlds/<id>/world.json — the CardStore pattern (a
// directory per World, so a cover image or bundled assets have a home later).

// WorldsDir is the on-disk saved-World root, $TERVA_HOME/worlds.
func WorldsDir() string { return filepath.Join(config.TervaHome(), "worlds") }

const worldJSONName = "world.json"

// WorldDoc is a saved World: the id is minted at creation (slug + short hash)
// and STABLE across updates — a World mutates in place, unlike a card.
type WorldDoc struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Characters is the roster (name → card ref), CharacterModels the per-
	// character model pins — the session Cast/CastModels shape, unchanged.
	Characters      map[string]string         `json:"characters,omitempty"`
	CharacterModels map[string]core.CastRoute `json:"character_models,omitempty"`
	// Lore is the World's lorebook, audience scoping and learned-when ledger
	// included — the whole point of saving is that this survives the session.
	Lore         []core.WorldLoreEntry `json:"lore,omitempty"`
	Coordination string                `json:"coordination,omitempty"`
	Created      time.Time             `json:"created"`
	Updated      time.Time             `json:"updated"`
}

// WorldStore is the library rooted at WorldsDir(). Created lazily on first
// save; a missing directory reads as an empty library.
type WorldStore struct{ dir string }

// NewWorldStore opens the library at the current $TERVA_HOME.
func NewWorldStore() *WorldStore { return &WorldStore{dir: WorldsDir()} }

// List returns every saved World, sorted by name then id. A directory that
// fails to parse is skipped rather than failing the listing.
func (s *WorldStore) List() ([]WorldDoc, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []WorldDoc
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if doc, err := s.Get(e.Name()); err == nil {
			out = append(out, doc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get loads one saved World by id.
func (s *WorldStore) Get(id string) (WorldDoc, error) {
	id = strings.TrimSpace(id)
	if id == "" || id != filepath.Base(id) {
		return WorldDoc{}, fmt.Errorf("invalid world id %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id, worldJSONName))
	if err != nil {
		return WorldDoc{}, fmt.Errorf("world %q: %w", id, err)
	}
	var doc WorldDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return WorldDoc{}, fmt.Errorf("world %q: %w", id, err)
	}
	doc.ID = id // the directory is authoritative
	return doc, nil
}

// Save writes a World. An empty doc.ID mints one (slug + short hash — stable
// thereafter) and stamps Created; a non-empty ID updates in place, preserving
// Created. Updated is stamped either way. Returns the stored doc.
func (s *WorldStore) Save(doc WorldDoc) (WorldDoc, error) {
	if strings.TrimSpace(doc.Name) == "" {
		return WorldDoc{}, fmt.Errorf("a World needs a name")
	}
	now := time.Now().UTC()
	if doc.ID == "" {
		doc.ID = worldID(doc.Name, now)
		doc.Created = now
	} else {
		if prev, err := s.Get(doc.ID); err == nil {
			doc.Created = prev.Created
		} else if doc.Created.IsZero() {
			doc.Created = now
		}
	}
	doc.Updated = now
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return WorldDoc{}, err
	}
	dir := filepath.Join(s.dir, doc.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return WorldDoc{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, worldJSONName), raw, 0o644); err != nil {
		return WorldDoc{}, err
	}
	return doc, nil
}

// Delete removes a saved World. Sessions that were members keep their embedded
// copies untouched — deleting the container never deletes the play.
func (s *WorldStore) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || id != filepath.Base(id) {
		return fmt.Errorf("invalid world id %q", id)
	}
	dir := filepath.Join(s.dir, id)
	if _, err := os.Stat(filepath.Join(dir, worldJSONName)); err != nil {
		return fmt.Errorf("world %q: %w", id, err)
	}
	return os.RemoveAll(dir)
}

const worldCoverName = "cover.png"

// CoverPath returns the on-disk path of a World's cover image, or "" when the
// World has none (or the id is invalid — the same escape guard as Get, so the
// result is always safe to hand to http.ServeFile).
func (s *WorldStore) CoverPath(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || id != filepath.Base(id) {
		return ""
	}
	p := filepath.Join(s.dir, id, worldCoverName)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// SetCover writes a World's cover image (the bytes are stored as given — the
// client sends what the user picked). The World must exist.
func (s *WorldStore) SetCover(id string, data []byte) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, id, worldCoverName), data, 0o644)
}

// RemoveCover deletes a World's cover image; a World without one is a no-op.
func (s *WorldStore) RemoveCover(id string) error {
	p := s.CoverPath(id)
	if p == "" {
		return nil
	}
	return os.Remove(p)
}

// worldID mints a stable, human-legible id: the name's slug plus a short hash
// of name+moment (unlike cardID's content hash — a World mutates in place, so
// its id must not follow its contents).
func worldID(name string, now time.Time) string {
	sum := sha256.Sum256([]byte(name + now.Format(time.RFC3339Nano)))
	h := hex.EncodeToString(sum[:])[:8]
	slug := cardSlug(name)
	if slug == "" {
		return h
	}
	return slug + "-" + h
}
