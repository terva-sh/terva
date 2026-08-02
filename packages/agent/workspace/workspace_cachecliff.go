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
func cacheCliffNote(cc core.CacheCliff) string {
	if !cc.Ongoing {
		return ""
	}
	return i18n.T("provider cache misses: the last %d dispatches re-read ~%s tokens the cache should have served — /compact cuts the cost if it keeps up",
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
