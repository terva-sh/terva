package lore

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/envcompat"
)

// Discover loads lore entries from the active tiers plus the effective
// collection Config. Every entry from every tier contributes — unlike
// personas, entries are not name-shadowed, since any entry may fire. Config
// is taken from the highest-priority tier that ships a lore.json, else
// zero-value defaults (which Select fills in).
//
// Tiers, highest priority first:
//
//	<cwd>/.terva/lore (+ rename-aware spellings)  — project, gated on trustProject
//	$TERVA_HOME/lore                              — personal
//	<ext>/lore for each enabled extension         — bundle (project exts gated)
//
// A caller that has already resolved Workspace Trust passes trustProject;
// an untrusted workspace contributes no project-local or project-extension
// lore, exactly like skills/extensions.
func Discover(tervaHome, cwd string, trustProject bool) ([]Entry, Config, []error) {
	var (
		entries []Entry
		errs    []error
		cfg     Config
		cfgSet  bool
	)
	for _, dir := range loreDirs(tervaHome, cwd, trustProject) {
		es, c, hasCfg, derrs := readLoreDir(dir)
		entries = append(entries, es...)
		errs = append(errs, derrs...)
		if hasCfg && !cfgSet {
			cfg, cfgSet = c, true // first (highest-priority) lore.json wins
		}
	}
	return entries, cfg, errs
}

// loreDirs lists the lore directories in priority order.
func loreDirs(tervaHome, cwd string, trustProject bool) []string {
	var dirs []string
	if cwd != "" && trustProject {
		for _, name := range envcompat.ProjectDirNames() {
			dirs = append(dirs, filepath.Join(cwd, name, "lore"))
		}
	}
	if tervaHome != "" {
		dirs = append(dirs, filepath.Join(tervaHome, "lore"))
	}
	dirs = append(dirs, extensionLoreDirs(tervaHome, cwd, trustProject)...)
	return dirs
}

// readLoreDir reads every *.md entry (recursively) and an optional
// lore.json from one directory. A missing directory is not an error.
func readLoreDir(dir string) (entries []Entry, cfg Config, hasCfg bool, errs []error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, Config{}, false, nil
	}
	if data, err := os.ReadFile(filepath.Join(dir, "lore.json")); err == nil {
		if c, cerr := parseConfig(data); cerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Join(dir, "lore.json"), cerr))
		} else {
			cfg, hasCfg = c, true
		}
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entries, keep walking
		}
		name := d.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") || strings.EqualFold(name, "README.md") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, rerr))
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = name
		}
		e, ok, perr := ParseEntry(string(raw), rel)
		if perr != nil {
			errs = append(errs, perr)
			return nil
		}
		if ok {
			entries = append(entries, e)
		}
		return nil
	})
	return entries, cfg, hasCfg, errs
}

// loreConfigJSON is the on-disk lore.json shape (all fields optional).
type loreConfigJSON struct {
	ScanDepth         *int  `json:"scan_depth"`
	TokenBudget       *int  `json:"token_budget"`
	RecursiveScanning *bool `json:"recursive_scanning"`
	CaseSensitive     *bool `json:"case_sensitive"`
	MatchWholeWords   *bool `json:"match_whole_words"`
}

func parseConfig(data []byte) (Config, error) {
	var j loreConfigJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return Config{}, err
	}
	var c Config
	if j.ScanDepth != nil {
		c.ScanDepth = *j.ScanDepth
	}
	if j.TokenBudget != nil {
		c.TokenBudget = *j.TokenBudget
	}
	if j.RecursiveScanning != nil {
		c.RecursiveScanning = *j.RecursiveScanning
	}
	if j.CaseSensitive != nil {
		c.CaseSensitive = *j.CaseSensitive
	}
	if j.MatchWholeWords != nil {
		c.MatchWholeWords = *j.MatchWholeWords
	}
	return c, nil
}

// extensionLoreDirs lists <extension>/lore for every enabled installed
// extension: global ($TERVA_HOME/extensions) always, project
// (.terva/extensions, rename-aware) only when the workspace is trusted.
// Mirrors skills.extensionSkillDirs.
func extensionLoreDirs(tervaHome, cwd string, trustProject bool) []string {
	var roots []string
	if tervaHome != "" {
		roots = append(roots, filepath.Join(tervaHome, "extensions"))
	}
	if cwd != "" && trustProject {
		for _, name := range envcompat.ProjectDirNames() {
			roots = append(roots, filepath.Join(cwd, name, "extensions"))
		}
	}
	var out []string
	for _, root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			extDir := filepath.Join(root, e.Name())
			mb, err := os.ReadFile(filepath.Join(extDir, "extension.json"))
			if err != nil {
				continue
			}
			var m struct {
				Enabled *bool `json:"enabled"`
			}
			if json.Unmarshal(mb, &m) != nil {
				continue
			}
			if m.Enabled != nil && !*m.Enabled {
				continue
			}
			loreDir := filepath.Join(extDir, "lore")
			if st, err := os.Stat(loreDir); err == nil && st.IsDir() {
				out = append(out, loreDir)
			}
		}
	}
	return out
}
