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

	// secretRoots are paths the READ tools refuse even though reads are
	// otherwise unconfined: credentials, transcripts, and the logs that
	// aggregate them. See CheckPathRead for why the read side is a deny
	// list where the write side is a containment check. Same
	// set-once-at-setup contract as readOnlyRoots.
	secretRoots []string

	// secretExceptions are paths carved back OUT of secretRoots — the
	// ext-*.log inside an otherwise-denied logs/. Checked before
	// secretRoots so the narrower rule wins.
	secretExceptions []roGlob

	locked atomic.Bool
}

// roGlob names files directly inside dir (canonical) whose base name
// matches pattern (filepath.Match).
type roGlob struct {
	dir     string
	pattern string
}

// NewSandbox returns a Sandbox rooted at cwd. It starts unlocked.
func NewSandbox(root string) *Sandbox {
	s := &Sandbox{Root: root}
	return s
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
	return s.checkUnder(path)
}

// CheckPathRead is the read-side check, and it is a DENY LIST where
// CheckPath is a containment check. A jailed agent may read anywhere
// except the registered secret roots. No-op when unlocked.
//
// The asymmetry is deliberate. The bash tool is not path-jailed — by design,
// documented at CheckCommand as a speed bump rather than a boundary — so
// anything `read` refused was already one `cat` away. Enforcing containment on
// one tool and not the other bought no confinement and cost real turns: in the
// session behind this change the model issued eight parallel reads under /tmp,
// had all eight refused, and fetched the same bytes through `git show | awk` on
// the next turn. That is the failure mode CheckCommand's own comment warns
// about for `cd` — "wastes turns and nudges the model toward trying to break
// out" — and the read side was doing exactly it.
//
// What remains denied is the set that is genuinely worth denying and that bash
// reaching it anyway does not excuse: credentials, session transcripts, and the
// log sink they leak into. Those are enumerated by the host (see
// build.restrictSensitiveReads), not inferred here.
//
// WRITES are unchanged: CheckPath still confines them to Root, and reading a
// file has never been the step that damages a tree.
func (s *Sandbox) CheckPathRead(path string) error {
	if !s.Locked() {
		return nil
	}
	target, err := canonicalOrParent(path)
	if err != nil {
		return fmt.Errorf("sandbox path: %w", err)
	}
	// A narrower allowance carved out of a denied dir wins over it.
	base := filepath.Base(target)
	for _, g := range s.secretExceptions {
		if filepath.Dir(target) != g.dir {
			continue
		}
		if ok, _ := filepath.Match(g.pattern, base); ok {
			return nil
		}
	}
	for _, secret := range s.secretRoots {
		if isUnder(secret, target) {
			return fmt.Errorf("jailed: path %q holds credentials or transcripts and is never readable by tools (bash cannot read it either; /unjail does not lift this)", path)
		}
	}
	return nil
}

// AddSecretRoot registers a path the read tools must always refuse, even
// though reads are otherwise unconfined when jailed. Paths are
// canonicalized once (unresolvable ones are skipped, since a path that
// does not exist cannot be read either). Call during setup, before any
// tool runs.
func (s *Sandbox) AddSecretRoot(paths ...string) {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		c, err := canonicalOrParent(p)
		if err != nil {
			continue
		}
		s.secretRoots = append(s.secretRoots, c)
	}
}

// AddSecretException carves a readable slice back out of a secret root:
// files directly inside dir whose base name matches pattern stay
// readable while the rest of dir does not. Non-recursive, mirroring
// AddReadOnlyGlob.
func (s *Sandbox) AddSecretException(dir, pattern string) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(pattern) == "" {
		return
	}
	c, err := canonicalOrParent(dir)
	if err != nil {
		return
	}
	s.secretExceptions = append(s.secretExceptions, roGlob{dir: c, pattern: pattern})
}

