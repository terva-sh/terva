package modes

// Session lifecycle and transcript navigation: /new, /sessions,
// /session export-import-fork-tree, /jump, fork selection, and the
// /migrate entry point.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// openLogoutDialog shows the provider picker for `/logout` with no
// argument. Only providers the user is currently logged into are
// listed, plus an "all" entry when more than one is present. If
// nothing's logged in, writes a status line instead of opening an
// empty dialog.
// openMigrateDialog plans a zot→terva migration and opens the staged // rename:keep
// /migrate dialog. Project-only runs (no user dir to copy) finalize
// the no-fallback marker right away — same rule as the CLI path.
func (i *Interactive) openMigrateDialog() {
	if i.cfg.Migration == nil || i.cfg.Migration.Plan == nil {
		i.mu.Lock()
		i.statusErr = "/migrate unavailable: host did not wire Migration"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	st := i.cfg.Migration.Plan()
	if st.NothingToDo {
		i.mu.Lock()
		i.statusOK = "nothing to migrate — already on the terva data dir"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if !st.UserDirApplicable && !st.AlreadyMigrated && i.cfg.Migration.Finalize != nil {
		if err := i.cfg.Migration.Finalize(); err != nil {
			i.mu.Lock()
			i.statusErr = "write the no-fallback marker: " + err.Error()
			i.mu.Unlock()
			i.invalidate()
			return
		}
	}
	i.migrateDialog.Open(st)
	i.invalidate()
}

// openBtwDialog opens the side-chat overlay with a frozen snapshot
// of the current main session. The optional argument is auto-
// submitted as the first question, so '/btw does X work?' fires the
// model call immediately instead of just opening an empty dialog.
func (i *Interactive) openBtwDialog(args []string) {
	ag := i.turns.Agent()
	if ag == nil {
		i.setStatusErr("not logged in. type /login first.")
		return
	}
	seed := strings.TrimSpace(strings.Join(args, " "))
	i.btwDialog.Open(i.cfg.Theme, ag, ag.System, i.cfg.Model, i.cfg.CWD, seed, i.invalidate)
	i.invalidate()
}

// openSkillsDialog opens the skill inspector. Opening it reloads: the
// picker reflects edits/adds made during the session, AND the live skill
// tool's catalog is refreshed so a newly-added skill is loadable by name.
// Neither touches the system prompt, so the prompt cache survives.
func (i *Interactive) openSkillsDialog() {
	var list []*skills.Skill
	switch {
	case i.cfg.ReloadSkills != nil:
		list = i.cfg.ReloadSkills()
	case i.cfg.SkillSnapshot != nil:
		list = i.cfg.SkillSnapshot()
	}
	i.skillsDialog.Open(list)
	i.invalidate()
}

// reloadSkillsDialog re-discovers skills (refreshing the live skill tool's
// catalog) and re-seeds the open picker, without rebuilding the system
// prompt. Bound to `r` in the skills dialog.
func (i *Interactive) reloadSkillsDialog() {
	if i.cfg.ReloadSkills == nil {
		return
	}
	list := i.cfg.ReloadSkills()
	i.skillsDialog.Open(list)
	i.setStatusOK(fmt.Sprintf("skills reloaded — %d available", len(list)))
	i.invalidate()
}

// slashSkill (/skill <name> [request]) primes the editor with a directive to
// use a specific skill, so the user completes and submits it. It refreshes
// the catalog first (cache-safe — see ReloadSkills) so a skill written this
// session resolves, validates the name, then fills the editor. The model
// loads the named skill via the `skill` tool when the request is sent.
//
// It deliberately PRIMES rather than submits: filling the editor never
// rebuilds the system prompt nor starts a turn from inside slash dispatch,
// and lets the user add/adjust the request before it runs.
func (i *Interactive) slashSkill(_ context.Context, parts []string, _ string) bool {
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		i.setStatusErr("usage: /skill <name> [request]  —  /skills to list")
		return false
	}
	name := parts[1]

	var list []*skills.Skill
	switch {
	case i.cfg.ReloadSkills != nil:
		list = i.cfg.ReloadSkills()
	case i.cfg.SkillSnapshot != nil:
		list = i.cfg.SkillSnapshot()
	}
	// Case-insensitive for typing convenience; echo the canonical name.
	var match *skills.Skill
	for _, s := range list {
		if strings.EqualFold(s.Name, name) {
			match = s
			break
		}
	}
	if match == nil {
		i.setStatusErr(fmt.Sprintf("no skill named %q — /skills to list", name))
		return false
	}

	i.ed.SetValue(skillDirective(match.Name, strings.TrimSpace(strings.Join(parts[2:], " "))))
	i.invalidate()
	return false
}

// skillDirective builds the editor preamble that points the model at a skill.
// A trailing task is appended inline; otherwise the cursor is left after the
// colon for the user to type their request.
func skillDirective(name, task string) string {
	d := fmt.Sprintf("Use the %q skill for: ", name)
	return d + task
}

// openJumpDialog builds a /jump picker from the current transcript.
// If the user typed "/jump foo" with a filter and it matches exactly
// one turn, jump there directly without showing the dialog.
func (i *Interactive) openJumpDialog(args []string) {
	if i.view == nil || len(i.view.Messages) == 0 {
		i.mu.Lock()
		i.statusErr = "nothing to jump to \u2014 the session is empty"
		i.mu.Unlock()
		return
	}
	filter := strings.TrimSpace(strings.Join(args, " "))
	i.jumpDialog.Open(i.view.Messages, filter)
	// Shortcut: with a filter argument that matches exactly one turn,
	// jump immediately and skip the picker.
	if filter != "" {
		if tgts := i.jumpDialog.Targets(); len(tgts) == 1 {
			t := tgts[0]
			i.jumpDialog.Close()
			i.applyJumpSelection(t.MessageIdx, t.TurnNo)
		}
	}
}

// applyJumpSelection scrolls the chat viewport so the user message at
// msgIdx is visible at (or near) the top of the chat area. Uses the
// anchor slice returned by view.BuildWithAnchors so the mapping from
// message index to row is exact, regardless of variable-height tool
// blocks above the target.
func (i *Interactive) applyJumpSelection(msgIdx, turnNo int) {
	cols := i.lastCols()
	chat, anchors := i.view.BuildWithAnchors(cols)
	var row int
	found := false
	for _, a := range anchors {
		if a.MessageIdx == msgIdx {
			row = a.Row
			found = true
			break
		}
	}
	if !found {
		i.mu.Lock()
		i.statusErr = "could not resolve jump target"
		i.mu.Unlock()
		return
	}

	chatLen := len(chat)
	page := i.chatPage()
	if page < 1 {
		page = 1
	}
	// scrollOffset is measured from the bottom of the chat slice, so
	// to place `row` at the top of the viewport we want:
	//     chatLen - scrollOffset - page == row
	// Solve for scrollOffset and clamp to [0, chatLen-page].
	offset := chatLen - (row + page)
	if offset < 0 {
		offset = 0
	}
	maxOffset := chatLen - page
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}

	i.mu.Lock()
	i.scrollOffset = offset
	i.parkedTurn = turnNo
	i.parkedTotal = totalTurnsLocked(i.view.Messages)
	i.statusOK = fmt.Sprintf("jumped to turn %d", turnNo)
	i.statusErr = ""
	i.mu.Unlock()
}

