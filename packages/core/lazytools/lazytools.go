// Package lazytools advertises only part of the tool registry (retro H2·b):
// the core group, the groups a session activated, and the tools an extension
// declares essential. Every other group stays hidden, still callable and still
// gated, and is offered to the model as a note on the ephemeral tail, which
// it acts on with activate_tools.
//
// It attaches through public seams, and the engine knows nothing of groups:
//
//   - Visibility implements core.ToolVisibility. The engine pins its
//     advertisement per segment, and re-pins when it grew, if BeginPrompt
//     asked for that (activation continuation).
//   - As a component, it adds the activation continuation gate as a Fallback
//     gate, so the host's gates for unfinished work outrank it.
//   - Each activation writes its tool_group row through core.Agent.AppendRecord.
//     A resume that forgot a group would re-send a different tools array.
//   - Segment is the inactive-group note, which the host's assembler puts on
//     the tail after its other Volatile segments. TailDelivered advances the
//     note's decay.
//   - It implements core.GroupRestorer, so core.Agent.Resume restores a
//     session's groups.
//
// Of finds the Visibility attached to an agent, which is how activate_tools and
// a skill reach it from a tool call.
package lazytools

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/i18n"
	"terva.sh/terva/packages/provider"
)

const (
	// NoteFull is the tail ID of the full inactive-group inventory, and
	// NoteBrief of its one-line standing form. They are separate IDs so the
	// decay is auditable: one is the noise a review found, the other is the
	// fix, and a single ID could not tell them apart in a log.
	NoteFull  = "capability.full"
	NoteBrief = "capability.brief"
)

// noteVerboseTurns is how many dispatches carry the full inactive-group
// inventory before it degrades to the one-line form. Three is enough for the
// model to have seen and weighed the offer; past that the same list re-arriving
// several hundred times is not information, and a model that answers it once
// then sees its own answer in the transcript answers it forever.
const noteVerboseTurns = 3

// continuationCap bounds the activation gate's fires per Prompt. Activation is
// monotonic (a group cannot be newly activated twice), so continuation chains
// are structurally bounded by the group count and this cap should never bind —
// defense in depth, deliberately a constant rather than configuration (the
// proposal's Decisions).
const continuationCap = 3

var (
	_ core.ToolVisibility    = (*Visibility)(nil)
	_ core.GroupRestorer     = (*Visibility)(nil)
	_ core.Binder            = (*Visibility)(nil)
	_ core.ContinuationGater = (*Visibility)(nil)
)

// Visibility is lazy tool visibility for one agent. The zero value is not
// usable; build one with New.
type Visibility struct {
	mu    sync.Mutex
	agent *core.Agent
	// active is the set of activated groups. base is the configured
	// always-active set New was given, kept so RestoreActiveGroups can rebuild
	// "config plus this session's" without unioning in whatever the outgoing
	// session had activated.
	active map[string]bool
	base   []string
	// pinned and pinnedReg are the active set and the registry at the engine's
	// last pin: what the current segment advertises. The note derives from
	// them, so the note and the advertised specs cannot disagree, and the gate
	// and Grew diff against them.
	pinned    map[string]bool
	pinnedReg core.Registry
	// continuationOff turns activation continuation off; the zero value keeps
	// it on, the agreed default. promptContinuation is its value at the start
	// of the running Prompt, which the gate reads.
	continuationOff    bool
	promptContinuation bool
	// noteFP and noteShown decay the note: the fingerprint of the inactive set
	// last dispatched, and how many dispatches carried the full inventory for
	// it. servedFP is the fingerprint of the note the last request segment
	// carried, which a delivery commits.
	noteFP    string
	noteShown int
	servedFP  string
}

// New returns a Visibility that advertises the core group plus the given
// always-active groups.
func New(active ...string) *Visibility {
	v := &Visibility{active: make(map[string]bool, len(active)), base: make([]string, 0, len(active))}
	for _, g := range active {
		if g != "" && g != core.CoreToolGroup {
			v.active[g] = true
			v.base = append(v.base, g)
		}
	}
	return v
}

