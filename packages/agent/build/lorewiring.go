package build

import (
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// PerTurnContext returns a per-turn context provider for this run's uncached
// tail: the triggered lore entries that match the agent's recent messages,
// followed by a card's post_history_instructions (static, but positioned
// after history). Returns nil when there's neither, so composeEphemeral skips
// it entirely.
//
// The closure captures ag and reads ag.Messages() at call time — the core
// invokes ContextProvider once per turn, after copying the transcript and
// outside its lock (see core/agent.go), so this is safe and always sees the
// latest turn. PHI comes last, matching SillyTavern's after-history position.
func (r *Resolved) PerTurnContext(ag *core.Agent) func() string {
	return r.tailProvider(ag, true)
}

// PerTurnContextPeek is the side-effect-free twin of PerTurnContext: it renders
// the same uncached tail from the agent's current messages but does NOT record
// which lore fired. Wired to core.Agent.ContextProviderPeek so the UI can SIZE
// the tail (e.g. /context) without overwriting the "fired last turn" record.
func (r *Resolved) PerTurnContextPeek(ag *core.Agent) func() string {
	return r.tailProvider(ag, false)
}

// tailProvider builds the per-turn tail closure. When record is true the
// resulting closure notes which lore fired (for /lore); when false it is pure.
//
// Archived durable memory joins here rather than as an EphemeralTail field,
// which matters: this is derived from Resolved and installed at exactly two
// places (NewAgent and the lore/trust rewire), so every host gets it without
// filling anything in. A field on EphemeralTail would have to be set by each of
// the three hosts that build one, and forgetting one is the failure that type
// exists to prevent — it is the same shape as the bug where both live cards
// vanished from the three hosts that re-derive.
func (r *Resolved) tailProvider(ag *core.Agent, record bool) func() string {
	if r == nil {
		return nil
	}
	// A non-nil note/userDesc/worldLore record keeps the tail live even with no
	// lore/PHI, so a note, user persona, or World lore added later takes effect —
	// see Resolve, which allocates them only for immersive sessions. Archived
	// memory keeps it live for the same reason: the closure reads the archive
	// every turn, so a session that starts with nothing archived still fires the
	// entry it archives at turn three.
	memoryRecall := r.MemoryRecall(ag, record)
	if len(r.loreTriggered) == 0 && r.postHistory == "" && r.note == nil && r.userDesc == nil &&
		r.worldLore == nil && memoryRecall == nil {
		return nil
	}
	triggered, cfg, phi, rec, note := r.loreTriggered, r.loreConfig, r.postHistory, r.loreFired, r.note
	world := r.worldLore
	userDesc, userName := r.userDesc, r.userName
	userGender, userPronouns := r.userGender, r.userPronouns
	return func() string {
		var parts []string
		// Archived durable memory leads the tail, which puts it FURTHEST from the
		// generation point of anything here. That is the right end for it: in an
		// immersive session the scene material — world lore, the pinned state
		// card, the persona, the card's post-history instructions — has to stay
		// nearest the model's output, and the agent's own recollections must not
		// come between the scene and the writing of it. In a coding session
		// nothing else is present and the position is moot.
		if memoryRecall != nil {
			if s := strings.TrimSpace(memoryRecall()); s != "" {
				parts = append(parts, s)
			}
		}
		// World lore (read live, so a world.lore.* edit lands next turn) joins the
		// file/card triggered entries in ONE Select: a shared budget, a shared
		// activation trace, and constants fire unconditionally there — World
		// constants deliberately ride this uncached tail, not the cached prefix,
		// because they are session-mutable. World entries lead the input so at
		// equal Order the stable sort gives the world's shared facts budget and
		// placement priority over a card's book.
		//
		// The pinned scene-state entry (SD4) is pulled OUT before the merge: it
		// bypasses Select entirely because the token budget can drop a constant
		// entry, and the state card must never be silently dropped — least of all
		// in a long session, which is exactly when the budget is tightest and the
		// pinned clock/ledger matter most. It renders as its own framed block
		// right after the lore.
		entries := triggered
		sceneState := ""
		if world != nil {
			if ws := world.Get(); len(ws) > 0 {
				merged := make([]lore.Entry, 0, len(ws)+len(triggered))
				for _, e := range ws {
					if core.IsSceneState(e.Name) {
						sceneState = strings.TrimSpace(e.Content)
						continue
					}
					merged = append(merged, e)
				}
				merged = append(merged, triggered...)
				entries = merged
			}
		}
		if len(entries) > 0 {
			res := lore.Select(entries, cfg, recentLoreScan(ag.Messages()), lore.ApproxTokens)
			fired := res.All()
			if record && rec != nil {
				// Record the full activation trace (which entries fired, why, and
				// what the budget dropped) — the proposal's "no silent truncation"
				// rule: an overflowed token_budget must leave a user-visible trace.
				rec.Set(loreFiredOf(res.Fired))
			}
			if s := lore.Render(fired); s != "" {
				parts = append(parts, loreReferenceFrame(s))
			}
		}
		if sceneState != "" {
			parts = append(parts, sceneStateFrame(sceneState))
		}
		// The user-persona description — who the user is in the story — sits after
		// the world lore and before the card's instructions, framed so the model
		// attributes it to {{user}} rather than the character. Read live so a
		// user.bind mid-session takes effect next turn (shared-pointer record).
		// Gated on the persona having ANY content — a description, a gender, or
		// pronouns. It used to be gated on the DESCRIPTION alone, so a user who
		// picked their pronouns from the dropdown and wrote no bio got nothing at
		// all: not their pronouns, and not even the anti-inference steer this frame
		// exists to carry. That is the likeliest way to fill the form in (the
		// dropdowns are one click; the bio is work) and it was the one that silently
		// did nothing.
		{
			d := ""
			if userDesc != nil {
				d = strings.TrimSpace(userDesc.Get())
			}
			if d != "" || strings.TrimSpace(userGender) != "" || strings.TrimSpace(userPronouns) != "" {
				parts = append(parts, userPersonaFrame(userName, userPronouns, userGender, d))
			}
		}
		if phi != "" {
			parts = append(parts, phi)
		}
		// The author's note comes LAST (the PHI slot precedes it), read live so a
		// note.set mid-session takes effect next turn: the record is a shared
		// pointer the workspace writes and this per-turn closure reads.
		if note != nil {
			if s := strings.TrimSpace(note.Get()); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	}
}

// loreReferenceFrame frames the fired-lore block for the per-turn tail, where
// it rides a synthetic user message AFTER the whole transcript — the most
// recent thing the model reads before it writes.
//
// That position is why the frame exists. Lore is written to SET UP a scene and
// is then never revised: a setup entry says "the lieutenant is expected at
// first light and should be asked her team size", the scene plays, she arrives,
// she is asked — and the entry still says she is expected, still fires on its
// keys, and still lands last. Unframed, the model read it as the current
// moment and re-staged an arrival that had already happened. So the frame does
// one job: name this block as reference, not state, and hand the tiebreak to
// the transcript.
//
// It is deliberately the INVERSE of sceneStateFrame's trust clause, and the
// two are not in conflict — they are the hierarchy. The pin is maintained and
// outranks stale prose; ordinary lore is not maintained and loses to it.
//
// Only the per-turn tail is framed. The cached prefix (PlaceConstant) and the
// routed-voice system prompt sit BEFORE the conversation, where "the
// conversation above" names nothing.
// Two pieces of text sit above the block, and BOTH are separately strippable
// by an overlay. Whitespace-only means absent, for either one.
//
// It is spelled as whitespace rather than "" because the catalog cannot express
// an empty override: keyedText treats "" as a miss and falls back to the
// compiled English, so an empty overlay would silently serve the very text it
// was written to remove.
//
// Why the HEADER is strippable and not just the guard: the guard-off arm
// measured nothing, twice, and this header was the obvious suspect. It already
// says "background the scene draws on", so that arm was arguably still
// disclaimed and the comparison disclaimer-versus-disclaimer.
//
// Stripping both settled it, and the answer was NO -- ON WORLD-BACKGROUND
// CONTENT. The bare arm scores exactly what the shipped text scores there, the
// header was not the reason either, and nothing on that path was doing a job
// because there was no job to do: the reply-hijack does not occur for a lore
// entry about the world, framed or bare.
//
// THAT SCOPE TURNED OUT TO BE THE WHOLE FINDING. Point the same machinery at
// content about the MODEL'S OWN CAPABILITIES (scripts/eval,
// lore-capability-inventory) and the result inverts: header-only hijacks 5 of 5,
// bare is clean 5 of 5, shipped -- header plus guard -- recovers 6 of 10. The
// HEADER CAUSES the hijack on non-narrative content and the guard repairs it.
// Its wording is the mechanism: "setting, characters, and what came before"
// amounts to "here is context I am handing you", which invites "I understand,
// you are providing context that...". The header is inert on the content it was
// written for and actively harmful on content it was not.
//
// The rung stays, because the rule that motivated building it holds even though
// its first application guessed wrong: a control arm has to strip everything
// that COULD do the job under test, not merely the string under test. That is
// what turned a plausible story into a measurement that contradicted it.
//
// That gives four rungs the eval can stand on:
//
//	shipped    guard + header + block
//	guard-off  header + block          (overlays/tail-background-guard-off.json)
//	bare       block                   (overlays/tail-background-bare.json)
//	guard-last header + block + guard  (overlays/tail-background-guard-last.json)
//
// The first three vary the guard's PRESENCE. Only guard-last varies its
// POSITION, which is the dimension the measured hijack actually turned on --
// see tailBackgroundGuardTrailing.
//
// extdriver's section skips its guard the same way. It has no header to strip.
func loreReferenceFrame(block string) string {
	frame := block
	if header := strings.TrimSpace(i18n.P("lore.reference.frame",
		"REFERENCE KNOWLEDGE (setting, characters, and what came before — background the scene draws on, not a record of where it stands now; where this disagrees with the conversation above, the conversation is what actually happened):")); header != "" {
		frame = header + "\n" + block
	}
	if guard := strings.TrimSpace(tailBackgroundGuard()); guard != "" {
		frame = guard + "\n\n" + frame
	}
	if trailing := strings.TrimSpace(tailBackgroundGuardTrailing()); trailing != "" {
		frame = frame + "\n\n" + trailing
	}
	return frame
}

// tailBackgroundGuardTrailing renders the SAME prohibition as
// tailBackgroundGuard, after the block instead of before it. It is ABSENT by
// default: the compiled fallback is a single space, which TrimSpace reduces to
// "", so the shipped composition is byte-identical to the one that predates
// this key. Nothing ships trailing.
//
// It exists because position was the one dimension the eval could not vary.
// Both frames concatenated guard-then-body unconditionally, so every arm
// varied the guard's presence and none could build the configuration that
// actually hijacked -- the inactive-groups note took 0-of-20 final answers with
// its prohibition buried after the inventory, and 20-of-20 once it led
// (inactiveGroupNote, packages/core/agent.go).
//
// Two keys rather than one position flag because the catalog is the seam the
// eval already owns: an overlay sets this key to the guard text and blanks
// tail.background.guard, and no runtime knob has to ship to make the arm
// expressible. The single space is load-bearing for the same reason the other
// overlays use one -- keyedText treats "" as a MISS and falls back to the
// compiled English, so a genuinely empty default would serve the guard twice.
func tailBackgroundGuardTrailing() string {
	return i18n.P("tail.background.guard.trailing", " ")
}

// tailBackgroundGuard is the prohibition that leads a REFERENCE block on the
// ephemeral tail. The same key is rendered by extdriver for extension context
// cards, so both say exactly one thing and the catalog is the single place the
// wording lives.
//
// It is the BACKGROUND shape, and it is neither of the two guards that already
// exist. context.pressure.guard says to proceed "as if the note were not here",
// which is right for a note to ignore and wrong here: lore is the material the
// answer is supposed to draw on, and an extension's task card is state the model
// is told to consult. stall.guard says "act on it", which is wrong in the other
// direction: this block is not a directive. So it prohibits only the thing that
// was actually measured going wrong — replying to the block instead of the user
// — and leaves the model free to use what the block contains.
//
// It deliberately does NOT reach the rest of the host tail. A card's
// post-history instructions and the author's note are steering the author wrote
// to shape the next reply; telling the model not to act on those would switch
// off the strongest instrument a character card has.
//
// Prohibition-first because that ordering is measured rather than assumed: the
// inactive-groups note took 0-of-20 final answers before the prohibition led and
// 20-of-20 after (see inactiveGroupNote in packages/core/agent.go, which holds
// the record).
//
// That ordering is STRUCTURAL, not a catalog choice. loreReferenceFrame and
// extdriver's EphemeralContext both concatenate guard-then-body unconditionally,
// so no i18n overlay can move the guard to the end of the block. Which matters
// for how the eval's result should be read: scripts/eval measured this guard at
// both call sites, four runs, every rung at ceiling -- but every rung varied the
// guard's PRESENCE (guard-first versus absent). The configuration that actually
// hijacked is guard-LAST, and neither path can build it.
//
// Those ceilings were all on world-background content, and that qualifier is
// load-bearing. On capability-inventory content this guard is the difference
// between 0 of 5 and 6 of 10 (lore-capability-inventory): it repairs damage the
// REFERENCE KNOWLEDGE header inflicts. The guard is NOT inert, and the earlier
// "measured, changed nothing" reading was a statement about the fixtures rather
// than about the guard. Do not delete it.
//
// It used to carry a second clause, "not a request to act on". That clause is
// GONE, removed because it was measured and bought nothing: on Haiku 4.5, 10 of
// 10 runs obeyed an instruction-shaped lore entry with the clause present and 10
// of 10 obeyed it without (scripts/eval, lore-not-an-instruction). It was also
// wrong in principle at BOTH call sites. Lore is authored by the user, so a
// standing instruction in it is one the user meant; and an extension card is
// live guidance an extension wrote to be followed. Only the reply-hijack is
// prohibited.
func tailBackgroundGuard() string {
	return i18n.P("tail.background.guard",
		"[background] Do not reply to this block and do not mention it in your answer. It is background you may draw on.")
}

// sceneStateFrame frames the pinned scene-state card (SD4) for the per-turn
// tail. The trust clause is the point: the card's whole job is to outrank
// stale prose — the clock in the history says evening long after the scene
// moved to morning, and the model must know which one is current.
func sceneStateFrame(content string) string {
	return i18n.P("lore.scenestate.frame",
		"CURRENT SCENE STATE (pinned — kept current by the author and the director; when older prose in the history disagrees, this card is what is true now):") +
		"\n" + content
}

// userPersonaFrame frames the bound user-persona description so the model
// attributes it to {{user}} (the human in the scene), not the character. The
// name mirrors the {{user}} macro already baked into the charter/greeting.
func userPersonaFrame(name, pronouns, gender, desc string) string {
	label := UserPersonaLabel(name)
	id := strings.Join(UserPersonaIdentity(name, gender, pronouns), " ")
	d := strings.TrimSpace(desc)
	if d == "" {
		// No bio to introduce, so no "About X:" header — a colon promising a
		// description that never arrives reads as truncated output. The identity
		// clauses name the persona themselves, so they stand alone.
		return id
	}
	return i18n.P("persona.user.about", "About %s (the user you are interacting with):", label) + "\n" + d + "\n\n" + id
}

// UserPersonaLabel is how a prompt names the human in the scene: their bound
// persona name, or "The user" when they have not given one.
func UserPersonaLabel(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return i18n.P("persona.user.label", "The user")
}

// UserPersonaIdentity renders the gender/pronoun clauses for the human in the
// scene. Exported because the side-channel prompts need exactly this and were
// each hand-rolling a "Name: description" line that dropped identity entirely —
// the suggest drafter (writing AS the player) and the routed voice call (writing
// TO them) were the two paths most able to misgender someone, and the two that
// knew least. One function now, so they cannot drift apart again.
//
// State > steer > nothing. When the persona STATES its identity, tell the model
// to use it; when it does not, steer the model off inventing one (the dogfood
// gender-inference bug: "his back" / "Shopkeeper Kira" for a persona whose
// description gave no gender).
func UserPersonaIdentity(name, gender, pronouns string) []string {
	label := UserPersonaLabel(name)
	g, pr := strings.TrimSpace(gender), strings.TrimSpace(pronouns)
	// "Prefer not to say" is an answer, not a gender. Restating it as one ("their
	// gender: Prefer not to say") tells the model nothing and reads as a value;
	// the user declining to state it is precisely the case for the steer.
	if strings.EqualFold(g, "prefer not to say") {
		g = ""
	}

	var id []string
	if g != "" {
		id = append(id, i18n.P("persona.user.gender", "%s's gender: %s.", label, g))
	}
	switch {
	case pr != "":
		id = append(id, i18n.P("persona.user.pronouns", "Refer to %s with %s pronouns.", label, pr))
	case g != "":
		// Gender stated, pronouns not. The old text asserted the gender and then, in
		// the same breath, told the model not to assume one — so it either ignored
		// the contradiction or ignored the gender. Say the true, narrower thing.
		id = append(id, i18n.P("persona.user.pronouns_unstated", "Their pronouns are not stated — use their name or the second person rather than inferring pronouns from the above."))
	case strings.TrimSpace(name) == "":
		id = append(id, i18n.P("persona.user.second_person", "Refer to the user in the second person; do not assume a gender or pronouns they have not stated."))
	default:
		id = append(id, i18n.P("persona.user.by_name", "Refer to %s by name or in the second person; do not assume a gender or pronouns they have not stated.", label))
	}
	return id
}

// UserPersonaBrief is the compact one-block form for side-channel prompts: who
// the player is, then how to refer to them. Empty only when nothing is bound at
// all, so a caller can fall back to its own "not specified" wording.
func UserPersonaBrief(name, desc, gender, pronouns string) string {
	who := strings.TrimSpace(name)
	if d := strings.TrimSpace(desc); d != "" {
		if who != "" {
			who += ": " + d
		} else {
			who = d
		}
	}
	if who == "" && strings.TrimSpace(gender) == "" && strings.TrimSpace(pronouns) == "" {
		return ""
	}
	if who == "" {
		who = UserPersonaLabel(name)
	}
	return who + "\n" + strings.Join(UserPersonaIdentity(name, gender, pronouns), " ")
}

// recentLoreScan returns the visible text of recent messages, newest first,
// for the lore keyword scan. Only user/assistant text is included;
// tool_use / tool_result and other non-text blocks are skipped.
func recentLoreScan(msgs []provider.Message) []string {
	out := make([]string, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != provider.RoleUser && m.Role != provider.RoleAssistant {
			continue
		}
		var text strings.Builder
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				text.WriteString(tb.Text)
				text.WriteByte(' ')
			}
		}
		if t := strings.TrimSpace(text.String()); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// composeEphemeral combines context providers into one, concatenating their
// non-empty outputs blank-line separated. nil providers are dropped; if none
// remain the result is nil (leaving ContextProvider unset).
func composeEphemeral(providers ...func() string) func() string {
	var active []func() string
	for _, p := range providers {
		if p != nil {
			active = append(active, p)
		}
	}
	switch len(active) {
	case 0:
		return nil
	case 1:
		return active[0]
	default:
		return func() string {
			var parts []string
			for _, p := range active {
				if s := strings.TrimSpace(p()); s != "" {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, "\n\n")
		}
	}
}

// EphemeralTail names the live cards that sit ON TOP of a run's own per-turn
// tail, and — through compose — fixes the order they sit in.
//
// The per-turn context is a stack. Its bottom is the run's own tail: the
// triggered lore entries, then a card's post_history_instructions, which must
// stay last of everything (its after-history position, matching SillyTavern's
// PHI semantics — see tailProvider), so nothing may be appended after it. Above
// that sit the cards that change between turns: an extension manager's, and the
// built-in task board's.
//
// The fields ARE the list, for the usual reason: this stack is assembled in one
// place — a host's session build — and RE-assembled in another. RewireLoreContext
// re-derives the run's tail whenever a lore edit, a user-persona change or a
// trust flip changes what belongs in it, and it used to install that fresh tail
// BARE. Both live cards vanished for the rest of the session: the model lost
// sight of its own open work and of every extension's context, silently, in the
// three hosts that re-derive. One type, one order, both paths.
type EphemeralTail struct {
	// Ext is the extension manager's live context cards
	// (extensions.Manager.EphemeralContext). nil when a host runs none — which
	// is not a reason to withhold the task card, so it is a field here rather
	// than a condition around the call.
	Ext func() string

	// Tasks is the session's task controller, whose card tells the model what
	// work it has open. It follows the CONTROLLER, not the extension manager:
	// the board is a first-party feature and must not be switched off by an
	// unrelated subsystem being absent.
	Tasks *tasktool.Controller
}

// compose stacks this tail's cards onto base, the run's own per-turn tail.
//
// composeEphemeral puts each NEW provider FIRST, so the order here reverses
// into what the model reads: task card, then extension cards, then the run's
// tail. That is the order every host has always sent, and the reason the two
// wiring calls this replaced had to stay in their original sequence.
//
// base must be a RUN TAIL, never an already-composed provider: composing onto
// one of those would fold the same cards in a second time. Both callers pass a
// bare tail — the build path passes what NewAgent installed, the rewire path
// passes the fresh Resolve's — which is also why the rewire can hand the whole
// composed result to SetContextProvider in one write, instead of reading the
// live provider back out from under a turn that may be running.
func (t EphemeralTail) compose(base func() string) func() string {
	out := composeEphemeral(t.Ext, base)
	if t.Tasks != nil {
		out = composeEphemeral(t.Tasks.Ephemeral, out)
	}
	return out
}

// ExtEphemeral is m's live context-card provider, or nil when there is no
// manager.
//
// It exists so a host can fill EphemeralTail.Ext unconditionally instead of
// guarding the wiring call — guarding the call is how the task card came to be
// nested under an extension check in three hosts. Note that the method value
// cannot simply be taken from a nil manager: Manager embeds *extdriver.Driver,
// so `m.EphemeralContext` dereferences m where it is written, not where it is
// called.
func ExtEphemeral(m *extensions.Manager) func() string {
	if m == nil {
		return nil
	}
	return m.EphemeralContext
}

// WireEphemeralTail folds the live cards onto the agent's per-turn tail at
// session build — BOTH the live provider and its side-effect-free sizing twin,
// so /context never undercounts what a turn actually sends.
//
// Call it once per session, after the agent exists and OUTSIDE any extension
// check. Every mode's wiring goes through here; composing the providers by hand
// invites the drift this helper exists to prevent, and hand-composition is
// exactly how the re-derivation path came to disagree with the build path.
func WireEphemeralTail(ag *core.Agent, t EphemeralTail) {
	if ag == nil {
		return
	}
	ag.ContextProvider = t.compose(ag.ContextProvider)
	ag.ContextProviderPeek = t.compose(ag.ContextProviderPeek)
}

// RebindTasks re-keys the built-in task store to a session so the board follows
// the active session across open / resume / fork / /new / /cd / close. Call it
// wherever EmitSessionStart fires. nil ctrl is a no-op; a rebind error is
// swallowed (a persistence hiccup must not break session start).
func RebindTasks(ctrl *tasktool.Controller, sess *core.Session) {
	if ctrl == nil {
		return
	}
	id := ""
	if sess != nil {
		id = sess.ID
	}
	_ = ctrl.Rebind(id)
}

// LoreFired is one entry that fired on the last turn — its identity, whether it
// is constant, the trigger keys that matched (empty for a constant entry), and
// whether the budget dropped it. This is the activation-trace record the steering
// surfaces read to answer "why is the character's lore what it is."
type LoreFired struct {
	Name     string
	Source   string
	Constant bool
	Keys     []string
	Dropped  bool
}

// LoreFiredRecord holds the activation trace of the most recent turn behind a
// pointer on Resolved, so the (value-copied) Resolved shares one record:
// PerTurnContext (the turn goroutine) writes it, the steering surfaces (/lore,
// the Stage drawer, /context) read it — guarded by the mutex.
type LoreFiredRecord struct {
	mu    sync.Mutex
	fired []LoreFired
}

// Set replaces the recorded trace (the per-turn tail calls this each turn).
func (rec *LoreFiredRecord) Set(fired []LoreFired) {
	rec.mu.Lock()
	rec.fired = fired
	rec.mu.Unlock()
}

// Get returns a copy of the last turn's activation trace — nil before the first
// turn (or after a turn that fired no lore).
func (rec *LoreFiredRecord) Get() []LoreFired {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]LoreFired(nil), rec.fired...)
}

// loreFiredOf maps a lore.Select trace to the record's form. An entry's
// display source falls back to its name when it carries no Source (file
// path / "card" / name).
func loreFiredOf(fired []lore.Fired) []LoreFired {
	out := make([]LoreFired, 0, len(fired))
	for _, f := range fired {
		src := f.Entry.Source
		if src == "" {
			src = f.Entry.Name
		}
		out = append(out, LoreFired{
			Name:     f.Entry.Name,
			Source:   src,
			Constant: f.Entry.Constant,
			Keys:     f.Keys,
			Dropped:  f.Dropped,
		})
	}
	return out
}

// NoteRecord holds a live tail string behind a pointer on Resolved, so the
// (value-copied) Resolved shares one value: the workspace (the note.set /
// user.bind goroutine) writes it, the per-turn tail closure (turn goroutine)
// reads it — guarded by the mutex. It backs both the author's note and the
// user-persona description; either edit is visible next turn with no cache bust.
type NoteRecord struct {
	mu   sync.Mutex
	text string
}

// Set replaces the note text (note.set); "" clears it.
func (n *NoteRecord) Set(text string) {
	n.mu.Lock()
	n.text = text
	n.mu.Unlock()
}

// Get returns the current note text.
func (n *NoteRecord) Get() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.text
}

