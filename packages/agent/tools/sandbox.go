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

	// secretNames deny by base NAME within a subtree, for keys whose
	// directories cannot be enumerated ahead of time. See AddSecretNameUnder.
	secretNames []roGlob

	// secretExceptions are paths carved back OUT of secretRoots — the
	// ext-*.log inside an otherwise-denied logs/. Checked before
	// secretRoots so the narrower rule wins.
	secretExceptions []roGlob

	// guardedRoots are subtrees whose readability is DECIDED AT READ TIME
	// rather than at setup. See AddGuardedRoot.
	guardedRoots []guardedRoot

	// writableRoots are directories OUTSIDE Root that mutating tools may
	// still target when jailed — terva-owned surfaces that exist to be
	// agent-written (the handoffs dir). Nothing here loosens containment
	// for the rest of the filesystem. Same set-once-at-setup contract as
	// secretRoots.
	writableRoots []string

	// ScratchDir is the writable root a refused write should NAME: the
	// sanctioned place for a file that does not belong in the user's tree.
	// Display only — the grant itself is an ordinary writableRoots entry, and
	// setting this without AddWritableRoot advertises a path that is still
	// jailed. The host sets both together (build.allowScratchWrites).
	ScratchDir string

	locked atomic.Bool
}

// roGlob names files directly inside dir (canonical) whose base name
// matches pattern (filepath.Match).
type roGlob struct {
	dir     string
	pattern string
}

// ReadGuard decides whether one path may be read, and says why not.
//
// It is consulted on EVERY read rather than once at setup, which is the whole
// reason it is a function. The subtrees it governs are component-owned — a
// connector's state directory, an extension's data directory — and a component
// is a live process that can write a plaintext credential into its own file at
// any moment. A verdict computed at startup would still be reporting "clean"
// for the rest of the session, which is precisely the wrong direction to be
// stale in.
//
// rel is the read target relative to the directory the guard was registered on,
// slash-separated. Relative rather than absolute so a guard never re-derives
// the path itself: the sandbox canonicalizes targets (resolving symlinks — on
// macOS every temp dir is one), and a guard comparing against an
// unresolved root silently matched NOTHING and allowed everything. Handing it
// an already-relative path removes the mismatch rather than documenting it.
type ReadGuard func(rel string) (allow bool, reason string)

// guardedRoot is one subtree and the guard that decides it.
type guardedRoot struct {
	dir   string
	guard ReadGuard
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
	// Denied by NAME within a subtree: a component's private key is created at
	// a path nobody can enumerate ahead of time (connectors/<any-name>/), and
	// AddSecretRoot skips paths that do not exist yet, so an exact list would
	// silently miss every connector configured after startup.
	for _, g := range s.secretNames {
		if base == g.pattern && isUnder(g.dir, target) {
			return fmt.Errorf("jailed: path %q is a private key and is never readable by tools (bash cannot read it either; /unjail does not lift this)", path)
		}
	}
	// Decided at read time, and LAST: a guard can only ever add a denial, never
	// lift one. The absolute rules above have already run, so a component tree
	// that a guard would happily allow still cannot hand back the private key
	// sitting inside it.
	for _, g := range s.guardedRoots {
		if !isUnder(g.dir, target) || target == g.dir {
			continue
		}
		rel, err := filepath.Rel(g.dir, target)
		if err != nil {
			continue
		}
		if allow, reason := g.guard(filepath.ToSlash(rel)); !allow {
			return fmt.Errorf("jailed: path %q is in a component directory that is not verifiably free of plaintext secrets — %s (bash cannot read it either; /unjail does not lift this)", path, reason)
		}
	}
	return nil
}

// AddSecretNameUnder denies every file with this exact base name anywhere
// beneath dir, however deep and whether or not it exists yet.
//
// For keys a component mints for itself: their directories are named after the
// component, so the set is open-ended, and denying the directory wholesale
// would hide state that is meant to stay inspectable.
func (s *Sandbox) AddSecretNameUnder(dir, name string) {
	c, err := canonicalOrParent(dir)
	if err != nil {
		return
	}
	s.secretNames = append(s.secretNames, roGlob{dir: c, pattern: name})
}

