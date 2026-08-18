package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/privfs"
)

// This file is the engine behind `terva migrate` and the TUI's
// /migrate: copy the legacy zot user data dir to the terva location,
// optionally rename a project's .zot/ to .terva/, and set the
// envcompat marker that turns off zot config-file autoloading.
// The CLI front-end in migratecmd.go is the ONLY front end. This used to name
// dialogs.MigrateDialog "the TUI front-end, wired through
// dialogs.MigrationHooks in cli.go"; no such wiring existed — the interactive
// migrator belonged to the direct driver and went with it, leaving the dialog
// unreachable and this sentence describing it anyway. The TUI's /migrate points
// at `terva migrate` and does nothing else.

// MigrationPlan is the immutable description of what a migration run
// would do, computed up front so both front-ends can show it before
// touching anything.
type MigrationPlan struct {
	OldDir     string // legacy data dir to copy from; "" = no user-dir step
	NewDir     string // migration target — also where the envcompat marker lands
	OldFromEnv bool   // OldDir came from $ZOT_HOME
	EnvNote    string // non-empty: env vars need updating after the move

	ProjectRoot     string // nearest ancestor of cwd holding .zot and/or .terva
	ProjectOldDir   string // <ProjectRoot>/.zot when present
	ProjectNewDir   string // <ProjectRoot>/.terva
	ProjectConflict bool   // both exist → manual merge, no rename offered

	AlreadyMigrated bool // the envcompat marker was already set
}

// UserDirApplicable reports whether there is a user data dir to copy.
func (p MigrationPlan) UserDirApplicable() bool { return p.OldDir != "" }

// ProjectApplicable reports whether a project .zot/ rename can be offered.
func (p MigrationPlan) ProjectApplicable() bool {
	return p.ProjectOldDir != "" && !p.ProjectConflict
}

// NothingToDo reports whether the run would be a no-op: already
// migrated and no legacy dirs left anywhere.
func (p MigrationPlan) NothingToDo() bool {
	return !p.UserDirApplicable() && p.ProjectOldDir == "" && p.AlreadyMigrated
}

