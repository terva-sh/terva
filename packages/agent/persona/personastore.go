package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"terva.sh/terva/packages/agent/slug"
)

// The persona side of the library. Unlike cards (untrusted data), a persona
// charter shapes identity in the cached prefix — a trusted-tier write — so the
// CONTROL surface gates create/edit; this file is just the read/serialize/write
// mechanics the workspace calls after it has checked trust.
//
// Personas already had a rich read side (All / Tiers / Origin);
// what was missing was a way to WRITE one back (persona files were hand-authored
// or copied verbatim by `persona init`). Marshal + Write close
// that, so the control plane can create a new persona and copy-to-edit a
// built-in, both landing in the user library at $TERVA_HOME/personas.

// Lookup finds a persona by a bare name/stem or a "namespace:name" ref,
// across every tier (user on-disk > extension > embedded), returning the
// highest-precedence match.
func Lookup(query string) (Persona, bool) {
	if strings.TrimSpace(query) == "" {
		return Persona{}, false
	}
	for _, p := range All() {
		if p.matches(query) {
			return p, true
		}
	}
	return Persona{}, false
}

// Marshal serializes a persona to its on-disk .md form (YAML frontmatter
// + charter body) — the inverse of Parse, so a round-trip is stable.
func Marshal(p Persona) ([]byte, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("persona: missing required name")
	}
	front, err := yaml.Marshal(frontmatter{
		Name:              strings.TrimSpace(p.Name),
		Pronunciation:     strings.TrimSpace(p.Pronunciation),
		Specialty:         strings.TrimSpace(p.Specialty),
		Summary:           strings.TrimSpace(p.Summary),
		Emoji:             strings.TrimSpace(p.Emoji),
		AccentColor:       strings.TrimSpace(p.AccentColor),
		Group:             strings.TrimSpace(p.Group),
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Immersive:         p.Immersive,
		AgentIntroduction: strings.TrimSpace(p.Introduction),
		// Extends, not the RESOLVED Inherited charter. Writing the resolved text
		// back would turn a reference into the hand-copied fork that `extends`
		// exists to replace — and it would do it the first time anyone opened
		// the persona in an editor and pressed save.
		Extends: strings.TrimSpace(p.Extends),
	})
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front) // yaml.Marshal already ends in a newline
	b.WriteString("---\n\n")
	if c := strings.TrimSpace(p.Charter); c != "" {
		b.WriteString(c)
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

// UserPath returns the on-disk library path for a persona name and
// whether a file already exists there — how the control surface tells a create
// (must be new) from an edit (may overwrite / shadow a built-in). A persona
// saved before slugs folded diacritics sits under the old spelling ("sepp.md"
// for "Seppä"); that file is reported instead, so an edit lands on the persona
// the user actually has — and Write is what moves it to the folded stem.
func UserPath(name string) (string, bool) {
	stem := slug.Of(name)
	if stem == "" {
		return "", false
	}
	p := filepath.Join(Dir(), stem+".md")
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	if legacy, ok := legacyPersonaFile(name); ok {
		return legacy, true
	}
	return p, false
}

// legacyPersonaFile reports the pre-diacritic-fold library file for name, if
// one is present AND actually names this persona — "Sepp" legitimately owns
// sepp.md, and a lookup for "Seppä" must not claim it.
func legacyPersonaFile(name string) (string, bool) {
	legacy := slug.Legacy(name)
	if legacy == "" || legacy == slug.Of(name) {
		return "", false
	}
	p := filepath.Join(Dir(), legacy+".md")
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	old, err := Parse(string(raw), p)
	if err != nil || !strings.EqualFold(old.Name, name) {
		return "", false
	}
	return p, true
}

// Write writes a persona into the user library ($TERVA_HOME/personas),
// returning the file path. Copy-to-edit of a built-in falls out for free: the
// written user file shadows the embedded one by Key (user tier wins) — which
// is exactly why the file must land on the FOLDED slug ("seppa.md"), the stem
// the built-in uses, never on a pre-fold spelling. Enforcing the trust gate is
// the CALLER's job — this is the mechanism, not the policy.
func Write(p Persona) (string, error) {
	raw, err := Marshal(p)
	if err != nil {
		return "", err
	}
	stem := slug.Of(p.Name)
	if stem == "" {
		return "", fmt.Errorf("persona: name %q has no usable filename", p.Name)
	}
	dest := filepath.Join(Dir(), stem+".md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return "", err
	}
	// A file this persona minted before diacritic folding ("sepp.md") would
	// otherwise linger as a second roster entry that still fails to shadow its
	// built-in. legacyPersonaFile has name-checked, so this cannot remove a
	// distinct persona's file.
	if legacy, ok := legacyPersonaFile(p.Name); ok && legacy != dest {
		_ = os.Remove(legacy)
	}
	return dest, nil
}

// Delete removes a persona from the user library ($TERVA_HOME/personas),
// the inverse of Write. It touches ONLY the user tier: the embedded crew
// and extension bundles are not on disk here and are unaffected, so deleting a
// user file that shadowed a built-in un-shadows it and the built-in becomes
// visible again — the way back from a copy-to-edit.
//
// Reports whether a file was removed, so a caller can tell "deleted" from
// "there was nothing of yours by that name" and answer accordingly. As with
// Write, the trust gate is the CALLER's job.
func Delete(name string) (bool, error) {
	dest, exists := UserPath(name)
	if dest == "" {
		return false, fmt.Errorf("persona: name %q has no usable filename", name)
	}
	if !exists {
		return false, nil
	}
	if err := os.Remove(dest); err != nil {
		return false, err
	}
	return true, nil
}
