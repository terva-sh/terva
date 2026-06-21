package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Sandbox guards tool access to the filesystem and shell. When Locked
// is true (1), file tools refuse paths outside Root and bash runs with
// a restricted environment.
//
// The value is designed to be shared across tool instances (by pointer).
// Enable/Disable are atomic so they can be toggled from the TUI.
type Sandbox struct {
	Root string

	// readOnlyRoots are extra directories the READ tools may reach even
	// when jailed, without making them writable — shared, version-pinned
	// assets like $TERVA_HOME/docs. Set once during setup via
	// AddReadOnlyRoot (before tools run); the path checks read them
	// concurrently, so don't mutate them afterwards.
	readOnlyRoots []string

	// readOnlyGlobs are finer-grained read allowances: a file directly
	// inside Dir whose base name matches Pattern is readable, nothing else
	// in Dir is. Used to expose a safe slice of an otherwise-sensitive dir
	// (e.g. logs/ext-*.log without the MCP/bot/connector/hooks logs). Same
	// set-once-at-setup contract as readOnlyRoots.
	readOnlyGlobs []roGlob

	locked atomic.Bool
}

// roGlob is one finer-grained read allowance: files directly inside dir
// (canonical) whose base name matches pattern (filepath.Match).
type roGlob struct {
	dir     string
	pattern string
}

// NewSandbox returns a Sandbox rooted at cwd. It starts unlocked.
func NewSandbox(root string) *Sandbox {
	s := &Sandbox{Root: root}
	return s
}

// AddReadOnlyRoot registers extra directories that read tools may read
// from even when jailed, without making them writable. Paths are
// canonicalized once (unresolvable ones are skipped). Intended for
// shared, version-matched assets the agent should always be able to
// inspect — terva's own docs, bundled skills/themes — that live outside
// the working directory. Call during setup, before any tool runs.
func (s *Sandbox) AddReadOnlyRoot(paths ...string) {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		c, err := canonicalOrParent(p)
		if err != nil {
			continue
		}
		s.readOnlyRoots = append(s.readOnlyRoots, c)
	}
}

// AddReadOnlyGlob registers a finer-grained read allowance: files
// directly inside dir whose base name matches pattern become readable
// (never writable), while the rest of dir stays blocked. Use it to
// expose a safe subset of an otherwise-sensitive directory — e.g.
// logs/ext-*.log without the MCP/bot/connector/hooks logs. dir is
// canonicalized once; an unresolvable dir or empty pattern is skipped.
// Matching is non-recursive: only files directly in dir qualify.
func (s *Sandbox) AddReadOnlyGlob(dir, pattern string) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(pattern) == "" {
		return
	}
	c, err := canonicalOrParent(dir)
	if err != nil {
		return
	}
	s.readOnlyGlobs = append(s.readOnlyGlobs, roGlob{dir: c, pattern: pattern})
}

// Lock enables sandboxing.
func (s *Sandbox) Lock() { s.locked.Store(true) }

// Unlock disables sandboxing.
func (s *Sandbox) Unlock() { s.locked.Store(false) }

// Locked reports whether the sandbox is enforcing limits.
func (s *Sandbox) Locked() bool { return s != nil && s.locked.Load() }

// CheckPath verifies that path resolves inside the WRITABLE sandbox root.
// Read-only roots do NOT satisfy it — mutating tools (write/edit) must
// stay within the jail's working directory. Returns an error describing
// the violation if not. No-op when unlocked. Callers should pass an
// already-absolute path (use resolvePath() first).
func (s *Sandbox) CheckPath(path string) error {
	return s.checkUnder(path, false)
}

// CheckPathRead is the read-side check: it permits the writable root AND
// any registered read-only root (e.g. $TERVA_HOME/docs). The read tools
// (read/grep/glob) use it so a jailed agent can still inspect shared,
// non-writable assets it's pointed at. No-op when unlocked.
func (s *Sandbox) CheckPathRead(path string) error {
	return s.checkUnder(path, true)
}

