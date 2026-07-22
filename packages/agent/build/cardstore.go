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

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/config"
)

// The character-card library. Cards were path-only before Stage: --card pointed
// at a PNG/JSON on disk, card.Load parsed it into strings, and the pixels (the
// avatar) were dropped on the floor. The library gives cards a home so every
// controller — the Stage app, the panel, the TUI — can inspect, choose, edit,
// and spawn from the same set, and so an imported card KEEPS its avatar.
//
// Layout: $TERVA_HOME/cards/<id>/ holding
//   - card.json   the normalized card (card.Marshal — a CCv2 document that
//                 round-trips unknown `extensions` verbatim). Source of truth.
//   - avatar.png  the ORIGINAL PNG bytes, when imported from a PNG. This is the
//                 portrait; card.json is data. Absent for a JSON import.
//
// A card is data, never code (the authority rule in package card is unchanged):
// editing a card here never grants it any capability.

// CardsDir is the on-disk card library root, $TERVA_HOME/cards.
func CardsDir() string { return filepath.Join(config.TervaHome(), "cards") }

const (
	cardJSONName   = "card.json"
	cardAvatarName = "avatar.png"
)

// StoredCard is a card as it lives in the library: a stable id, the parsed card,
// its normalized JSON bytes, and whether an avatar image was retained.
type StoredCard struct {
	ID   string
	Card card.Card
	// Raw is the normalized card.json (card.Marshal output) — handed to clients
	// verbatim so an editor round-trips `extensions` it does not itself render.
	Raw       []byte
	AvatarExt string // "png" when an avatar was retained, else ""
	// Added is when the card entered the library — the card directory's mtime,
	// which is stamped when the dir is created at import and (unlike card.json's
	// mtime) is NOT disturbed by an in-place edit of the card, so it stays the
	// import time. Zero if the directory could not be stat'd. Powers the
	// "recently added" sort; no separate storage or migration.
	Added time.Time
	// Warnings are non-fatal notes from the import that a user should see — a
	// downscaled or dropped portrait, say. An imported card is usable with
	// warnings; they explain what the library did to it, so a surprise (a
	// missing avatar) has a stated reason instead of looking like a bug.
	Warnings []string
}

// HasAvatar reports whether an avatar image is stored alongside the card.
func (c StoredCard) HasAvatar() bool { return c.AvatarExt != "" }

// CardStore is the library rooted at CardsDir(). The directory is created lazily
// on the first import; a missing directory reads as an empty library.
type CardStore struct{ dir string }

// NewCardStore opens the library at the current $TERVA_HOME. Callers that set
// TERVA_HOME (tests, project scope) must do so before constructing the store.
func NewCardStore() *CardStore { return &CardStore{dir: CardsDir()} }

// List returns every stored card, sorted by name then id. A card directory that
// fails to parse is skipped rather than failing the whole listing.
func (s *CardStore) List() ([]StoredCard, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []StoredCard
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sc, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool {
		ni, nj := strings.ToLower(out[i].Card.Name), strings.ToLower(out[j].Card.Name)
		if ni == nj {
			return out[i].ID < out[j].ID
		}
		return ni < nj
	})
	return out, nil
}

// Get loads one card by id.
func (s *CardStore) Get(id string) (StoredCard, error) {
	if err := validCardID(id); err != nil {
		return StoredCard{}, err
	}
	dir := filepath.Join(s.dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, cardJSONName))
	if err != nil {
		if os.IsNotExist(err) {
			return StoredCard{}, fmt.Errorf("card %q not found", id)
		}
		return StoredCard{}, err
	}
	c, err := card.ParseJSON(raw)
	if err != nil {
		return StoredCard{}, fmt.Errorf("card %q: %w", id, err)
	}
	sc := StoredCard{ID: id, Card: c, Raw: raw, AvatarExt: avatarExt(dir)}
	if fi, err := os.Stat(dir); err == nil {
		sc.Added = fi.ModTime()
	}
	return sc, nil
}

// ImportPath imports a card from a server-local .png or .json path.
func (s *CardStore) ImportPath(path string) (StoredCard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StoredCard{}, err
	}
	return s.ImportBytes(data)
}

// ImportBytes imports a card from raw file bytes (a PNG with an embedded card,
// or a bare JSON card). A PNG's pixels are kept as the avatar, downscaled first
// when the portrait is oversized (see normalizeAvatar). The id is derived from
// the card's content, so re-importing the same card is idempotent (same id, same
// directory) rather than piling up duplicates.
func (s *CardStore) ImportBytes(data []byte) (StoredCard, error) {
	c, err := card.LoadBytes(data)
	if err != nil {
		return StoredCard{}, err
	}
	raw, err := card.Marshal(c)
	if err != nil {
		return StoredCard{}, err
	}
	id := cardID(c.Name, raw)
	dir := filepath.Join(s.dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return StoredCard{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, cardJSONName), raw, 0o644); err != nil {
		return StoredCard{}, err
	}
	ext := ""
	var warnings []string
	// More than one `chara` chunk means the parse had to choose. Say so: the
	// candidates are routinely different REVISIONS of the character rather than
	// duplicates, so the unchosen one is not equivalent, and a user comparing
	// this import against the same card elsewhere deserves to know why they
	// differ. See card.CountCharaChunks for why first-wins is the rule.
	if n := card.CountCharaChunks(data); n > 1 {
		warnings = append(warnings, fmt.Sprintf(
			"this PNG embeds %d character records; imported the first. They are often different revisions of the card, so check the description and greeting look right.", n))
	}
	// A V3 card's data now survives import and round-trips unharmed, but some of
	// what V3 can DECLARE is not yet something terva acts on. Say which, on
	// arrival, so a lorebook that never fires has a stated reason rather than
	// looking like a bug in the card.
	warnings = append(warnings, card.UnsupportedV3Features(c)...)
	if card.IsPNG(data) {
		avatar, note := normalizeAvatar(data)
		if note != "" {
			warnings = append(warnings, note)
		}
		if avatar != nil {
			if err := os.WriteFile(filepath.Join(dir, cardAvatarName), avatar, 0o644); err != nil {
				return StoredCard{}, err
			}
			ext = "png"
		}
	}
	return StoredCard{ID: id, Card: c, Raw: raw, AvatarExt: ext, Warnings: warnings}, nil
}

