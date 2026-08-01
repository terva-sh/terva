package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/provider"
)

// A provider caches by PREFIX MATCH: the longest leading run of the request that
// is byte-identical to something it already holds. Everything after the first
// difference is re-read at full price. So the only property that keeps a long
// conversation affordable is that consecutive requests share a prefix and differ
// only by APPENDING — and nothing in terva ever checked that.
//
// promptPrefix (prefix.go) watches the model, endpoint, system prompt, tool
// specs and cache key, because those are the things a HOST swap rewrites. Its
// doc comment says outright that "the transcript itself can move freely", which
// is true of appending and false of everything else. A transcript that is
// rebuilt rather than extended — the same conversation, different bytes — is
// invisible to that guard and fatal to the cache.
//
// This is the missing half. It digests the whole cacheable prefix in render
// order as a LADDER, one cumulative rung per element, so a comparison between
// two dispatches yields not "something changed" but the exact rung where they
// diverged: the tool specs, the system prompt, or message #k of #n. That
// distinction is the difference between a fact and a hunt — a measured session
// showed cache reads pinned at a constant 8,704 tokens across 79 requests, which
// says the match died at a fixed offset every single time and says nothing about
// where that offset falls.
//
// The ladder is digests, not values, because unlike promptPrefix nothing here
// needs to REPLAY the old prefix — it only needs to locate the divergence. One
// sha256 per message per dispatch, reused across rungs.

// prefixLadder is the cumulative digest of a request's cacheable prefix, one
// rung per element in the order a provider sees them.
type prefixLadder struct {
	rungs  [][32]byte
	labels []string
	// msgCount is how many of the rungs are messages, so a divergence can be
	// reported as "message 12 of 340" — the ratio is what says whether this was
	// a rewrite of ancient history or a churn at the tail.
	msgCount int
}

// routeKey is the non-content half of a cache match: the model, the routing key
// the provider matches WITHIN, and whether extended thinking is on. None of them
// is rendered into the prompt, and all three decide whether a byte-identical
// prefix hits — right bytes down the wrong route cost full price. Sitting at
// rung 0 means a route change reports as itself rather than as "tools changed".
func routeKey(model, cacheKey, reasoning string) []byte {
	return []byte(model + "\x00" + cacheKey + "\x00" + reasoning)
}

// buildPrefixLadder digests tools, then system, then each message — the shape
// tests and benchmarks use when the route is not under examination.
func buildPrefixLadder(tools []provider.Tool, system string, msgs []provider.Message) prefixLadder {
	return buildPrefixLadderFull("", "", "", tools, system, msgs)
}

// buildPrefixLadderFull digests the whole prefix in the order a provider hashes
// it: route, tools, system, then each message. Getting that order wrong would
// misreport WHICH element diverged while still correctly detecting THAT one did.

func buildPrefixLadderFull(model, cacheKey, reasoning string, tools []provider.Tool, system string, msgs []provider.Message) prefixLadder {
	l := prefixLadder{
		rungs:    make([][32]byte, 0, len(msgs)+2),
		labels:   make([]string, 0, len(msgs)+2),
		msgCount: len(msgs),
	}
	var running [32]byte

	step := func(label string, payload []byte) {
		h := sha256.New()
		h.Write(running[:])
		h.Write(payload)
		copy(running[:], h.Sum(nil))
		l.rungs = append(l.rungs, running)
		l.labels = append(l.labels, label)
	}

	step("route", routeKey(model, cacheKey, reasoning))
	step("tools", toolsDigestPayload(tools))
	step("system", []byte(system))
	for i, m := range msgs {
		step(fmt.Sprintf("message %d", i), messageDigestPayload(m))
	}
	return l
}

