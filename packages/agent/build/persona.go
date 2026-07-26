package build

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"terva.sh/terva/packages/agent/config"
)

// BuiltinPersonasFS embeds the shipped Persona crew. Mirrors the skills
// package's all:builtin embed, but personas are a file per item (with optional
// team subdirectories) rather than a directory per item.
//
//go:embed all:personas/builtin
var BuiltinPersonasFS embed.FS

const BuiltinPersonasRoot = "personas/builtin"

// Persona is a resolved Persona: identity + display metadata + a behavioral
// charter. By default the charter is layered *additively* on top of terva's
// invariant harness identity. A Persona that sets `immersive: true` instead has
// its charter *replace* the default coding-assistant identity (routed through
// the same path as --system-prompt/SYSTEM.md) — for roleplay and chat-companion
// personas that need to own the identity, not just flavor it. An explicit
// --system-prompt/SYSTEM.md still wins over an immersive Persona.
type Persona struct {
	Name              string
	Pronunciation     string
	Specialty         string
	Summary           string
	Emoji             string
	AccentColor       string
	RecommendedSkills []string
	GoodFor           []string
	AvoidFor          []string
	// Immersive, when true, makes Charter the whole system-prompt identity
	// (replacing terva's "expert coding assistant" intro + conventions) rather
	// than an additive layer. Stock hosts that don't know the field treat the
	// Persona as additive, so a file degrades gracefully.
	Immersive bool
	// Introduction, when set, replaces terva's generated identity intro (the
	// branded "expert coding assistant operating inside terva…" opening) with
	// the Persona's own verbatim text, while KEEPING terva's conventions
	// bracketing — the middle ground between an additive Persona (terva's intro)
	// and an immersive one (Charter owns the whole prompt). From the
	// agent_introduction frontmatter field. Ignored when Immersive is set.
	Introduction string
	Charter      string // the markdown body, trimmed
	// Namespace groups the Persona: a team subdirectory under personas/, or the
	// extension name for an extension-shipped Persona. "" = top-level. The
	// qualified name is "<namespace>:<name>".
	Namespace string
	// Group is the shelf a roster files this Persona under ("Stage", "Review").
	// Free text, from the `group` frontmatter field, and purely organisational:
	// unlike Namespace it is NOT part of the ref, so renaming a group never
	// invalidates a --persona flag or a session's recorded identity. That is
	// exactly why it, and not Namespace, is the field a user may assign.
	Group string
	// Source is "embedded:<rel>" for a built-in, "ext:<name>:<rel>" for an
	// extension bundle, an absolute/relative path for a user file, or "" for the
	// legacy name-only swap (no charter).
	Source string
}

// immersiveCustom returns the charter to use as the entire system-prompt
// identity when an immersive Persona should own it, or "" to keep the additive
// path. An explicit custom prompt (--system-prompt / SYSTEM.md) always wins, so
// a non-empty custom short-circuits to "".
func immersiveCustom(custom string, p Persona) string {
	if custom == "" && p.Immersive && strings.TrimSpace(p.Charter) != "" {
		return p.Charter
	}
	return ""
}

// Label is the self-introduction label for greetings/banners: the name with
// its pronunciation hint when known ("Mieli (MYEH-lee)"), else the bare name.
func (p Persona) Label() string {
	if strings.TrimSpace(p.Pronunciation) != "" {
		return p.Name + " (" + p.Pronunciation + ")"
	}
	return p.Name
}

// Phonetic returns the pronunciation hint, or "" when the Persona carries none.
func (p Persona) Phonetic() string { return strings.TrimSpace(p.Pronunciation) }

// Builtin reports whether the Persona came from the embedded crew (vs a
// hand-authored on-disk file).
func (p Persona) Builtin() bool { return strings.HasPrefix(p.Source, "embedded:") }

// GroupLabel is the shelf a roster files this Persona under: its declared
// `group`, falling back to its Namespace, else "" for ungrouped.
//
// The fallback is what makes a bundle self-organising: an extension shipping
// five personas, or a team subdirectory under personas/, gets a shelf of its own
// without anyone writing `group:` five times. Derived rather than stored, so
// MarshalPersona never writes back a group the author did not choose.
func (p Persona) GroupLabel() string {
	if g := strings.TrimSpace(p.Group); g != "" {
		return g
	}
	return strings.TrimSpace(p.Namespace)
}

