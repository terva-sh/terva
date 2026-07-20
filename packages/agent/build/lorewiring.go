package build

import (
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
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
func (r *Resolved) tailProvider(ag *core.Agent, record bool) func() string {
	// A non-nil note/userDesc/worldLore record keeps the tail live even with no
	// lore/PHI, so a note, user persona, or World lore added later takes effect —
	// see Resolve, which allocates them only for immersive sessions.
	if r == nil || (len(r.loreTriggered) == 0 && r.postHistory == "" && r.note == nil && r.userDesc == nil && r.worldLore == nil) {
		return nil
	}
	triggered, cfg, phi, rec, note := r.loreTriggered, r.loreConfig, r.postHistory, r.loreFired, r.note
	world := r.worldLore
	userDesc, userName := r.userDesc, r.userName
	userGender, userPronouns := r.userGender, r.userPronouns
	return func() string {
		var parts []string
		// World lore (read live, so a world.lore.* edit lands next turn) joins the
		// file/card triggered entries in ONE Select: a shared budget, a shared
		// activation trace, and constants fire unconditionally there — World
		// constants deliberately ride this uncached tail, not the cached prefix,
		// because they are session-mutable. World entries lead the input so at
		// equal Order the stable sort gives the world's shared facts budget and
		// placement priority over a card's book.
		entries := triggered
		if world != nil {
			if ws := world.Get(); len(ws) > 0 {
				merged := make([]lore.Entry, 0, len(ws)+len(triggered))
				merged = append(merged, ws...)
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
				parts = append(parts, s)
			}
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
	return "About " + label + " (the user you are interacting with):\n" + d + "\n\n" + id
}

// UserPersonaLabel is how a prompt names the human in the scene: their bound
// persona name, or "The user" when they have not given one.
func UserPersonaLabel(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "The user"
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
		id = append(id, label+"'s gender: "+g+".")
	}
	switch {
	case pr != "":
		id = append(id, "Refer to "+label+" with "+pr+" pronouns.")
	case g != "":
		// Gender stated, pronouns not. The old text asserted the gender and then, in
		// the same breath, told the model not to assume one — so it either ignored
		// the contradiction or ignored the gender. Say the true, narrower thing.
		id = append(id, "Their pronouns are not stated — use their name or the second person rather than inferring pronouns from the above.")
	case label == "The user":
		id = append(id, "Refer to the user in the second person; do not assume a gender or pronouns they have not stated.")
	default:
		id = append(id, "Refer to "+label+" by name or in the second person; do not assume a gender or pronouns they have not stated.")
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

// WireExtEphemeral folds an extension manager's live context cards into the
// agent's per-turn tail — BOTH the live provider and its side-effect-free
// sizing twin, so /context never undercounts what a turn actually sends.
// Extension context is placed BEFORE the run's own tail: a card's
// post_history_instructions must stay last (its after-everything position,
// matching SillyTavern's PHI semantics — see tailProvider), so nothing may be
// appended after it. Every mode's ext wiring goes through here; composing the
// two providers by hand invites the drift this helper exists to prevent.
func WireExtEphemeral(ag *core.Agent, ephemeral func() string) {
	ag.ContextProvider = composeEphemeral(ephemeral, ag.ContextProvider)
	ag.ContextProviderPeek = composeEphemeral(ephemeral, ag.ContextProviderPeek)
}

// WireTasksEphemeral folds the built-in task controller's live context card into
// the agent's per-turn tail (live provider + sizing twin), the same way
// WireExtEphemeral folds extension cards — through composeEphemeral, so the card
// sits before the run's own lore/PHI tail. nil ctrl is a no-op.
func WireTasksEphemeral(ag *core.Agent, ctrl *tasktool.Controller) {
	if ctrl == nil {
		return
	}
	ag.ContextProvider = composeEphemeral(ctrl.Ephemeral, ag.ContextProvider)
	ag.ContextProviderPeek = composeEphemeral(ctrl.Ephemeral, ag.ContextProviderPeek)
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

// loreFiredOf maps a lore.Select trace to the record's form (resolving each
// entry's display source the way loreSourcesOf does).
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

// loreSourcesOf maps entries to their source labels (file path / "card" / name).
func loreSourcesOf(entries []lore.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		s := e.Source
		if s == "" {
			s = e.Name
		}
		out = append(out, s)
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
