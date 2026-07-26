package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
)

// UserPersona is a saved "who I am in the story" identity — a name + description
// the user reuses across chats, distinct from an agent persona (which shapes the
// CHARACTER). Stored one JSON file per persona under $TERVA_HOME/userpersonas,
// keyed by a slug of the name. Exactly one may be Default: the identity a new
// immersive session pre-fills, so a chat no longer opens as the literal "User".
type UserPersona struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Pronouns    string `json:"pronouns,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

// UserPersonasDir is the saved-user-persona library, mirroring CardsDir/PersonasDir.
func UserPersonasDir() string { return filepath.Join(config.TervaHome(), "userpersonas") }

// UserPersonaStore is a flat directory of saved user personas.
type UserPersonaStore struct{ dir string }

// NewUserPersonaStore opens the default library.
func NewUserPersonaStore() *UserPersonaStore { return &UserPersonaStore{dir: UserPersonasDir()} }

// List returns every saved user persona, name-sorted. A missing directory is an
// empty library, not an error; a corrupt entry is skipped rather than failing.
func (s *UserPersonaStore) List() ([]UserPersona, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []UserPersona
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if p, err := s.read(filepath.Join(s.dir, e.Name())); err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *UserPersonaStore) read(path string) (UserPersona, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return UserPersona{}, err
	}
	var p UserPersona
	if err := json.Unmarshal(raw, &p); err != nil {
		return UserPersona{}, err
	}
	if p.Ref == "" {
		p.Ref = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return p, nil
}

// Get returns one saved persona by ref (a name slug).
func (s *UserPersonaStore) Get(ref string) (UserPersona, error) {
	slug := cardSlug(ref)
	if slug == "" {
		return UserPersona{}, fmt.Errorf("user persona: empty ref")
	}
	p, err := s.read(filepath.Join(s.dir, slug+".json"))
	if err != nil && os.IsNotExist(err) {
		// Saved before slugs folded diacritics? The file sits under the old
		// spelling; accept it only when it names this persona, so "Zoë"
		// cannot pick up a distinct "Zo".
		if old, ok := s.legacy(ref); ok {
			return old, nil
		}
	}
	return p, err
}

// legacy returns the persona filed under name's pre-diacritic-fold slug, if
// that file exists and actually names this persona.
func (s *UserPersonaStore) legacy(name string) (UserPersona, bool) {
	slug := legacyCardSlug(name)
	if slug == "" || slug == cardSlug(name) {
		return UserPersona{}, false
	}
	old, err := s.read(filepath.Join(s.dir, slug+".json"))
	if err != nil || !strings.EqualFold(old.Name, name) {
		return UserPersona{}, false
	}
	return old, true
}

// Save upserts a persona keyed by a slug of its name, returning the stored form
// (with its ref). An empty name is an error.
func (s *UserPersonaStore) Save(p UserPersona) (UserPersona, error) {
	slug := cardSlug(p.Name)
	if slug == "" {
		return UserPersona{}, fmt.Errorf("user persona: name %q has no usable filename", p.Name)
	}
	p.Ref = slug
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return UserPersona{}, err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return UserPersona{}, err
	}
	if err := os.WriteFile(filepath.Join(s.dir, slug+".json"), raw, 0o644); err != nil {
		return UserPersona{}, err
	}
	// The save lands on the folded slug; the same persona's pre-fold file
	// would otherwise stay behind as a second List entry. s.legacy has
	// name-checked, so a distinct persona's file is never touched.
	if _, ok := s.legacy(p.Name); ok {
		_ = os.Remove(filepath.Join(s.dir, legacyCardSlug(p.Name)+".json"))
	}
	return p, nil
}

// Delete removes a saved persona; a missing one is a no-op.
func (s *UserPersonaStore) Delete(ref string) error {
	slug := cardSlug(ref)
	if slug == "" {
		return fmt.Errorf("user persona: empty ref")
	}
	if err := os.Remove(filepath.Join(s.dir, slug+".json")); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// Nothing under the folded slug — the persona may still be filed
		// under its pre-fold spelling (name-checked, like Get).
		if _, ok := s.legacy(ref); ok {
			if lerr := os.Remove(filepath.Join(s.dir, legacyCardSlug(ref)+".json")); lerr != nil && !os.IsNotExist(lerr) {
				return lerr
			}
		}
	}
	return nil
}

// Default returns the persona marked default, if any.
func (s *UserPersonaStore) Default() (UserPersona, bool) {
	list, err := s.List()
	if err != nil {
		return UserPersona{}, false
	}
	for _, p := range list {
		if p.Default {
			return p, true
		}
	}
	return UserPersona{}, false
}

// SetDefault marks ref as the default and clears the flag on every other persona,
// so exactly one is default. An empty ref clears the default entirely.
func (s *UserPersonaStore) SetDefault(ref string) error {
	slug := cardSlug(ref)
	list, err := s.List()
	if err != nil {
		return err
	}
	found := false
	for _, p := range list {
		want := slug != "" && p.Ref == slug
		if p.Ref == slug {
			found = true
		}
		if p.Default != want {
			p.Default = want
			if _, err := s.Save(p); err != nil {
				return err
			}
		}
	}
	if slug != "" && !found {
		return fmt.Errorf("user persona: no persona %q", ref)
	}
	return nil
}