// Of returns the Visibility attached to a, or nil when a has none (lazy tools
// off, or a is nil). Every method on a nil *Visibility is safe and reports the
// feature as off.
func Of(a *core.Agent) *Visibility {
	if a == nil {
		return nil
	}
	v, _ := a.ToolVisibility().(*Visibility)
	return v
}

// Bind implements core.Binder: it keeps the agent v reads. A host passes v to
// core.WithComponent, which calls Bind, makes v the tool visibility and adds
// the activation continuation gate.
func (v *Visibility) Bind(a *core.Agent) {
	v.mu.Lock()
	v.agent = a
	v.mu.Unlock()
}

// ContinuationGates implements core.ContinuationGater with the activation
// gate. It is a Fallback gate, so a gate for unfinished work outranks it.
func (v *Visibility) ContinuationGates() []core.ContinuationGate {
	return []core.ContinuationGate{{
		Cause:    "activation",
		Cap:      continuationCap,
		Fallback: true,
		Fire:     v.fire,
	}}
}

// Advertise implements core.ToolVisibility: the core group, the active groups,
// and every essential tool.
func (v *Visibility) Advertise(reg core.Registry, pin bool) func(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	active := copySet(v.active)
	if pin {
		v.pinned, v.pinnedReg = active, reg
	}
	return visibleIn(reg, active)
}

// Grew implements core.ToolVisibility.
func (v *Visibility) Grew(reg core.Registry) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.newlyActiveLocked(reg)) > 0
}

// BeginPrompt implements core.ToolVisibility. It snapshots activation
// continuation for the Prompt, so a live toggle takes effect on the next one.
func (v *Visibility) BeginPrompt() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.promptContinuation = !v.continuationOff
	return v.promptContinuation
}

// fire is the activation continuation gate. It fires when the ended segment
// newly activated a group, so a model that deliberately finished its reply
// after activate_tools is re-prompted with those tools actually live. It diffs
// against the current pin, and the boundary then re-pins.
func (v *Visibility) fire(provider.StopReason) (string, bool) {
	v.mu.Lock()
	on, a := v.promptContinuation, v.agent
	v.mu.Unlock()
	// on is set only by BeginPrompt, which the engine calls on the agent's own
	// visibility, so a gate from any other one stays quiet here.
	if !on || a == nil {
		return "", false
	}
	// Outside the lock: the snapshot takes the agent's.
	reg := a.ToolsSnapshot()
	v.mu.Lock()
	newly := v.newlyActiveLocked(reg)
	v.mu.Unlock()
	if len(newly) == 0 {
		return "", false
	}
	return "[activation continuation] Now live: " + strings.Join(newly, ", ") + ". Continue where you left off.", true
}