// totalTurnsLocked counts user messages in the transcript. Caller is
// assumed to hold i.mu (the name is a mild reminder; this function
// itself doesn't touch shared state beyond the slice it's handed).
func totalTurnsLocked(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			n++
		}
	}
	return n
}

// startNewSession closes the current session and starts a fresh one in
// the same directory via the cli-provided callback, then resets the TUI
// to an empty transcript. The previous session stays on disk (resume it
// later with /sessions); this is the in-place equivalent of quitting and
// relaunching terva. Dispatched by /new, which is a turn-cancelling command
// so the agent is idle by the time we get here.
func (i *Interactive) startNewSession() {
	if i.cfg.NewSession == nil {
		i.mu.Lock()
		i.statusErr = "starting a new session is not wired in this build"
		i.statusOK = ""
		i.mu.Unlock()
		return
	}
	if err := i.cfg.NewSession(i.cfg.Provider, i.cfg.Model); err != nil {
		i.mu.Lock()
		i.statusErr = err.Error()
		i.statusOK = ""
		i.mu.Unlock()
		return
	}
	i.mu.Lock()
	// The callback reset the agent's transcript and cost; mirror that in
	// the view and the status-bar meters.
	if ag := i.turns.Agent(); ag != nil {
		i.view.Messages = ag.Messages()
	}
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.cumUsage = provider.Usage{}
	i.lastCtxInput = 0
	i.parkedTurn = 0
	i.parkedTotal = 0
	i.scrollOffset = 0
	i.extNotes = nil
	i.helpBlock = nil
	i.statusErr = ""
	i.statusOK = "started a new session"
	i.view.InvalidateRenderCache()
	// Wipe the screen + scrollback so the previous session doesn't linger
	// above the fresh one. terva renders in main-screen flow mode, so prior
	// frames stay in the terminal's native scrollback; without this, /new
	// looks like nothing happened ("did I start a new session?"). Clear() is
	// VS Code-safe (the renderer's keepScrollback guard), and mirrors the
	// theme/reasoning settings handlers.
	if i.rend != nil {
		i.rend.Clear()
	}
	i.mu.Unlock()
	i.invalidate()
}

