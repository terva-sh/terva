package workspace

import (
	"fmt"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The cache-cliff note. core's detector (cachecliff.go) proves the shape —
// append-only dispatches whose cache reads collapsed while the prompt kept
// growing — and this file is only the wording and the keyed-note lifecycle:
// one note per session, rewritten as the run grows, retracted the moment a
// dispatch hits again. A session that never cliffs never posts one.
//
// The note is live state, not an event log: it is true exactly while the
// provider keeps re-reading the transcript, and the retract is wired to the
// same observer that raised it — the detector's end-of-run event — not to a
// turn boundary the outage does not respect.

// cacheCliffNoteKey names the keyed note, per session so two sessions
// cliffing at once do not overwrite each other's numbers.
func cacheCliffNoteKey(sessionID string) string {
	return "cache-cliff:" + sessionID
}

// cacheCliffNote renders the detector's event into the note line, or "" for
// the retract. Pure, so the wording and the retract contract are testable
// without a daemon.
//
// On the advice: compaction is NOT a fix and the note must not imply it is.
// Session 20260803-160431 (gpt-5.6-sol) took two compactions and never cached
// conversation content again — 120 consecutive dispatches pinned at the
// system+tools floor, $72.81, across an 8-hour gap. What compaction changes is
// the SIZE of each miss, because the transcript being re-read is smaller; the
// misses themselves are provider-side routing and outlive it. Offering
// "/compact" alone sends someone to spend a summarization round-trip on a run
// it cannot stop.
func cacheCliffNote(cc core.CacheCliff) string {
	if !cc.Ongoing {
		return ""
	}
	return i18n.T("provider cache misses: the last %d dispatches re-read ~%s tokens the cache should have served — /compact makes each miss cheaper but does not end the run; that needs a new session or another model",
		cc.Dispatches, roughTokens(cc.RereadTokens))
}

// roughTokens renders a token count at note precision — "312K", "1.4M". The
// number is a running waste estimate, and false precision would read as a
// bill.
func roughTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dK", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
