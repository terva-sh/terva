package core

import (
	"encoding/json"
	"fmt"
	"os"

	"terva.sh/terva/packages/provider"
)

// CompactionSpan is what one compaction checkpoint summarized away, reconstructed
// from the append-only transcript. The superseded "message" rows are never
// rewritten (a "compaction" row is a checkpoint the loader honors, not a
// deletion), so this is a pure, deterministic read. See
// docs/proposals/context-inspector.md.
type CompactionSpan struct {
	Ordinal     int                // 0-based position among the file's "compaction" rows
	PrevOrdinal int                // the checkpoint before it, or -1 if there is nothing further to reveal
	Replaced    []provider.Message // the messages the checkpoint folded into its summary
	KeptCount   int                // messages the checkpoint preserved verbatim (its tail)
	Total       int                // total compaction checkpoints in the file
	// Clear is true when the TARGET checkpoint is a /clear rather than a compaction,
	// and PrevClear when the one BEHIND it is. A clear is written as an empty
	// checkpoint (AppendCompaction(nil), workspace_session.go): it summarized nothing
	// and kept nothing, so its Replaced is the whole conversation before it and it
	// leaves no summary message in the transcript to hang a divider on.
	//
	// A clear is a deliberate act — "I am done with that; start fresh" — and closer
	// to a session boundary than to a compaction, which merely condenses a
	// conversation you are still having. So a client walking history backward STOPS
	// at one: on PrevClear it stops chaining automatically and offers to cross only
	// as an explicit, separate choice. Deliberate to make, deliberate to undo.
	//
	// PrevOrdinal stays truthful either way — the floor is a policy the caller
	// applies, not a fact hidden from it. Crossing is served, not refused; a client
	// that means it passes the clear's own ordinal.
	//
	// This is an INTENT boundary, NOT redaction. The rows are still in the JSONL in
	// plaintext, and --replay's raw mode, export, and session_inspect all read them.
	// Nothing here makes a secret pasted before a clear go away.
	Clear     bool
	PrevClear bool
}

// RevealCompaction reconstructs what the compaction checkpoint at ordinal
// summarized away (ordinal < 0 means the latest). It replays the loader's
// walk — appending "message" rows, resetting on each "compaction" row — and
// snapshots the accumulated transcript at the instant just before the target
// checkpoint resets it, which is exactly the input that checkpoint summarized.
// The summarized-away span is that input minus the tail the checkpoint kept
// (inferred from the checkpoint's own output length, not the AutoCompactKeepTail
// constant, since orphan-tool repair can shorten the stored tail). Read-only;
// the live session is untouched.
func RevealCompaction(path string, ordinal int) (CompactionSpan, error) {
	f, err := os.Open(path)
	if err != nil {
		return CompactionSpan{}, err
	}
	defer f.Close()

	// A parsed row: either an appended message or a checkpoint with its output.
	type row struct {
		compaction bool
		msg        provider.Message
		out        []provider.Message
	}
	rep := &loadReport{}
	var rows []row
	if err := forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		switch head.Type {
		case "message":
			if m, err := hydrateMessage(line, rep); err == nil && len(m.Content) > 0 {
				rows = append(rows, row{msg: m})
			}
		case "compaction":
			if out, err := hydrateCompaction(line, rep); err == nil {
				rows = append(rows, row{compaction: true, out: out})
			}
		}
		return nil
	}); err != nil {
		return CompactionSpan{}, err
	}

	total := 0
	for _, r := range rows {
		if r.compaction {
			total++
		}
	}
	if total == 0 {
		return CompactionSpan{}, fmt.Errorf("session has no compaction checkpoints")
	}
	target := ordinal
	if target < 0 {
		target = total - 1
	}
	if target < 0 || target >= total {
		return CompactionSpan{}, fmt.Errorf("no compaction checkpoint at ordinal %d (have %d)", ordinal, total)
	}

	var messages, input []provider.Message
	keptCount := 0
	c := 0
	// A clear is an EMPTY checkpoint: it kept no tail and left no summary behind.
	// Recorded per checkpoint so the floor below can see one without re-walking.
	clear := make([]bool, total)
	for _, r := range rows {
		if !r.compaction {
			messages = append(messages, r.msg)
			continue
		}
		clear[c] = len(r.out) == 0
		if c == target {
			input = append([]provider.Message(nil), messages...) // snapshot before the reset
			if keptCount = len(r.out) - 1; keptCount < 0 {
				keptCount = 0 // a "clear" checkpoint has no summary/tail
			}
		}
		messages = r.out
		c++
	}

	span := input
	if n := len(input) - keptCount; n >= 0 && n <= len(input) {
		span = input[:n]
	}
	prev := target - 1 // -1 when target is the first checkpoint
	return CompactionSpan{
		Ordinal:     target,
		PrevOrdinal: prev,
		Replaced:    span,
		KeptCount:   keptCount,
		Total:       total,
		Clear:       clear[target],
		PrevClear:   prev >= 0 && clear[prev],
	}, nil
}
