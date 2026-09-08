package modes

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// runSwarm dispatches /swarm subcommands. Layout:
//
//	/swarm                       -> open the dashboard
//	/swarm list                  -> open the dashboard
//	/swarm new [--model M] [--provider P] [--persona N] [--backend B] [--reasoning E] <task...>
//	                             -> spawn an agent (optionally pinned to a model,
//	                                persona, thinking effort, or a worker backend —
//	                                claude/terva/…; empty backend = a native swarm
//	                                agent). A foreign backend is gated on
//	                                external_workers being on. The effort is
//	                                independent of the model, so it pairs with any
//	                                of them.
//	/swarm kill <id>             -> stop a running agent
//	/swarm remove <id>           -> tear down a terminated agent
//	/swarm logs <id>             -> open the scrollable transcript view
//	/swarm send <id> <text...>   -> send a follow-up user turn to <id>
//	/swarm resume [id]           -> resume an agent (omit id to pick from a list)
//	/swarm attach <id>           -> (planned) drop into the agent's TUI
//
// When neither backend is available the command tells the user the
// feature is disabled instead of pretending to work.
func (i *Interactive) runSwarm(ctx context.Context, args []string) {
	// One callback set serves the whole dispatch, driving the tasks surface
	// (spawn/stop/remove/send/resume verbs + the cached snapshot). spawn and
	// resume return the affected agent's id when the backend can know it —
	// surface actions carry no result payload, so spawn returns "" and the
	// status message omits the id (it shows up in the dashboard).
	var (
		snapshotFn func() []swarm.AgentSnapshot
		stopFn     func(id string) error
		removeFn   func(id string) error
		archiveFn  func(id string) error
		spawnFn    func(f spawnFlags) (string, error)
		sendFn     func(id, text string) error
		resumeFn   func(id string) (string, error)
	)
	switch {
	case i.cfg.Carrier != nil && i.cfg.CarrierTasks:
		snapshotFn = i.carrierTaskSnapshot
		stopFn = func(id string) error { return i.carrierTaskAction("stop", map[string]string{"id": id}) }
		removeFn = func(id string) error { return i.carrierTaskAction("remove", map[string]string{"id": id}) }
		archiveFn = func(id string) error { return i.carrierTaskAction("archive", map[string]string{"id": id}) }
		spawnFn = func(f spawnFlags) (string, error) {
			return "", i.carrierTaskAction("spawn", map[string]string{
				"task": f.Task, "model": f.Model, "provider": f.Provider,
				"persona": f.Persona, "backend": f.Backend, "reasoning": f.Reasoning,
			})
		}
		sendFn = func(id, text string) error {
			return i.carrierTaskAction("send", map[string]string{"id": id, "text": text})
		}
		resumeFn = func(id string) (string, error) {
			return id, i.carrierTaskAction("resume", map[string]string{"id": id})
		}
		// Open with a fresh snapshot even if no change signal arrived since
		// the last fetch (the poller's signature can lag a just-issued verb).
		// The fill is synchronous here — opening the dashboard is a user
		// action, not a render frame, and the dialog's first paint should
		// show current rows.
		i.invalidateCarrierTasks()
		i.fetchCarrierTasks()
	default:
		i.mu.Lock()
		i.statusErr = i18n.T("swarm is disabled in this build")
		i.statusOK = ""
		i.mu.Unlock()
		return
	}

	sub := ""
	rest := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
		// Guard the args[1:] reslice: when only the subcommand was
		// typed (e.g. bare "/swarm new"), args has length 1 and the
		// naive args[1:] is fine, but when args is empty (bare
		// "/swarm") the reslice is [1:0] and panics. The len>0 branch
		// here keeps both cases safe.
		if len(args) > 1 {
			rest = strings.TrimSpace(strings.Join(args[1:], " "))
		}
	}

	// spawnAdapter / resumeAdapter narrow the backend fns to the signatures
	// the dialog expects. Defined once here so the three Open()-shaped entry
	// points (list, logs/view-jump, resume) feed the dialog identical
	// callbacks.
	spawnAdapter := func(task, model, provider string) error {
		// The dashboard's inline spawn editor is native-only (like persona, a
		// backend is a command flag, not a picker); pass an empty backend.
		_, err := spawnFn(spawnFlags{Task: task, Model: model, Provider: provider})
		return err
	}
	resumeAdapter := func(id string) error {
		_, err := resumeFn(id)
		return err
	}

	// Pin every fresh spawn to whatever the host's /model selection
	// is right now. This is captured at /swarm time — if the user
	// wants a different model for the next swarm agent, they pick it
	// via /model first (globally), or, while inside the spawn
	// editor, by typing /model on its own line to pop the picker.
	i.swarmDialog.SetCurrentModel(i.cfg.Model, i.cfg.Provider)
	if i.cfg.LoggedInProviders != nil {
		i.swarmDialog.SetLoggedInProviders(i.cfg.LoggedInProviders())
		if i.cfg.HiddenModels != nil {
			i.swarmDialog.SetHiddenModels(i.cfg.HiddenModels())
		}
	}

	switch sub {
	case "", "list", "ls", "ps":
		i.swarmDialog.SetArchive(archiveFn)
		i.swarmDialog.Open(
			snapshotFn,
			stopFn,
			removeFn,
			spawnAdapter,
			sendFn,
			resumeAdapter,
			i.cfg.CWD,
		)
	case "new", "spawn":
		if rest == "" {
			i.swarmStatus("", i18n.T("/swarm new <task>: missing task"))
			return
		}
		// Permit `--model X --provider Y` flags before the task so
		// scripts can pin a model without going through the dialog.
		// Anything that isn't a recognised flag terminates parsing
		// and the rest becomes the task; this keeps `/swarm new
		// --model foo do a thing` and `/swarm new do --model thing`
		// (where --model is part of the task) unambiguous — only
		// leading flags are consumed.
		flags := parseSpawnFlags(rest)
		if flags.Task == "" {
			i.swarmStatus("", i18n.T("/swarm new: missing task (after any --model/--provider/--persona/--backend/--reasoning flags)"))
			return
		}
		// Refuse an effort word the ladder does not know here, rather than let
		// it reach the child's own --reasoning and fail the spawn from inside a
		// subprocess. The ladder is rendered, never spelled out.
		if !provider.ValidReasoningLevel(flags.Reasoning) {
			i.swarmStatus("", i18n.T("/swarm new: --reasoning must be %s", provider.ReasoningLadder()))
			return
		}
		id, err := spawnFn(flags)
		if err != nil {
			i.swarmStatus("", i18n.T("spawn: %s", err.Error()))
			return
		}
		// Name everything that was pinned. This used to be a switch over the
		// combinations, which reported the first flag it matched and hid the
		// rest: `--persona x --model y` said only the persona.
		var pinned []string
		for _, p := range []struct{ label, value string }{
			{i18n.T("backend %s", flags.Backend), flags.Backend},
			{i18n.T("persona %s", flags.Persona), flags.Persona},
			{i18n.T("model %s", flags.Model), flags.Model},
			{i18n.T("thinking %s", flags.Reasoning), flags.Reasoning},
		} {
			if p.value != "" {
				pinned = append(pinned, p.label)
			}
		}
		switch {
		case id != "" && len(pinned) > 0:
			i.swarmStatus(i18n.T("spawned %s (%s)", id, strings.Join(pinned, ", ")), "")
		case id != "":
			i.swarmStatus(i18n.T("spawned %s", id), "")
		case len(pinned) > 0:
			i.swarmStatus(i18n.T("spawned (%s)", strings.Join(pinned, ", ")), "")
		default:
			i.swarmStatus(i18n.T("spawned"), "")
		}
	case "kill", "stop":
		if rest == "" {
			i.swarmStatus("", i18n.T("/swarm kill <id>: missing id"))
			return
		}
		if err := stopFn(rest); err != nil {
			i.swarmStatus("", i18n.T("kill: %s", err.Error()))
			return
		}
		i.swarmStatus(i18n.T("stopped %s", rest), "")
	case "remove", "rm":
		if rest == "" {
			i.swarmStatus("", i18n.T("/swarm remove <id>: missing id"))
			return
		}
		if err := removeFn(rest); err != nil {
			i.swarmStatus("", i18n.T("remove: %s", err.Error()))
			return
		}
		i.swarmStatus(i18n.T("removed %s", rest), "")
	case "archive":
		if rest == "" {
			i.swarmStatus("", i18n.T("/swarm archive <id>: missing id"))
			return
		}
		if archiveFn == nil {
			i.swarmStatus("", i18n.T("archive is unavailable in this build"))
			return
		}
		if err := archiveFn(rest); err != nil {
			i.swarmStatus("", i18n.T("archive: %s", err.Error()))
			return
		}
		// Say where it went. "Archived" alone reads like a softer delete, and
		// the one thing the user needs to know is that the transcript is still
		// on disk and reachable without terva.
		i.swarmStatus(i18n.T("archived %s — compressed under swarm/archive/", rest), "")
	case "logs", "log", "view":
		if rest == "" {
			i.swarmStatus("", i18n.T("/swarm logs <id>: missing id"))
			return
		}
		i.swarmDialog.SetArchive(archiveFn)
		ok := i.swarmDialog.OpenViewing(
			rest,
			snapshotFn,
			stopFn,
			removeFn,
			spawnAdapter,
			sendFn,
			resumeAdapter,
			i.cfg.CWD,
		)
		if !ok {
			i.swarmStatus("", i18n.T("/swarm logs: no agent matching %s", rest))
		}
	case "resume", "reattach", "reopen":
		if rest == "" {
			// No id given: open the dashboard with the cursor
			// pre-positioned on the first resumable agent, and
			// tell the user how many there are so they know what
			// to expect. Pressing R confirms; ↑/↓ to pick a
			// different row first.
			count := i.swarmDialog.OpenForResume(
				snapshotFn,
				stopFn,
				removeFn,
				spawnAdapter,
				sendFn,
				resumeAdapter,
				i.cfg.CWD,
			)
			switch count {
			case 0:
				i.swarmStatus("", i18n.T("/swarm resume: no resumable agents (none detached or terminated)"))
			case 1:
				i.swarmStatus(i18n.T("1 resumable agent, press R to resume"), "")
			default:
				i.swarmStatus(i18n.T("%d resumable agents, ↑/↓ to pick, R to resume", count), "")
			}
			return
		}
		id, err := resumeFn(rest)
		if err != nil {
			i.swarmStatus("", i18n.T("resume: %s", err.Error()))
			return
		}
		i.swarmStatus(i18n.T("resumed %s", id), "")
	case "send", "prompt", "msg":
		// /swarm send <id> <text...> is the non-interactive
		// counterpart of pressing 'p' in the dashboard. We split the
		// joined `rest` ourselves rather than reusing the dispatcher's
		// already-fielded args[] because the text may contain spaces
		// the user expects to be preserved verbatim.
		id, text := splitIDAndRest(rest)
		if id == "" {
			i.swarmStatus("", i18n.T("/swarm send <id> <text>: missing id"))
			return
		}
		if text == "" {
			i.swarmStatus("", i18n.T("/swarm send <id> <text>: missing text"))
			return
		}
		if err := sendFn(id, text); err != nil {
			i.swarmStatus("", dialogs.FriendlySendErr(id, err))
			return
		}
		i.swarmStatus(i18n.T("sent to %s", id), "")
	case "attach":
		// PTY-reparenting is a significant chunk of work I haven't
		// landed yet (see the design sketch). Recognise the name so
		// /swarm attach doesn't fall through to the generic "unknown
		// subcommand" path — that error message is misleading because
		// it makes attach sound like a typo instead of a planned
		// feature. Point the user at /swarm logs in the meantime.
		i.swarmStatus("", i18n.T("/swarm attach: not implemented yet (needs PTY reparenting). Use /swarm logs %s to watch its transcript.", firstWord(rest)))
	default:
		i.swarmStatus("", i18n.T("/swarm: unknown subcommand %s (try list / new / kill / remove / logs / send / resume)", sub))
	}
}

