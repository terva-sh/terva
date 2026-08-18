package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/slug"

	"terva.sh/terva/packages/privfs"
)

// A card's default model is terva-owned metadata, kept OUTSIDE the card exactly
// like a card group (see groupstore.go): filing a card with a preferred model
// never rewrites the card JSON, so an exported or shared card carries none of
// the local choice. One flat file per card, keyed by the library card id:
//
//	$TERVA_HOME/card-models/<cardid>.json  — {provider, model}
//
// The pref is a SEED, not a live binding — a session started from the card
// resolves its base model through this (the top rung of Card → World →
// Workspace; see Workspace.effectiveDefaultModel), and thereafter owns its own
// model. Clearing the pref (Set with both fields empty) deletes the file, so the
// card falls back to the workspace default again.

// CardModel is a card's default provider+model. Both empty means "unset" — the
// store never persists an empty pref (Set deletes instead), so a loaded doc
// always names at least a model.
type CardModel struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// CardModelDir is the on-disk root, created lazily on first Set; a missing
// directory reads as "no card has a pref".
func CardModelDir() string { return filepath.Join(config.TervaHome(), "card-models") }

// CardModelStore is the flat library of per-card model prefs.
type CardModelStore struct{ dir string }

// NewCardModelStore opens the store at the current $TERVA_HOME.
func NewCardModelStore() *CardModelStore { return &CardModelStore{dir: CardModelDir()} }

// Get loads a card's pref. The bool is false (with no error) when the card has
// none — the common case — so a fallback lookup reads cleanly as "not set".
func (s *CardModelStore) Get(cardID string) (CardModel, bool, error) {
	if err := slug.ValidID(cardID); err != nil {
		return CardModel{}, false, err
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, cardID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return CardModel{}, false, nil
		}
		return CardModel{}, false, fmt.Errorf("card model %q: %w", cardID, err)
	}
	var doc CardModel
	if err := json.Unmarshal(raw, &doc); err != nil {
		return CardModel{}, false, fmt.Errorf("card model %q: %w", cardID, err)
	}
	// A file that somehow holds an empty pref reads as unset rather than as a
	// pref for the empty model.
	if doc.Provider == "" && doc.Model == "" {
		return CardModel{}, false, nil
	}
	return doc, true, nil
}

// Set writes a card's pref, or deletes it when both fields are empty (the "clear,
// fall back to the workspace default" choice). Writing only a model with no
// provider is allowed — the resolver disambiguates it against the catalog like
// any unqualified id.
func (s *CardModelStore) Set(cardID, provider, model string) error {
	if err := slug.ValidID(cardID); err != nil {
		return err
	}
	if provider == "" && model == "" {
		return s.Delete(cardID)
	}
	raw, err := json.MarshalIndent(CardModel{Provider: provider, Model: model}, "", "  ")
	if err != nil {
		return err
	}
	if err := privfs.MkdirAll(s.dir); err != nil {
		return err
	}
	return privfs.WriteFileMode(filepath.Join(s.dir, cardID+".json"), raw, 0o644)
}

// Delete drops a card's pref. A card that never had one is not an error — the
// end state (no pref) is the same either way.
func (s *CardModelStore) Delete(cardID string) error {
	if err := slug.ValidID(cardID); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.dir, cardID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("card model %q: %w", cardID, err)
	}
	return nil
}
