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
	PrevOrdinal int                // the checkpoint before it, or -1 if this is the first
	Replaced    []provider.Message // the messages the checkpoint folded into its summary
	KeptCount   int                // messages the checkpoint preserved verbatim (its tail)
	Total       int                // total compaction checkpoints in the file
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
	for _, r := range rows {
		if !r.compaction {
			messages = append(messages, r.msg)
			continue
		}
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
	}, nil
}
