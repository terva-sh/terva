package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"terva.sh/terva/packages/provider"
)

// ReplayRowKind identifies a transcript row the session player walks forward.
type ReplayRowKind string

const (
	// ReplayRowMessage is a persisted conversation message (user/assistant/tool).
	ReplayRowMessage ReplayRowKind = "message"
	// ReplayRowUsage is a per-turn usage row (with its running cumulative).
	ReplayRowUsage ReplayRowKind = "usage"
	// ReplayRowCompaction is a checkpoint: the summary the loader would honor by
	// resetting the transcript. The player animates it (effective mode) or
	// ignores it (raw mode) — it never rewrites earlier rows.
	ReplayRowCompaction ReplayRowKind = "compaction"
)

// ReplayRow is one transcript row preserved in file order. It is the forward
// twin of RevealCompaction's backward reconstruction: where OpenSession
// collapses history at each "compaction" checkpoint (resetting its message
// set), ReadReplayRows keeps every row so a player can re-emit the session as
// a live-looking scene — including the pre-compaction turns a checkpoint later
// summarized away. See docs/proposals/session-player.md.
type ReplayRow struct {
	Kind ReplayRowKind

	// Message is set when Kind == ReplayRowMessage.
	Message provider.Message

	// Usage/Cumulative are set when Kind == ReplayRowUsage, as recorded.
	Usage      provider.Usage
	Cumulative provider.Usage
	// Delegated marks a usage row as a SUB-AGENT's spend booked against this
	// session rather than a request this session sent. A cost or cache-hit
	// rollup that mixes them reports the child's cold prompt as the parent
	// missing its cache.
	Delegated bool
	// At is when a usage row was written, ZERO for rows from before the stamp
	// existed. Unlike Message.Time it is NOT backfilled from a neighbour: a
	// message's time only has to order the scene, while this one is read to
	// measure gaps between dispatches, and a guessed instant would answer that
	// question with a fabrication rather than an absence.
	At time.Time

	// Checkpoint is the summary output a compaction folded its input into,
	// set when Kind == ReplayRowCompaction. Honoring it replaces the live
	// transcript with these messages (what OpenSession does); the player uses
	// it to animate the compaction and resync the effective transcript.
	Checkpoint []provider.Message
}

// ReadReplayRows walks a session JSONL and returns every message, usage, and
// compaction row in file order, WITHOUT OpenSession's checkpoint collapse,
// plus the session's metadata. Read-only; the live session is untouched.
//
// meta/directive/rename rows carry no replay event and are skipped. Empty
// messages are dropped to mirror OpenSession's transcript build. Corrupt rows
// are skipped rather than failing the whole read (best-effort, like the
// loader), so a partially-written tail still plays up to the last good row.
func ReadReplayRows(path string) ([]ReplayRow, SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, SessionMeta{}, err
	}
	defer f.Close()

	var meta SessionMeta
	var rows []ReplayRow
	rep := &loadReport{}
	if _, err := walkSession(f, rep, sessionWalkHooks{
		onMeta: func(m SessionMeta, _ []byte) { meta = m },
		onMessage: func(m provider.Message, _ int, _ []byte) {
			rows = append(rows, ReplayRow{Kind: ReplayRowMessage, Message: m})
		},
		onUsage: func(u, cum provider.Usage, _ int, delegated bool, at time.Time, _ []byte) {
			rows = append(rows, ReplayRow{Kind: ReplayRowUsage, Usage: u, Cumulative: cum, Delegated: delegated, At: at})
		},
		onCompaction: func(out, _ []provider.Message, _ int, _ []byte) {
			// walkSession aliases `out` as its live effective transcript after the
			// checkpoint, and a following amend (delete/replace) mutates that backing
			// array in place — so a retained reference would be corrupted later. Copy
			// per the walk-hook contract (sessionWalkHooks doc).
			rows = append(rows, ReplayRow{Kind: ReplayRowCompaction, Checkpoint: append([]provider.Message(nil), out...)})
		},
	}); err != nil {
		return nil, SessionMeta{}, err
	}
	// Same zero-Time backfill OpenSession applies (see backfillZeroTimes): a
	// pre-stamp-fix greeting row must not play back from year one.
	last := meta.Started
	for i := range rows {
		if rows[i].Kind != ReplayRowMessage {
			continue
		}
		if rows[i].Message.Time.IsZero() {
			rows[i].Message.Time = last
		} else {
			last = rows[i].Message.Time
		}
	}
	return rows, meta, nil
}