// personaFrontmatter is the YAML head of a persona .md. omitempty keeps a
// serialized persona (MarshalPersona) from emitting a wall of empty keys;
// omitempty affects marshaling only, so parsing is unchanged (an absent optional
// field already meant its zero value).
type personaFrontmatter struct {
	Name              string   `yaml:"name"`
	Pronunciation     string   `yaml:"pronunciation,omitempty"`
	Specialty         string   `yaml:"specialty,omitempty"`
	Summary           string   `yaml:"summary,omitempty"`
	Emoji             string   `yaml:"emoji,omitempty"`
	AccentColor       string   `yaml:"accent_color,omitempty"`
	Group             string   `yaml:"group,omitempty"`
	RecommendedSkills []string `yaml:"recommended_skills,omitempty"`
	GoodFor           []string `yaml:"good_for,omitempty"`
	AvoidFor          []string `yaml:"avoid_for,omitempty"`
	Immersive         bool     `yaml:"immersive,omitempty"`
	AgentIntroduction string   `yaml:"agent_introduction,omitempty"`
}

// ParsePersona parses a Persona .md (YAML frontmatter + charter body). A
// missing `name` is an error — unlike skills, a Persona with no identity is
// not usable.
func ParsePersona(raw, source string) (Persona, error) {
	front, body := splitPersonaFrontmatter(raw)
	var fm personaFrontmatter
	if strings.TrimSpace(front) != "" {
		if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
			return Persona{}, fmt.Errorf("persona %s: parse frontmatter: %w", source, err)
		}
	}
	p := Persona{
		Name:              strings.TrimSpace(fm.Name),
		Pronunciation:     strings.TrimSpace(fm.Pronunciation),
		Specialty:         strings.TrimSpace(fm.Specialty),
		Summary:           strings.TrimSpace(fm.Summary),
		Emoji:             strings.TrimSpace(fm.Emoji),
		AccentColor:       strings.TrimSpace(fm.AccentColor),
		Group:             strings.TrimSpace(fm.Group),
		RecommendedSkills: fm.RecommendedSkills,
		GoodFor:           fm.GoodFor,
		AvoidFor:          fm.AvoidFor,
		Immersive:         fm.Immersive,
		Introduction:      strings.TrimSpace(fm.AgentIntroduction),
		Charter:           strings.TrimSpace(body),
		Source:            source,
	}
	if p.Name == "" {
		return Persona{}, fmt.Errorf("persona %s: missing required 'name'", source)
	}
	return p, nil
}

// splitPersonaFrontmatter splits "---\n<yaml>\n---\n<body>", returning
// (frontmatter, body), or ("", raw) when no frontmatter is present. Mirrors
// skills.splitFrontmatter, which is private to that package.
func splitPersonaFrontmatter(raw string) (string, string) {
	rest := strings.TrimLeft(raw, " \t\r\n")
	if !strings.HasPrefix(rest, "---") {
		return "", raw
	}
	rest = strings.TrimPrefix(rest, "---")
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", raw // malformed; treat as no frontmatter
	}
	front := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, " \t\r\n")
	return front, body
}

// ResolvePersona resolves the active Persona for a run:
//
//  1. override (--Persona): a built-in/on-disk name, or a path; user-provided
//     ⇒ trusted.
//  2. $TERVA_HOME/Persona.md — a hand-authored root Persona.
//  3. default_persona in config.json — a name pointer into personas/**.
//  4. embedded Mieli — preserving the legacy TERVA_PERSONA_NAME / persona_name
//     name swap (name-only, no charter) for back-compat.
//
// When both Persona.md and default_persona are set, the file wins (with a
// warning). A non-empty override or default_persona that resolves to nothing is
// an error (fail-fast on a typo), like an explicitly-named missing context file.
func ResolvePersona(override string) (Persona, error) {
	if s := strings.TrimSpace(override); s != "" {
		return loadPersonaByNameOrPath(s)
	}

	cfg, _ := config.LoadConfig()

	rootFile := filepath.Join(config.TervaHome(), "persona.md")
	switch raw, err := os.ReadFile(rootFile); {
	case err == nil:
		if strings.TrimSpace(cfg.DefaultPersona) != "" {
			fmt.Fprintf(os.Stderr, "terva: both %s and default_persona are set; using %s\n", rootFile, rootFile)
		}
		return ParsePersona(string(raw), rootFile)
	case !errors.Is(err, os.ErrNotExist):
		return Persona{}, fmt.Errorf("read %s: %w", rootFile, err)
	}

	if name := strings.TrimSpace(cfg.DefaultPersona); name != "" {
		return loadPersonaByName(name)
	}

	// Embedded default, preserving the legacy name-only override. A custom name
	// (set the old way) is a bare swap with no charter, exactly as before.
	name := config.PersonaName()
	if name == config.DefaultPersonaName {
		return loadEmbeddedPersona("mieli")
	}
	return Persona{Name: name}, nil
}

