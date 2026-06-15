//go:build terva_acp

package acp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/modes"
)

// Phase 4c: slash-command advertisement + native execution of the
// headless-safe subset.
//
// terva's slash registry (packages/agent/modes) is mostly TUI-coupled:
// many commands open pickers/dialogs an ACP editor can't drive (/model,
// /sessions, /login, ...). A growing handful, though, are pure agent- or
// session-control whose *effect* has a headless meaning, so we reproduce that
// effect here against the in-process core.Agent / session state without the
// interactive TUI plumbing.
//
// Capability honesty (§13): we advertise ONLY the commands we can actually
// honor over ACP, so the editor's command palette never offers a verb that
// would silently no-op or get forwarded to the model. Concretely:
//
//   - ADVERTISED + EXECUTED NATIVELY:
//     /clear, /compact — agent-control, drive the core.Agent directly.
//     /help — a text overview of terva + the ACP-available commands.
//     /permissions — a read-only summary of the approval mode, the policy's
//     rules, and the session-scoped grants the confirmer has cached.
//     /jail, /unjail — toggle the session's sandbox confinement to / off the
//     session cwd (the same *tools.Sandbox the TUI's /jail toggles).
//     /skills — a text list of the discovered (non-builtin) SKILL.md skills.
//     /context — a read-only summary of what the session's extensions are
//     injecting into the model (static guidance + live cards), read from the
//     host's ContextSnapshot of the live manager.
//     /reload-ext — hot-reloads the session's extensions through the host and
//     reports the reload stats; it then re-advertises the command catalog since
//     the extension command set may have changed.
//     /trust, /untrust — trust (or untrust) the session's workspace from inside
//     the editor — the only in-editor way to trust a workspace over ACP. /trust
//     persists the cwd's trust verdict (/trust parent trusts descendants too)
//     and reloads the extension manager so project extensions go live now;
//     project skills/context are baked into the prompt at build time, so they
//     apply on a NEW session. /untrust is the inverse. Both re-advertise the
//     command catalog (the reload may change the extension command set).
//   - EXCLUDED (TUI-only pickers / dialogs the ACP frontend can't drive, or
//     verbs already covered by an ACP-native mechanism): /model and
//     /sessions in particular — the editor already has the model *config
//     option* (Phase 4b) and session/list. Likewise /login, /logout,
//     /session, /jump, /btw, /swarm, /connect, /migrate, /settings, /new,
//     /study, /exit — each opens a dialog, mutates TUI-only state, or has no
//     headless meaning.
//
// Extension-registered slash commands are layered ON TOP of the built-in set
// (Phase 4c): a session whose host wired an extensions.Manager carries an
// extCommands snapshot + an invokeExtCommand closure (the acp package can't
// import extensions/extproto, so the host supplies both on the SessionAgent).
// Unlike the TUI-only built-ins above — curated down to the headless-safe
// subset — extension commands are author-defined and meant to be invoked, so
// we advertise them ALL and execute them via the host closure (translating the
// response action to ACP). A built-in always wins a name collision with an
// extension command (the dup is skipped from the advertised set and the
// built-in handler runs).

// nativeSlash describes a built-in command terva executes natively under
// ACP. handle runs the command against the live session — receiving the
// trailing argument text (empty for the no-arg commands; /trust reads "parent"
// from it) — and returns the stopReason the prompt turn resolves with.
type nativeSlash struct {
	handle func(ctx context.Context, sess *session, arg string) string
}

// nativeSlashCommands is the curated allowlist: the built-in slash commands
// the ACP run mode both advertises AND executes natively. Keyed by the
// canonical command name (with leading slash). This is the single source of
// truth for both availableCommands() (what we advertise) and
// handleSlashCommand() (what we execute), so the two can never drift.
//
// Assigned in init() rather than a var initializer to break an
// initialization cycle: runHelpCommand renders availableCommands(), which
// reads this map, so a plain var initializer would make the map refer to a
// handler that refers back to the map.
var nativeSlashCommands map[string]nativeSlash

