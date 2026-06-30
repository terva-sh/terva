package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// nsFromRel derives a persona's namespace from its path relative to the scan
// root: the first directory component (a team subdir), or "" for a top-level
// file.
func nsFromRel(rel string) string {
	rel = filepath.ToSlash(rel)
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

// stem is the file stem of the persona's Source ("review-crew/vartija.md" → "vartija").
func (p Persona) stem() string { return personaStem(p.Source) }

// Qualified is the display name: "<namespace>:<name>", or the bare name when
// top-level.
func (p Persona) Qualified() string {
	if p.Namespace == "" {
		return p.Name
	}
	return p.Namespace + ":" + p.Name
}

// Ref is the canonical, resolvable reference to pass to --persona / swarm_spawn:
// "<namespace>:<stem>" (or the bare stem when top-level). Space-free and stable.
func (p Persona) Ref() string {
	st := p.stem()
	if st == "" {
		st = strings.ToLower(p.Name)
	}
	if p.Namespace == "" {
		return st
	}
	return p.Namespace + ":" + st
}

// key is the case-insensitive identity used for dedup/override:
// "<namespace>:<stem>". A user persona overrides an extension/built-in one by
// matching this (same namespace, same file stem).
func (p Persona) key() string {
	return strings.ToLower(p.Namespace) + ":" + strings.ToLower(p.stem())
}

// matches reports whether p satisfies a persona query — a bare "name"/"stem" or
// a qualified "namespace:name" — case-insensitive, matching the name part
// against the frontmatter name or the file stem.
func (p Persona) matches(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	ns, name := "", q
	if i := strings.IndexByte(q, ':'); i >= 0 {
		ns, name = q[:i], q[i+1:]
	}
	if ns != "" && ns != strings.ToLower(p.Namespace) {
		return false
	}
	return name == strings.ToLower(p.Name) || name == strings.ToLower(p.stem())
}

// FromExtension reports whether the persona came from an extension bundle.
func (p Persona) FromExtension() bool { return strings.HasPrefix(p.Source, "ext:") }

// Origin is a short provenance label for display: "built-in", "ext:<name>", the
// on-disk path, or "name-only" (the legacy swap).
func (p Persona) Origin() string {
	switch {
	case strings.HasPrefix(p.Source, "embedded:"):
		return "built-in"
	case strings.HasPrefix(p.Source, "ext:"):
		rest := strings.TrimPrefix(p.Source, "ext:")
		if i := strings.IndexByte(rest, ':'); i >= 0 {
			return "ext:" + rest[:i]
		}
		return "ext:" + rest
	case p.Source == "":
		return "name-only"
	default:
		return p.Source
	}
}

// personaTiers returns the persona library in precedence order: user on-disk >
// extension bundles > embedded crew. Resolution returns the first match; the
// merged roster dedups by qualified name keeping the highest tier.
func personaTiers() [][]Persona {
	return [][]Persona{listOnDiskPersonas(), listExtensionPersonas(), listEmbeddedPersonas()}
}

// extPersonaRoot is one enabled extension's personas/ directory + its namespace.
type extPersonaRoot struct {
	namespace string
	dir       string
}

// extensionPersonaRoots returns the personas/ dir of every globally-installed
// extension that is enabled (manifest `enabled` != false) and not in the user's
// disable_extensions list — mirroring skills.extensionSkillDirs. Phase A scans
// $TERVA_HOME/extensions only; project extensions (trust-gated) are Phase B.
func extensionPersonaRoots(tervaHome string, userDisabled map[string]bool) []extPersonaRoot {
	if tervaHome == "" {
		return nil
	}
	root := filepath.Join(tervaHome, "extensions")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []extPersonaRoot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		extDir := filepath.Join(root, e.Name())
		mb, err := os.ReadFile(filepath.Join(extDir, "extension.json"))
		if err != nil {
			continue
		}
		var m struct {
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
		}
		if json.Unmarshal(mb, &m) != nil {
			continue
		}
		if m.Enabled != nil && !*m.Enabled {
			continue // manifest-disabled: follow skills
		}
		ns := strings.TrimSpace(m.Name)
		if ns == "" {
			ns = e.Name()
		}
		if userDisabled[strings.ToLower(ns)] || userDisabled[strings.ToLower(e.Name())] {
			continue // config disable_extensions (user layer)
		}
		pdir := filepath.Join(extDir, "personas")
		if st, err := os.Stat(pdir); err == nil && st.IsDir() {
			out = append(out, extPersonaRoot{namespace: ns, dir: pdir})
		}
	}
	return out
}

// listExtensionPersonas discovers personas shipped by enabled global extensions,
// namespaced by the extension name and sourced "ext:<name>:<rel>".
func listExtensionPersonas() []Persona {
	userDisabled := map[string]bool{}
	if cfg, err := LoadConfig(); err == nil {
		for _, n := range cfg.DisableExtensions {
			userDisabled[strings.ToLower(strings.TrimSpace(n))] = true
		}
	}
	var out []Persona
	for _, er := range extensionPersonaRoots(TervaHome(), userDisabled) {
		ns := er.namespace
		out = append(out, readPersonasFromFS(os.DirFS(er.dir), ".",
			func(rel string) string { return "ext:" + ns + ":" + rel },
			func(string) string { return ns })...)
	}
	return out
}
