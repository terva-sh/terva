package core

import (
	"bytes"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// prefixCacheTTL is how long a provider holds a cached prefix without a hit.
// Anthropic's default is five minutes and terva never asks for the one-hour
// tier (Request carries no TTL field), so past this the prefix is already cold:
// the change costs nothing extra, and offering to save the re-read would be
// offering to save money that is already gone.
const prefixCacheTTL = 5 * time.Minute

// prefixChangeMinTokens is the smallest cached prefix worth interrupting
// someone over. Below it the invalidation is real but trivial, and a blocking
// dialog costs more attention than the tokens cost money. Deliberately a
// constant, and deliberately high: this guard's entire value is that it fires
// rarely and means something when it does.
const prefixChangeMinTokens = 50_000

// promptPrefix is the cached prompt prefix of the last request terva actually
// put on the wire — the VALUES, not a digest of them.
//
// Providers cache by prefix match, and the render order is tools → system →
// messages: a change anywhere invalidates everything after it, and a model or
// endpoint swap invalidates the lot. So this is exactly the set of request
// fields a change to which costs the user a fresh, full-price read of the
// entire transcript. Everything else about a request — max_tokens, the
// ephemeral tail, the transcript itself — can move freely.
//
// It must be RETAINED rather than hashed, because by the time anything can
// react to a prefix change the agent no longer holds the prefix that changed:
// an extension reload has already rewritten a.System and a.Tools; a /model
// switch has already rewritten a.Model and a.Client. What is still warm in the
// provider's cache is the OLD prefix, and after the swap this struct is the
// only copy of it left. A digest is enough to NOTICE a change; it is not enough
// to keep SPENDING against what is still warm, which is what compacting on the
// outgoing model requires.
//
// Distinct from turnPin, which freezes system+tools across the steps of one
// segment so a mid-turn host swap can't evict the cache underneath a running
// tool loop. A pin is what a turn intends to send; this is what was sent.
type promptPrefix struct {
	model  string
	client provider.Client
	system string
	tools  []provider.Tool

	// reasoning is prefix, unintuitively. It is not rendered into the prompt at
	// all — but changing the thinking configuration invalidates the cached
	// MESSAGE blocks, which is the expensive tier and the entire saving a
	// cache-aware compaction is chasing. A summarizer that helpfully turned
	// thinking off to save output tokens would throw away a 90k cache read to
	// do it.
	reasoning string

	// reasoningSet rides alongside reasoning so a compaction request rebuilt
	// from this prefix resolves the SAME effective thinking level the live turn
	// did (EffectiveReasoning takes both). Without it a user-set level that
	// differs from the model's DefaultReasoning would resolve back to the model
	// default in the warm request and miss the very cache it aims to reuse.
	reasoningSet bool

	// cacheKey is the provider's cache-routing key (Request.PromptCacheKey).
	// Not prefix content — it selects WHICH cache the prefix is matched
	// against, which here amounts to the same thing: right bytes, wrong route,
	// full price.
	cacheKey string

	// temperature is not part of any provider's prefix match. It rides along so
	// a request assembled against this prefix reproduces the sampling shape of
	// the turn it came from, and so compaction can be assembled without
	// reaching back into the agent for one straggler field.
	temperature *float32

	// sentAt is when this prefix went on the wire. A provider's cache has a TTL
	// (see prefixCacheTTL), so past it there is no warm prefix left to protect —
	// and a guard that warned anyway would be offering to save an expense the
	// user already paid by walking away for lunch.
	sentAt time.Time
}

// recordDispatch snapshots the prefix of a request as it goes on the wire.
//
// Called from oneTurn, the single assembly chokepoint for real turns, once the
// client has accepted the request: a Stream that never opened cached nothing,
// and claiming otherwise would aim the next compaction at a prefix the provider
// has never seen. Compaction's own request deliberately does NOT record — it is
// a side computation, not a turn, and its prefix is not the one the conversation
// continues from.
func (a *Agent) recordDispatch(client provider.Client, req provider.Request) {
	// Shallow-copy the spec slice: it is retained across turns, and
	// SpecsVisible builds it fresh per call today but is under no obligation
	// to keep doing so.
	tools := append([]provider.Tool(nil), req.Tools...)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastSent = &promptPrefix{
		model:        req.Model,
		client:       client,
		system:       req.System,
		tools:        tools,
		reasoning:    req.Reasoning,
		reasoningSet: req.ReasoningSet,
		cacheKey:     req.PromptCacheKey,
		temperature:  req.Temperature,
		sentAt:       time.Now(),
	}
}

// livePrefix is what the NEXT request would be assembled from — the agent's
// current fields, which a host may have rewritten since the last dispatch. a.mu
// must be held.
func (a *Agent) livePrefixLocked() promptPrefix {
	return promptPrefix{
		model:        a.Model,
		client:       a.Client,
		system:       a.System,
		tools:        a.Tools.SpecsVisible(a.turnToolsLocked(a.Tools).visible),
		reasoning:    a.Reasoning,
		reasoningSet: a.ReasoningSet,
		cacheKey:     a.cacheID,
	}
}

// pendingPrefixChange reports the prompt-prefix invalidation the next request
// would pay for: why the cache is about to miss, and how many cached tokens get
// re-read at full price when it does.
//
// It compares the live prefix against the last dispatched one, and that single
// choice is what makes the awkward requirements fall out for free instead of
// needing bookkeeping. Ten extension reloads between two turns are ONE
// difference, not ten events to coalesce — so the offer is made once however
// many things changed. A change that is reverted before the next turn compares
// equal again and cancels itself silently, with no "pending offer" to withdraw.
// And the cost is read from the newest usage row, so the number quoted is always
// the bill about to be paid rather than some historical high-water mark.
func (a *Agent) pendingPrefixChange() (reason string, tokens int, ok bool) {
	a.mu.Lock()
	sent := a.lastSent
	live := a.livePrefixLocked()
	a.mu.Unlock()

	// Nothing dispatched, so nothing is cached, so there is nothing to lose.
	if sent == nil {
		return "", 0, false
	}
	// The prefix has already aged out. The change is free; say nothing.
	if time.Since(sent.sentAt) > prefixCacheTTL {
		return "", 0, false
	}

	// What the last turn actually had cached is exactly what a change destroys.
	// Read it from the usage row rather than estimating it: a provider that
	// reported no cache reads has no cache to lose — a transcript below the
	// minimum cacheable size, a backend with no prefix cache at all, or the
	// reasoning models that re-read from every user-message boundary regardless,
	// where a "this will cost you a fresh read" warning would simply be false.
	// Measuring beats maintaining a list of which providers cache.
	last := a.cost.LastTurnUsage()
	tokens = last.CacheReadTokens + last.CacheWriteTokens
	if tokens < prefixChangeMinTokens {
		return "", 0, false
	}

	reasons := prefixDiff(*sent, live)
	if len(reasons) == 0 {
		return "", 0, false
	}
	return strings.Join(reasons, ", "), tokens, true
}

// prefixDiff names every way the live prefix differs from the dispatched one.
// Any single entry invalidates the transcript's cache, so the list is a
// description rather than a severity ranking — one reason and four cost the
// same, which is precisely why they coalesce into one offer.
func prefixDiff(sent, live promptPrefix) []string {
	var out []string
	if sent.model != live.model {
		out = append(out, i18n.T("the model changed (%s → %s)", sent.model, live.model))
	}
	if !sameClient(sent.client, live.client) {
		out = append(out, i18n.T("the provider endpoint changed"))
	}
	if !sameTools(sent.tools, live.tools) {
		out = append(out, i18n.T("the tool set changed"))
	}
	if sent.system != live.system {
		out = append(out, i18n.T("the system prompt changed"))
	}
	// Enabling or disabling extended thinking invalidates the cached message
	// blocks. Whether merely changing the LEVEL does — low to high, same
	// thinking, different budget — is undocumented, and terva has never measured
	// it. So this fires only on the transition that is known to cost, and
	// under-warns rather than crying wolf. If the measurement ever lands, this is
	// the line that changes.
	if (sent.reasoning == "") != (live.reasoning == "") {
		if live.reasoning == "" {
			out = append(out, i18n.T("extended thinking was turned off"))
		} else {
			out = append(out, i18n.T("extended thinking was turned on"))
		}
	}
	return out
}

// sameClient reports whether two clients are the same object.
//
// Identity, not equivalence: a host swaps the client when it re-resolves an
// endpoint or rotates credentials (SetClientAndModel), and a new object is
// exactly the signal worth acting on. Terva resolves credentials per request
// INSIDE the client rather than by minting a new one, so this does not fire on
// a token refresh. Every provider.Client is a pointer type, so == is pointer
// comparison and cannot panic on a non-comparable dynamic type.
func sameClient(x, y provider.Client) bool {
	if x == nil || y == nil {
		return x == nil && y == nil
	}
	return x == y
}

// sameTools compares the ADVERTISED tool specs, field by field. Not a length
// check: an extension reload that swaps one tool for another, or rewrites a
// description, changes the bytes the provider hashed without changing the count.
func sameTools(x, y []provider.Tool) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i].Name != y[i].Name || x[i].Description != y[i].Description {
			return false
		}
		if !bytes.Equal(x[i].Schema, y[i].Schema) {
			return false
		}
	}
	return true
}