func init() {
	nativeSlashCommands = map[string]nativeSlash{
		"/clear":       {handle: runClearCommand},
		"/compact":     {handle: runCompactCommand},
		"/help":        {handle: runHelpCommand},
		"/permissions": {handle: runPermissionsCommand},
		"/jail":        {handle: runJailCommand},
		"/unjail":      {handle: runUnjailCommand},
		"/skills":      {handle: runSkillsCommand},
		"/context":     {handle: runContextCommand},
		"/reload-ext":  {handle: runReloadExtCommand},
		"/trust":       {handle: runTrustCommand},
		"/untrust":     {handle: runUntrustCommand},
	}
}

// availableCommands builds the available_commands_update payload for sess: the
// curated subset of modes.BuiltinSlashCommands() that this run mode can honor
// (the nativeSlashCommands allowlist), PLUS every extension-registered command
// the session's host wired (sess.extCommands). Each command carries its
// description and (when it takes an argument) its hint. The curated built-ins
// take no argument today, so no input block is attached to them; an extension
// command with a Hint gets one.
//
// Collision policy: a built-in wins. An extension command whose bare name
// matches a built-in we advertise is skipped (the built-in handler is what
// runs for that head), so the editor never sees two palette entries with the
// same name and the user always gets the built-in.
//
// Returns nil when nothing is advertisable so the caller can skip the emit
// (an empty available_commands_update advertises an empty palette, which is
// fine, but skipping avoids a needless notification).
func availableCommands(sess *session) []AvailableCommand {
	out := make([]AvailableCommand, 0, len(nativeSlashCommands))
	// Track advertised bare names so an extension command can't shadow (or
	// duplicate) a built-in we already advertise.
	seen := make(map[string]bool, len(nativeSlashCommands))
	for _, info := range modes.BuiltinSlashCommands() {
		if _, ok := nativeSlashCommands[info.Name]; !ok {
			continue
		}
		// ACP command names are bare (no leading slash); the editor prepends
		// the slash in its palette. Strip the registry's slash.
		bare := strings.TrimPrefix(info.Name, "/")
		cmd := AvailableCommand{Name: bare, Description: info.Desc}
		if info.Hint != "" {
			cmd.Input = &AvailableCommandInput{Hint: info.Hint}
		}
		out = append(out, cmd)
		seen[bare] = true
	}

	// Append the session's extension commands. They are author-defined and
	// meant to be invoked, so we advertise them all (no curation) — only
	// skipping a name that collides with an advertised built-in (built-in
	// wins).
	if sess != nil && sess.extCommands != nil {
		for _, ec := range sess.extCommands() {
			bare := strings.TrimPrefix(ec.Name, "/")
			if bare == "" || seen[bare] {
				continue
			}
			cmd := AvailableCommand{Name: bare, Description: ec.Description}
			if ec.Hint != "" {
				cmd.Input = &AvailableCommandInput{Hint: ec.Hint}
			}
			out = append(out, cmd)
			seen[bare] = true
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// emitAvailableCommands sends the curated command catalog as an
// available_commands_update for the session. Emitted on session/new and
// after a session/load replay so the editor's command palette is populated
// for both a fresh and a resumed session (§10).
func (s *agentServer) emitAvailableCommands(sess *session) {
	cmds := availableCommands(sess)
	if cmds == nil {
		return
	}
	sess.emit(map[string]any{
		"sessionUpdate":     UpdateAvailableCommands,
		"availableCommands": cmds,
	})
}

// slashHead returns the leading slash-command name (with the slash) when
// text is a slash command, plus the trailing argument text, plus ok. It
// mirrors the TUI's head-parsing: a single simple word after the "/"
// (letters/digits/-/_) so paths ("/Users/...") and regexes ("/foo.bar/")
// are NOT mistaken for commands and instead run as an ordinary prompt.
func slashHead(text string) (head, arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 2 || trimmed[0] != '/' {
		return "", "", false
	}
	head = trimmed
	if i := strings.IndexAny(trimmed, " \t\n"); i >= 0 {
		head = trimmed[:i]
		arg = strings.TrimSpace(trimmed[i:])
	}
	// The word after "/" must be a single simple token; anything with a
	// path separator or a dot is a path/regex, not a command.
	for _, r := range head[1:] {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return "", "", false
		}
	}
	return head, arg, true
}

// isBuiltinSlash reports whether head is a known visible built-in command
// (consulting the exported registry catalog). Used only to phrase the
// graceful-degradation note — a known-but-TUI-only command vs an outright
// unknown verb.
func isBuiltinSlash(head string) bool {
	for _, c := range modes.BuiltinSlashCommands() {
		if c.Name == head {
			return true
		}
	}
	return false
}

// handleSlashCommand intercepts a leading slash command in a prompt and
// handles it natively instead of forwarding it to the model (§10). The
// return is (handled, stopReason): when handled is true, the prompt turn is
// done and resolves with stopReason; when false, the prompt is ordinary
// text and must run as a normal model turn.
//
// Four cases:
//   - a natively-executed built-in (the nativeSlashCommands allowlist) runs
//     against the live agent and resolves the turn (a built-in wins a name
//     collision with an extension command);
//   - an extension-registered command (the session's extCommands) is invoked
//     through the host and its response action applied (see handleExtCommand);
//   - a known-but-not-native built-in (an advertised-by-nobody TUI command
//     the editor sent anyway) degrades gracefully: a brief note that it
//     isn't available headless, turn resolves end_turn — never silently
//     forwarded to the model;
//   - an unknown slash command also degrades gracefully with a note.
//
// A non-slash prompt returns handled=false untouched.
func (s *agentServer) handleSlashCommand(ctx context.Context, sess *session, text string) (handled bool, stopReason string) {
	head, arg, ok := slashHead(text)
	if !ok {
		return false, ""
	}

	// Built-in wins a name collision: check the native allowlist first, so an
	// extension can never shadow /clear, /help, etc.
	if native, ok := nativeSlashCommands[head]; ok {
		return true, native.handle(ctx, sess, arg)
	}

	// Extension-registered command? The bare name (no leading slash) is what
	// the extension registered and what we advertised. extCommandHas checks the
	// session's advertised set so we only route a head the host actually owns.
	bare := strings.TrimPrefix(head, "/")
	if extCommandHas(sess, bare) {
		return true, s.handleExtCommand(ctx, sess, bare, arg)
	}

	// A slash command we don't execute natively. Whether it's a real but
	// TUI-only built-in or an outright unknown verb, the headless answer is
	// the same: tell the user it isn't available here and end the turn,
	// rather than forwarding "/foo" to the model as a literal prompt.
	note := "Command " + head + " isn't available over ACP."
	if !isBuiltinSlash(head) {
		note = "Unknown command " + head + "."
	}
	sess.emit(map[string]any{
		"sessionUpdate": UpdateAgentMessageChunk,
		"content":       textContentBlock(note),
	})
	return true, StopEndTurn
}

// extCommandHas reports whether bare names an extension command this session
// advertised. Consulting the same extCommands snapshot availableCommands() used
// keeps "what we route" in lockstep with "what we advertise": the editor only
// learns a command from available_commands_update, so a head it sends as a
// command was advertised, and a head matching a built-in already short-circuited
// above (built-in wins).
func extCommandHas(sess *session, bare string) bool {
	if sess == nil || sess.extCommands == nil {
		return false
	}
	for _, ec := range sess.extCommands() {
		if strings.TrimPrefix(ec.Name, "/") == bare {
			return true
		}
	}
	return false
}

// handleExtCommand invokes an extension-registered command through the host
// closure and applies the response action, translated to ACP. It mirrors the
// interactive invokeExtensionCommand action handling, adapted to the headless
// ACP surface:
//
//   - prompt    -> run a real model turn with the returned text (the meaningful
//     "do work" action), resolving with the turn's stopReason.
//   - display   -> emit the Display text as an agent_message_chunk; end_turn.
//   - noop      -> nothing emitted; end_turn.
//   - error     -> emit the Error text as a chunk; end_turn.
//   - insert / open_panel (TUI-only — ACP has no editor input / panel) -> the
//     host already folded the text into Display, so we render it as a chunk so
//     nothing is silently lost; end_turn.
//
// A transport error (or a nil invoke closure, which would be a host bug for an
// advertised command) surfaces as a chunk and ends the turn — never crashes,
// never forwards "/foo" to the model.
func (s *agentServer) handleExtCommand(ctx context.Context, sess *session, name, arg string) string {
	if sess.invokeExtCommand == nil {
		sess.chunk("Command /" + name + " is not available in this session.")
		return StopEndTurn
	}
	res, err := sess.invokeExtCommand(ctx, name, arg)
	if err != nil {
		sess.chunk("extension /" + name + ": " + err.Error())
		return StopEndTurn
	}

	switch res.Action {
	case ExtActionPrompt:
		text := strings.TrimSpace(res.Prompt)
		if text == "" {
			// Nothing to hand the model — treat as a noop rather than running an
			// empty turn.
			return StopEndTurn
		}
		// Run a real model turn with the extension's task text through the same
		// turn path an ordinary prompt uses (PromptWithPolicy + translator),
		// resolving with the turn's own stopReason. The caller (the prompt
		// handler) already holds the turn mutex + registered the turn ctx for
		// permissions, so this reuses that live turn.
		return s.runModelTurn(ctx, sess, text, nil)
	case ExtActionDisplay, ExtActionInsert, ExtActionOpenPanel:
		// display, plus the TUI-only insert/open_panel degraded to display by
		// the host: render the text so nothing is silently lost.
		sess.chunk(res.Display)
		return StopEndTurn
	case ExtActionError:
		sess.chunk("extension /" + name + ": " + res.Error)
		return StopEndTurn
	case ExtActionNoop, "":
		return StopEndTurn
	default:
		sess.chunk("extension /" + name + ": unknown action " + string(res.Action))
		return StopEndTurn
	}
}

// runClearCommand executes /clear natively: it drops the agent's transcript
// (SetMessages(nil)) so the next turn starts fresh, mirroring the TUI's
// slashClear (which also resets TUI-only state the ACP frontend doesn't
// have). Narrated as an agent_message_chunk so the editor shows feedback,
// then the turn ends.
func runClearCommand(_ context.Context, sess *session, _ string) string {
	sess.agent.SetMessages(nil)
	sess.emit(map[string]any{
		"sessionUpdate": UpdateAgentMessageChunk,
		"content":       textContentBlock("Cleared the conversation."),
	})
	return StopEndTurn
}

// runCompactCommand executes /compact natively: it summarizes and replaces
// the transcript via core.Agent.Compact (keepTail=4, matching the TUI and
// rpc), streaming the summary deltas as agent_message_chunks so the editor
// shows progress, then ends the turn. A compaction error (e.g. an empty
// transcript) is surfaced as a chunk; the turn still resolves end_turn.
func runCompactCommand(ctx context.Context, sess *session, _ string) string {
	sink := func(delta string) {
		if delta == "" {
			return
		}
		sess.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock(delta),
		})
	}
	if _, err := sess.agent.Compact(ctx, 4, sink); err != nil {
		sess.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock("Compaction failed: " + err.Error()),
		})
	}
	return StopEndTurn
}

