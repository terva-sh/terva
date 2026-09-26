package session

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Store is the JSONL session file (decision 0007) as a core.TranscriptStore.
// It also keeps every diagnostic record, and it owns the one policy that is
// the file's rather than the engine's: which cache-cliff events become rows.
//
// core.AttachTranscriptStore serializes the calls, so the cliff state needs no
// lock of its own. Attach one Store per agent.
type Store struct {
	sess *Session

	cliffPeak core.CacheCliff
	cliffOpen bool
}

// NewStore returns the store that writes to sess.
func NewStore(sess *Session) *Store { return &Store{sess: sess} }

var (
	_ core.TranscriptStore       = (*Store)(nil)
	_ core.TranscriptDiagnostics = (*Store)(nil)
)

func (s *Store) AppendMessage(m provider.Message) error { return s.sess.AppendMessage(m) }

func (s *Store) AppendCompaction(messages []provider.Message, res core.CompactResult) error {
	return s.sess.AppendCompaction(messages, res)
}

// AppendUsage keeps the three kinds on one cumulative stream, because the total
// has to stay one coherent timeline for a crash to recover it, and marks each
// row with whose it was.
//
// A sub-agent's spend rides its own marker; without it, a child's cold prompt
// reads as this session's cache collapsing to every offline analysis. A
// host's own one-off completion is marked with the surface that spent it, and
// the mark is the whole point: unmarked, an idle next-step suggestion's
// request is a turn of this session to every offline reader, and what the
// suggestions cost cannot be asked.
func (s *Store) AppendUsage(r core.UsageRecord) error {
	switch r.Kind {
	case core.UsageDelegated:
		return s.sess.AppendDelegatedUsage(r.Usage, r.Cumulative)
	case core.UsageSideChannel:
		return s.sess.AppendSideChannelUsage(r.Source, r.Usage, r.Cumulative)
	default:
		return s.sess.AppendUsage(r.Usage, r.Cumulative)
	}
}

// AppendToolGroupActivation records a lazy-tool activation. activeGroups is
// in-memory and NewAgent rebuilds it from config, so without this row a
// --resume silently drops what the model activated — and the tools array sits
// AHEAD of the system prompt and every message in the provider's cached
// prefix. The resumed run therefore invalidates the whole transcript, then
// invalidates it again when the model notices the tool is missing and
// re-activates. Measured at ~$3.13 on one 225-request session, from a single
// lost group.
func (s *Store) AppendToolGroupActivation(group string) error {
	return s.sess.AppendToolGroupActivation(group)
}

// AppendImageExclusion makes image-rejection recovery permanent: the agent
// drops an image the provider 400'd on, and the loader re-applies this
// directive, so a resumed session never re-sends the bad image and re-fails.
//
// Before the hooks became registrations the agent fired this event and the
// session could write the directive, but no host joined them, so every resume
// re-sent the rejected image and paid the recovery again. Both doc comments
// claimed otherwise. The hook was invisible precisely because nothing
// referenced it.
func (s *Store) AppendImageExclusion(sha256Hex string) error {
	return s.sess.AppendImageExclusion(sha256Hex, "provider rejected the image")
}

// AppendEscalation records rung 3 of the stuck-loop hatch, which swapped (or
// tried to swap) the model. The swap already wrote a "meta" row via
// UpdateModel, indistinguishable from a user /model switch; this records the
// escalation that caused it so the log can tell the two apart. The engine
// fires it only when a target is configured, so unconfigured sessions grow no
// escalation rows.
func (s *Store) AppendEscalation(rec core.EscalationRecord) error {
	return s.sess.AppendEscalation(rec)
}

// AppendStall records rung 1 of the stuck-loop hatch: the detector nudged a
// repeating model. The nudge only rides the ephemeral tail, so without this
// row nothing in the log says it fired. Recorded for every session that runs
// the (default-on) detector, not just ones with an escalation target.
func (s *Store) AppendStall(rec core.StallRecord) error { return s.sess.AppendStall(rec) }

// AppendRetry records a transient provider failure that was waited out. This
// is the only durable trace a SUCCESSFUL retry leaves: the abandoned attempt
// is dropped from the transcript on purpose, and the error sidecar only
// records failures nothing recovered — so absorbing an outage cleanly used to
// look identical to being slow. It fires for both ladders; rec.Phase says
// which, and the compaction one is the expensive half (each attempt carries
// the whole transcript).
func (s *Store) AppendRetry(rec core.RetryRecord) error { return s.sess.AppendRetry(rec) }

// AppendTail records what the harness appended to the request after the cache
// breakpoint — the generalization of the stall row. The tail is composed per
// request and discarded, so without this a session file holds the model's
// REACTION to a prompt injection and no trace of the injection. It fires on
// change, so a session whose tail is stable grows one row, not one per turn.
func (s *Store) AppendTail(rec core.TailRecord) error { return s.sess.AppendTail(rec) }

// AppendPrefixDivergence records that the cacheable prefix was rebuilt rather
// than extended, so the provider re-read everything after the divergence at
// full price. Nothing else records it: a mutated prefix is invisible in the
// transcript and shows up only as a cache-read figure with no explanation.
func (s *Store) AppendPrefixDivergence(d core.PrefixDivergence) error {
	return s.sess.AppendPrefixDivergence(d)
}

// AppendTransport records which connection and edge each dispatch physically
// rode. The prefix row proves the BYTES were stable through a cache collapse;
// this row is the other half — whether the request re-dialed or changed edge
// colo — so a floor-pinned run can be read against transport churn instead of
// ending at "provider-side".
func (s *Store) AppendTransport(ti provider.TransportInfo) error {
	return s.sess.AppendTransport(ti)
}

// AppendCacheCliff writes the two transitions of a provider-side cache
// collapse, not every event. The detector shipped observer-only, raising a
// sticky note and leaving nothing on disk, which cost the cache investigation
// twice: a finished session could not say whether it had fired, and the
// experiment that would settle the cause has to run while a session IS
// collapsed — with nothing announcing that one currently was.
//
// The write-once policy lives here rather than in core because core's event
// cadence is right for what it serves: the note tracks a changing fact and
// wants every update. The file wants the two transitions. Holding the last
// ongoing event is what lets the closing row carry the totals the run
// reached, since the end-of-run event carries zero counts.
//
// End is the exception, and it comes off the terminal event rather than the
// peak: only that event knows whether the provider served the prefix again or
// terva voided its own baseline. Copying it onto the peak is what stops a
// voided run reading as a recovery on disk.
func (s *Store) AppendCacheCliff(cc core.CacheCliff) error {
	if cc.Ongoing {
		s.cliffPeak = cc
		if !s.cliffOpen {
			s.cliffOpen = true
			return s.sess.AppendCacheCliff(cc, true)
		}
		return nil
	}
	if !s.cliffOpen {
		return nil
	}
	s.cliffOpen = false
	closing := s.cliffPeak
	closing.End = cc.End
	return s.sess.AppendCacheCliff(closing, false)
}
