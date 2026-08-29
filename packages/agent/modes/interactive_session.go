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
	"time"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// openLogoutDialog shows the provider picker for `/logout` with no
// argument. Only providers the user is currently logged into are
// listed, plus an "all" entry when more than one is present. If
// nothing's logged in, writes a status line instead of opening an
// empty dialog.
// slashMigrate points at the CLI, which is the only front end the legacy
// data-directory migration has. // rename:keep
//
// It was openMigrateDialog, and the name was the last thing still claiming a
// dialog existed: the interactive migrator belonged to the direct driver, and
// no front end has carried it since that driver was removed. dialogs.
// MigrateDialog and its MigrationHooks surface — 340 lines plus a 136-line test
// — stayed in the tree for that whole time with nothing able to reach them, and
// migrate.go went on describing them as "the TUI front-end". They are gone.
//
// The command stays, and now says what to run. Deleting the Spec too would have
// been tidier, but a user who still has a pre-rename data directory and reaches
// for /migrate is exactly who this is for, and "unknown command" tells them
// nothing.
func (i *Interactive) slashMigrate() {
	i.mu.Lock()
	i.statusErr = i18n.T("/migrate is a command-line step: run `terva migrate` from a shell")
	i.mu.Unlock()
	i.invalidate()
}

// openBtwDialog lives in interactive_btw.go — it opens the side-chat overlay
// against a snapshot frozen by the daemon's sidechat surface.

// openSkillsDialog opens the skill inspector. Opening it reloads: the
// picker reflects edits/adds made during the session, AND the live skill
// tool's catalog is refreshed so a newly-added skill is loadable by name.
// Neither touches the system prompt, so the prompt cache survives.
func (i *Interactive) openSkillsDialog() {
	i.skillsDialog.Open(skills.VisibleSkills(i.currentSkills()))
	i.invalidate()
}

// currentSkills returns the session's ACTIVE catalog — built-ins included,
// each winner carrying what it shadowed. Callers that render a list filter it
// through skills.VisibleSkills; callers that resolve a name must not, or
// /skill could never reach a built-in.
func (i *Interactive) currentSkills() []*skills.Skill {
	switch {
	case i.cfg.ReloadSkills != nil:
		return i.cfg.ReloadSkills()
	case i.cfg.SkillSnapshot != nil:
		return i.cfg.SkillSnapshot()
	}
	return nil
}

// SkillReload is what a /reload-skills did, as the TUI needs to report it. The
// daemon-side original is workspace.SkillReloadStats; this is the front end's
// flattened copy, so modes stays independent of the workspace package (the same
// arrangement acp.ReloadStats has with the extension reload).
type SkillReload struct {
	Available     int      // skills listed, matching the /skills picker's count
	Added         []string // qualified names that appeared
	Removed       []string // qualified names that went away
	PromptRebuilt bool     // the manifest changed, so the next turn starts uncached
}

// runReloadSkills backs /reload-skills: re-scan the SKILL.md ladder, swap the
// live catalog, and rebuild the prompt only when the manifest actually changed.
//
// It reports the rebuild explicitly. A dropped prompt cache is a real cost the
// user just paid, and the reason the same act is silent on a body edit but
// noisy on a new skill is otherwise invisible.
func (i *Interactive) runReloadSkills() {
	if i.cfg.ReloadSkillsAndPrompt == nil {
		i.setStatusErr(i18n.T("skills are not available in this session"))
		return
	}
	st := i.cfg.ReloadSkillsAndPrompt()

	msg := i18n.T("skills reloaded — %d available", st.Available)
	if len(st.Added) > 0 {
		msg += i18n.T(" · added %s", strings.Join(st.Added, ", "))
	}
	if len(st.Removed) > 0 {
		msg += i18n.T(" · removed %s", strings.Join(st.Removed, ", "))
	}
	if st.PromptRebuilt {
		msg += i18n.T(" · prompt rebuilt, next turn starts uncached")
	}
	i.setStatusOK(msg)
	i.invalidate()
}