// newlyActiveLocked returns the sorted names of tools in reg whose group is
// active now but was not at the pin: what a continuation announces as newly
// live, and the growth test for a re-pin. An essential tool was already
// advertised at the pin, so it is never newly live. v.mu is held.
func (v *Visibility) newlyActiveLocked(reg core.Registry) []string {
	var names []string
	for name, t := range reg {
		g := core.ToolGroup(t)
		if g == core.CoreToolGroup || v.pinned[g] || !v.active[g] || core.ToolEssential(t) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SetContinuation turns activation continuation on or off: the gate that
// resumes a Prompt whose segment activated a group, and the re-pin that makes
// its tools live (docs/proposals/activation-continuation.md). On by default.
// It takes effect on the next Prompt.
func (v *Visibility) SetContinuation(on bool) {
	if v == nil {
		return
	}
	v.mu.Lock()
	v.continuationOff = !on
	v.mu.Unlock()
}

// ContinuationEnabled reports whether activation continuation is on.
// activate_tools reads it to tell the model whether it will be continued
// automatically after it finishes its reply. False on a nil Visibility.
func (v *Visibility) ContinuationEnabled() bool {
	if v == nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.continuationOff
}

// Activate marks a group advertised from the next pin. It returns false if the
// group was already active, or is the always-on core group. Visibility only:
// it never grants authority.
//
// A real activation writes a tool_group row. That is not bookkeeping: the tools
// array sits ahead of the system prompt and every message in the provider's
// cached prefix, so a resume that forgets an activated group re-sends a
// different tools array and invalidates the whole transcript, then invalidates
// it a second time when the model notices the tool is gone and re-activates.
// One measured session paid ~$3.13 that way.
func (v *Visibility) Activate(group string) bool {
	if v == nil || group == "" || group == core.CoreToolGroup {
		return false
	}
	v.mu.Lock()
	if v.active == nil {
		v.active = map[string]bool{}
	}
	if v.active[group] {
		v.mu.Unlock()
		return false
	}
	v.active[group] = true
	a := v.agent
	v.mu.Unlock()
	record(a, group)
	return true
}

// ActivateForTools activates the groups of the named tools: a skill declaring
// the tools it needs (its allowed-tools) surfaces their groups on load.
// Visibility only, so it never grants authority. Names absent from the agent's
// registry, and core-group names, are skipped. It returns the groups newly
// activated, sorted.
func (v *Visibility) ActivateForTools(names []string) []string {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	a := v.agent
	v.mu.Unlock()
	if a == nil {
		return nil
	}
	reg := a.ToolsSnapshot()
	v.mu.Lock()
	if v.active == nil {
		v.active = map[string]bool{}
	}
	var activated []string
	for _, n := range names {
		t, ok := reg[n]
		if !ok {
			continue
		}
		g := core.ToolGroup(t)
		if g == core.CoreToolGroup || v.active[g] {
			continue
		}
		v.active[g] = true
		activated = append(activated, g)
	}
	v.mu.Unlock()
	sort.Strings(activated)
	// Persisted like an activate_tools activation, and for the same reason.
	for _, g := range activated {
		record(a, g)
	}
	return activated
}

// record writes a group's tool_group row. Outside v.mu, because the write
// takes the store's lock and an observer of it may call back in.
func record(a *core.Agent, group string) {
	if a == nil {
		return
	}
	a.AppendRecord(func(s core.TranscriptStore) error { return s.AppendToolGroupActivation(group) })
}

// RestoreActiveGroups makes the active set exactly the configured always-active
// groups plus the ones the given session activated. It implements
// core.GroupRestorer, and terva calls it at session binding.
//
// It REPLACES rather than unions, because binding also happens on a session
// SWITCH (resume, fork, /new, /cd). Unioning would let a group activated in the
// outgoing session leak into the incoming one, which advertises tools that
// session has no tool_group row for, so its next resume would drop them and pay
// the very invalidation this exists to prevent.
//
// A group whose extension is no longer installed is harmless: visibility
// resolves against the live registry, which has no tools in it. It writes no
// rows, because these activations are already on disk.
func (v *Visibility) RestoreActiveGroups(groups []string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.active = make(map[string]bool, len(v.base)+len(groups))
	for _, g := range v.base {
		v.active[g] = true
	}
	for _, g := range groups {
		if g != "" && g != core.CoreToolGroup {
			v.active[g] = true
		}
	}
}

// Active returns the activated groups, sorted.
func (v *Visibility) Active() []string {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.active))
	for g := range v.active {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Advertised reports the live advertisement as a predicate over tool names:
// what the next pin would send. filtered is false on a nil Visibility, and then
// visible admits every tool. /context uses it to separate the advertised weight
// from installed-but-inactive schemas.
func (v *Visibility) Advertised() (visible func(name string) bool, filtered bool) {
	if v == nil {
		return func(string) bool { return true }, false
	}
	v.mu.Lock()
	a := v.agent
	active := copySet(v.active)
	v.mu.Unlock()
	var reg core.Registry
	if a != nil {
		reg = a.ToolsSnapshot()
	}
	return visibleIn(reg, active), true
}

// Note returns the inactive-group note as the next request would carry it,
// from the live state, in the form the decay picks. Empty when nothing is
// hidden. /context uses it for the bytes deferred discovery costs.
func (v *Visibility) Note() string {
	if v == nil {
		return ""
	}
	v.mu.Lock()
	a := v.agent
	v.mu.Unlock()
	if a == nil {
		return ""
	}
	reg := a.ToolsSnapshot()
	v.mu.Lock()
	defer v.mu.Unlock()
	text, _, _ := v.noteLocked(reg, v.active)
	return text
}

// Segment is the note as a Volatile segment, tagged NoteFull or NoteBrief. For
// a request it derives from the pinned state, so it names the groups the
// request's specs leave out. For a peek it derives from the live state, which
// the next pin will use. Side-effect free apart from remembering what a request
// was served, which TailDelivered commits: the engine re-assembles per retry
// attempt, and every attempt must carry the same note.
func (v *Visibility) Segment(mode core.AssembleMode) core.Segment {
	seg := core.Segment{Stability: core.Volatile, Tag: NoteFull}
	if v == nil {
		return seg
	}
	var reg core.Registry
	var active map[string]bool
	v.mu.Lock()
	if mode == core.AssembleRequest && v.pinned != nil {
		reg, active = v.pinnedReg, v.pinned
		v.mu.Unlock()
	} else {
		a := v.agent
		v.mu.Unlock()
		if a == nil {
			return seg
		}
		reg = a.ToolsSnapshot()
		v.mu.Lock()
		active = copySet(v.active)
		v.mu.Unlock()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	text, id, fp := v.noteLocked(reg, active)
	seg.Tag, seg.Content = id, text
	if mode == core.AssembleRequest {
		v.servedFP = fp
	}
	return seg
}

// TailDelivered advances the note's decay when a request carried it. A changed
// inactive set restarts the verbose run, so a newly installed extension is
// announced in full rather than inheriting the previous set's silence. A
// continue turn carries no tail, so it never advances the decay.
func (v *Visibility) TailDelivered(ids []string) {
	if v == nil {
		return
	}
	carried := false
	for _, id := range ids {
		if id == NoteFull || id == NoteBrief {
			carried = true
			break
		}
	}
	if !carried {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.servedFP != v.noteFP {
		v.noteFP, v.noteShown = v.servedFP, 1
		return
	}
	if v.noteShown < noteVerboseTurns {
		v.noteShown++
	}
}

// noteLocked picks the note's form for reg and active: the full inventory
// until it has been delivered noteVerboseTurns times for this inactive set,
// the brief line after. It returns the text, its tail ID and the set's
// fingerprint. v.mu is held.
func (v *Visibility) noteLocked(reg core.Registry, active map[string]bool) (text, id, fp string) {
	groups, byGroup := inactiveGroups(reg, active)
	if len(groups) == 0 {
		return "", NoteFull, ""
	}
	fp = strings.Join(groups, "\x00")
	if fp != v.noteFP || v.noteShown < noteVerboseTurns {
		return fullNote(v.translatorLocked(), groups, byGroup), NoteFull, fp
	}
	return briefNote(v.translatorLocked(), groups), NoteBrief, fp
}

// translatorLocked is the translator of the agent v is bound to, nil for the
// process-wide one. v.mu is held.
func (v *Visibility) translatorLocked() i18n.Translator {
	if v.agent == nil {
		return nil
	}
	return v.agent.Translator()
}

// ToolsInGroup returns the names of the tools in reg in a group, sorted. Empty
// means no such group is installed (an activate_tools guard).
func ToolsInGroup(reg core.Registry, group string) []string {
	var names []string
	for name, t := range reg {
		if core.ToolGroup(t) == group {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ToolSpecsInGroup returns the provider specs (name, description, schema) of
// the tools in reg in a group, name-sorted for a stable render. activate_tools
// echoes these into its result so the model can compose its next call at once,
// since an activated group's schemas reach the advertised tools only at the
// next pin. It reuses SpecsVisible, so the order matches the advertised one.
func ToolSpecsInGroup(reg core.Registry, group string) []provider.Tool {
	return reg.SpecsVisible(func(name string) bool {
		t, ok := reg[name]
		return ok && core.ToolGroup(t) == group
	})
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if v {
			out[k] = true
		}
	}
	return out
}

// visibleIn advertises a tool iff its group is core or active, or the tool is
// declared essential (load-bearing) by its extension: an essential tool stays
// advertised even from an inactive group, so guidance that names it does not
// point at a deferred tool. A name absent from reg is advertised (never hidden
// by a stale predicate).
func visibleIn(reg core.Registry, active map[string]bool) func(name string) bool {
	return func(name string) bool {
		t, ok := reg[name]
		if !ok {
			return true
		}
		g := core.ToolGroup(t)
		return g == core.CoreToolGroup || active[g] || core.ToolEssential(t)
	}
}

// fullNote summarizes the groups hidden this turn — the model reads it
// from the ephemeral tail and can bring one in with activate_tools. groups is
// never empty. It lists tool names (not schemas) so discovery costs a few
// bytes, not the whole schema (retro H2·b: the cache-cheap capability line).
func fullNote(tr i18n.Translator, groups []string, byGroup map[string][]string) string {
	var b strings.Builder
	// Model-facing prompt injection (rides the ephemeral tail), so it is
	// translatable via the prompts catalog.
	//
	// The prohibition comes FIRST, before the inventory it governs, and that
	// order is measured — do not reorder it without re-running the A/B
	// (scripts/eval, `session-inspect-cost` final-answer row). Both
	// predecessors failed differently. The original imperative opener ("Call
	// activate_tools with a group name to load them") read as a question
	// re-asked every turn: one reviewed session answered it 109 times in 217
	// assistant messages — the note is ephemeral and costs no cache, but the
	// model's ANSWER lands in the transcript, is re-sent every turn, and
	// survives compaction. The inventory phrasing that fixed that buried
	// "needs no reply" mid-block, and on first exposure Haiku answered the
	// note INSTEAD of the user in 20 of 20 runs — right tool call, right
	// result, answer displaced on the way out. Prohibition-first recovered
	// 20 of 20 answers on the same A/B (2026-08).
	b.WriteString(i18n.In(tr).P("tools.lazy.inactive_groups",
		"[inactive tool groups] Do not reply to this note. Do not mention it in your answer. Complete the request of the user as if the note were not here. The note lists installed capabilities whose tool schemas are not loaded this turn. If a task needs one, `activate_tools <group>` loads it. The load changes visibility only, and each tool still requires its normal permission when used:"))
	for _, g := range groups {
		names := byGroup[g]
		sort.Strings(names)
		fmt.Fprintf(&b, "\n  - %s: %s", g, strings.Join(names, ", "))
	}
	return b.String()
}

// briefNote is the note's standing form: group names only, one line,
// no per-tool inventory. The full note is information the first few times it
// appears and noise for the several hundred turns after, during which the
// inactive set has not changed and the model has already decided. This keeps
// activate_tools' description honest (it points at "the [inactive tool groups]
// note") without re-asking.
func briefNote(tr i18n.Translator, groups []string) string {
	return i18n.In(tr).P("tools.lazy.inactive_groups_brief",
		"[inactive tool groups] Do not reply to this note. If a task needs a group, `activate_tools <group>` loads it. Inactive: %s",
		strings.Join(groups, ", "))
}

// inactiveGroups collects the hidden groups and their tool names, both sorted.
// The fingerprint the decay keys on is derived from this, so a group appearing
// or disappearing re-shows the full note while a stable set stays quiet.
func inactiveGroups(reg core.Registry, active map[string]bool) ([]string, map[string][]string) {
	byGroup := map[string][]string{}
	for name, t := range reg {
		g := core.ToolGroup(t)
		// Essential tools are already advertised (visibleIn), so they are
		// not "inactive" — listing them here would tell the model to
		// activate_tools for a tool it can already see. A group all of whose
		// tools are essential drops out of the note entirely.
		if g == core.CoreToolGroup || active[g] || core.ToolEssential(t) {
			continue
		}
		byGroup[g] = append(byGroup[g], name)
	}
	if len(byGroup) == 0 {
		return nil, nil
	}
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return groups, byGroup
}