// StreamReplayMessages walks a session JSONL like ReadReplayRows but retains
// nothing: each message row is hydrated, handed to fn with its replay-row
// index, then discarded — so a caller inspecting a large transcript holds one
// message at a time instead of the whole file. Usage and compaction rows are
// counted (keeping row numbers aligned with ReadReplayRows' indexes, except
// for corrupt rows, which that reader drops) but never decoded — skipping
// checkpoint hydration entirely. Input is bounded at the read boundary: any
// single row past jsonlPerLineCeiling is skipped without being materialized
// whole (flagging truncated), and maxBytes > 0 caps the cumulative row bytes
// scanned; hitting either stops the walk with truncated=true and everything
// streamed so far still delivered. ctx is checked per delivered row so a long
// scan aborts promptly (returning ctx.Err()). Corrupt rows are skipped
// (best-effort, like the loader). The meta row is returned whenever present.
//
// Callers that also want the per-turn usage rows — what a turn cost, and how
// much of it hit the prefix cache — want [StreamReplayRows], which this
// delegates to. Cost is not derivable from the messages alone.
func StreamReplayMessages(ctx context.Context, path string, maxBytes int64, fn func(row int, m provider.Message)) (meta SessionMeta, truncated bool, err error) {
	return StreamReplayRows(ctx, path, maxBytes, func(row int, r ReplayRow) {
		if r.Kind == ReplayRowMessage {
			fn(row, r.Message)
		}
	})
}

// StreamReplayRows is StreamReplayMessages' general form: it delivers message
// AND usage rows in file order, under the same retain-nothing, bounded-input,
// cancellable contract. A usage row carries the turn's own Usage plus the
// running Cumulative exactly as recorded.
//
// Compaction rows are still counted but not hydrated — a checkpoint's message
// set is the one row whose payload is unbounded in the number of messages it
// holds, and materializing it would defeat the streaming guarantee the callers
// of this function are here for. Anything needing checkpoints wants
// ReadReplayRows.
func StreamReplayRows(ctx context.Context, path string, maxBytes int64, fn func(row int, r ReplayRow)) (meta SessionMeta, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, false, err
	}
	defer f.Close()

	row := 0
	rep := &loadReport{}
	// Streaming twin of backfillZeroTimes: the meta row leads the file, so its
	// Started is in hand before any pre-stamp-fix zero-Time row streams past.
	var last time.Time
	// An oversized row is skipped by the reader (never hydrated); it still means
	// the window is incomplete, so flag truncation.
	onOversize := func(int64) { truncated = true }
	walkErr := forEachJSONLLineBounded(f, jsonlPerLineCeiling, maxBytes, onOversize, func(line []byte) error {
		// Cancellation is checked once per delivered row so a long scan aborts
		// promptly; the drain/cumulative bounds cap the only path fn doesn't see.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		switch head.Type {
		case "meta":
			var mrow struct {
				Meta SessionMeta `json:"meta"`
			}
			if err := json.Unmarshal(line, &mrow); err == nil {
				meta = mrow.Meta
				if last.IsZero() {
					last = meta.Started
				}
			}
		case "message":
			m, err := hydrateMessage(line, rep)
			if err != nil || len(m.Content) == 0 {
				return nil
			}
			if m.Time.IsZero() {
				m.Time = last
			} else {
				last = m.Time
			}
			fn(row, ReplayRow{Kind: ReplayRowMessage, Message: m})
			row++
		case "usage":
			// Decoded, unlike compaction: a usage row is two fixed-size token
			// structs, so hydrating it costs nothing the stream cares about.
			// A corrupt one is skipped like any other row, but still consumes
			// its row number so coordinates stay aligned with ReadReplayRows.
			var urow struct {
				Usage      provider.Usage `json:"usage"`
				Cumulative provider.Usage `json:"cumulative"`
				At         *time.Time     `json:"at"`
			}
			if err := json.Unmarshal(line, &urow); err == nil {
				out := ReplayRow{Kind: ReplayRowUsage, Usage: urow.Usage, Cumulative: urow.Cumulative}
				if urow.At != nil {
					out.At = *urow.At
				}
				fn(row, out)
			}
			row++
		case "compaction":
			row++
		}
		return nil
	})
	if errors.Is(walkErr, errJSONLCumulative) {
		truncated = true
		walkErr = nil
	}
	if walkErr != nil {
		return SessionMeta{}, truncated, walkErr // e.g. context.Canceled or an I/O error
	}
	return meta, truncated, nil
}