// Edit replaces a stored card's fields with a full edited card document
// (CCv2 wrapper or flat), re-serializing it canonically. The id (directory) and
// any avatar are unchanged — editing card data never moves or reshoots the
// portrait. The payload is validated (a card needs a name) before anything is
// written.
func (s *CardStore) Edit(id string, cardJSON []byte) (StoredCard, error) {
	if err := validCardID(id); err != nil {
		return StoredCard{}, err
	}
	dir := filepath.Join(s.dir, id)
	if _, err := os.Stat(filepath.Join(dir, cardJSONName)); err != nil {
		if os.IsNotExist(err) {
			return StoredCard{}, fmt.Errorf("card %q not found", id)
		}
		return StoredCard{}, err
	}
	c, err := card.ParseJSON(cardJSON)
	if err != nil {
		return StoredCard{}, err
	}
	raw, err := card.Marshal(c)
	if err != nil {
		return StoredCard{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, cardJSONName), raw, 0o644); err != nil {
		return StoredCard{}, err
	}
	return StoredCard{ID: id, Card: c, Raw: raw, AvatarExt: avatarExt(dir)}, nil
}

// Delete removes a card and its avatar from the library.
func (s *CardStore) Delete(id string) error {
	if err := validCardID(id); err != nil {
		return err
	}
	dir := filepath.Join(s.dir, id)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("card %q not found", id)
		}
		return err
	}
	return os.RemoveAll(dir)
}

// AvatarPath returns the filesystem path of a card's retained avatar image, or
// "" if the card has none or the id is invalid. The /media route serves it.
func (s *CardStore) AvatarPath(id string) string {
	if validCardID(id) != nil {
		return ""
	}
	dir := filepath.Join(s.dir, id)
	if avatarExt(dir) == "" {
		return ""
	}
	return filepath.Join(dir, cardAvatarName)
}

// cardFavoritesName is the flat set of favorited card ids, a sibling of the
// cards/ directory (so it rides TERVA_HOME with the store). Favoriting is a
// per-library preference — a highlight and a sort priority, not card data — so
// it lives OUTSIDE the card: filing one never rewrites the card, and deleting a
// card never touches this file (a stale id is filtered on read, like a group's
// stale member).
const cardFavoritesName = "card-favorites.json"

func (s *CardStore) favoritesPath() string {
	return filepath.Join(filepath.Dir(s.dir), cardFavoritesName)
}

// Favorites returns the set of favorited card ids. A missing file is an empty
// set, never an error.
func (s *CardStore) Favorites() (map[string]bool, error) {
	raw, err := os.ReadFile(s.favoritesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("card favorites: %w", err)
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// SetFavorite adds or removes a card id from the favorites set. Idempotent; the
// id is validated (a favorite is only meaningful for a real card id) and the set
// is written sorted so the file is stable.
func (s *CardStore) SetFavorite(id string, fav bool) error {
	if err := validCardID(id); err != nil {
		return err
	}
	set, err := s.Favorites()
	if err != nil {
		return err
	}
	if set[id] == fav {
		return nil
	}
	if fav {
		set[id] = true
	} else {
		delete(set, id)
	}
	ids := make([]string, 0, len(set))
	for k := range set {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.favoritesPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.favoritesPath(), raw, 0o644)
}

// ResolveCardRef turns a --card / CreateOpts.Card reference into a path
// card.Load can read. An existing readable file passes through unchanged (the
// classic --card <path> flow); otherwise the ref is treated as a library id and
// resolved to that card's stored card.json — which is itself a valid card
// document, so nothing downstream has to know the difference. This is what lets a
// controller spawn a session straight from a library id.
func ResolveCardRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if fi, err := os.Stat(ref); err == nil && !fi.IsDir() {
		return ref, nil
	}
	if validCardID(ref) == nil {
		p := filepath.Join(CardsDir(), ref, cardJSONName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("card %q is neither a readable file nor a library id", ref)
}

// avatarExt reports the retained avatar's extension for a card dir, or "".
func avatarExt(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, cardAvatarName)); err == nil {
		return "png"
	}
	return ""
}

// cardID is a stable, human-legible library id: a slug of the card name plus a
// short content hash. The hash makes an import idempotent and disambiguates two
// cards that share a name.
func cardID(name string, normalized []byte) string {
	sum := sha256.Sum256(normalized)
	h := hex.EncodeToString(sum[:])[:12]
	slug := cardSlug(name)
	if slug == "" {
		return h
	}
	return slug + "-" + h
}

// cardSlug lowercases a name and keeps [a-z0-9], collapsing every other run to a
// single dash, capped so an absurd name can't make an absurd directory.
func cardSlug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if b.Len() > 0 && !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// validCardID rejects ids that could escape the library directory. Ids come off
// the wire (cards.get / cards.edit / cards.delete), so a "../../etc" id must not
// resolve to a real path.
func validCardID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("invalid card id %q", id)
	}
	return nil
}