// reloadSkillsDialog re-discovers skills (refreshing the live skill tool's
// catalog) and re-seeds the open picker, without rebuilding the system
// prompt. Bound to `r` in the skills dialog.
func (i *Interactive) reloadSkillsDialog() {
	if i.cfg.ReloadSkills == nil {
		return
	}
	vis := skills.VisibleSkills(i.cfg.ReloadSkills())
	i.skillsDialog.Open(vis)
	i.setStatusOK(fmt.Sprintf("skills reloaded — %d available", len(vis)))
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
		i.setStatusErr(i18n.T("usage: /skill <name> [request]  —  /skills to list"))
		return false
	}
	name := parts[1]

	// Resolve accepts the bare name and the namespace-qualified spelling
	// ("claude:handoff"), case-insensitively, over the ACTIVE catalog —
	// built-ins included, since a bare name now commonly resolves to one.
	list := i.currentSkills()
	match := skills.Resolve(list, name)
	if match == nil {
		i.setStatusErr(i18n.T("no skill named %q — /skills to list", name))
		return false
	}

	// Ref, not Name: the directive has to name the skill the way the `skill`
	// tool will resolve it back, and for a shadowed skill the bare name
	// belongs to somebody else.
	i.ed.SetValue(skillDirective(match.Ref(), strings.TrimSpace(strings.Join(parts[2:], " "))))
	i.invalidate()
	return false
}

// skillDirective builds the editor preamble that points the model at a skill.
// A trailing task is appended inline; otherwise the cursor is left after the
// colon for the user to type their request.
func skillDirective(name, task string) string {
	return i18n.P("skill.directive", "Use the %q skill for: %s", name, task)
}

// openJumpDialog builds a /jump picker from the current transcript.
// If the user typed "/jump foo" with a filter and it matches exactly
// one turn, jump there directly without showing the dialog.
func (i *Interactive) openJumpDialog(args []string) {
	if i.view == nil || len(i.view.Messages) == 0 {
		i.mu.Lock()
		i.statusErr = i18n.T("nothing to jump to \u2014 the session is empty")
		i.mu.Unlock()
		return
	}
	filter := strings.TrimSpace(strings.Join(args, " "))
	// view.Messages, so JumpScroll: these indices resolve through
	// view.BuildWithAnchors, which anchors the same slice the chat paints.
	i.jumpDialog.Open(i.view.Messages, filter, dialogs.JumpScroll)
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
		i.statusErr = i18n.T("could not resolve jump target")
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
		i.statusErr = i18n.T("starting a new session is not wired in this build")
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
	// The callback reset the session's transcript and cost; mirror that in
	// the view and the status-bar meters. The pump copy is the source of
	// truth — the fresh session's snapshot refills it — and buildChat reads
	// it again on the next frame anyway.
	i.view.Messages = i.carrierTranscriptLocked()
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.cumUsage = provider.Usage{}
	i.costBase = 0
	i.costBaseAt = time.Now()
	i.editsAdded, i.editsRemoved = 0, 0
	// A suggestion made against the conversation just left must not greet the
	// new one. Erasing the composer no longer ends an offer (that is what lets a
	// user change their mind), so the places where the CONVERSATION moves have
	// to end it themselves — here, on the switch path, and on send.
	i.ed.SetGhost("")
	i.lastCtxInput = 0
	i.parkedTurn = 0
	i.parkedTotal = 0
	i.scrollOffset = 0
	i.resetNotes()
	i.helpBlock = nil
	i.statusErr = ""
	i.statusOK = i18n.T("started a new session")
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
		i.statusErr = i18n.T("session loading is not wired in this build")
		i.mu.Unlock()
		return
	}
	i.mu.Lock()
	if i.sessionLoading {
		i.statusErr = i18n.T("already resuming a session")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.sessionLoading = true
	i.statusOK = i18n.T("resuming session: %s", path)
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
		i.statusOK = i18n.T("resumed session: %s", path)
		i.statusErr = ""
		i.parkedTurn = 0
		i.parkedTotal = 0
		i.scrollOffset = 0
		// Fresh transcript swapped in: drop the auto-follow baseline so
		// the next render's follow guard doesn't read the wholesale
		// length change as a delta and jump the viewport.
		i.prevChatLen = 0
		i.prevChatCols = 0
		i.resetNotes()
		i.view.InvalidateRenderCache()
		// The transcript comes from the pump: LoadSession re-binds the
		// carrier, which drops the old messages and arms the tail cap for
		// the new binding's first snapshot. Nothing to mirror here.
		i.view.Messages = i.carrierTranscriptLocked()
		// The cost and context meters are seeded by the new binding's first
		// snapshot (seedSessionMeters); the edits tally starts over here.
		i.editsAdded, i.editsRemoved = 0, 0
		i.invalidate()
	}()
}

