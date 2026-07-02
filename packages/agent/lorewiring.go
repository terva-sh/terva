package agent

import (
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// perTurnContext returns a per-turn context provider for this run's uncached
// tail: the triggered lore entries that match the agent's recent messages,
// followed by a card's post_history_instructions (static, but positioned
// after history). Returns nil when there's neither, so composeEphemeral skips
// it entirely.
//
// The closure captures ag and reads ag.Messages() at call time — the core
// invokes ContextProvider once per turn, after copying the transcript and
// outside its lock (see core/agent.go), so this is safe and always sees the
// latest turn. PHI comes last, matching SillyTavern's after-history position.
func (r *Resolved) perTurnContext(ag *core.Agent) func() string {
	return r.tailProvider(ag, true)
}

// perTurnContextPeek is the side-effect-free twin of perTurnContext: it renders
// the same uncached tail from the agent's current messages but does NOT record
// which lore fired. Wired to core.Agent.ContextProviderPeek so the UI can SIZE
// the tail (e.g. /context) without overwriting the "fired last turn" record.
func (r *Resolved) perTurnContextPeek(ag *core.Agent) func() string {
	return r.tailProvider(ag, false)
}

// tailProvider builds the per-turn tail closure. When record is true the
// resulting closure notes which lore fired (for /lore); when false it is pure.
func (r *Resolved) tailProvider(ag *core.Agent, record bool) func() string {
	if r == nil || (len(r.loreTriggered) == 0 && r.postHistory == "") {
		return nil
	}
	triggered, cfg, phi, rec := r.loreTriggered, r.loreConfig, r.postHistory, r.loreFired
	return func() string {
		var parts []string
		if len(triggered) > 0 {
			res := lore.Select(triggered, cfg, recentLoreScan(ag.Messages()), lore.ApproxTokens)
			fired := res.All()
			if record && rec != nil {
				// Record what fired AND what the budget dropped, for /lore —
				// the proposal's "no silent truncation" rule: an overflowed
				// token_budget must leave a user-visible trace.
				rec.set(loreSourcesOf(fired), loreSourcesOf(res.Dropped))
			}
			if s := lore.Render(fired); s != "" {
				parts = append(parts, s)
			}
		}
		if phi != "" {
			parts = append(parts, phi)
		}
		return strings.Join(parts, "\n\n")
	}
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

// wireExtEphemeral folds an extension manager's live context cards into the
// agent's per-turn tail — BOTH the live provider and its side-effect-free
// sizing twin, so /context never undercounts what a turn actually sends.
// Extension context is placed BEFORE the run's own tail: a card's
// post_history_instructions must stay last (its after-everything position,
// matching SillyTavern's PHI semantics — see tailProvider), so nothing may be
// appended after it. Every mode's ext wiring goes through here; composing the
// two providers by hand invites the drift this helper exists to prevent.
func wireExtEphemeral(ag *core.Agent, ephemeral func() string) {
	ag.ContextProvider = composeEphemeral(ephemeral, ag.ContextProvider)
	ag.ContextProviderPeek = composeEphemeral(ephemeral, ag.ContextProviderPeek)
}

// loreFiredRecord holds the lore entry sources that fired — and that the
// token budget dropped — on the most recent turn. It sits behind a pointer on
// Resolved so the (value-copied) Resolved shares one record: perTurnContext
// (turn goroutine) writes it, /lore (TUI goroutine) reads it — guarded by the
// mutex.
type loreFiredRecord struct {
	mu      sync.Mutex
	sources []string
	dropped []string
}

func (rec *loreFiredRecord) set(sources, dropped []string) {
	rec.mu.Lock()
	rec.sources = sources
	rec.dropped = dropped
	rec.mu.Unlock()
}

func (rec *loreFiredRecord) get() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.sources...)
}

func (rec *loreFiredRecord) getDropped() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.dropped...)
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