// toolsDigestPayload serializes the advertised specs the way a provider would
// see them — name, description, schema, IN ORDER. Order is part of the payload,
// not normalized away: a reordered tools array is a real cache invalidation, and
// a guard that sorted first would hide the most likely way for one to happen.
func toolsDigestPayload(tools []provider.Tool) []byte {
	h := sha256.New()
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(tools)))
	h.Write(n[:])
	for _, t := range tools {
		h.Write([]byte(t.Name))
		h.Write([]byte{0})
		h.Write([]byte(t.Description))
		h.Write([]byte{0})
		h.Write(t.Schema)
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

// messageDigestPayload digests a message through the SAME encoder the session
// file uses. Reusing it is deliberate: a bespoke encoder here would be a second
// definition of "what a message is", and would drift from the one on disk
// exactly when someone adds a content block kind — the moment this guard most
// needs to be right.
//
// A message that cannot be encoded digests as its role plus a marker rather than
// being skipped: skipping would make two different transcripts compare equal,
// which is the one failure this must never have.
func messageDigestPayload(m provider.Message) []byte {
	b, err := json.Marshal(encodeWireMessage(m))
	if err != nil {
		return append([]byte("unencodable:"), []byte(m.Role)...)
	}
	return b
}

// PrefixDivergence describes how one dispatch's prefix relates to the previous
// one's.
type PrefixDivergence struct {
	// Appended is the healthy case: every rung the two share is identical, and
	// the new request only added more. A provider re-reads nothing.
	Appended bool
	// Rung is the first index at which the two ladders differ; -1 when they did
	// not. Everything from here on is re-read at full price.
	Rung int
	// Label names that rung ("tools", "system", "message 12").
	Label string
	// MsgCount and PrevMsgCount give the divergence its scale.
	MsgCount, PrevMsgCount int
	// CachedTokens is what the previous request actually had cached, read from
	// its usage row rather than estimated — the bill this divergence causes.
	CachedTokens int
}

// Mutation reports whether the transcript was REBUILT rather than extended: a
// divergence at a rung that both requests share. This is the expensive,
// invisible case — the same conversation re-rendered into different bytes — as
// opposed to a deliberate, explicable invalidation like a model swap.
func (d PrefixDivergence) Mutation() bool {
	return !d.Appended && d.Rung >= 0
}

// comparePrefixLadders locates the first rung at which two ladders differ.
func comparePrefixLadders(prev, cur prefixLadder) PrefixDivergence {
	n := len(prev.rungs)
	if len(cur.rungs) < n {
		n = len(cur.rungs)
	}
	d := PrefixDivergence{
		Rung:         -1,
		MsgCount:     cur.msgCount,
		PrevMsgCount: prev.msgCount,
	}
	for i := 0; i < n; i++ {
		if prev.rungs[i] != cur.rungs[i] {
			d.Rung = i
			d.Label = cur.labels[i]
			return d
		}
	}
	// Every shared rung matched. A shorter new ladder is still not an append —
	// the transcript got SMALLER, which is what a compaction does, and which a
	// provider cannot match beyond the shared run either.
	d.Appended = len(cur.rungs) >= len(prev.rungs)
	if !d.Appended {
		d.Rung = len(cur.rungs)
		d.Label = "truncated"
	}
	return d
}

// watchPrefix compares this dispatch's prefix against the previous one and fires
// the observer when the transcript was rebuilt rather than extended.
//
// Called from oneTurn where the request is already assembled, so it measures the
// BYTES THAT WENT ON THE WIRE rather than re-deriving them from agent state. A
// guard that rebuilt its own view of the prefix could agree with itself while
// disagreeing with what was sent, which is the whole failure it exists to catch.
//
// Reports only mutations, never ordinary appends: appends are every healthy
// request, and a row per request would be the noise this is meant to cut
// through. A compaction's own truncation IS reported — it is expected, and a
// reader needs the expected one on the record to tell it apart from the
// unexplained ones that follow.
// The ladder is rebuilt from scratch every dispatch, digesting every message
// rather than reusing the previous request's per-message digests.
//
// That looks like the O(n²) it is, and the obvious fix — memoize per message,
// keyed on the transcript epoch — was written up and then dropped, because
// measuring it first said not to. BenchmarkBuildPrefixLadder: 2.6ms and 1.8MB
// for a 1500-message, 797KB transcript, against a request that spends SECONDS at
// the provider. Across the whole 857-request session that motivated this, the
// total is about two seconds of CPU.
//
// It is also the safer shape, which is why the measurement was worth taking.
// Memoizing means trusting a signal that says "these messages did not change" —
// and this guard exists precisely because that assumption was already wrong once
// (promptPrefix's "the transcript can move freely"). A digest that skips
// re-reading a message cannot detect that message changing, so the optimisation
// would have to be exactly right about which mutations bump the epoch, forever.
// Recomputing has no such obligation.
//
// Revisit if a transcript carrying large images ever makes this show up in a
// profile — image bytes are re-hashed per request and no benchmark covers them.
func (a *Agent) watchPrefix(req provider.Request, msgs []provider.Message) {
	if !a.prefixDivergenceRecordingOn() {
		return
	}
	cur := buildPrefixLadderFull(req.Model, req.PromptCacheKey, req.Reasoning, req.Tools, req.System, msgs)

	a.mu.Lock()
	prev := a.lastLadder
	a.lastLadder = &cur
	a.mu.Unlock()

	if prev == nil {
		return // first dispatch: nothing to diverge from
	}
	d := comparePrefixLadders(*prev, cur)
	if d.Appended {
		return
	}
	last := a.cost.LastTurnUsage()
	d.CachedTokens = last.CacheReadTokens + last.CacheWriteTokens
	a.firePrefixDivergence(d)
}

// SetPrefixDivergenceRecording toggles the prefix-divergence recorder (the
// engine feature prefix_divergence_recording, default ON — see the feature
// declaration for why a diagnostic ships enabled rather than opt-in).
//
// Off, watchPrefix returns before doing any work. The retained ladder is
// deliberately NOT cleared: a session that toggles the feature back on mid-run
// would otherwise have nothing to compare against and would miss the first
// divergence after it, which is the one most likely to be interesting.
func (a *Agent) SetPrefixDivergenceRecording(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prefixDivRecording = on
}

// PrefixDivergenceRecordingEnabled reports whether prefix divergences are
// recorded. Exported so a host can show the toggle's state, and so the build
// funnel's default is testable — core's zero value is off, and the shipped
// default lives in build/enginefeatures.go.
func (a *Agent) PrefixDivergenceRecordingEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefixDivRecording
}

func (a *Agent) prefixDivergenceRecordingOn() bool { return a.PrefixDivergenceRecordingEnabled() }