// Note returns the session's live author's-note record, or nil for a session
// that carries no note (a non-immersive session). The workspace retains this
// pointer so note.set can update the value the per-turn tail reads.
func (r *Resolved) Note() *NoteRecord {
	if r == nil {
		return nil
	}
	return r.note
}

// User returns the session's live user-persona description record, or nil for a
// non-immersive session. The workspace retains this pointer so user.bind can
// update the value the per-turn tail reads. Its twin, the user-persona NAME, is
// baked into the cached prefix (Args.As), not carried here.
func (r *Resolved) User() *NoteRecord {
	if r == nil {
		return nil
	}
	return r.userDesc
}

// LoreFired returns the session's lore activation-trace record — the shared
// pointer the per-turn tail writes each turn. The workspace retains it (and
// re-fetches it after a reloadLore that swaps the tail provider) so the lore
// surface and /context can show what fired last turn.
func (r *Resolved) LoreFired() *LoreFiredRecord {
	if r == nil {
		return nil
	}
	return r.loreFired
}

// WorldLoreRecord holds the session's live World lore entries behind a pointer
// on Resolved, the same shared-pointer contract as NoteRecord: the workspace
// (the world.lore.* goroutine) writes it, the per-turn tail closure (turn
// goroutine) reads it — guarded by the mutex. Entries are stored in engine
// form (lore.Entry) so the tail merges them straight into Select.
type WorldLoreRecord struct {
	mu      sync.Mutex
	entries []lore.Entry
}

