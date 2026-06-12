package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// updateAllExtensions iterates $TERVA_HOME/extensions/* and tries to
// update each one in place via `git pull --ff-only`. Failures are
// per-extension and never abort the loop; the caller can treat any
// error here as advisory only.
//
// Update strategy per extension:
//
//  1. Skip if disabled in its extension.json (the user opted out).
//  2. Skip if the directory has no .git/ (not a git checkout — we
//     have no way to fetch new content).
//  3. Stash any dirty working-tree state (including untracked
//     files like a runtime todos.json) so the pull doesn't refuse
//     with "your local changes would be overwritten".
//  4. git pull --ff-only with a per-extension timeout. Refuse to
//     merge or rebase; if upstream diverged, log "diverged" and
//     leave the user to sort it out.
//  5. Pop the stash. If pop produces conflicts, leave the
//     conflict markers in place, log it, and move on (better than
//     silently discarding the user's runtime files).
//
// We deliberately do NOT run any build step (go build / npm install /
// make) after the pull. Auto-executing arbitrary build scripts from a
// remote git URL would be a real footgun. Extension authors are
// expected to commit a working binary (or instruct the user to
// rebuild manually via /reload-ext + their own build).
func updateAllExtensions(tervaHome string) {
	dir := filepath.Join(tervaHome, "extensions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return // no extensions installed; nothing to do
		}
		fmt.Fprintf(os.Stderr, "terva update: skipping extension update (read %s: %v)\n", dir, err)
		return
	}

	// Stable order so output is reproducible.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	if len(names) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("terva update: updating extensions...")

	var updated, upToDate, skipped, failed int
	for _, name := range names {
		extDir := filepath.Join(dir, name)
		status := updateOneExtension(extDir, name)
		switch status {
		case "updated":
			updated++
		case "up-to-date":
			upToDate++
		case "skipped":
			skipped++
		default:
			failed++
		}
	}

	fmt.Printf("terva update: extensions: %d updated, %d up-to-date, %d skipped, %d failed\n",
		updated, upToDate, skipped, failed)
}