// compactionPrefix returns the prefix a compaction should be assembled against:
// the last one dispatched, because that is the one the provider still has warm.
//
// This is what "compact on the outgoing model" means in code. A /model switch
// rewrites a.Model and a.Client immediately — they are read per STEP, not
// pinned (oneTurn), so the switch lands on the very next request and cannot be
// gated. By the time a compaction runs in RESPONSE to that switch, the agent
// already holds the incoming model. Summarizing on it would send a cold,
// transcript-sized read to a model that has never seen this conversation,
// paying in full the exact bill the compaction exists to avoid. The outgoing
// model still has the whole thing cached; that is where the summary is cheap.
//
// Reading the prefix as one struct under one lock also closes a live data race:
// SetModel and SetClientAndModel write a.Model / a.Client under a.mu, while
// compactHeld used to read them (and a.Temperature) unlocked. Compact's
// single-flight guard excludes other TURNS, not a host's model swap — and a
// swap concurrent with a compaction is not a hypothetical here, it is the
// headline case.
//
// warm reports whether this prefix was actually dispatched — i.e. whether a
// provider is holding it in cache. When it is false the returned prefix is
// assembled from the live agent fields: a resumed session compacted before its
// first turn. There is no warm cache to aim at in that case, so the current
// model is both the only available answer and the right one — and the
// cache-aware summarizer has nothing to be cache-aware ABOUT, so it must not
// run.
func (a *Agent) compactionPrefix() (p promptPrefix, warm bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastSent != nil {
		return *a.lastSent, true
	}
	return promptPrefix{
		model:        a.Model,
		client:       a.Client,
		system:       a.System,
		tools:        a.Tools.SpecsVisible(a.turnToolsLocked(a.Tools).visible),
		reasoning:    a.Reasoning,
		reasoningSet: a.ReasoningSet,
		cacheKey:     a.cacheID,
		temperature:  a.Temperature,
	}, false
}

