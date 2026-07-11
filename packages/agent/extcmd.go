package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/ignore"
)

// runExtCommand dispatches `terva ext ...` subcommands. Returns
// (handled=true, err) if rawArgs starts with "ext"; otherwise
// (handled=false, nil) so the main router falls through to the
// regular flag parser.
func runExtCommand(rawArgs []string, version string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "ext" {
		return false, nil
	}
	if len(rawArgs) == 1 {
		printExtHelp()
		return true, nil
	}
	switch rawArgs[1] {
	case "list":
		return true, extList()
	case "doctor":
		return true, extDoctor(version)
	case "logs":
		return true, extLogs(rawArgs[2:])
	case "enable":
		return true, extToggle(rawArgs[2:], true)
	case "disable":
		return true, extToggle(rawArgs[2:], false)
	case "remove", "rm":
		return true, extRemove(rawArgs[2:])
	case "install":
		return true, extInstall(rawArgs[2:])
	case "pack":
		return true, extPackInstall(rawArgs[2:])
	case "migrate":
		return true, extMigrate(rawArgs[2:])
	case "upgrade", "update":
		return true, extUpgrade(rawArgs[2:])
	case "help", "-h", "--help":
		printExtHelp()
		return true, nil
	default:
		printExtHelp()
		return true, i18n.Errorf("unknown ext subcommand: %s", rawArgs[1])
	}
}

func printExtHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.ext", `terva ext — manage extensions

usage:
  terva ext list                    list installed extensions and their state
  terva ext doctor                  diagnose extension discovery and registration
  terva ext logs <name> [-f]        cat / tail an extension's stderr log
  terva ext enable <name>           re-enable a disabled extension
  terva ext disable <name>          disable without removing
  terva ext remove <name>           delete an extension directory
  terva ext upgrade <name>...       fast-forward-pull an installed extension's git checkout
  terva ext install <path|git-url>  copy / clone an extension into $TERVA_HOME/extensions/
  terva ext pack install [pack]     bulk-install an extension pack (default: built-in "core")
  terva ext migrate [pack]          rename look-alike manual installs to a pack's canonical names

