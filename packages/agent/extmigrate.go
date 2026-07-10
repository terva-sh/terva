package agent

// Duplicate-extension migration. When a pack canonicalizes an extension's
// install dir name (e.g. "index" for the repo terva-ext-index), a user who
// installed it manually earlier has it under a non-canonical dir
// (terva-ext-index) — terva then sees a near-duplicate. Detection matches
// an installed dir to a pack entry by git origin, manifest name, and dir
// basename; migration RENAMES it to the canonical name. Because terva keys
// per-extension config by MANIFEST name (stable across a dir rename) and
// the enabled flag lives in the manifest file, a rename preserves config,
// enabled-state, and any local edits automatically — no re-keying needed.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
)

// installedExt is one scanned global install: its dir, the basename, the
// parsed manifest, and its git origin URL ("" for a non-git / local copy).
type installedExt struct {
	Dir       string
	DirBase   string
	Manifest  extdriver.Manifest
	OriginURL string
}

// scanInstalledExtensions reads every global ($TERVA_HOME/extensions)
// install's manifest + git origin. Migration is global-scoped: that's
// where `ext pack install` writes and where manual installs collide.
func scanInstalledExtensions() []installedExt {
	root := filepath.Join(config.TervaHome(), "extensions")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []installedExt
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "extension.json"))
		if err != nil {
			continue
		}
		var mf extdriver.Manifest
		if json.Unmarshal(raw, &mf) != nil || mf.Name == "" {
			continue
		}
		out = append(out, installedExt{Dir: dir, DirBase: e.Name(), Manifest: mf, OriginURL: gitOriginURL(dir)})
	}
	return out
}

// gitOriginURL returns a checkout's origin remote URL, or "" when dir is
// not a git repo / has no origin. Read-only.
func gitOriginURL(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// normalizeRepoURL canonicalizes a clone URL for equality: it strips the
// scheme, a user@ prefix, a trailing .git and slash, folds the scp-form
// host:owner/repo to host/owner/repo, and lowercases — so https, git@, and
// ssh spellings of the same repo compare equal.
func normalizeRepoURL(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, ".git")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		u = strings.TrimPrefix(u, p)
	}
	if i := strings.Index(u, "@"); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:] // drop a leading user@
	}
	u = strings.Replace(u, ":", "/", 1) // scp form host:owner/repo
	return strings.TrimSuffix(u, "/")
}

type matchKind int

const (
	matchNone matchKind = iota
	matchMaybe
	matchConfident
)

// duplicateCandidate is one installed look-alike of a pack entry.
type duplicateCandidate struct {
	Entry   PackEntry
	Inst    installedExt
	Kind    matchKind
	Reasons []string
}

// detectDuplicates finds installed extensions that look like a misnamed
// copy of entry, EXCLUDING the entry's own canonical dir. A git-origin
// match — or manifest-name AND dir-basename both lining up with the source
// repo — is confident (safe under --yes); a single weak signal is a maybe
// (always prompts, skipped under --yes).
func detectDuplicates(entry PackEntry, insts []installedExt) []duplicateCandidate {
	canonical := entry.entryName()
	sourceBase := strings.TrimSuffix(filepath.Base(entry.Source), ".git")
	wantOrigin := normalizeRepoURL(entry.Source)

	var out []duplicateCandidate
	for _, d := range insts {
		if d.DirBase == canonical {
			continue // already the canonical install — nothing to migrate
		}
		originMatch := wantOrigin != "" && d.OriginURL != "" && normalizeRepoURL(d.OriginURL) == wantOrigin
		nameMatch := d.Manifest.Name == canonical || (sourceBase != "" && d.Manifest.Name == sourceBase)
		dirMatch := sourceBase != "" && d.DirBase == sourceBase

		var reasons []string
		if originMatch {
			reasons = append(reasons, "origin matches "+entry.Source)
		}
		if nameMatch {
			reasons = append(reasons, "manifest name "+d.Manifest.Name)
		}
		if dirMatch {
			reasons = append(reasons, "dir name "+d.DirBase)
		}

		kind := matchNone
		switch {
		case originMatch, nameMatch && dirMatch:
			kind = matchConfident
		case nameMatch || dirMatch:
			kind = matchMaybe
		}
		if kind == matchNone {
			continue
		}
		out = append(out, duplicateCandidate{Entry: entry, Inst: d, Kind: kind, Reasons: reasons})
	}
	return out
}

