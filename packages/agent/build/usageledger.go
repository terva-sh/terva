package build

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"

	"terva.sh/terva/packages/privfs"
)

// The workspace usage ledger: where spend goes when there is no session to book
// it to.
//
//	$TERVA_HOME/usage.jsonl — one JSON object per booked call, append-only
//
// WHY it exists. Every model call in terva has, until now, belonged to a
// session, and a session's own file is its ledger: Agent.RecordSideChannelUsage
// fires the usage observers and the row lands in the transcript. That works
// because the caller always had a session in the frame.
//
// The sessionless verbs break the assumption. worlds.doctor reads a SAVED World
// — its roster, its lorebook, the scenes played in it — from the Library shelf,
// with no scene open. It is a real, billable model call with no transcript to
// live in. The three alternatives were all worse: dropping the row makes spend
// invisible, booking it to whichever scene happened to be checked as evidence
// attributes a World-wide review to one night's play, and demanding a session
// purely as a billing carrier gives back the property the verb exists to have.
//
// So: a workspace-scoped, append-only ledger. Append-only for the reason session
// files are — a row is a fact about something that already happened, and a
// format that never rewrites cannot lose an earlier one to a later crash.
//
// Deliberately NOT a general accounting system. It records what was spent, by
// which verb, against which subject, and at what moment. Aggregation is a
// reader's job (Read + your own arithmetic), because the useful groupings are
// not knowable here and a ledger that pre-aggregates is one that has already
// thrown away the rows you turn out to want.

// UsageLedgerPath is the on-disk ledger, created lazily on the first booking; a
// missing file reads as "nothing sessionless has been spent yet".
func UsageLedgerPath() string { return filepath.Join(config.TervaHome(), "usage.jsonl") }

// UsageRecord is one booked sessionless call.
type UsageRecord struct {
	// At is when the call was booked (RFC 3339, so the file stays readable by
	// eye — this is a forensics surface before it is a data one).
	At time.Time `json:"at"`
	// Verb is the ctrlproto method that spent it, e.g. "worlds.doctor". The
	// primary grouping key: "what did the world doctor cost me this week" is
	// the question this file exists to answer.
	Verb string `json:"verb"`
	// Subject is what the call was ABOUT — a World id for worlds.doctor. Kept
	// separate from Verb so spend can be traced to the thing it was spent on,
	// which is the attribution booking-to-a-scene got wrong.
	Subject string `json:"subject,omitempty"`
	// Provider and Model name what answered. A ledger row records no price
	// sheet, and Usage.CostUSD was stamped at the prices of the model that
	// answered (see provider.Usage), so the model has to travel with it or a
	// later reader cannot tell a cheap call from an expensive one.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	provider.Usage
}

// UsageLedger is the append-only ledger at UsageLedgerPath().
type UsageLedger struct {
	mu   sync.Mutex
	path string
}

// NewUsageLedger opens the ledger at the current $TERVA_HOME.
func NewUsageLedger() *UsageLedger { return &UsageLedger{path: UsageLedgerPath()} }

// Book appends one record. A zero-valued usage is dropped rather than written:
// a provider that reported nothing is not a spend, and a file of empty rows
// would make the ledger's own size a lie about how much has been happening.
//
// Errors are returned rather than swallowed, but callers are expected to treat
// a failed booking as non-fatal — losing the row is bad, and failing the user's
// doctor run because the row could not be written is worse.
func (l *UsageLedger) Book(rec UsageRecord) error {
	if rec.Usage == (provider.Usage{}) {
		return nil
	}
	if rec.At.IsZero() {
		return fmt.Errorf("a usage record needs a moment")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := privfs.MkdirAll(filepath.Dir(l.path)); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

// Read returns every booked record in file order (oldest first). A missing file
// is an empty ledger, not an error.
//
// A line that fails to parse is SKIPPED rather than failing the read. An
// append-only file written by a process that can be killed mid-write can end in
// a partial line, and one torn final row must not make every earlier row
// unreadable — which is the whole point of choosing a line-per-record format.
func (l *UsageLedger) Read() ([]UsageRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []UsageRecord
	sc := bufio.NewScanner(f)
	// A usage row is small, but the default 64KiB token limit is a silent
	// truncation waiting to happen; give it room and let a genuinely oversized
	// line be skipped as unparseable rather than cut in half.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec UsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		// Return what was read alongside the error: a partial ledger is more
		// useful than none, and the caller decides whether to trust it.
		return out, err
	}
	return out, nil
}
