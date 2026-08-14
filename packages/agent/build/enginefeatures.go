package build

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// Engine features: runtime-toggleable agent-loop behaviors
// (docs/proposals/activation-continuation.md stage 3). A feature is a
// declaration — id, display strings, default, and how it lands on a live
// agent — projected into the workspace settings surface (one SettingItem per
// feature, so the web panel, the TUI /settings dialog, and any ctrlproto
// client get the toggle for free) and applied at the NewAgent funnel every
// host passes through. Persisted overrides live in config
// `engine_features` keyed by id; absent ids use the default, which keeps a
// hand-edited config for headless runs one line.
//
// This is a seam, not a platform: it holds the handful of loop behaviors whose
// value is genuinely still open, and correctness behaviors (the open-work and
// swarm-hold continuation gates) are NOT features — they have no off switch. A
// feature earns its toggle by having a real question attached to it; when the
// question is answered, the toggle should go away with it.

// EngineFeature is one toggleable loop behavior. Title and Desc are declared
// with i18n.M (init time, before i18n.Configure) and translated at render.
type EngineFeature struct {
	ID      string
	Title   string
	Desc    string
	Default bool
	// RequiresLazyTools hides the toggle while lazy tools are off — for a
	// feature that is meaningless without them (the settings pane nests it
	// under the lazy_tools toggle the way auto_swarm_nudge nests under
	// auto_swarm). It gates only display: Apply still runs at build, where
	// the feature's own semantics decide whether it matters.
	RequiresLazyTools bool
	Apply             func(a *core.Agent, on bool)
}