// openSessionsDialog wires the /sessions picker's seams from cfg and shows
// it. Shared by the /sessions slash handler and the --resume boot open
// (OpenSessionsOnBoot): re-wiring Rename/List on every open keeps the dialog
// pointed at the current carrier closures rather than a stale capture.
func (i *Interactive) openSessionsDialog() {
	i.sessionDialog.Rename = i.cfg.RenameSessionFile
	i.sessionDialog.List = i.cfg.ListSessions
	i.sessionDialog.ListArchived = i.cfg.ListArchivedSessions
	i.sessionDialog.Open(i.cfg.TervaHome, i.cfg.CWD)
}

// generateSessionTitle runs the picker's `g` binding: on-demand title
// generation via the host seam, off the main loop (it blocks on a model
// call). One at a time — a second `g` while one runs is refused, not queued.
// The result lands back on the main loop: the dialog row updates in place
// (SetTitleFor no-ops if the dialog moved on) and the status line reports.
func (i *Interactive) generateSessionTitle(path string) {
	gen := i.cfg.GenerateSessionTitle
	if gen == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("title generation isn't available here")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if i.titleGenBusy {
		return
	}
	i.titleGenBusy = true
	i.mu.Lock()
	i.statusOK = i18n.T("generating title…")
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
	go func() {
		title, err := gen(path)
		i.runOnMain(func() {
			i.titleGenBusy = false
			i.mu.Lock()
			defer i.mu.Unlock()
			if err != nil {
				i.statusErr = err.Error()
				i.statusOK = ""
				return
			}
			i.sessionDialog.SetTitleFor(path, title)
			i.statusOK = i18n.T("titled: %s", title)
		})
	}()
}

