// Package extroots answers one question for every surface that loads
// extension-shipped content: which installed extensions are enabled, and where
// do they live?
//
// It exists because that question had three different answers. The extension
// manager honoured the resolved (user ∪ project) disable_extensions; the
// persona library honoured only the USER layer of it; and skill discovery
// honoured no disable list at all — so an extension the user had switched off
// lost its tools and its personas while its skills kept being injected into the
// system prompt. A skill is instructions the model reads, which makes "off"
// meaning three things a security answer rather than a tidiness one.
//
// The scan and the enabled/disabled rule live here. What each caller PASSES is
// still the caller's own decision, and deliberately not the same: skills load
// project extension roots when the workspace is trusted, personas do not. That
// difference is now two arguments at a call site instead of two scanners that
// happen to disagree.
//
// Kept deliberately lean — stdlib plus envcompat — because packages/agent/skills
// imports nothing else, and the point of this package is to be importable by
// both of its callers rather than to grow into the extension manager.
package extroots

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/envcompat"
)

// Root is one installed, enabled extension.
type Root struct {
	// Dir is the extension's own directory, the one holding extension.json.
	Dir string
	// DirName is that directory's basename.
	DirName string
	// ManifestName is the `name` field of extension.json, or "" when the
	// manifest does not set one.
	ManifestName string
	// Project reports whether this root came from a project extensions dir
	// rather than the global one. Only ever true when the caller asked for
	// project roots AND the workspace is trusted.
	Project bool
}

// Name is the extension's preferred name: its manifest name, else its
// directory name.
//
// 🪤 Callers do NOT agree on which of the two to use, and this does not make
// them. Skill namespaces are built from DirName and persona namespaces from
// this; an extension whose manifest name differs from its directory therefore
// answers to `ext:<dir>` for skills and `<manifest>:` for personas. That
// mismatch predates this package and is a naming question, not a gating one —
// recorded here rather than silently unified, because unifying it would rename
// live skill or persona refs.
func (r Root) Name() string {
	if n := strings.TrimSpace(r.ManifestName); n != "" {
		return n
	}
	return r.DirName
}

// Gate is what a workspace is allowed to contribute: whether project-local
// content loads at all, and which installed extensions the operator has
// switched off.
//
// One value rather than two loose arguments, because every surface that loads
// extension-shipped content needs both and the second was the one easy to
// leave out — skills and lore each honoured only the manifest flag, so an
// extension the user had disabled kept injecting instructions after its tools
// and personas were gone. Aliased as skills.Gate and lore.Gate so a caller
// names it in the package it is calling.
type Gate struct {
	// TrustProject gates every PROJECT-local source, including project
	// extension bundles. False is the safe default.
	TrustProject bool
	// Disabled is the RESOLVED (user ∪ project) disable_extensions set.
	Disabled []string
}

// Enabled returns every installed extension that is enabled, global
// ($TERVA_HOME/extensions) before project, in directory order.
//
// An extension is skipped when its manifest sets `"enabled": false`, or when
// EITHER its manifest name or its directory name appears in g.Disabled —
// case-insensitively. Both spellings, because disable_extensions is written by
// hand and a user who typed the name they see in a directory listing meant it.
//
// g.TrustProject gates the project roots: an untrusted workspace contributes
// none, because a project extension would not load there either. Pass cwd ""
// or TrustProject false for global-only.
//
// A directory with no readable or parseable extension.json is skipped rather
// than reported: this is a discovery walk over a directory the user owns, and
// one malformed manifest must not hide the rest.
func Enabled(tervaHome, cwd string, g Gate) []Root {
	off := make(map[string]bool, len(g.Disabled))
	for _, n := range g.Disabled {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			off[n] = true
		}
	}

	type scanRoot struct {
		dir     string
		project bool
	}
	var roots []scanRoot
	if tervaHome != "" {
		roots = append(roots, scanRoot{dir: filepath.Join(tervaHome, "extensions")})
	}
	if cwd != "" && g.TrustProject {
		for _, dirName := range envcompat.ProjectDirNames() {
			roots = append(roots, scanRoot{dir: filepath.Join(cwd, dirName, "extensions"), project: true})
		}
	}

	var out []Root
	for _, root := range roots {
		entries, err := os.ReadDir(root.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			extDir := filepath.Join(root.dir, e.Name())
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
				continue // manifest-disabled by whoever installed it
			}
			r := Root{
				Dir:          extDir,
				DirName:      e.Name(),
				ManifestName: strings.TrimSpace(m.Name),
				Project:      root.project,
			}
			if off[strings.ToLower(r.Name())] || off[strings.ToLower(r.DirName)] {
				continue // switched off by the operator
			}
			out = append(out, r)
		}
	}
	return out
}

// SubDir returns the extension's <dir>/<name> directory when it exists, and
// whether it does — the shape both callers want ("does this extension ship a
// skills/ or personas/ directory?").
func (r Root) SubDir(name string) (string, bool) {
	d := filepath.Join(r.Dir, name)
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d, true
	}
	return "", false
}
