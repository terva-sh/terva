package persona

import (
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extroots"
)

// nsFromRel derives a Persona's namespace from its path relative to the scan
// root: the first directory component (a team subdir), or "" for a top-level
// file.
func nsFromRel(rel string) string {
	rel = filepath.ToSlash(rel)
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

// stem is the file stem of the Persona's Source ("review-crew/vartija.md" → "vartija").
func (p Persona) stem() string { return stemOf(p.Source) }

// Qualified is the display name: "<namespace>:<name>", or the bare name when
// top-level.
func (p Persona) Qualified() string {
	if p.Namespace == "" {
		return p.Name
	}
	return p.Namespace + ":" + p.Name
}

// Ref is the canonical, resolvable reference to pass to --Persona / swarm_spawn:
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

// Key is the case-insensitive identity used for dedup/override:
// "<namespace>:<stem>". A user Persona overrides an extension/built-in one by
// matching this (same namespace, same file stem).
func (p Persona) Key() string {
	return strings.ToLower(p.Namespace) + ":" + strings.ToLower(p.stem())
}

// splitRef splits a persona query into its namespace and name halves:
// "review-crew:vartija" → ("review-crew", "vartija"), "vartija" → ("", "vartija").
// A bare query carries no namespace, which is NOT the same as naming the
// top-level one — see matches and UserPath for what each does with that.
//
// Shared rather than parsed twice: matches decides which roster entry a query
// selects and UserPath decides which FILE it selects, and the two disagreeing
// about where the colon is would mean a query that resolves to one persona and
// writes over another.
func splitRef(query string) (ns, name string) {
	q := strings.TrimSpace(query)
	if i := strings.IndexByte(q, ':'); i >= 0 {
		return q[:i], q[i+1:]
	}
	return "", q
}

// matches reports whether p satisfies a Persona query — a bare "name"/"stem" or
// a qualified "namespace:name" — case-insensitive, matching the name part
// against the frontmatter name or the file stem.
func (p Persona) matches(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	ns, name := splitRef(q)
	if ns != "" && ns != strings.ToLower(p.Namespace) {
		return false
	}
	return name == strings.ToLower(p.Name) || name == strings.ToLower(p.stem())
}

// FromExtension reports whether the Persona came from an extension bundle.
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

// Tiers returns the Persona library in precedence order: user on-disk >
// extension bundles > embedded crew. Resolution returns the first match; the
// merged roster dedups by qualified name keeping the highest tier.
func Tiers() [][]Persona {
	return [][]Persona{listOnDisk(), listExtensionPersonas(), listEmbedded()}
}

// listExtensionPersonas discovers personas shipped by enabled global
// extensions, namespaced by the extension name and sourced "ext:<name>:<rel>".
//
// 🪤 Two limits here are DECISIONS, not omissions, and both are visible as
// arguments below rather than as a scanner that quietly lacks the capability:
//
//   - Global roots only (no cwd, no project trust). A project extension's personas
//     are not discovered even in a trusted workspace, while its skills are.
//   - The USER layer of disable_extensions only. A project config that disables
//     an extension hides its tools and its skills; its personas stay listed.
//
// Both follow from what this package is: a library with no cwd and no trust
// verdict — its own doc calls reading neither the property that let it leave
// package build — so the resolved (user ∪ project) config is not available
// here without threading a resolver through every caller of All/Lookup/Resolve.
// The exposure differs in kind, which is why the trade was taken: a skill is
// injected into the prompt whether or not you asked for it, and a persona is
// something you pick.
func listExtensionPersonas() []Persona {
	userDisabled := []string{}
	if cfg, err := config.LoadConfig(); err == nil {
		userDisabled = cfg.DisableExtensions
	}
	var out []Persona
	for _, r := range extroots.Enabled(config.TervaHome(), "", extroots.Gate{Disabled: userDisabled}) {
		pdir, ok := r.SubDir("personas")
		if !ok {
			continue
		}
		// The manifest name, where skills use the directory name — see the note
		// on extroots.Root.Name.
		ns := r.Name()
		out = append(out, readFromFS(os.DirFS(pdir), ".",
			func(rel string) string { return "ext:" + ns + ":" + rel },
			func(string) string { return ns })...)
	}
	return out
}
