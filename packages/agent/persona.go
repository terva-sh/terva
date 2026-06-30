package agent

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
)

// builtinPersonasFS embeds the shipped persona crew. Mirrors the skills
// package's all:builtin embed, but personas are a file per item (with optional
// team subdirectories) rather than a directory per item.
//
//go:embed all:personas/builtin
var builtinPersonasFS embed.FS

const builtinPersonasRoot = "personas/builtin"

// Persona is a resolved persona: identity + display metadata + a behavioral
// charter. By default the charter is layered *additively* on top of terva's
// invariant harness identity. A persona that sets `immersive: true` instead has
// its charter *replace* the default coding-assistant identity (routed through
// the same path as --system-prompt/SYSTEM.md) — for roleplay and chat-companion
// personas that need to own the identity, not just flavor it. An explicit
// --system-prompt/SYSTEM.md still wins over an immersive persona.
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
	// persona as additive, so a file degrades gracefully.
	Immersive bool
	Charter   string // the markdown body, trimmed
	// Namespace groups the persona: a team subdirectory under personas/, or the
	// extension name for an extension-shipped persona. "" = top-level. The
	// qualified name is "<namespace>:<name>".
	Namespace string
	// Source is "embedded:<rel>" for a built-in, "ext:<name>:<rel>" for an
	// extension bundle, an absolute/relative path for a user file, or "" for the
	// legacy name-only swap (no charter).
	Source string
}

// immersiveCustom returns the charter to use as the entire system-prompt
// identity when an immersive persona should own it, or "" to keep the additive
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

// Phonetic returns the pronunciation hint, or "" when the persona carries none.
func (p Persona) Phonetic() string { return strings.TrimSpace(p.Pronunciation) }

// Builtin reports whether the persona came from the embedded crew (vs a
// hand-authored on-disk file).
func (p Persona) Builtin() bool { return strings.HasPrefix(p.Source, "embedded:") }

type personaFrontmatter struct {
	Name              string   `yaml:"name"`
	Pronunciation     string   `yaml:"pronunciation"`
	Specialty         string   `yaml:"specialty"`
	Summary           string   `yaml:"summary"`
	Emoji             string   `yaml:"emoji"`
	AccentColor       string   `yaml:"accent_color"`
	RecommendedSkills []string `yaml:"recommended_skills"`
	GoodFor           []string `yaml:"good_for"`
	AvoidFor          []string `yaml:"avoid_for"`
	Immersive         bool     `yaml:"immersive"`
}

// parsePersona parses a persona .md (YAML frontmatter + charter body). A
// missing `name` is an error — unlike skills, a persona with no identity is
// not usable.
func parsePersona(raw, source string) (Persona, error) {
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
		RecommendedSkills: fm.RecommendedSkills,
		GoodFor:           fm.GoodFor,
		AvoidFor:          fm.AvoidFor,
		Immersive:         fm.Immersive,
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

// ResolvePersona resolves the active persona for a run:
//
//  1. override (--persona): a built-in/on-disk name, or a path; user-provided
//     ⇒ trusted.
//  2. $TERVA_HOME/persona.md — a hand-authored root persona.
//  3. default_persona in config.json — a name pointer into personas/**.
//  4. embedded Mieli — preserving the legacy TERVA_PERSONA_NAME / persona_name
//     name swap (name-only, no charter) for back-compat.
//
// When both persona.md and default_persona are set, the file wins (with a
// warning). A non-empty override or default_persona that resolves to nothing is
// an error (fail-fast on a typo), like an explicitly-named missing context file.
func ResolvePersona(override string) (Persona, error) {
	if s := strings.TrimSpace(override); s != "" {
		return loadPersonaByNameOrPath(s)
	}

	cfg, _ := LoadConfig()

	rootFile := filepath.Join(TervaHome(), "persona.md")
	switch raw, err := os.ReadFile(rootFile); {
	case err == nil:
		if strings.TrimSpace(cfg.DefaultPersona) != "" {
			fmt.Fprintf(os.Stderr, "terva: both %s and default_persona are set; using %s\n", rootFile, rootFile)
		}
		return parsePersona(string(raw), rootFile)
	case !errors.Is(err, os.ErrNotExist):
		return Persona{}, fmt.Errorf("read %s: %w", rootFile, err)
	}

	if name := strings.TrimSpace(cfg.DefaultPersona); name != "" {
		return loadPersonaByName(name)
	}

	// Embedded default, preserving the legacy name-only override. A custom name
	// (set the old way) is a bare swap with no charter, exactly as before.
	name := PersonaName()
	if name == DefaultPersonaName {
		return loadEmbeddedPersona("mieli")
	}
	return Persona{Name: name}, nil
}

// loadPersonaByNameOrPath treats a value containing a slash or ending in .md as
// a file path, otherwise as a persona name resolved against personas/**.
func loadPersonaByNameOrPath(s string) (Persona, error) {
	if strings.Contains(s, "/") || strings.HasSuffix(s, ".md") {
		raw, err := os.ReadFile(s)
		if err != nil {
			return Persona{}, fmt.Errorf("read persona %s: %w", s, err)
		}
		return parsePersona(string(raw), s)
	}
	return loadPersonaByName(s)
}

// loadPersonaByName finds a persona by a bare name/stem or a qualified
// "namespace:name", case-insensitive, searching user > extension > embedded so
// a higher tier shadows a lower one of the same qualified name.
func loadPersonaByName(query string) (Persona, error) {
	for _, set := range personaTiers() {
		for _, p := range set {
			if p.matches(query) {
				return p, nil
			}
		}
	}
	return Persona{}, fmt.Errorf("persona %q not found (looked in %s, extension bundles, and the built-in crew)", query, personasDir())
}

// loadEmbeddedPersona loads a single built-in persona by file stem from the
// top level of the embedded crew (e.g. "mieli").
func loadEmbeddedPersona(stem string) (Persona, error) {
	raw, err := fs.ReadFile(builtinPersonasFS, path.Join(builtinPersonasRoot, stem+".md"))
	if err != nil {
		return Persona{}, fmt.Errorf("load built-in persona %q: %w", stem, err)
	}
	return parsePersona(string(raw), "embedded:"+stem+".md")
}

// AllPersonas returns the merged roster across tiers (user > extension >
// embedded), deduped by qualified name (a higher tier shadows a lower one of
// the same namespace:stem), sorted by qualified name.
func AllPersonas() []Persona {
	seen := map[string]bool{}
	var out []Persona
	for _, set := range personaTiers() {
		for _, p := range set {
			k := p.key()
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

// personasDir is the on-disk persona library, $TERVA_HOME/personas.
func personasDir() string { return filepath.Join(TervaHome(), "personas") }

func listEmbeddedPersonas() []Persona {
	return readPersonasFromFS(builtinPersonasFS, builtinPersonasRoot,
		func(rel string) string { return "embedded:" + rel },
		nsFromRel)
}

func listOnDiskPersonas() []Persona {
	dir := personasDir()
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
		if persona, err := parsePersona(string(raw), sourceFor(rel)); err == nil {
			persona.Namespace = nsFor(rel)
			out = append(out, persona)
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