// checkUnder is the WRITE-side containment check: the target must resolve
// inside the writable Root. Reads take the deny-list path in CheckPathRead
// instead — see the note there for why the two sides differ.
func (s *Sandbox) checkUnder(path string) error {
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
	return fmt.Errorf("jailed: cannot WRITE %q — it is outside the sandbox root %q (reading it is allowed; use /unjail to lift the write jail)", path, s.Root)
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
	// Commands that are dangerous by NAME, wherever they appear. These stay
	// substring matches because the thing being matched is the invocation
	// itself, not one of its arguments.
	banned := []string{
		"sudo ", "su ",
		"chmod -R ", "chown -R ",
		"mkfs", "dd if=",
	}
	lower := strings.ToLower(cmd)
	for _, b := range banned {
		if strings.Contains(lower, strings.ToLower(b)) {
			return fmt.Errorf("jailed: command contains banned pattern %q (use /unjail to disable)", b)
		}
	}
	// The secret deny list applies here too. Enforcing it on `read` alone would
	// rebuild, for the one case that actually matters, the same split posture
	// this sandbox just abandoned: the model would be refused auth.json by the
	// file tool and handed it by `cat`. This is a speed bump of exactly the
	// same strength as everything else in this function — a runtime-assembled
	// path or an interpreter still walks past it — but it is at least the same
	// speed bump on both routes.
	if err := s.checkSecretArgs(cmd); err != nil {
		return err
	}
	// Commands that are dangerous by TARGET are matched on whole arguments.
	// "rm -rf /" as a substring matched `rm -rf /tmp/build` — and every other
	// absolute path — so the guard rejected the explicit, auditable spelling of
	// a scratch cleanup while permitting the same deletion through a generated
	// path. A model that hits it does not stop; it reaches for `mktemp -d` and
	// carries on, which is strictly worse for anyone reading the transcript.
	if err := checkDestructiveTarget(cmd); err != nil {
		return err
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

// checkSecretArgs rejects a shell command that names a denied path. Only
// arguments that LOOK like paths are resolved — one containing a separator, or
// starting with ~ or $HOME — so a bare word that happens to read "sessions"
// costs nothing. Matching reuses CheckPathRead so the two routes cannot drift.
func (s *Sandbox) checkSecretArgs(cmd string) error {
	if len(s.secretRoots) == 0 {
		return nil
	}
	for _, tok := range strings.Fields(cmd) {
		arg := expandHome(unquoteArg(tok))
		if !strings.ContainsRune(arg, filepath.Separator) && !strings.Contains(arg, "/") {
			continue
		}
		if !filepath.IsAbs(arg) {
			arg = filepath.Join(s.Root, arg)
		}
		if err := s.CheckPathRead(arg); err != nil {
			return err
		}
	}
	return nil
}

// checkDestructiveTarget rejects a command whose TARGET is a filesystem root
// rather than something beneath one: `rm -rf /`, `rm -rf ~`, and a raw write to
// a device node. Arguments are compared whole, so a path *under* a root — the
// overwhelmingly common, legitimate case — passes.
//
// Like the rest of the sandbox this is a speed bump, not a security boundary:
// `rm -rf "$HOME"` with the variable set elsewhere, or a path assembled at
// runtime, sails through. It exists to catch the fat-finger, and a check that
// also catches `rm -rf /tmp/scratch` is not catching fat-fingers, it is
// training the model to route around the guard.
func checkDestructiveTarget(cmd string) error {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return nil
	}
	name := strings.ToLower(fields[0])
	args := fields[1:]

	switch {
	case name == "rm":
		recursive, force := false, false
		var targets []string
		for _, a := range args {
			if strings.HasPrefix(a, "--") {
				switch a {
				case "--recursive":
					recursive = true
				case "--force":
					force = true
				}
				continue
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				if strings.ContainsRune(a, 'r') || strings.ContainsRune(a, 'R') {
					recursive = true
				}
				if strings.ContainsRune(a, 'f') {
					force = true
				}
				continue
			}
			targets = append(targets, a)
		}
		if !recursive {
			return nil // a non-recursive rm cannot take out a tree
		}
		_ = force // force changes the prompt, not the blast radius
		for _, t := range targets {
			if isFilesystemRoot(t) {
				return fmt.Errorf("jailed: refusing to delete the filesystem root %q recursively (use /unjail to disable)", t)
			}
		}
	case name == "dd":
		// Writing to a device is the hazard; `dd of=/tmp/img` is ordinary.
		for _, a := range args {
			v, ok := strings.CutPrefix(a, "of=")
			if !ok {
				continue
			}
			v = unquoteArg(v)
			if isFilesystemRoot(v) || strings.HasPrefix(v, "/dev/") {
				return fmt.Errorf("jailed: refusing a raw write to %q (use /unjail to disable)", v)
			}
		}
	}
	return nil
}

// isFilesystemRoot reports whether an argument names a root whose recursive
// deletion is categorically a mistake: /, the home directory, or either one
// with a trailing glob.
func isFilesystemRoot(arg string) bool {
	arg = strings.TrimSpace(unquoteArg(arg))
	// A trailing /* is the same blast radius as the directory itself.
	arg = strings.TrimSuffix(arg, "*")
	arg = strings.TrimSuffix(arg, "/")
	switch arg {
	case "", "/", "~", "$HOME", "${HOME}":
		// "" is what "/" and "~" reduce to after the trims above.
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if arg == strings.TrimSuffix(home, "/") {
			return true
		}
	}
	return false
}

// unquoteArg strips one layer of surrounding quotes, matching cdTarget.
func unquoteArg(a string) string {
	if len(a) >= 2 {
		if (a[0] == '"' && a[len(a)-1] == '"') || (a[0] == '\'' && a[len(a)-1] == '\'') {
			return a[1 : len(a)-1]
		}
	}
	return a
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