extensions live under:
  $TERVA_HOME/extensions/<name>/extension.json   (global)
  ./.terva/extensions/<name>/extension.json      (project-local)`))
}

// extList walks both the global and project-local extension dirs and
// prints a one-row-per-extension table.
func extList() error {
	type row struct {
		Scope    string
		Name     string
		Version  string
		Enabled  string
		Language string
		Dir      string
	}
	var rows []row
	for scope, dir := range extensionDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			extDir := filepath.Join(dir, e.Name())
			mfPath := filepath.Join(extDir, "extension.json")
			raw, err := os.ReadFile(mfPath)
			if err != nil {
				continue
			}
			var m struct {
				Name     string `json:"name"`
				Version  string `json:"version"`
				Language string `json:"language"`
				Enabled  *bool  `json:"enabled"`
			}
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			enabled := "yes"
			if m.Enabled != nil && !*m.Enabled {
				enabled = "no"
			}
			rows = append(rows, row{
				Scope: scope, Name: m.Name, Version: m.Version,
				Enabled: enabled, Language: m.Language, Dir: extDir,
			})
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no extensions installed")
		fmt.Fprintln(os.Stderr, "see docs/extensions.md to write your own, or `terva ext install <path|url>`")
		return nil
	}
	fmt.Printf("%-12s  %-20s  %-10s  %-8s  %-10s  %s\n", "scope", "name", "version", "enabled", "language", "dir")
	for _, r := range rows {
		fmt.Printf("%-12s  %-20s  %-10s  %-8s  %-10s  %s\n",
			r.Scope, r.Name, dashIfEmpty(r.Version),
			r.Enabled, dashIfEmpty(r.Language), r.Dir)
	}
	return nil
}

// extLogs locates the named extension's log file and either cats or
// tails it (-f).
func extLogs(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva ext logs <name> [-f]")
	}
	name := args[0]
	follow := false
	for _, a := range args[1:] {
		if a == "-f" || a == "--follow" {
			follow = true
		}
	}
	logPath := filepath.Join(config.TervaHome(), "logs", "ext-"+name+".log")
	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("no log for %q at %s", name, logPath)
	}
	if !follow {
		f, err := os.Open(logPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(os.Stdout, f)
		return err
	}
	cmd := exec.Command("tail", "-F", logPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// extToggle flips the enabled flag in an extension's manifest.
func extToggle(args []string, enabled bool) error {
	if len(args) == 0 {
		verb := "enable"
		if !enabled {
			verb = "disable"
		}
		return i18n.Errorf("usage: terva ext %s <name>", verb)
	}
	name := args[0]
	dir, err := findExtensionDir(name)
	if err != nil {
		return err
	}
	if err := config.SetManifestEnabled(dir, enabled); err != nil {
		return err
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", state, name)
	return nil
}

// extRemove deletes an extension's directory after a confirmation
// prompt (skip with --yes).
func extRemove(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva ext remove <name> [--yes]")
	}
	name := args[0]
	yes := false
	for _, a := range args[1:] {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	dir, err := findExtensionDir(name)
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintf(os.Stderr, "remove %s ? [y/N] ", dir)
		var resp string
		_, _ = fmt.Scanln(&resp)
		if !strings.EqualFold(strings.TrimSpace(resp), "y") {
			fmt.Fprintln(os.Stderr, "aborted")
			return nil
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed %s\n", dir)
	return nil
}

// errExtAlreadyInstalled signals that installOne found an extension
// already present at its destination. The single-install CLI treats it
// as a hard error ("remove it first"); the pack installer treats it as a
// graceful skip so a re-run, or a partially-present pack, completes.
var errExtAlreadyInstalled = errors.New("extension already installed")

// cloneArgs builds the argv for the shallow git clone. A non-empty ref
// (branch OR tag) becomes --branch; absent, git checks out the remote's
// default HEAD. Kept pure so the branch logic is unit-testable without
// invoking git.
func cloneArgs(src, out, ref string) []string {
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	return append(args, src, out)
}

// installOne places a single extension under $TERVA_HOME/extensions/ and
// returns the install path. src is a local directory or a git URL; ref
// (branch or tag) applies to git sources only; nameOverride, when set,
// names the install dir instead of deriving it from the source basename.
// Validates that the destination contains an extension.json (rolling a
// git clone back on failure). Returns errExtAlreadyInstalled (with the
// destination path) when something is already installed there — callers
// decide whether that's fatal or a skip. installOne does not print on
// success; the caller reports the outcome.
func installOne(src, ref, nameOverride string) (out string, err error) {
	dest := filepath.Join(config.TervaHome(), "extensions")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}

	if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "git@") || strings.HasSuffix(src, ".git") {
		name := nameOverride
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(src), ".git")
		}
		if !safeInstallName(name) {
			return "", fmt.Errorf("cannot derive a safe extension name from %q", src)
		}
		out = filepath.Join(dest, name)
		if _, err := os.Stat(out); err == nil {
			return out, errExtAlreadyInstalled
		}
		cmd := exec.Command("git", cloneArgs(src, out, ref)...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return out, fmt.Errorf("git clone: %w", err)
		}
		if _, err := os.Stat(filepath.Join(out, "extension.json")); err != nil {
			_ = os.RemoveAll(out)
			return out, fmt.Errorf("installed dir lacks extension.json; aborted and rolled back")
		}
		// The clone target was the repo basename; canonicalize to the
		// manifest name (only known after cloning) so terva-ext-foo lands at
		// foo. An explicit override or an already-correct dir is left alone.
		if nameOverride == "" {
			if mn := manifestName(out); mn != "" && mn != filepath.Base(out) {
				canonical := filepath.Join(dest, mn)
				if _, err := os.Stat(canonical); err == nil {
					_ = os.RemoveAll(out) // already installed under the canonical name
					return canonical, errExtAlreadyInstalled
				}
				if err := os.Rename(out, canonical); err != nil {
					// The basename install works; keep it rather than lose it.
					fmt.Fprintf(os.Stderr, "note: installed at %s; could not rename to canonical %s: %v\n", out, canonical, err)
					return out, nil
				}
				out = canonical
			}
		}
		return out, nil
	}

	// Local path: must be a directory containing extension.json. A ref is
	// meaningless here (there's nothing to check out), so warn and ignore.
	if ref != "" {
		fmt.Fprintf(os.Stderr, "note: ref %q ignored for local-path source %s\n", ref, src)
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", src)
	}
	if _, err := os.Stat(filepath.Join(src, "extension.json")); err != nil {
		return "", fmt.Errorf("source lacks extension.json")
	}
	// Resolve to an absolute, cleaned path before deriving the install
	// name. Otherwise relative sources like "." or "./" collapse to a
	// basename of ".", and the destination wrongly resolves to the
	// extensions/ parent directory (which terva creates on first run),
	// triggering a false "already exists" failure.
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	// terva keys an extension by its manifest `name`, so install under that
	// (when no explicit override) instead of the source basename — a repo
	// named terva-ext-foo with manifest name "foo" installs to foo. Fall
	// back to the basename when the manifest name is missing or unsafe.
	name := nameOverride
	if name == "" {
		name = manifestName(absSrc)
	}
	if name == "" {
		name = filepath.Base(absSrc)
	}
	if !safeInstallName(name) {
		return "", fmt.Errorf("cannot derive a safe extension name from %q", src)
	}
	out = filepath.Join(dest, name)
	if _, err := os.Stat(out); err == nil {
		return out, errExtAlreadyInstalled
	}
	if err := copyDir(absSrc, out); err != nil {
		return out, err
	}
	return out, nil
}

// safeInstallName reports whether name is a single, safe path component to
// install under extensions/. An extension's name can come from an
// untrusted source (a cloned repo's extension.json, a third-party pack
// entry), so it must not contain a path separator or be a dot-dir — else
// filepath.Join could escape the extensions dir.
func safeInstallName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}

// manifestName returns the sanitized extension name declared in
// <dir>/extension.json, or "" when it can't be read, is unset, or is not a
// safe single path component — in which case the caller falls back to the
// (already-safe) source basename. Reads only the name field, like
// findExtensionDir, to avoid importing extdriver here.
func manifestName(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "extension.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	name := strings.TrimSpace(m.Name)
	if !safeInstallName(name) {
		return ""
	}
	return name
}

// extInstall copies a local directory or shallow-clones a git URL
// into $TERVA_HOME/extensions/. Validates the destination contains an
// extension.json before reporting success.
func extInstall(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva ext install <path|git-url>")
	}
	out, err := installOne(args[0], "", "")
	if errors.Is(err, errExtAlreadyInstalled) {
		return fmt.Errorf("destination %s already exists; remove it first", out)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "installed %s\n", out)
	return nil
}

// extUpgrade updates one or more installed extensions in place by
// fast-forward-pulling their git checkout — the same per-extension logic
// `terva update` runs over the whole set, scoped to the names you pass.
// Disabled extensions and non-git installs are reported and skipped. No
// build step runs: a self-bootstrapping launcher (run.sh) rebuilds on the
// next spawn (see docs/extensions.md). Use `terva update` to upgrade every
// extension plus the terva binary at once.
func extUpgrade(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva ext upgrade <name>... (use `terva update` for every extension + the binary)")
	}
	var failed, updated int
	for _, name := range args {
		dir, err := findExtensionDir(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed++
			continue
		}
		switch updateOneExtension(dir, name) {
		case "updated":
			updated++
		case "failed":
			failed++
		}
	}
	if updated > 0 {
		fmt.Println("restart terva (or /reload-ext in a running session) to pick up the new version")
	}
	if failed > 0 {
		return fmt.Errorf("%d extension(s) failed to upgrade", failed)
	}
	return nil
}

func extensionDirs() map[string]string {
	out := map[string]string{}
	if h := config.TervaHome(); h != "" {
		out["global"] = filepath.Join(h, "extensions")
	}
	if cwd, err := os.Getwd(); err == nil {
		out["project"] = filepath.Join(cwd, ".terva", "extensions")
	}
	return out
}

// findExtensionDir resolves an extension by the name shown in
// `terva ext list` — which is the manifest's declared name — OR by its
// install-directory basename. The two differ when the install dir is the
// source repo name (e.g. dir "terva-ext-index" for manifest name "index"),
// so accepting both means `ext upgrade index` works just like the list
// shows it. The directory-name match wins first (fast path, back-compat).
func findExtensionDir(name string) (string, error) {
	// Search global before project so a name present in both resolves
	// deterministically — extensionDirs is a map, and ranging it is unordered.
	dirs := extensionDirs()
	roots := make([]string, 0, 2)
	if d, ok := dirs["global"]; ok {
		roots = append(roots, d)
	}
	if d, ok := dirs["project"]; ok {
		roots = append(roots, d)
	}
	if dir, ok := config.MatchExtensionDir(roots, name); ok {
		return dir, nil
	}
	return "", fmt.Errorf("extension %q not found", name)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// copyDir does a recursive copy of src to dst preserving file mode
// bits. Used by `terva ext install <local-path>`.
//
// Entries matched by the source's root .gitignore are skipped, and
// .git itself is always skipped. This keeps non-portable, regeneratable
// directories (e.g. .venv with hardcoded rpaths, node_modules, target/)
// out of the installed copy so the extension stays functional at its new
// location.
func copyDir(src, dst string) error {
	ig := loadGitignore(src)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel != "." {
			name := filepath.Base(rel)
			if info.IsDir() && name == ".git" {
				return filepath.SkipDir
			}
			if ig.Match(filepath.ToSlash(rel), info.IsDir()) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// gitignore matching lives in packages/ignore so the @-file picker in
// packages/agent/modes can share it without an import cycle. These
// thin aliases keep the existing call sites (and tests) terse.
type gitignore = ignore.Gitignore

func loadGitignore(root string) *gitignore { return ignore.Load(root) }

func loadGitignoreFromString(data string) *gitignore { return ignore.Parse(data) }