// updateOneExtension updates the extension in extDir and returns a
// short status string used for the summary line: "updated",
// "up-to-date", "skipped", or "failed". The exact string is also what
// gets printed to the user, prefixed with the extension name.
func updateOneExtension(extDir, name string) string {
	// 1. Read manifest to honour `enabled: false`.
	mPath := filepath.Join(extDir, "extension.json")
	mBytes, err := os.ReadFile(mPath)
	if err != nil {
		fmt.Printf("  %-30s  skipped: no extension.json\n", name)
		return "skipped"
	}
	var manifest struct {
		Enabled *bool `json:"enabled,omitempty"`
	}
	_ = json.Unmarshal(mBytes, &manifest) // bad JSON -> treat as enabled
	if manifest.Enabled != nil && !*manifest.Enabled {
		fmt.Printf("  %-30s  skipped: disabled\n", name)
		return "skipped"
	}

	// 2. Must be a git checkout.
	gitDir := filepath.Join(extDir, ".git")
	if st, err := os.Stat(gitDir); err != nil || (!st.IsDir() && !st.Mode().IsRegular()) {
		// .git can be a directory (normal clone) or a file (worktree).
		fmt.Printf("  %-30s  skipped: not a git checkout\n", name)
		return "skipped"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 3. Stash anything dirty (including untracked files like
	// extension runtime state). Use --include-untracked. Empty
	// worktree -> "No local changes to save" and the stash list
	// doesn't grow.
	stashRef, stashed, err := gitStash(ctx, extDir)
	if err != nil {
		fmt.Printf("  %-30s  failed: stash: %v\n", name, err)
		return "failed"
	}

	// 4. Try a fast-forward pull. We avoid plain `git pull` because
	// it can rebase or merge depending on user config.
	out, err := runGit(ctx, extDir, "pull", "--ff-only", "--quiet")
	if err != nil {
		// If we stashed, try to restore so we leave the worktree
		// in at least the state we found it.
		if stashed {
			_ = gitStashPop(ctx, extDir, stashRef)
		}
		fmt.Printf("  %-30s  failed: %s\n", name, summariseGitError(out, err))
		return "failed"
	}

	// Detect whether the pull actually changed HEAD. `git pull --quiet`
	// prints nothing on either outcome, so we ask git directly.
	changed, _ := pullChangedHead(ctx, extDir)

	// 5. Pop the stash. If conflict, leave conflict markers and warn.
	if stashed {
		if err := gitStashPop(ctx, extDir, stashRef); err != nil {
			fmt.Printf("  %-30s  warning: pull ok but stash pop had conflicts; resolve in %s\n", name, extDir)
			// Treat as updated rather than failed: the source code
			// did move, the user just has to clean up runtime state.
			if changed {
				return "updated"
			}
			return "up-to-date"
		}
	}

	if changed {
		fmt.Printf("  %-30s  updated\n", name)
		return "updated"
	}
	fmt.Printf("  %-30s  up-to-date\n", name)
	return "up-to-date"
}

// gitStash creates a stash including untracked files. Returns:
//
//	stashRef = "stash@{0}" when something was actually stashed, "" otherwise.
//	stashed  = true if a stash entry was created.
//
// We capture stashRef explicitly (instead of always popping "stash@{0}")
// so a stash created by something else before/after us doesn't get
// accidentally popped.
func gitStash(ctx context.Context, dir string) (stashRef string, stashed bool, err error) {
	// Tag the stash with a recognisable name so the user can find it
	// later if pop fails and they want to inspect it.
	msg := fmt.Sprintf("terva-update-%d", time.Now().Unix())
	out, err := runGit(ctx, dir, "stash", "push", "--include-untracked", "-m", msg)
	if err != nil {
		return "", false, fmt.Errorf("%s", strings.TrimSpace(out))
	}
	// Git prints "No local changes to save" when there is nothing to
	// stash. That's not an error, just nothing to pop.
	if strings.Contains(out, "No local changes to save") {
		return "", false, nil
	}
	// Resolve our specific stash by looking it up by message. The new
	// stash always lands at the top of the list when push succeeds.
	listOut, lerr := runGit(ctx, dir, "stash", "list")
	if lerr == nil {
		for _, line := range strings.Split(listOut, "\n") {
			if strings.Contains(line, msg) {
				// line looks like: "stash@{0}: On main: terva-update-1700000000"
				if idx := strings.Index(line, ":"); idx > 0 {
					return strings.TrimSpace(line[:idx]), true, nil
				}
			}
		}
	}
	// Fallback: assume top of stack. Almost always correct.
	return "stash@{0}", true, nil
}

// gitStashPop pops a specific stash ref. Returns an error if the pop
// produced conflicts; the conflict markers stay in the worktree.
func gitStashPop(ctx context.Context, dir, ref string) error {
	if ref == "" {
		return nil
	}
	out, err := runGit(ctx, dir, "stash", "pop", ref)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(out))
	}
	return nil
}

// pullChangedHead returns true if the most recent commit's hash differs
// from the previous one. We compare HEAD against HEAD@{1} (the reflog
// entry just before the pull). If there is no @{1} (fresh clone, no
// reflog), we assume "changed" = false to be conservative.
func pullChangedHead(ctx context.Context, dir string) (bool, error) {
	cur, err := runGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	prev, err := runGit(ctx, dir, "rev-parse", "HEAD@{1}")
	if err != nil {
		return false, nil // no reflog entry -> nothing previous to compare against
	}
	return strings.TrimSpace(cur) != strings.TrimSpace(prev), nil
}

// runGit runs `git <args...>` in dir and returns merged stdout+stderr.
// Refuses to allocate a tty even on terminals where git might try to
// prompt for credentials — we want every interactive prompt to fail
// fast instead of hanging the update loop.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Disable interactive credential prompts. If a user has a private
	// repo extension and no cached credentials, the pull fails fast.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// summariseGitError turns a multi-line git error into a single line
// suitable for the summary table. We keep the most informative-looking
// line and drop the rest.
func summariseGitError(output string, err error) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return err.Error()
	}
	// Prefer lines that look like an actual error message over hints.
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "hint:") || strings.HasPrefix(l, "Hint:") {
			continue
		}
		return l
	}
	return strings.TrimSpace(lines[0])
}

// (unused but kept so callers can write to a discard sink if they ever
// want to suppress git noise rather than log it.)
var _ = io.Discard
