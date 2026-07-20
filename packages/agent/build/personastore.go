package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The persona side of the library. Unlike cards (untrusted data), a persona
// charter shapes identity in the cached prefix — a trusted-tier write — so the
// CONTROL surface gates create/edit; this file is just the read/serialize/write
// mechanics the workspace calls after it has checked trust.
//
// Personas already had a rich read side (AllPersonas / PersonaTiers / Origin);
// what was missing was a way to WRITE one back (persona files were hand-authored
// or copied verbatim by `persona init`). MarshalPersona + WritePersona close
// that, so the control plane can create a new persona and copy-to-edit a
// built-in, both landing in the user library at $TERVA_HOME/personas.

// LookupPersona finds a persona by a bare name/stem or a "namespace:name" ref,
// across every tier (user on-disk > extension > embedded), returning the
// highest-precedence match.
func LookupPersona(query string) (Persona, bool) {
	if strings.TrimSpace(query) == "" {
		return Persona{}, false
	}
	for _, p := range AllPersonas() {
		if p.matches(query) {
			return p, true
		}
	}
	return Persona{}, false
}

// MarshalPersona serializes a persona to its on-disk .md form (YAML frontmatter
// + charter body) — the inverse of ParsePersona, so a round-trip is stable.
func MarshalPersona(p Persona) ([]byte, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("persona: missing required name")
	}
	front, err := yaml.Marshal(personaFrontmatter{
		Name:              strings.TrimSpace(p.Name),
		Pronunciation:     strings.TrimSpace(p.Pronunciation),
		Specialty:         strings.TrimSpace(p.Specialty),
		Summary:           strings.TrimSpace(p.Summary),
		Emoji:             strings.TrimSpace(p.Emoji),
		AccentColor:       strings.TrimSpace(p.AccentColor),
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Immersive:         p.Immersive,
		AgentIntroduction: strings.TrimSpace(p.Introduction),
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

// UserPersonaPath returns the on-disk library path for a persona name and
// whether a file already exists there — how the control surface tells a create
// (must be new) from an edit (may overwrite / shadow a built-in).
func UserPersonaPath(name string) (string, bool) {
	slug := cardSlug(name)
	if slug == "" {
		return "", false
	}
	p := filepath.Join(PersonasDir(), slug+".md")
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return p, false
}

// WritePersona writes a persona into the user library ($TERVA_HOME/personas),
// returning the file path. Copy-to-edit of a built-in falls out for free: the
// written user file shadows the embedded one by Key (user tier wins). Enforcing
// the trust gate is the CALLER's job — this is the mechanism, not the policy.
func WritePersona(p Persona) (string, error) {
	raw, err := MarshalPersona(p)
	if err != nil {
		return "", err
	}
	dest, _ := UserPersonaPath(p.Name)
	if dest == "" {
		return "", fmt.Errorf("persona: name %q has no usable filename", p.Name)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

// DeletePersona removes a persona from the user library ($TERVA_HOME/personas),
// the inverse of WritePersona. It touches ONLY the user tier: the embedded crew
// and extension bundles are not on disk here and are unaffected, so deleting a
// user file that shadowed a built-in un-shadows it and the built-in becomes
// visible again — the way back from a copy-to-edit.
//
// Reports whether a file was removed, so a caller can tell "deleted" from
// "there was nothing of yours by that name" and answer accordingly. As with
// WritePersona, the trust gate is the CALLER's job.
func DeletePersona(name string) (bool, error) {
	dest, exists := UserPersonaPath(name)
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