// spawnFlags is what `/swarm new` accepts before the task body. A struct
// rather than a row of return values: the parser grew to five flags, and the
// call sites had stopped being readable.
type spawnFlags struct {
	Model     string
	Provider  string
	Persona   string
	Backend   string
	Reasoning string
	Task      string
}

// parseSpawnFlags consumes any leading `--model X` / `--provider Y` /
// `--persona Z` / `--backend B` / `--reasoning E` flags from s and returns
// them with the remaining task body. We deliberately only honour LEADING
// flags so a task like "check --model lookup" doesn't accidentally swallow
// part of its prose as the model name.
//
// Each flag takes a two-token form (`--model X`) and a single-token form
// (`--model=X`). A flag with no value following it is still consumed, so a
// dangling `--model` doesn't leak into the task; the caller surfaces
// "missing task" instead.
func parseSpawnFlags(s string) spawnFlags {
	var out spawnFlags
	// One table, so a sixth flag is a row and not another copy of the same
	// eleven lines. Order does not matter; the names are distinct.
	into := map[string]*string{
		"--model":     &out.Model,
		"--provider":  &out.Provider,
		"--persona":   &out.Persona,
		"--backend":   &out.Backend,
		"--reasoning": &out.Reasoning,
	}

	fields := strings.Fields(s)
	i := 0
	for i < len(fields) {
		f := fields[i]
		if dst, ok := into[f]; ok {
			if i+1 < len(fields) {
				*dst = fields[i+1]
				i += 2
			} else {
				i++
			}
			continue
		}
		if name, value, ok := strings.Cut(f, "="); ok {
			if dst, known := into[name]; known {
				*dst = value
				i++
				continue
			}
		}
		break
	}
	out.Task = strings.TrimSpace(strings.Join(fields[i:], " "))
	return out
}

// splitIDAndRest splits "<id> <text...>" into (id, text). The text
// half preserves all whitespace after the first token so the agent
// receives the user's prompt verbatim (modulo a single trim of the
// boundary space). Returns ("", "") when s is empty so the caller
// can surface a missing-id error.
func splitIDAndRest(s string) (id, text string) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", ""
	}
	cut := strings.IndexAny(s, " \t")
	if cut < 0 {
		return s, ""
	}
	return s[:cut], strings.TrimLeft(s[cut+1:], " \t")
}

// firstWord returns the first whitespace-separated token of s, or
// "<id>" when s is empty. Used to keep the "/swarm attach" hint
// readable even when the user typed no argument.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "<id>"
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

func (i *Interactive) swarmStatus(ok, errMsg string) {
	i.mu.Lock()
	i.statusOK = ok
	i.statusErr = errMsg
	i.mu.Unlock()
	i.invalidate()
}