// loadPersonaByNameOrPath treats a value containing a slash or ending in .md as
// a file path, otherwise as a Persona name resolved against personas/**.
func loadPersonaByNameOrPath(s string) (Persona, error) {
	if strings.Contains(s, "/") || strings.HasSuffix(s, ".md") {
		raw, err := os.ReadFile(s)
		if err != nil {
			return Persona{}, fmt.Errorf("read persona %s: %w", s, err)
		}
		return ParsePersona(string(raw), s)
	}
	return loadPersonaByName(s)
}

// loadPersonaByName finds a Persona by a bare name/stem or a qualified
// "namespace:name", case-insensitive, searching user > extension > embedded so
// a higher tier shadows a lower one of the same qualified name.
func loadPersonaByName(query string) (Persona, error) {
	for _, set := range PersonaTiers() {
		for _, p := range set {
			if p.matches(query) {
				return p, nil
			}
		}
	}
	return Persona{}, fmt.Errorf("persona %q not found (looked in %s, extension bundles, and the built-in crew)", query, PersonasDir())
}

// loadEmbeddedPersona loads a single built-in Persona by file stem from the
// top level of the embedded crew (e.g. "mieli").
func loadEmbeddedPersona(stem string) (Persona, error) {
	raw, err := fs.ReadFile(BuiltinPersonasFS, path.Join(BuiltinPersonasRoot, stem+".md"))
	if err != nil {
		return Persona{}, fmt.Errorf("load built-in persona %q: %w", stem, err)
	}
	return ParsePersona(string(raw), "embedded:"+stem+".md")
}

// AllPersonas returns the merged roster across tiers (user > extension >
// embedded), deduped by qualified name (a higher tier shadows a lower one of
// the same namespace:stem), sorted by qualified name.
func AllPersonas() []Persona {
	seen := map[string]bool{}
	var out []Persona
	for _, set := range PersonaTiers() {
		for _, p := range set {
			k := p.Key()
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Qualified() < out[j].Qualified() })
	return out
}

// PersonasDir is the on-disk Persona library, $TERVA_HOME/personas.
func PersonasDir() string { return filepath.Join(config.TervaHome(), "personas") }

func listEmbeddedPersonas() []Persona {
	return readPersonasFromFS(BuiltinPersonasFS, BuiltinPersonasRoot,
		func(rel string) string { return "embedded:" + rel },
		nsFromRel)
}

func listOnDiskPersonas() []Persona {
	dir := PersonasDir()
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	return readPersonasFromFS(os.DirFS(dir), ".",
		func(rel string) string { return filepath.Join(dir, filepath.FromSlash(rel)) },
		nsFromRel)
}

// readPersonasFromFS walks root in fsys, parsing every .md (except README.md)
// into a Persona. sourceFor and nsFor map the path RELATIVE to root onto
// Persona.Source and Persona.Namespace. Unparseable files are skipped so one
// broken file can't hide the rest.
func readPersonasFromFS(fsys fs.FS, root string, sourceFor, nsFor func(rel string) string) []Persona {
	var out []Persona
	_ = fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") || strings.EqualFold(path.Base(p), "README.md") {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		rel := p
		if root != "." {
			rel = strings.TrimPrefix(p, root+"/")
		}
		if Persona, err := ParsePersona(string(raw), sourceFor(rel)); err == nil {
			Persona.Namespace = nsFor(rel)
			out = append(out, Persona)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Qualified() < out[j].Qualified() })
	return out
}

// personaStem returns the file stem of a Source, handling all three source
// shapes: "embedded:review-crew/vartija.md", "ext:websearch:deep-researcher.md"
// (colons, not path separators, prefix the bundle), and "/path/to/vartija.md".
func personaStem(source string) string {
	s := source
	switch {
	case strings.HasPrefix(s, "embedded:"):
		s = strings.TrimPrefix(s, "embedded:")
	case strings.HasPrefix(s, "ext:"):
		// "ext:<name>:<rel>" → <rel>
		rest := strings.TrimPrefix(s, "ext:")
		if i := strings.IndexByte(rest, ':'); i >= 0 {
			s = rest[i+1:]
		} else {
			s = rest
		}
	}
	return strings.TrimSuffix(path.Base(filepath.ToSlash(s)), ".md")
}