// applySessionSelection loads the given session via the cli-provided
// callback and snaps the viewport to the bottom (the latest message)
// so the user lands at the live tail of the resumed conversation.
func (i *Interactive) applySessionSelection(path string) {
	if i.cfg.LoadSession == nil {
		i.mu.Lock()
		i.statusErr = "session loading is not wired in this build"
		i.mu.Unlock()
		return
	}
	i.mu.Lock()
	if i.sessionLoading {
		i.statusErr = "already resuming a session"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.sessionLoading = true
	i.statusOK = "resuming session: " + path
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()

	go func() {
		err := i.cfg.LoadSession(path)
		i.mu.Lock()
		defer i.mu.Unlock()
		i.sessionLoading = false
		if err != nil {
			i.statusErr = err.Error()
			i.statusOK = ""
			i.invalidate()
			return
		}
		i.statusOK = "resumed session: " + path
		i.statusErr = ""
		i.parkedTurn = 0
		i.parkedTotal = 0
		i.scrollOffset = 0
		// Fresh transcript swapped in: drop the auto-follow baseline so
		// the next render's follow guard doesn't read the wholesale
		// length change as a delta and jump the viewport.
		i.prevChatLen = 0
		i.prevChatCols = 0
		i.extNotes = nil
		i.view.InvalidateRenderCache()
		if ag := i.turns.Agent(); ag != nil {
			i.view.Messages = ag.Messages()
			i.cumUsage = ag.Cost()
			if last := ag.LastTurnUsage(); last.InputTokens > 0 || last.CacheReadTokens > 0 || last.CacheWriteTokens > 0 {
				i.lastCtxInput = last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
			} else {
				i.lastCtxInput = 0
			}
			// Snap to the tail again — the swap brought in a fresh
			// transcript whose markdown / chroma cost we don't want
			// blocking the redraw.
			if len(i.view.Messages) > initialResumeTailLimit {
				i.view.TailLimit = initialResumeTailLimit
			} else {
				i.view.TailLimit = 0
			}
		}
		i.invalidate()
	}()
}

// openSessionOpsDialog shows the picker for `/session` with no arg.
// Always offers export, import, fork, tree; the handlers bail with
// a clear status message when the precondition isn't met (empty
// transcript on fork; no parent/siblings on tree).
func (i *Interactive) openSessionOpsDialog() {
	items := []sessionOpsItem{
		{label: "export", action: "export", hint: "write the current session to a .tervasession file"},
		{label: "import", action: "import", hint: "load a .tervasession file into this directory"},
		{label: "fork", action: "fork", hint: "branch from a past user message into a new session"},
		{label: "tree", action: "tree", hint: "switch between branches in this directory"},
	}
	i.sessionOpsDialog.Open(items)
	i.invalidate()
}

// doSessionOp dispatches export, import, fork, or tree. arg is the
// optional positional argument from e.g. /session export <path>
// or /session import <path>; fork and tree ignore it.
func (i *Interactive) doSessionOp(action, arg string) {
	switch action {
	case "export":
		i.doSessionExport(arg)
	case "import":
		i.doSessionImport(arg)
	case "fork":
		i.doSessionFork()
	case "tree":
		i.doSessionTree()
	default:
		i.mu.Lock()
		i.statusErr = "unknown /session action: " + action + " (use export, import, fork, or tree)"
		i.mu.Unlock()
		i.invalidate()
	}
}

// doSessionExport writes the live session file to destination path
// dst. When dst is empty we default to ~/Downloads (falling back to
// the user's home directory if it doesn't exist). The helper
// expands a leading `~` and creates any missing parent directories.
func (i *Interactive) doSessionExport(dst string) {
	if i.cfg.CurrentSessionPath == nil {
		i.mu.Lock()
		i.statusErr = "export: no session is active (running with --no-session?)"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	src := i.cfg.CurrentSessionPath()
	if src == "" {
		i.mu.Lock()
		i.statusErr = "export: no session is active (running with --no-session?)"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	// Persist any in-memory agent messages to the session file so
	// the export carries the full conversation. Without this, the
	// default lazy-flush-at-exit strategy leaves most of a running
	// session unwritten and the export ends up with only the meta.
	if i.cfg.FlushSession != nil {
		i.cfg.FlushSession()
	}
	dst = unquotePath(dst)
	if dst == "" {
		dst = defaultExportDir()
	} else {
		dst = expandTilde(dst)
	}
	out, err := core.ExportSession(src, dst)
	if err != nil {
		i.mu.Lock()
		i.statusErr = "export: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = "exported session to " + friendlyPath(out)
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// doSessionImport copies the .tervasession file at src into the
// running cwd's sessions directory and loads it as the active
// session, same as `/sessions` -> pick. When src is empty we ask
// the user to pass a path (no usable default here).
func (i *Interactive) doSessionImport(src string) {
	src = unquotePath(src)
	if src == "" {
		i.mu.Lock()
		i.statusErr = "import: pass a path — e.g. /session import ~/Downloads/work.tervasession"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	src = expandTilde(src)
	if _, err := os.Stat(src); err != nil {
		i.mu.Lock()
		i.statusErr = "import: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	newPath, err := core.ImportSession(src, i.cfg.TervaHome, i.cfg.CWD, i.cfg.Version)
	if err != nil {
		i.mu.Lock()
		i.statusErr = "import: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if i.cfg.LoadSession == nil {
		i.mu.Lock()
		i.statusOK = "imported session at " + friendlyPath(newPath) + " (run /sessions to resume it)"
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if err := i.cfg.LoadSession(newPath); err != nil {
		i.mu.Lock()
		i.statusErr = "import: load failed: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = "imported and switched to session " + friendlyPath(newPath)
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// defaultExportDir returns ~/Downloads when it exists, or ~ as a
// fallback, or /tmp on exotic machines with no home dir.
func defaultExportDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return os.TempDir()
	}
	downloads := filepath.Join(home, "Downloads")
	if fi, err := os.Stat(downloads); err == nil && fi.IsDir() {
		return downloads
	}
	return home
}

// expandTilde turns a leading ~ into the user's home directory.
// Returns the input unchanged when there's no tilde or no home.
func expandTilde(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if len(p) == 1 {
		return home
	}
	if p[1] == '/' || p[1] == filepath.Separator {
		return filepath.Join(home, p[2:])
	}
	return p
}

// unquotePath strips a matching pair of surrounding single or
// double quotes. Drag-drop paste in the tui auto-quotes dropped
// file paths so the shell-like `/session import 'foo bar.zs'`
// stays well-formed; when the TUI's own slash handler consumes
// the arg, we want the raw path back.
func unquotePath(p string) string {
	p = strings.TrimSpace(p)
	if len(p) >= 2 {
		first := p[0]
		last := p[len(p)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			p = p[1 : len(p)-1]
		}
	}
	return p
}

// friendlyPath collapses the user's home directory to a leading ~
// so status messages read cleanly. Falls back to the raw path when
// the home dir is unknown.
func friendlyPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// doSessionFork opens the /jump turn picker in "fork mode". The
// next selection branches the current session at that user turn
// instead of scrolling the viewport to it.
func (i *Interactive) doSessionFork() {
	if i.cfg.CurrentSessionPath == nil || i.cfg.CurrentSessionPath() == "" {
		i.mu.Lock()
		i.statusErr = "fork: no session is active (running with --no-session?)"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	msgs := []provider.Message{}
	if ag := i.turns.Agent(); ag != nil {
		msgs = ag.Messages()
	}
	if len(msgs) == 0 {
		i.mu.Lock()
		i.statusErr = "fork: transcript is empty; nothing to fork from"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.pendingFork = true
	i.jumpDialog.Open(msgs, "")
	i.invalidate()
}

// doSessionTree shows the branch topology picker for this cwd.
// Pick an entry to switch into it (same semantics as /sessions
// picking a past session, but with the parent/child indentation).
func (i *Interactive) doSessionTree() {
	if i.cfg.TervaHome == "" || i.cfg.CWD == "" {
		i.mu.Lock()
		i.statusErr = "tree: session storage not configured"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	// Flush the running agent's transcript first so its message
	// count + latest preview are accurate in the tree.
	if i.cfg.FlushSession != nil {
		i.cfg.FlushSession()
	}
	roots := core.BuildSessionTree(i.cfg.TervaHome, i.cfg.CWD)
	if len(roots) == 0 {
		i.mu.Lock()
		i.statusErr = "tree: no sessions in this directory yet"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	current := ""
	if i.cfg.CurrentSessionPath != nil {
		current = i.cfg.CurrentSessionPath()
	}
	if !i.sessionTreeDialog.Open(roots, current) {
		i.mu.Lock()
		i.statusErr = "tree: no branches to show"
		i.mu.Unlock()
	}
	i.invalidate()
}

// applySessionTreeSelection switches the running agent to the
// session file at path. Thin wrapper around LoadSession that also
// writes a status line.
func (i *Interactive) applySessionTreeSelection(path string) {
	if i.cfg.LoadSession == nil {
		i.mu.Lock()
		i.statusErr = "tree: session swap not available in this build"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if err := i.cfg.LoadSession(path); err != nil {
		i.mu.Lock()
		i.statusErr = "tree: load failed: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = "switched to branch " + friendlyPath(path)
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// applyForkSelection branches the current session at msgIdx+1 (so
// the selected user message and everything before it is included
// in the new branch), then switches the running agent to the new
// file. Called from the jump-dialog handler when pendingFork=true.
func (i *Interactive) applyForkSelection(msgIdx int) {
	i.pendingFork = false
	if i.cfg.CurrentSessionPath == nil {
		i.mu.Lock()
		i.statusErr = "fork: no session is active"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	src := i.cfg.CurrentSessionPath()
	if src == "" {
		i.mu.Lock()
		i.statusErr = "fork: no session is active"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if i.cfg.FlushSession != nil {
		i.cfg.FlushSession()
	}
	// msgIdx is 0-indexed message position; copy msgIdx+1 rows so
	// the selected user message is included.
	upTo := msgIdx + 1
	newPath, err := core.BranchSession(src, i.cfg.TervaHome, i.cfg.CWD, i.cfg.Version, upTo)
	if err != nil {
		i.mu.Lock()
		i.statusErr = "fork: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if i.cfg.LoadSession == nil {
		i.mu.Lock()
		i.statusOK = "forked at message " + formatInt(upTo) + " (run /sessions to resume)"
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if err := i.cfg.LoadSession(newPath); err != nil {
		i.mu.Lock()
		i.statusErr = "fork: switch failed: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = "forked and switched to new branch at " + friendlyPath(newPath)
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}