// migrateDuplicate renames a look-alike to the pack's canonical dir name,
// or removes it as a redundant duplicate when the canonical install
// already exists. Failure-safe: the rename is atomic, and the old dir is
// only removed once the canonical is confirmed present. A dry run reports
// the planned action with no side effects.
func migrateDuplicate(c duplicateCandidate, dryRun bool) (string, error) {
	canonical := c.Entry.entryName()
	canonicalDir := filepath.Join(config.TervaHome(), "extensions", canonical)
	old := c.Inst.Dir

	canonicalExists := false
	if _, err := os.Stat(canonicalDir); err == nil {
		canonicalExists = true
	}

	if dryRun {
		if canonicalExists {
			return "would remove duplicate (canonical already installed)", nil
		}
		return "would rename to " + canonical, nil
	}

	if canonicalExists {
		// The canonical install already exists, so this is a redundant
		// duplicate. Carry a user's disable intent onto the canonical (the
		// only setting a removal would otherwise drop), then remove it.
		if !c.Inst.Manifest.IsEnabled() {
			_ = config.SetManifestEnabled(canonicalDir, false)
		}
		if err := os.RemoveAll(old); err != nil {
			return "", err
		}
		return "removed duplicate", nil
	}

	if err := os.Rename(old, canonicalDir); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(canonicalDir, "extension.json")); err != nil {
		return "", fmt.Errorf("renamed dir lacks extension.json")
	}
	return "renamed", nil
}

// migrateConfirmed decides whether one candidate proceeds: confident
// matches default-yes (and proceed silently under --yes); maybe matches
// always prompt and are skipped under --yes (never auto-delete on a weak
// signal). A dry run always proceeds (so the plan is reported).
func migrateConfirmed(in *bufio.Reader, c duplicateCandidate, opts packInstallOpts) bool {
	if opts.dryRun {
		return true
	}
	q := fmt.Sprintf("migrate %s -> %s (%s)?", c.Inst.DirBase, c.Entry.entryName(), strings.Join(c.Reasons, ", "))
	switch c.Kind {
	case matchConfident:
		return opts.yes || promptYesNo(in, q, true)
	case matchMaybe:
		if opts.yes {
			fmt.Fprintf(os.Stderr, "  skip   migrate %s (uncertain match; rerun without --yes to confirm)\n", c.Inst.DirBase)
			return false
		}
		return promptYesNo(in, q, false)
	}
	return false
}

// extMigrate is the standalone `terva ext migrate [pack]` — re-runs
// look-alike detection against a pack (default core) and offers each
// migration, outside of a pack install. Honors --dry-run and --yes.
func extMigrate(args []string) error {
	dryRun, yes, packArg := false, false, ""
	for _, a := range args {
		switch {
		case a == "--dry-run" || a == "-n":
			dryRun = true
		case a == "--yes" || a == "-y":
			yes = true
		case a == "help" || a == "-h" || a == "--help":
			fmt.Fprintln(os.Stderr, "usage: terva ext migrate [core | https://… | path.json] [--dry-run] [--yes]")
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		case packArg == "":
			packArg = a
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	if packArg == "" {
		packArg = "core"
	}
	p, source, err := resolvePack(packArg)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "migrate against pack: %s (%s)\n", dashIfEmpty(p.Name), source)

	in := bufio.NewReader(os.Stdin)
	opts := packInstallOpts{yes: yes, dryRun: dryRun, migrate: true}
	insts := scanInstalledExtensions()
	var migrated, failed, found int
	for _, e := range p.Extensions {
		for _, c := range detectDuplicates(e, insts) {
			found++
			if !migrateConfirmed(in, c, opts) {
				continue
			}
			out, err := migrateDuplicate(c, dryRun)
			if err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "  FAIL   migrate %s: %v\n", c.Inst.DirBase, err)
				continue
			}
			migrated++
			fmt.Fprintf(os.Stderr, "  migrate %s -> %s (%s)\n", c.Inst.DirBase, e.entryName(), out)
		}
		if !dryRun {
			insts = scanInstalledExtensions() // pick up the rename for later entries
		}
	}
	if found == 0 {
		fmt.Fprintln(os.Stderr, "no look-alike extensions to migrate")
		return nil
	}
	fmt.Fprintf(os.Stderr, "\n%d migrated, %d failed\n", migrated, failed)
	if failed > 0 {
		return fmt.Errorf("%d migration(s) failed", failed)
	}
	return nil
}
