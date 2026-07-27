package modes

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
)

// runSwarm dispatches /swarm subcommands. Layout:
//
//	/swarm                       -> open the dashboard
//	/swarm list                  -> open the dashboard
//	/swarm new [--model M] [--provider P] [--persona N] [--backend B] <task...>
//	                             -> spawn an agent (optionally pinned to a model,
//	                                persona, or a worker backend — claude/terva/…;
//	                                empty backend = a native swarm agent). A foreign
//	                                backend is gated on external_workers being on.
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
		spawnFn    func(task, model, provider, persona, backend string) (string, error)
		sendFn     func(id, text string) error
		resumeFn   func(id string) (string, error)
	)
	switch {
	case i.cfg.Carrier != nil && i.cfg.CarrierTasks:
		snapshotFn = i.carrierTaskSnapshot
		stopFn = func(id string) error { return i.carrierTaskAction("stop", map[string]string{"id": id}) }
		removeFn = func(id string) error { return i.carrierTaskAction("remove", map[string]string{"id": id}) }
		spawnFn = func(task, model, provider, persona, backend string) (string, error) {
			return "", i.carrierTaskAction("spawn", map[string]string{
				"task": task, "model": model, "provider": provider, "persona": persona, "backend": backend,
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
		_, err := spawnFn(task, model, provider, "", "")
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
	}

	switch sub {
	case "", "list", "ls", "ps":
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
		model, provider, persona, backend, task := parseSpawnFlags(rest)
		if task == "" {
			i.swarmStatus("", i18n.T("/swarm new: missing task (after any --model/--provider/--persona/--backend flags)"))
			return
		}
		id, err := spawnFn(task, model, provider, persona, backend)
		if err != nil {
			i.swarmStatus("", i18n.T("spawn: %s", err.Error()))
			return
		}
		switch {
		case backend != "" && id != "":
			i.swarmStatus(i18n.T("spawned %s (backend %s)", id, backend), "")
		case backend != "":
			i.swarmStatus(i18n.T("spawned (backend %s)", backend), "")
		case persona != "" && id != "":
			i.swarmStatus(i18n.T("spawned %s (persona %s)", id, persona), "")
		case persona != "":
			i.swarmStatus(i18n.T("spawned (persona %s)", persona), "")
		case model != "" && id != "":
			i.swarmStatus(i18n.T("spawned %s (model %s)", id, model), "")
		case model != "":
			i.swarmStatus(i18n.T("spawned (model %s)", model), "")
		case id != "":
			i.swarmStatus(i18n.T("spawned %s", id), "")
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
	case "logs", "log", "view":
		if rest == "" {
			i.swarmStatus("", i18n.T("/swarm logs <id>: missing id"))
			return
		}
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

// parseSpawnFlags consumes any leading `--model X` / `--provider Y` /
// `--persona Z` flags from s and returns them along with the remaining
// task body. We deliberately only honour LEADING flags so a task like
// "check --model lookup" doesn't accidentally swallow part of its prose
// as the model name.
//
// Recognised forms:
//
//	--model X            two-token form
//	--model=X            single-token form
//	--provider X         two-token form
//	--provider=X         single-token form
//	--persona X          two-token form (built-in/installed name, or .md path)
//	--persona=X          single-token form
func parseSpawnFlags(s string) (model, provider, persona, backend, task string) {
	fields := strings.Fields(s)
	i := 0
	for i < len(fields) {
		f := fields[i]
		switch {
		case f == "--model":
			// Consume the flag even when no value follows so a
			// dangling "--model" doesn't leak into the task. The
			// caller surfaces "missing task" instead.
			if i+1 < len(fields) {
				model = fields[i+1]
				i += 2
			} else {
				i++
			}
			continue
		case strings.HasPrefix(f, "--model="):
			model = strings.TrimPrefix(f, "--model=")
			i++
			continue
		case f == "--provider":
			if i+1 < len(fields) {
				provider = fields[i+1]
				i += 2
			} else {
				i++
			}
			continue
		case strings.HasPrefix(f, "--provider="):
			provider = strings.TrimPrefix(f, "--provider=")
			i++
			continue
		case f == "--persona":
			if i+1 < len(fields) {
				persona = fields[i+1]
				i += 2
			} else {
				i++
			}
			continue
		case strings.HasPrefix(f, "--persona="):
			persona = strings.TrimPrefix(f, "--persona=")
			i++
			continue
		case f == "--backend":
			if i+1 < len(fields) {
				backend = fields[i+1]
				i += 2
			} else {
				i++
			}
			continue
		case strings.HasPrefix(f, "--backend="):
			backend = strings.TrimPrefix(f, "--backend=")
			i++
			continue
		}
		break
	}
	task = strings.TrimSpace(strings.Join(fields[i:], " "))
	return
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