// PlanMigration inspects the environment and the tree above cwd and
// describes the migration without performing any of it.
func PlanMigration(cwd string) MigrationPlan {
	var p MigrationPlan
	p.AlreadyMigrated = envcompat.ZotFallbackDisabled()

	// Target: $TERVA_HOME (new spelling only — same rule the envcompat
	// marker uses, so finalize and readers agree), else the OS default.
	p.NewDir = os.Getenv("TERVA_HOME")
	if p.NewDir == "" {
		p.NewDir = envcompat.DefaultHome()
	}

	// Source: an explicit $ZOT_HOME wins; otherwise the OS-default zot
	// dir, but only when it actually exists.
	if v := os.Getenv("ZOT_HOME"); v != "" {
		p.OldDir = v
		p.OldFromEnv = true
		p.EnvNote = fmt.Sprintf("you have ZOT_HOME=%s set; after migrating, set TERVA_HOME=%s (or unset ZOT_HOME) — ZOT_* spellings keep working but are deprecated", v, p.NewDir)
	} else if old := envcompat.LegacyDefaultHome(); old != "" {
		if st, err := os.Stat(old); err == nil && st.IsDir() {
			p.OldDir = old
		}
	}

	// Identity guard: TERVA_HOME pointing at the zot dir, or one
	// symlinked to the other — nothing to copy, the data already lives
	// at the target.
	if p.OldDir != "" && samePath(p.OldDir, p.NewDir) {
		p.OldDir = ""
		p.OldFromEnv = false
	}

	// Project scan: same nearest-wins walk as LoadProjectConfig — the
	// first level holding either spelling decides, so the rename offer
	// matches what the readers would actually pick up.
	if dir, err := filepath.Abs(cwd); err == nil && cwd != "" {
		for {
			oldP := filepath.Join(dir, ".zot")
			newP := filepath.Join(dir, ".terva")
			oldOK := isDir(oldP)
			newOK := isDir(newP)
			if oldOK || newOK {
				p.ProjectRoot = dir
				p.ProjectNewDir = newP
				if oldOK {
					p.ProjectOldDir = oldP
					p.ProjectConflict = newOK
				}
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return p
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// samePath reports whether two paths name the same directory once
// cleaned and with symlinks resolved (best-effort: unresolvable paths
// fall back to the cleaned spelling).
func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	ra, errA := filepath.EvalSymlinks(ca)
	rb, errB := filepath.EvalSymlinks(cb)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}

// MigrationCopyReport is what the copy pass actually did.
type MigrationCopyReport struct {
	FilesCopied     int
	SymlinksCopied  int
	SkippedExisting []string // rel paths whose destination already existed
	Errors          []string // per-entry failures; the walk continues past them
}

// Clean reports a copy with no errors — the bar for offering to
// remove the old dir or setting the no-fallback marker.
func (r MigrationCopyReport) Clean() bool { return len(r.Errors) == 0 }

// CopyUserData copies oldDir into newDir without ever clobbering: a
// file that already exists at the destination is skipped and reported.
// Symlinks are recreated (connectors/ installs are symlinks); absolute
// targets inside oldDir are rewritten to the new location so they
// don't point back into a dir the user may delete next. Irregular
// files (sockets, FIFOs, devices) are skipped silently — they're
// runtime endpoints, not data.
func CopyUserData(oldDir, newDir string) MigrationCopyReport {
	var rep MigrationCopyReport
	walkErr := filepath.WalkDir(oldDir, func(src string, d fs.DirEntry, err error) error {
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(oldDir, src)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", src, err))
			return nil
		}
		if rel == "." {
			// The destination root may not exist yet (fresh install
			// that never launched terva).
			if err := privfs.MkdirAll(newDir); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", newDir, err))
				return fs.SkipDir
			}
			return nil
		}
		// The one-shot-note marker only means something inside the
		// legacy dir; carrying it over would just be litter.
		if rel == ".terva-migration-note-shown" {
			return nil
		}
		dest := filepath.Join(newDir, rel)
		switch {
		case d.IsDir():
			info, err := d.Info()
			mode := fs.FileMode(0o755)
			if err == nil {
				mode = info.Mode().Perm()
			}
			// Preserve the owner bits (structure, +x on dirs) but strip
			// group/other so a migrated tree lands private, matching new writes.
			if err := os.MkdirAll(dest, mode&0o700); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", rel, err))
				return fs.SkipDir
			}
		case d.Type()&fs.ModeSymlink != 0:
			if _, err := os.Lstat(dest); err == nil {
				rep.SkippedExisting = append(rep.SkippedExisting, rel)
				return nil
			}
			target, err := os.Readlink(src)
			if err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", rel, err))
				return nil
			}
			if filepath.IsAbs(target) {
				if r, err := filepath.Rel(oldDir, target); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
					target = filepath.Join(newDir, r)
				}
			}
			if err := os.Symlink(target, dest); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", rel, err))
				return nil
			}
			rep.SymlinksCopied++
		case !d.Type().IsRegular():
			// Sockets, FIFOs, devices — e.g. a dead swarm agent's
			// in.sock inbox. There are no contents to copy (open(2)
			// on a socket fails with ENXIO), so they must not dirty
			// the report and block finalization.
		default:
			if err := copyFileNoClobber(src, dest, d); err != nil {
				if os.IsExist(err) {
					rep.SkippedExisting = append(rep.SkippedExisting, rel)
					return nil
				}
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", rel, err))
				return nil
			}
			rep.FilesCopied++
		}
		return nil
	})
	if walkErr != nil {
		rep.Errors = append(rep.Errors, walkErr.Error())
	}
	return rep
}

func copyFileNoClobber(src, dest string, d fs.DirEntry) error {
	mode := fs.FileMode(0o644)
	if info, err := d.Info(); err == nil {
		mode = info.Mode().Perm()
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Strip group/other bits but keep owner bits, so a migrated binary stays
	// executable while the copy is private (matches privfs new-file modes).
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode&0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// RemoveOldUserDir deletes the legacy data dir. It refuses unless the
// copy was clean and the dirs are genuinely distinct — a partial copy
// must never end with the only complete copy being deleted.
func RemoveOldUserDir(p MigrationPlan, rep MigrationCopyReport) error {
	if p.OldDir == "" {
		return fmt.Errorf("no legacy dir to remove")
	}
	if !rep.Clean() {
		return fmt.Errorf("refusing to remove %s: the copy reported %d error(s)", p.OldDir, len(rep.Errors))
	}
	if samePath(p.OldDir, p.NewDir) {
		return fmt.Errorf("refusing to remove %s: it is the migration target", p.OldDir)
	}
	return os.RemoveAll(p.OldDir)
}

// RenameProjectDir renames <ProjectRoot>/.zot to <ProjectRoot>/.terva.
func RenameProjectDir(p MigrationPlan) error {
	if p.ProjectOldDir == "" {
		return fmt.Errorf("no project .zot directory found")
	}
	if p.ProjectConflict {
		return fmt.Errorf("%s already exists — merge %s into it by hand", p.ProjectNewDir, p.ProjectOldDir)
	}
	return os.Rename(p.ProjectOldDir, p.ProjectNewDir)
}

// FinalizeMigration sets the persistent marker that stops terva from
// autoloading zot config files (the legacy data dir and .zot project
// dirs). ZOT_* env vars and .zotsession import are unaffected.
func FinalizeMigration() error {
	return envcompat.SetZotFallbackDisabled(true)
}