// checkUnder is the shared containment check. The target must resolve
// inside the writable Root, or — when allowReadOnly — inside one of the
// read-only roots.
func (s *Sandbox) checkUnder(path string, allowReadOnly bool) error {
	if !s.Locked() {
		return nil
	}
	rootAbs, err := canonical(s.Root)
	if err != nil {
		return fmt.Errorf("sandbox root: %w", err)
	}
	// Resolve the target to an absolute path. Walk up until we find an
	// existing parent so symlinks inside nonexistent dirs are still caught.
	target, err := canonicalOrParent(path)
	if err != nil {
		return fmt.Errorf("sandbox path: %w", err)
	}
	if isUnder(rootAbs, target) {
		return nil
	}
	if allowReadOnly {
		for _, ro := range s.readOnlyRoots {
			if isUnder(ro, target) {
				return nil
			}
		}
		// Finer-grained allowances: a file directly inside a glob's dir
		// whose base name matches the pattern (e.g. logs/ext-*.log).
		base := filepath.Base(target)
		for _, g := range s.readOnlyGlobs {
			if filepath.Dir(target) != g.dir {
				continue
			}
			if ok, _ := filepath.Match(g.pattern, base); ok {
				return nil
			}
		}
	}
	return fmt.Errorf("jailed: path %q is outside sandbox root %q (use /unjail to disable)", path, s.Root)
}

// CheckCommand applies a lightweight sanity check to a bash command
// when jailed. We cannot fully sandbox a shell, but we can reject the
// most obvious escapes so the model does not accidentally touch files
// outside root via absolute paths.
//
// Each command in a compound line is checked independently
// (DecomposeBashCommand), so a banned command or a sandbox-escaping `cd`
// cannot hide behind a harmless leading one (`ls && cd /etc`). This is a
// speed bump for the model, not a security boundary: a determined
// adversary can still escape, and unparsable lines fall back to a single
// whole-string check.
func (s *Sandbox) CheckCommand(cmd string) error {
	if !s.Locked() {
		return nil
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	scopes := DecomposeBashCommand(cmd)
	if len(scopes) == 0 {
		scopes = []string{cmd}
	}
	for _, sc := range scopes {
		if err := s.checkCommandScope(sc); err != nil {
			return err
		}
	}
	return nil
}

// checkCommandScope runs the banned-pattern and cd-escape heuristics
// against one simple command from a (possibly compound) line.
func (s *Sandbox) checkCommandScope(cmd string) error {
	// Reject obvious destructive roots.
	banned := []string{
		"rm -rf /", "rm -rf ~", "rm -rf $HOME",
		"sudo ", "su ",
		"chmod -R ", "chown -R ",
		"mkfs", "dd if=", "dd of=/",
	}
	lower := strings.ToLower(cmd)
	for _, b := range banned {
		if strings.Contains(lower, strings.ToLower(b)) {
			return fmt.Errorf("jailed: command contains banned pattern %q (use /unjail to disable)", b)
		}
	}
	// Reject only a `cd` whose target actually resolves OUTSIDE the
	// sandbox root. A cd into a subdirectory of root — even spelled as an
	// absolute path (`cd /abs/inside/root && build`) — is allowed, because
	// blanket-rejecting it wastes turns and nudges the model toward trying
	// to break out. Bare `cd` / `cd -` are left to the path checks on later
	// tool calls. Still a speed bump, not a security boundary.
	if target, ok := cdTarget(strings.TrimSpace(cmd)); ok {
		if err := s.checkCDTarget(target); err != nil {
			return err
		}
	}
	return nil
}

// canonical returns an absolute, symlink-resolved path. Errors on missing files.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// canonicalOrParent returns the canonical path for p; if p doesn't exist,
// it walks up until it finds an existing directory, then appends the
// remaining path components. This catches symlink-escapes in non-existent
// subtrees (e.g. "new-file" inside a symlinked dir).
func canonicalOrParent(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// If the full path exists, resolve it.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// Otherwise, find the longest existing prefix.
	remaining := ""
	current := abs
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remaining), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		remaining = filepath.Join(filepath.Base(current), remaining)
		current = parent
	}
}