// EngineFeatures lists every engine feature, in display order.
var EngineFeatures = []EngineFeature{
	{
		ID:                "activation_continuation",
		Title:             i18n.M("Activation continuation"),
		Desc:              i18n.M("When the agent activates a tool group and finishes its reply, automatically continue it with those tools live instead of waiting for your next message."),
		Default:           true,
		RequiresLazyTools: true,
		Apply:             func(a *core.Agent, on bool) { a.SetActivationContinuation(on) },
	},
	{
		ID:    "cache_aware_compaction",
		Title: i18n.M("Cache-aware compaction"),
		Desc:  i18n.M("Summarize the conversation using the prompt the provider already has cached, instead of building a separate one. Measured at 99.5% cache-served on Anthropic — the bespoke summarizer re-reads the whole conversation at full price. Turn it off to go back to the dedicated summarizer, which gets a purpose-built prompt rather than writing from inside the agent's own persona."),
		// ON, to dogfood it. The cost side is measured and settled: 99.5%
		// cache-served on Anthropic, 84.9% on codex, zero tool_use fallbacks
		// (docs/plans/cache-aware-compaction-ab.md).
		//
		// What is NOT settled is summary QUALITY on a real transcript, and the
		// pre-registered rule said not to flip the default until it was. This
		// overrides that rule deliberately and with the reason on record: living
		// with the feature is how the quality evidence gets gathered, and the
		// alternative — a synthetic A/B nobody has time to run — leaves a 6x
		// saving unclaimed indefinitely. The rule still governs whether it STAYS
		// on; see the plan's decision criteria, which are unchanged.
		//
		// The risk being accepted, stated plainly: a MID-TURN compaction whose
		// summary omits an already-executed side effect makes the resuming agent
		// re-run it. That is what the cold path's dedicated summarizer prompt was
		// buying, and it is the one failure mode worth watching for.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetCacheAwareCompaction(on) },
	},
	{
		ID:    "provider_compaction",
		Title: i18n.M("Let the provider compact the conversation"),
		Desc:  i18n.M("Some backends compact a conversation themselves and hand back a replacement transcript: your messages verbatim, and one encrypted summary standing in for everything the assistant said. It is cheaper than writing a summary, and it is not portable — only the provider that made it can read it, so switching providers means rebuilding the conversation from the session file and compacting again. Off by default: the strategy works, but whether it actually buys back the prompt cache has not been measured yet. Only OpenAI Codex offers it today; on every other provider this does nothing."),
		// OFF, and the A/B that was supposed to decide this has now been RUN
		// (2026-08-04, 3 reps, $2.82 live — docs/reviews/…-cache-collapse.md §10).
		// It came back against the feature, so this default is a result rather
		// than a placeholder:
		//
		//   - /responses/compact NEVER reads the prompt cache. Cache read was 0
		//     in 3 of 3 reps, on a transcript the backend demonstrably held —
		//     the warm summarizer read that same content at 97% on the next
		//     call. It is a different route and does not participate.
		//   - So the compaction call is ~4.7x MORE expensive, repeatably:
		//     median $0.0731 against the warm summarizer's $0.0154, which is
		//     cheap precisely because it reuses the conversation's own prefix.
		//   - And the post-compaction saving that was supposed to pay for that
		//     is undetectable at n=3 (medians 74.9k vs 84.0k full-price tokens,
		//     ranges overlapping).
		//
		// The honest boundary: the harness reproduced the cache SCATTER but
		// never the collapse, so it cannot say the strategy fails to fix the
		// collapse — only that it costs more and buys nothing measurable in a
		// healthy session. Re-open it against a REAL collapsed session, which
		// needs the recovery trigger wired first.
		//
		// The risk being held back is unchanged and independent of cost: a blob
		// replayed to a provider that cannot decrypt it is amnesia with no
		// symptom, and the recovery path that catches it is not wired yet.
		Default: false,
		Apply:   func(a *core.Agent, on bool) { a.SetProviderCompaction(on) },
	},
	{
		ID:    "prefix_change_guard",
		Title: i18n.M("Offer to compact before a cache-invalidating change"),
		Desc:  i18n.M("When something changes the prompt the provider has cached — switching model, reloading an extension — the next message silently re-reads the whole conversation at full price. Ask first, and offer to condense it while the old prompt is still cached. Needs cache-aware compaction to be on: without it, compacting costs the same full-price read as sending, so there would be nothing to offer."),
		// Now that cache-aware compaction defaults on, this comes alive with it —
		// it was written to be inert without it, because the offer is only honest
		// when there is a saving behind it. Measured: an unguarded model switch on
		// a 66k transcript cost $0.42; guarded, $0.066.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetPrefixChangeGuard(on) },
	},
	{
		ID:    "stuck_loop_detection",
		Title: i18n.M("Detect stuck tool loops"),
		Desc:  i18n.M("Notice when the model repeats the same tool call, or hits the same error, over and over without progress — and nudge it once to read state or change tack instead of spinning. In-band and local only: it never switches models or sends anything anywhere. The escalation half (offer to hand a stuck step to a stronger model) builds on this and ships separately."),
		// ON. Detection + a one-turn nudge is safe — no model swap, no egress, just
		// an ephemeral note when the model provably spins (same call, or same
		// error, three times in a short window). Grounded in a real session where a
		// small local model repeated one failing task_update 18 times; the escape
		// hatches that act on the signal (ask, escalate) are opt-in and land later.
		// See docs/proposals/stuck-loop-escalation.md.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetStallDetection(on) },
	},
	{
		ID:    "stuck_loop_escalation",
		Title: i18n.M("Escalate stuck loops to a stronger model"),
		Desc:  i18n.M("When a tool loop keeps going after the nudge, offer to hand the stuck step to a stronger model and continue on it — the swap you'd otherwise make by hand. Requires the detector above (it's the trigger) and an escalation target in config (escalation.provider + escalation.model); with no target it does nothing. You're asked before the swap, which sends the conversation to that provider."),
		// ON, but INERT without a configured target and a host that binds an
		// Escalator (a nil Escalator makes the runLoop driver a no-op). So "on"
		// surprises no one: a user with no escalation.target set never sees it, and
		// one who sets a target is asked before any swap (ask-first; auto is 3c).
		// This mirrors prefix_change_guard, which is declared-on but inert without
		// cache-aware compaction. See docs/plans/stuck-loop-escalation-rung3.md.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetStuckLoopEscalation(on) },
	},
	{
		ID:    "prefix_divergence_recording",
		Title: i18n.M("Record cache-invalidating prompt changes"),
		Desc:  i18n.M("Providers cache by matching the start of the prompt, so a conversation only stays cheap while each request merely APPENDS to the last. When something rebuilds the prompt instead, everything after the change is re-read at full price and nothing says so — it shows up only as a cost you cannot explain. This records what changed and where, in the session file, whenever it happens."),
		// ON, and the argument for that is the whole point: a diagnostic that
		// ships off is never enabled at the moment the rare thing happens, and
		// this one's entire value is retrospective — reading rows back out of a
		// session nobody knew would go wrong. Two measured sessions lost 10.5%
		// and 32.2% of their spend to zero-cache turns with no trace of why.
		//
		// It earns "always on" by being quiet and cheap. Quiet: it writes only
		// when the prompt was rebuilt rather than extended, which is never in a
		// healthy session. Cheap: measured at 2.6ms and 1.8MB for a 1500-message,
		// 797KB transcript (BenchmarkBuildPrefixLadder) — around 0.02% of a
		// request that spends seconds at the provider.
		//
		// The toggle exists anyway, for the case that benchmark does NOT cover: a
		// transcript carrying large images, whose bytes are re-hashed per request.
		// Nobody has measured that, and an off switch is cheaper than finding out
		// the hard way.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetPrefixDivergenceRecording(on) },
	},
	{
		ID:    "shell_result_context",
		Title: i18n.M("Show ! command results to the agent"),
		Desc:  i18n.M("When you run a command with !, hand its output to the agent along with your next message, so you can ask about what you just ran instead of pasting it back in. The output rides one request and is never written to the conversation. Off by default because it changes where that output goes: today a ! command's result stays on this machine, and turning this on sends it to your model provider — including whatever `!env` or `!cat` happens to print."),
		// OFF, and this one is not a taste question. Every other feature here is
		// off or on according to whether it HELPS; this one is off according to
		// where data goes. The escape's output is local today — its comment in
		// interactive_shell.go says the block is parked "so it never enters the
		// model conversation" — and enabling this sends it to a provider. That is
		// the leak vector the secrets-at-rest work is built around, arrived at
		// without the model doing anything.
		//
		// So the default is off, the description says what enabling it means
		// rather than only what it does, and the client checks before sending: a
		// daemon reached over `terva serve` may be on another host, and gating
		// only here would put the output on the wire to that host before
		// discarding it there. Core is the authority anyway, because a client
		// that skips the check must not get to decide this for the user.
		Default: false,
		Apply:   func(a *core.Agent, on bool) { a.SetShellResultContext(on) },
	},
	{
		ID:    "transport_recording",
		Title: i18n.M("Record which connection each request rode"),
		Desc:  i18n.M("Some cache misses have nothing to do with the prompt: the request simply reached the provider over a fresh connection or a different edge, and landed on a machine that had never seen the conversation. This records the transport picture of each request — connection reuse, edge identifier, the provider's request id — next to its usage row, on providers that report it, so an unexplained expensive stretch can be read against how those requests physically travelled."),
		// ON, for the prefix recorder's reason: this diagnostic's value is
		// retrospective, and the sessions it exists for are the ones nobody
		// knew would go wrong (one measured session burned ~$100 of full-price
		// re-reads with byte-stable prompts — the layer this records was the
		// only one with no data). It is quiet in the sense that matters —
		// one small row per request, no extra network traffic, a few header
		// reads — and the off switch exists for anyone who would rather not
		// persist edge/request identifiers in session files.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetTransportRecording(on) },
	},
}

// EngineFeatureByID resolves a feature by its id (the settings-action lookup).
func EngineFeatureByID(id string) (EngineFeature, bool) {
	for _, f := range EngineFeatures {
		if f.ID == id {
			return f, true
		}
	}
	return EngineFeature{}, false
}

// EngineFeatureOn resolves a feature's on/off from the persisted overrides
// (config `engine_features`), falling back to its default.
func EngineFeatureOn(overrides map[string]bool, f EngineFeature) bool {
	if v, ok := overrides[f.ID]; ok {
		return v
	}
	return f.Default
}