// SetCacheAwareCompaction toggles the cache-aware summarizer (the engine
// feature cache_aware_compaction; the shipped default — ON — lives in
// build/enginefeatures.go, core's zero value stays off). Off, a compaction
// builds its own bespoke prefix — its own system prompt, no tools, the transcript
// flattened into one text block — which by construction matches nothing the
// provider has cached, so every compaction is a full-price cold read of the
// whole conversation. On, it summarizes against the WARM prefix and pays cache
// rates for the transcript it re-reads.
//
// A toggle rather than a replacement because the two paths do not necessarily
// produce equally good summaries: the cold path gets a purpose-built
// summarization system prompt and a transcript explicitly framed as material,
// while the warm path has to ask for a summary from inside the agent's own
// persona with its tools still advertised. That is a real quality question, and
// it wants an A/B, not an assertion.
func (a *Agent) SetCacheAwareCompaction(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cacheAwareCompaction = on
}

// CacheAwareCompactionEnabled reports whether compaction summarizes against the
// warm prefix. Exported so a host can show which summarizer is in play — and so
// the build funnel's default is testable, which is the only place the shipped
// default actually lives (core's zero value is off).
func (a *Agent) CacheAwareCompactionEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cacheAwareCompaction
}

func (a *Agent) cacheAwareCompactionOn() bool { return a.CacheAwareCompactionEnabled() }

// SetProviderCompaction toggles handing compaction to the backend (the engine
// feature provider_compaction). Off in both places — core's zero value and the
// shipped default — which is the point rather than an oversight.
//
// What it changes is not which summarizer runs but what a checkpoint IS. The
// client strategies produce prose: terva can read it, store it, show it, and
// replay it against any model on any provider. This produces an encrypted blob
// only the issuing backend can decrypt, standing in for the assistant turns it
// removed — cheaper, bounded, and not portable. A conversation compacted this
// way and then pointed at a different provider has to be rebuilt from the
// session file and compacted again (ReadSessionPreCompaction), because the
// alternative is a transcript that reads continuous while missing half its
// history.
//
// The reason it ships off is narrower than that, and worth stating plainly: the
// saving it exists for is UNMEASURED. That the endpoint works, what it returns,
// how the blob scales, and that the result is provider-bound but not
// model-bound are all measured. Whether replacing a client summary with it
// actually recovers the cache reads a compaction currently costs is not, and
// that is the only question a default flip should turn on.
func (a *Agent) SetProviderCompaction(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providerCompaction = on
}

// ProviderCompactionEnabled reports whether compaction is handed to the
// backend. Exported for the same reason as CacheAwareCompactionEnabled: a host
// can show which strategy is in play, and the build funnel's default — the only
// place the shipped default lives — becomes testable.
func (a *Agent) ProviderCompactionEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.providerCompaction
}

func (a *Agent) providerCompactionOn() bool { return a.ProviderCompactionEnabled() }

// SetPrefixChangeGuard toggles the pre-turn prefix-change guard (the engine
// feature prefix_change_guard, default ON — but see the economics below, which
// keep it inert until cache-aware compaction is enabled too).
func (a *Agent) SetPrefixChangeGuard(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prefixGuard = on
}

// PrefixChangeGuardEnabled reports whether the pre-turn guard is armed. Note it
// is inert without CacheAwareCompactionEnabled: the offer is only honest when
// there is a saving behind it (offerCompactOnPrefixChange).
func (a *Agent) PrefixChangeGuardEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefixGuard
}

func (a *Agent) prefixGuardOn() bool { return a.PrefixChangeGuardEnabled() }