// openSessionOpsDialog shows the picker for `/session` with no arg.
// Always offers export, import, fork, tree; the handlers bail with
// a clear status message when the precondition isn't met (empty
// transcript on fork; no parent/siblings on tree).
func (i *Interactive) openSessionOpsDialog() {
	items := []dialogs.SessionOpsItem{
		{Label: "export", Action: "export", Hint: "write the current session to a .tervasession file"},
		{Label: "import", Action: "import", Hint: "load a .tervasession file into this directory"},
		{Label: "fork", Action: "fork", Hint: "branch from a past user message into a new session"},
		{Label: "tree", Action: "tree", Hint: "switch between branches in this directory"},
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
		i.statusErr = i18n.T("unknown /session action: %s (use export, import, fork, or tree)", action)
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
		i.statusErr = i18n.T("export: no session is active (running with --no-session?)")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	src := i.cfg.CurrentSessionPath()
	if src == "" {
		i.mu.Lock()
		i.statusErr = i18n.T("export: no session is active (running with --no-session?)")
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
		i.statusErr = i18n.T("export: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = i18n.T("exported session to %s", friendlyPath(out))
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
		i.statusErr = i18n.T("import: pass a path — e.g. /session import ~/Downloads/work.tervasession")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	src = expandTilde(src)
	if _, err := os.Stat(src); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("import: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	newPath, err := core.ImportSession(src, i.cfg.TervaHome, i.cfg.CWD, i.cfg.Version)
	if err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("import: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if i.cfg.LoadSession == nil {
		i.mu.Lock()
		i.statusOK = i18n.T("imported session at %s (run /sessions to resume it)", friendlyPath(newPath))
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if err := i.cfg.LoadSession(newPath); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("import: load failed: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = i18n.T("imported and switched to session %s", friendlyPath(newPath))
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
		i.statusErr = i18n.T("fork: no session is active (running with --no-session?)")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	msgs := i.carrierTranscript()
	if len(msgs) == 0 {
		i.mu.Lock()
		i.statusErr = i18n.T("fork: transcript is empty; nothing to fork from")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	// The effective transcript, so JumpFork: these indices are BranchSession's
	// cut points, which it counts over the same effective stream.
	i.jumpDialog.Open(msgs, "", dialogs.JumpFork)
	i.invalidate()
}

// doSessionTree shows the branch topology picker for this cwd.
// Pick an entry to switch into it (same semantics as /sessions
// picking a past session, but with the parent/child indentation).
func (i *Interactive) doSessionTree() {
	if i.cfg.TervaHome == "" || i.cfg.CWD == "" {
		i.mu.Lock()
		i.statusErr = i18n.T("tree: session storage not configured")
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
		i.statusErr = i18n.T("tree: no sessions in this directory yet")
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
		i.statusErr = i18n.T("tree: no branches to show")
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
		i.statusErr = i18n.T("tree: session swap not available in this build")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if err := i.cfg.LoadSession(path); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("tree: load failed: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = i18n.T("switched to branch %s", friendlyPath(path))
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// applyForkSelection branches the current session at msgIdx+1 (so
// the selected user message and everything before it is included
// in the new branch), then switches the running agent to the new
// file. Called from the jump-dialog handler on a JumpFork selection —
// msgIdx indexes the effective transcript the picker was opened with.
func (i *Interactive) applyForkSelection(msgIdx int) {
	if i.cfg.CurrentSessionPath == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("fork: no session is active")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	src := i.cfg.CurrentSessionPath()
	if src == "" {
		i.mu.Lock()
		i.statusErr = i18n.T("fork: no session is active")
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
		i.statusErr = i18n.T("fork: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if i.cfg.LoadSession == nil {
		i.mu.Lock()
		i.statusOK = i18n.T("forked at message %s (run /sessions to resume)", formatInt(upTo))
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if err := i.cfg.LoadSession(newPath); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("fork: switch failed: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = i18n.T("forked and switched to new branch at %s", friendlyPath(newPath))
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// The session lifecycle verbs, driven from the /sessions picker.
//
// Archive and delete are deliberately adjacent and deliberately different: one
// moves a transcript into a compressed directory nothing lists, the other
// destroys it. The picker confirms only the destructive one — asking twice for
// a reversible act trains people to answer yes without reading.

// archiveSessionAt archives the session at path and re-lists the picker so the
// row leaves under the user's eyes.
func (i *Interactive) archiveSessionAt(path string) {
	if i.cfg.ArchiveSession == nil {
		i.setStatusErr(i18n.T("archiving isn't available here"))
		return
	}
	if err := i.cfg.ArchiveSession(path); err != nil {
		i.setStatusErr(i18n.T("archive: %s", err.Error()))
		return
	}
	i.sessionDialog.Refresh(i.cfg.TervaHome, i.cfg.CWD)
	i.mu.Lock()
	i.statusErr = ""
	i.statusOK = i18n.T("archived — press A in /sessions to browse or restore it")
	i.mu.Unlock()
	i.invalidate()
}

// deleteSessionAt destroys the session at path. The picker has already
// confirmed; this is the act itself.
func (i *Interactive) deleteSessionAt(path string) {
	if i.cfg.DeleteSession == nil {
		i.setStatusErr(i18n.T("deleting isn't available here"))
		return
	}
	if err := i.cfg.DeleteSession(path); err != nil {
		i.setStatusErr(i18n.T("delete: %s", err.Error()))
		return
	}
	i.sessionDialog.Refresh(i.cfg.TervaHome, i.cfg.CWD)
	i.mu.Lock()
	i.statusErr = ""
	i.statusOK = i18n.T("deleted")
	i.mu.Unlock()
	i.invalidate()
}

// restoreArchivedSession brings an archived transcript back and drops the picker
// onto the live list, where the restored session now is — leaving the user in
// the archive staring at a row that just left it would be the wrong place.
func (i *Interactive) restoreArchivedSession(id string) {
	if i.cfg.RestoreArchivedSession == nil {
		i.setStatusErr(i18n.T("restoring isn't available here"))
		return
	}
	if err := i.cfg.RestoreArchivedSession(id); err != nil {
		i.setStatusErr(i18n.T("restore: %s", err.Error()))
		return
	}
	i.sessionDialog.ShowArchived(false)
	i.sessionDialog.Refresh(i.cfg.TervaHome, i.cfg.CWD)
	i.mu.Lock()
	i.statusErr = ""
	i.statusOK = i18n.T("restored — it is back in the session list")
	i.mu.Unlock()
	i.invalidate()
}
