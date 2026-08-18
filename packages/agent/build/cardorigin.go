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

// Where a card came from is terva-owned metadata ABOUT a card, so — like a
// card's edit history (cardhistory.go), its group membership (groupstore.go), or
// its default model (cardmodelstore.go) — it lives OUTSIDE the card directory,
// keyed by the library card id:
//
//	$TERVA_HOME/card-origin/<cardid>.json  — {world, forked_from}
//
// Two reasons it is a sibling root rather than a field in the card:
//
//   - a card you export or share must not carry a record of which of YOUR
//     Worlds it was made for;
//   - StoredCard.Added is the card directory's mtime, and writing anything new
//     inside that directory would turn "added" into "last edited" and reshuffle
//     the library's recency sort.
//
// WHY it exists: a World's roster holds a plain card ref, and CardStore.Edit
// rewrites a card IN PLACE — the content hash is minted at import and never
// re-derived. So one library card is shared by every World, every session, and
// the shelf, and an edit accepted inside one World changes it everywhere. That
// is the hazard the world doctor would otherwise ship: its first accepted card
// proposal would rewrite a character other Worlds are still playing.
//
// The answer is copy-on-write (CardStore.Fork), and this store is what makes the
// result legible afterwards: without it a forked variant is an anonymous
// near-duplicate cluttering the library, indistinguishable from a card the
// author made on purpose.
//
// Note what is NOT stored: whether a variant should be HIDDEN. That is derived
// (see VariantOf callers) from whether the World still exists and still rosters
// the card, so deleting a World un-orphans its variants automatically and no
// cleanup pass has to exist to be forgotten.

// CardOrigin records that a card was forked for a World. World is the
// worlds-library id it was made for; ForkedFrom is the card it was forked from,
// which may itself be a fork.
type CardOrigin struct {
	World      string `json:"world,omitempty"`
	ForkedFrom string `json:"forked_from,omitempty"`
}

// CardOriginDir is the on-disk root, created lazily on the first record; a
// missing directory reads as "every card is an original".
func CardOriginDir() string { return filepath.Join(config.TervaHome(), "card-origin") }

// CardOriginStore is the flat library of per-card provenance.
type CardOriginStore struct{ dir string }

// NewCardOriginStore opens the store at the current $TERVA_HOME.
func NewCardOriginStore() *CardOriginStore { return &CardOriginStore{dir: CardOriginDir()} }

func (s *CardOriginStore) path(id string) (string, error) {
	if err := slug.ValidID(id); err != nil {
		return "", fmt.Errorf("invalid card id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Get loads a card's origin. The bool is false (with no error) when the card has
// none — the common case, and the reason a caller reads cleanly as "not a fork".
func (s *CardOriginStore) Get(id string) (CardOrigin, bool, error) {
	p, err := s.path(id)
	if err != nil {
		return CardOrigin{}, false, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return CardOrigin{}, false, nil
		}
		return CardOrigin{}, false, err
	}
	var o CardOrigin
	if err := json.Unmarshal(raw, &o); err != nil {
		return CardOrigin{}, false, fmt.Errorf("card origin %q: %w", id, err)
	}
	return o, true, nil
}

// Set records a card's origin. A record naming no World is meaningless — the
// whole point is which World the fork belongs to — so it deletes instead,
// keeping "no file" as the single representation of "not a fork".
func (s *CardOriginStore) Set(id string, o CardOrigin) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if o.World == "" {
		return s.Delete(id)
	}
	if err := privfs.MkdirAll(s.dir); err != nil {
		return err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return privfs.WriteFileMode(p, raw, 0o644)
}

// Delete drops a card's origin record; a card without one is a no-op.
func (s *CardOriginStore) Delete(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// All loads every recorded origin, keyed by card id. Used by the library
// listing, which has to decide about a whole shelf at once and would otherwise
// stat one file per card.
func (s *CardOriginStore) All() (map[string]CardOrigin, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]CardOrigin{}, nil
		}
		return nil, err
	}
	out := make(map[string]CardOrigin, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		id := name[:len(name)-len(".json")]
		// A record that fails to parse is skipped rather than failing the whole
		// listing: provenance is decoration, and one bad file must not take the
		// card library down with it.
		if o, ok, err := s.Get(id); err == nil && ok {
			out[id] = o
		}
	}
	return out, nil
}