// chunk emits a single agent_message_chunk carrying text, the common shape of
// every native command's feedback. Empty text is dropped (the editor has
// nothing to render and an empty chunk is noise).
func (s *session) chunk(text string) {
	if text == "" {
		return
	}
	s.emit(map[string]any{
		"sessionUpdate": UpdateAgentMessageChunk,
		"content":       textContentBlock(text),
	})
}

// runHelpCommand executes /help natively: it emits a plain-text overview of
// what terva is plus the commands the editor can run over ACP, derived from
// availableCommands() so the list never drifts from what is actually
// advertised + executed. No model call; resolves end_turn. The interactive
// /help also lists TUI keybindings, which have no headless meaning, so those
// are deliberately omitted.
func runHelpCommand(_ context.Context, sess *session, _ string) string {
	var b strings.Builder
	b.WriteString("terva is a coding agent. You're driving it over ACP from your editor; ")
	b.WriteString("type a request and terva runs tools (read/edit files, run commands) to carry it out.\n\n")
	b.WriteString("Slash commands available here:\n")
	cmds := availableCommands(sess)
	// availableCommands() returns the curated catalog in registry order; render
	// it the same way the editor's palette would (bare name + description).
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	for _, c := range cmds {
		b.WriteString("  /")
		b.WriteString(c.Name)
		if c.Description != "" {
			b.WriteString(" — ")
			b.WriteString(c.Description)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nOther terva commands (e.g. /model, /sessions) are handled by your editor's own UI ")
	b.WriteString("(the model selector and session list), not as slash commands here.")
	sess.chunk(b.String())
	return StopEndTurn
}

// runPermissionsCommand executes /permissions natively: a read-only text
// summary of the session's approval posture — the current approval mode, the
// policy's permission rules, and the session-scoped allow_always /
// reject_always grants the ACP confirmer has cached (§8). Mirrors the TUI's
// buildPermissionsView in content, dropped of the theme/colour coupling. No
// model call; resolves end_turn.
func runPermissionsCommand(_ context.Context, sess *session, _ string) string {
	var b strings.Builder
	gate := sess.gate

	// A pure-yolo session has no gate (no rules, no grants to report) — the
	// whole summary collapses to the one line.
	if gate == nil {
		sess.chunk("Approval mode: yolo (no permission gate) — every tool runs without asking.")
		return StopEndTurn
	}

	// Approval mode. currentMode() is the live session mode (seeded from the
	// gate, updated by session/set_mode); fall back to the gate when unset.
	mode := sess.currentMode()
	if mode == "" {
		mode = string(gate.Mode())
	}
	b.WriteString("Approval mode: ")
	b.WriteString(mode)
	b.WriteString("\n  (switch it from your editor's mode menu — session/set_mode.)\n\n")

	// Policy rules (first match wins; user → project → extension).
	rules := gate.Rules()
	if len(rules) == 0 {
		b.WriteString("Rules: none in effect.\n")
	} else {
		b.WriteString("Rules (first match wins; user → project → extension):\n")
		lastSrc := ""
		for _, r := range rules {
			if r.Source != lastSrc {
				b.WriteString("  [")
				b.WriteString(r.Source)
				b.WriteString("]\n")
				lastSrc = r.Source
			}
			b.WriteString("    ")
			b.WriteString(string(r.Decision))
			b.WriteString("  ")
			b.WriteString(r.Tool)
			if r.Args != nil {
				b.WriteString("  args~/")
				b.WriteString(r.Args.String())
				b.WriteString("/")
			}
			if r.Reason != "" {
				b.WriteString("  — ")
				b.WriteString(r.Reason)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	// This session's grants. Two sources: the gate's own allow-always state
	// (allowAll + per-tool grants) and the ACP confirmer's session cache
	// (allow_always / reject_always remembered for the editor).
	b.WriteString("This session:\n")
	wrote := false
	allowAll, tools := gate.Grants()
	if allowAll {
		b.WriteString("  allow all tools (granted this session)\n")
		wrote = true
	}
	for _, t := range tools {
		b.WriteString("  always allow: ")
		b.WriteString(t)
		b.WriteString("\n")
		wrote = true
	}
	cachedAllow, cachedReject := sess.cachedDecisions()
	for _, t := range cachedAllow {
		b.WriteString("  always allow (editor): ")
		b.WriteString(t)
		b.WriteString("\n")
		wrote = true
	}
	for _, t := range cachedReject {
		b.WriteString("  always reject (editor): ")
		b.WriteString(t)
		b.WriteString("\n")
		wrote = true
	}
	if !wrote {
		b.WriteString("  no session grants yet.\n")
	}

	sess.chunk(strings.TrimRight(b.String(), "\n"))
	return StopEndTurn
}

// runJailCommand executes /jail natively: it confines the session's tools to
// the session cwd by locking the shared *tools.Sandbox, mirroring the TUI's
// /jail (i.cfg.Sandbox.Lock()). The sandbox is shared by pointer with every
// file/shell tool in the agent's registry, so the next tool call is gated by
// it. Emits a confirmation chunk; resolves end_turn. With no sandbox in the
// build it degrades to a note (capability honesty), never silently no-ops.
func runJailCommand(_ context.Context, sess *session, _ string) string {
	if sess.sandbox == nil {
		sess.chunk("Sandbox not available in this build; /jail has no effect.")
		return StopEndTurn
	}
	sess.sandbox.Lock()
	sess.chunk("Jailed to " + sess.cwd + " (tools cannot touch paths outside this directory).")
	return StopEndTurn
}

// runUnjailCommand executes /unjail natively: it releases the sandbox
// confinement (Unlock), mirroring the TUI's /unjail. Emits a confirmation
// chunk; resolves end_turn. Degrades to a note with no sandbox.
func runUnjailCommand(_ context.Context, sess *session, _ string) string {
	if sess.sandbox == nil {
		sess.chunk("Sandbox not available in this build; /unjail has no effect.")
		return StopEndTurn
	}
	sess.sandbox.Unlock()
	sess.chunk("Unjailed (tools may touch paths outside " + sess.cwd + " again).")
	return StopEndTurn
}

// runSkillsCommand executes /skills natively: it lists the discovered
// (non-builtin) SKILL.md skills — name + one-line description — mirroring the
// TUI's /skills picker, which the model loads on demand via the `skill` tool.
// The list comes from the session's skills snapshot func (re-discovered each
// call so edits made during the session show up). No model call; resolves
// end_turn. With no skills source (or none discovered) it reports that.
func runSkillsCommand(_ context.Context, sess *session, _ string) string {
	if sess.skills == nil {
		sess.chunk("Skills are not available in this session.")
		return StopEndTurn
	}
	list := sess.skills()
	if len(list) == 0 {
		sess.chunk("No skills discovered. Add a SKILL.md to .terva/skills or your project.")
		return StopEndTurn
	}
	var b strings.Builder
	b.WriteString("Discovered skills (the model loads these on demand via the skill tool):\n")
	for _, sk := range list {
		if sk == nil {
			continue
		}
		b.WriteString("  ")
		b.WriteString(sk.Name)
		if sk.Description != "" {
			b.WriteString(" — ")
			b.WriteString(sk.Description)
		}
		if sk.Source != "" {
			b.WriteString("  [")
			b.WriteString(sk.Source)
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	sess.chunk(strings.TrimRight(b.String(), "\n"))
	return StopEndTurn
}

// runContextCommand executes /context natively: a read-only summary of what
// the session's extensions are injecting into the model — static system
// guidance and live context cards — mirroring the TUI's slashContext (the
// transparency half of the context-card capability). The list comes from the
// session's extContext snapshot func (the host's ContextSnapshot of the live
// manager), re-read each call. No model call; resolves end_turn. With no
// extension manager it reports "extensions are not enabled"; with a manager but
// nothing injected, "no extension is contributing context".
func runContextCommand(_ context.Context, sess *session, _ string) string {
	if sess.extContext == nil {
		sess.chunk("Extensions are not enabled in this session.")
		return StopEndTurn
	}
	items := sess.extContext()
	if len(items) == 0 {
		sess.chunk("No extension is contributing context to the model.")
		return StopEndTurn
	}
	var b strings.Builder
	b.WriteString("Extension context injected into the model:")
	for _, it := range items {
		if it.Kind == "static" {
			b.WriteString("\n")
			b.WriteString(it.Source)
			b.WriteString(" (system guidance):")
		} else {
			label := it.Label
			b.WriteString("\n")
			b.WriteString(it.Source)
			b.WriteString(" (card ")
			b.WriteString(strconv.Quote(label))
			b.WriteString("):")
		}
		for _, line := range strings.Split(strings.TrimRight(it.Text, "\n"), "\n") {
			b.WriteString("\n  ")
			b.WriteString(line)
		}
	}
	sess.chunk(b.String())
	return StopEndTurn
}

// runReloadExtCommand executes /reload-ext natively: it hot-reloads the
// session's extensions (re-read manifests, respawn subprocesses) via the host's
// reloadExtensions closure, mirroring the TUI's runReloadExt. The closure runs
// on the live turn ctx; its ReloadStats are summarized as an
// agent_message_chunk in the same wording the interactive command uses. The
// ACP-specific bit: because the reload may have changed the extension command
// set, we re-emit available_commands_update afterwards (the live ExtCommands
// snapshot reflects the reload automatically), so the editor's palette stays in
// lockstep. No model call; resolves end_turn. With no extension manager it
// degrades to a note.
func runReloadExtCommand(ctx context.Context, sess *session, _ string) string {
	if sess.reloadExtensions == nil {
		sess.chunk("No extension manager in this build; nothing to reload.")
		return StopEndTurn
	}
	stats := sess.reloadExtensions(ctx)
	msg := fmt.Sprintf("reloaded: %d stopped, %d loaded (%d ready)", stats.Stopped, stats.Loaded, stats.Ready)
	if stats.Errors > 0 {
		msg += fmt.Sprintf(", %d error(s)", stats.Errors)
	}
	sess.chunk(msg)
	// Re-advertise the command catalog: the extension set may have changed, and
	// the editor only learns commands from available_commands_update. The
	// session's ExtCommands closure reads the live manager, so the freshly
	// reloaded command set is reflected automatically.
	sess.srv.emitAvailableCommands(sess)
	return StopEndTurn
}

// runTrustCommand executes /trust natively: it trusts the session's workspace
// from inside the editor — the only in-editor way to trust a workspace over ACP
// (until this, ACP only restricted + logged a warning). The optional argument
// "parent" trusts the whole tree under the cwd (so every repo beneath it is
// trusted), mirroring the TUI's `/trust parent`. The host's trustWorkspace
// closure persists the verdict and makes project content go live for this
// session by reloading the extension manager — so project extensions are
// discovered now. Project skills and context, however, are baked into the
// session's system prompt at build time and can't be re-injected mid-session,
// so the confirmation says they take effect on a NEW session. Because the reload
// may have added extension commands, we re-advertise the catalog afterwards (the
// same lockstep /reload-ext keeps). No model call; resolves end_turn. With no
// trustWorkspace closure (host didn't wire it) it degrades to a clear note.
func runTrustCommand(_ context.Context, sess *session, arg string) string {
	if sess.trustWorkspace == nil {
		sess.chunk("Workspace trust isn't available over ACP in this build; nothing to trust.")
		return StopEndTurn
	}
	// The single recognized argument is "parent" — trust descendants too. Any
	// other trailing text is ignored (the TUI is equally lenient).
	parent := strings.EqualFold(strings.TrimSpace(arg), "parent")
	if err := sess.trustWorkspace(parent); err != nil {
		sess.chunk("trust: " + err.Error())
		return StopEndTurn
	}
	scope := "this directory"
	if parent {
		scope = "this directory and its descendants"
	}
	sess.chunk("Trusted " + scope + "; project extensions reloaded — skills and context apply to new sessions.")
	// Re-advertise the command catalog: the reload may have brought project
	// extensions (and their commands) online, and the editor only learns commands
	// from available_commands_update.
	sess.srv.emitAvailableCommands(sess)
	return StopEndTurn
}

// runUntrustCommand executes /untrust natively: the symmetric inverse of
// /trust. The host's untrustWorkspace closure drops the session cwd from the
// trust store and reloads the extension manager with trust withdrawn, so project
// extensions are torn down this session; project skills/context (prompt-baked)
// stay until a new session. Re-advertises the catalog afterwards. No model call;
// resolves end_turn. With no untrustWorkspace closure it degrades to a note.
func runUntrustCommand(_ context.Context, sess *session, _ string) string {
	if sess.untrustWorkspace == nil {
		sess.chunk("Workspace trust isn't available over ACP in this build; nothing to untrust.")
		return StopEndTurn
	}
	if err := sess.untrustWorkspace(); err != nil {
		sess.chunk("untrust: " + err.Error())
		return StopEndTurn
	}
	sess.chunk("Untrusted this directory; project extensions unloaded — skills and context stop on new sessions.")
	// Re-advertise the command catalog: dropping the project extensions removes
	// their commands, and the editor only learns the change from
	// available_commands_update.
	sess.srv.emitAvailableCommands(sess)
	return StopEndTurn
}