// AddGuardedRoot registers a subtree whose readability is decided per read by
// guard, rather than fixed at setup.
//
// For the component-owned trees — connectors/, ext-data/ — where the answer is
// not a property of the path but of what is currently IN it, and where the
// component is a live process that can change that answer mid-session. See
// [ReadGuard], and build.componentReadGuard for the verdict itself.
//
// A guard can only deny. Everything on the absolute lists is refused before any
// guard runs, so registering one can never widen what is readable.
func (s *Sandbox) AddGuardedRoot(dir string, guard ReadGuard) {
	if guard == nil {
		return
	}
	// Kept even when the directory does not exist yet, unlike AddSecretRoot: a
	// connector configured after startup creates its directory then, and a
	// guard skipped for absence would be exactly the one that mattered.
	c, err := canonicalOrParent(dir)
	if err != nil {
		return
	}
	s.guardedRoots = append(s.guardedRoots, guardedRoot{dir: c, guard: guard})
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

// AddWritableRoot registers a directory outside Root that mutating tools
// may still write while jailed. Unlike AddSecretRoot it keeps paths that
// do not exist yet — the write tool creates missing parents, and a fresh
// home has no handoffs/ until the first handoff is written; skipping the
// grant then would jail exactly the first use. Call during setup, before
// any tool runs.
func (s *Sandbox) AddWritableRoot(paths ...string) {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		c, err := canonicalOrParent(p)
		if err != nil {
			continue
		}
		s.writableRoots = append(s.writableRoots, c)
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
	if s.underWritableGrant(target) {
		return nil
	}
	// Name the actor, and name the alternative. /unjail is a USER slash
	// command the model cannot run, so a refusal that says "use /unjail"
	// points the model at a door it has no hand for. It routes around the jail
	// instead: in the session behind this change a refused write to /tmp came
	// back one turn later as `cat > /tmp/... <<'PY'`, which is the same write
	// with the review surface stripped off. The containment is deliberate —
	// an out-of-root write SHOULD have to reach for bash, where the command
	// classifier can see it — but the scratch case is not what that edge is
	// for. Naming the scratch dir keeps the ordinary case a reviewable tool
	// call and leaves the edge for writes that deserve it.
	if s.ScratchDir != "" {
		return fmt.Errorf("jailed: cannot WRITE %q — it is outside the sandbox root %q. Reading it is allowed. Put scratch files under %s. Ask the user to run /unjail if you need to write here", path, s.Root, s.ScratchDir)
	}
	return fmt.Errorf("jailed: cannot WRITE %q — it is outside the sandbox root %q. Reading it is allowed. Ask the user to run /unjail if you need to write here", path, s.Root)
}

// underWritableGrant reports whether target falls inside a directory
// registered with AddWritableRoot. target must ALREADY be canonical, which is
// what keeps a grant from being widened: a symlink planted inside a writable
// root that points elsewhere resolves outside it before it gets here, so it
// fails this test rather than riding the grant out of the jail.
//
// Shared by the write check and the `cd` check so the two cannot drift. They
// did drift: the write side honoured grants from the day they were added and
// the cd side never learned about them, which left every granted directory
// writable but not workable.
func (s *Sandbox) underWritableGrant(target string) bool {
	for _, w := range s.writableRoots {
		if isUnder(w, target) {
			return true
		}
	}
	return false
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
			return fmt.Errorf("jailed: command contains banned pattern %q (ask the user to run /unjail if this is intended)", b)
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
	// Every kind of denial, not just secretRoots. The original test was written
	// when roots were the only kind; secretNames and guardedRoots would each
	// have been skipped by it on a sandbox that registered nothing else, so bash
	// would have walked past a rule the file tools enforce. Unreachable today
	// (restrictSensitiveReads always adds roots) and wrong to leave load-bearing.
	if len(s.secretRoots) == 0 && len(s.secretNames) == 0 && len(s.guardedRoots) == 0 {
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
				return fmt.Errorf("jailed: refusing to delete the filesystem root %q recursively (ask the user to run /unjail if this is intended)", t)
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
				return fmt.Errorf("jailed: refusing a raw write to %q (ask the user to run /unjail if this is intended)", v)
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
		return fmt.Errorf("jailed: cd outside sandbox root is not allowed (ask the user to run /unjail if this is intended)")
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
	if isUnder(rootAbs, target) {
		return nil
	}
	// A directory the agent may WRITE is a directory it must be able to work
	// IN. Granting the write and refusing the `cd` is not a narrower rule, it
	// is an incoherent one: the tools that make a checkout useful — a build, a
	// test run, any script that resolves paths against its own repo root — take
	// a working directory, not a list of absolute paths. The agent that hits
	// this does not stop; it rewrites every command with `git -C`, `--prefix`
	// and `sh -c 'cd …'` until something sticks, which is the same work with
	// the jail's reasoning stripped out of it.
	//
	// This does not widen the jail. It admits exactly the directories a host
	// already registered with AddWritableRoot, and canonicalization upstream
	// means a symlink inside one cannot carry the grant anywhere else.
	if s.underWritableGrant(target) {
		return nil
	}
	return fmt.Errorf("jailed: cd outside sandbox root is not allowed (ask the user to run /unjail if this is intended)")
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