// Set replaces the World lore entries (a world.lore.* edit); nil clears them.
func (w *WorldLoreRecord) Set(entries []lore.Entry) {
	w.mu.Lock()
	w.entries = entries
	w.mu.Unlock()
}

// Get returns the current World lore entries (the slice the record holds —
// callers must not mutate it; Set always replaces wholesale).
func (w *WorldLoreRecord) Get() []lore.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.entries
}

// WorldLore returns the session's live World-lore record, or nil for a
// non-immersive session. The workspace retains this pointer (seeding it from
// persisted meta on materialize) so world.lore.* edits update the entries the
// per-turn tail scans.
func (r *Resolved) WorldLore() *WorldLoreRecord {
	if r == nil {
		return nil
	}
	return r.worldLore
}

// newWorldLoreRecord allocates a live World-lore record for an immersive
// session, or nil for a coding session — the twin of newNoteRecord.
func newWorldLoreRecord(immersive bool) *WorldLoreRecord {
	if !immersive {
		return nil
	}
	return &WorldLoreRecord{}
}

// WorldLoreEntries maps persisted World lore (SessionMeta.WorldLore) onto
// engine entries for the per-turn scan. Source "world" tags the activation
// trace; DefaultOrder keeps world entries peers of a card's book, with input
// position (world first — see tailProvider) breaking the tie.
func WorldLoreEntries(meta []core.WorldLoreEntry) []lore.Entry {
	if len(meta) == 0 {
		return nil
	}
	out := make([]lore.Entry, 0, len(meta))
	for _, e := range meta {
		out = append(out, lore.Entry{
			Name:     e.Name,
			Keys:     append([]string(nil), e.Keys...),
			Constant: e.Constant,
			Order:    lore.DefaultOrder,
			Content:  e.Content,
			Source:   "world",
		})
	}
	return out
}