// isUnder reports whether target is equal to root or a descendant of it.
func isUnder(root, target string) bool {
	rootSep := root
	if !strings.HasSuffix(rootSep, string(filepath.Separator)) {
		rootSep += string(filepath.Separator)
	}
	return target == root || strings.HasPrefix(target, rootSep)
}

// DisplayPath returns the path the model should see in tool results and
// error messages. When jailed, an absolute path inside the sandbox root
// is rewritten relative to that root ("./pkg/foo.go"), keeping absolute
// paths out of the context window so the model is nudged toward relative
// paths instead of trying to escape the jail. Paths outside root,
// unjailed sessions, and already-relative inputs are returned unchanged.
//
// abs is the resolved absolute path; given is the path exactly as the
// model supplied it, used as the fallback when no relative form fits.
func (s *Sandbox) DisplayPath(abs, given string) string {
	if !s.Locked() {
		return given
	}
	rootAbs, err := canonical(s.Root)
	if err != nil {
		return given
	}
	target, err := canonicalOrParent(abs)
	if err != nil {
		return given
	}
	if !isUnder(rootAbs, target) {
		return given
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return given
	}
	if rel == "." {
		return "."
	}
	return "./" + filepath.ToSlash(rel)
}

// cdTarget extracts the destination of a leading `cd <dir>` command.
// Returns ok=false when seg is not a `cd` invocation or has no explicit
// target (bare `cd` / `cd -` go home / previous dir; those are left to
// the path checks on subsequent tool calls).
func cdTarget(seg string) (string, bool) {
	seg = strings.TrimSpace(seg)
	if seg != "cd" && !strings.HasPrefix(seg, "cd ") {
		return "", false
	}
	arg := strings.TrimSpace(strings.TrimPrefix(seg, "cd"))
	if arg == "" || arg == "-" {
		return "", false
	}
	// Drop surrounding quotes if the model wrapped the path.
	if len(arg) >= 2 {
		if (arg[0] == '"' && arg[len(arg)-1] == '"') || (arg[0] == '\'' && arg[len(arg)-1] == '\'') {
			arg = arg[1 : len(arg)-1]
		}
	}
	return arg, true
}

// checkCDTarget resolves a `cd` destination (relative to the sandbox
// root, with ~ and $HOME expansion) and rejects it only when it lands
// outside the root.
func (s *Sandbox) checkCDTarget(dir string) error {
	rootAbs, err := canonical(s.Root)
	if err != nil {
		return fmt.Errorf("sandbox root: %w", err)
	}
	expanded := expandHome(dir)
	// A leading forward slash is an absolute POSIX path. The shell uses
	// POSIX-style paths regardless of host OS, but on Windows
	// filepath.IsAbs("/etc") is false and filepath.Join would fold it back
	// inside root, letting a `cd /etc` escape slip through. Treat it as an
	// unconditional escape attempt.
	if strings.HasPrefix(expanded, "/") && !filepath.IsAbs(expanded) {
		return fmt.Errorf("jailed: cd outside sandbox root is not allowed (use /unjail to disable)")
	}
	if !filepath.IsAbs(expanded) {
		// Relative targets (including `..`) resolve against the sandbox
		// root, which is the bash tool's working directory when jailed.
		expanded = filepath.Join(s.Root, expanded)
	}
	target, err := canonicalOrParent(expanded)
	if err != nil {
		return fmt.Errorf("sandbox path: %w", err)
	}
	if !isUnder(rootAbs, target) {
		return fmt.Errorf("jailed: cd outside sandbox root is not allowed (use /unjail to disable)")
	}
	return nil
}

// expandHome replaces a leading ~, ~/, or $HOME with the user's home
// directory so cd-target resolution matches what the shell would do.
func expandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	switch {
	case p == "~" || p == "$HOME":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case strings.HasPrefix(p, "$HOME/"):
		return filepath.Join(home, p[len("$HOME/"):])
	default:
		return p
	}
}
