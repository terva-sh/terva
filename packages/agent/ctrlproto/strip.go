package ctrlproto

import "terva.sh/terva/packages/core"

// The workspace hub broadcasts events in the FULL wire form — image blocks
// carry their raw Data so in-process subscribers render real pixels at zero
// cost (the slices are shared, never serialized). A serialized carrier must
// not ship those payloads to a client that didn't ask for them, so the
// serve loop strips them at the connection boundary unless the negotiated
// contract includes FeatureImageData.
//
// Every helper here is copy-on-strip: one Event value is shared by every
// subscriber of a session, so the original is NEVER mutated — a stripped
// copy is built lazily, and only for the parts that actually held data.
// Stripping yields exactly the lean wire shape (Bytes stays; Data goes),
// so a non-negotiating client sees byte-identical frames to the pre-Full
// protocol.

// HistoryWindow is how many trailing messages a snapshot carries to a client that
// negotiated FeatureHistoryWindow. It pages the rest in with conversation.history.
//
// 80 is the TUI's own first-paint cap (modes.initialResumeTailLimit) and is chosen
// the same way: comfortably more than a screenful, so the common case never pages at
// all, while a multi-thousand-message session stops shipping whole down a phone's
// WebSocket at the end of every turn.
const HistoryWindow = 80

// windowSnapshot cuts a snapshot's transcript down to its last HistoryWindow
// messages, stamping the Base the window starts at so the client can place it.
//
// Copy-on-window, like every helper here: one Event value is shared by every
// subscriber of a session, so the original is NEVER mutated. A snapshot already
// short enough is returned untouched apart from its placement fields, which are the
// same values a full transcript would carry.
func windowSnapshot(ev Event, limit int) Event {
	if ev.Snapshot == nil || limit <= 0 || len(ev.Snapshot.Messages) <= limit {
		return ev
	}
	snap := *ev.Snapshot
	base := len(snap.Messages) - limit
	// Reslice, not copy: the messages themselves are shared read-only, and the whole
	// point of this function is to stop moving them around.
	snap.Messages = snap.Messages[base:]
	snap.Base = base
	ev.Snapshot = &snap
	return ev
}

// stripImageData returns ev without outbound image payloads, copying only
// what it must. Events with no image data are returned unchanged.
func stripImageData(ev Event) Event {
	if ev.Message != nil {
		if m, changed := msgWithoutImageData(*ev.Message); changed {
			ev.Message = &m
		}
	}
	if blocks, changed := blocksWithoutImageData(ev.Result); changed {
		ev.Result = blocks
	}
	if ev.Snapshot != nil && len(ev.Snapshot.Messages) > 0 {
		var msgs []core.WireMessage
		for i, m := range ev.Snapshot.Messages {
			mm, changed := msgWithoutImageData(m)
			if !changed {
				continue
			}
			if msgs == nil {
				msgs = append([]core.WireMessage(nil), ev.Snapshot.Messages...)
			}
			msgs[i] = mm
		}
		if msgs != nil {
			snap := *ev.Snapshot
			snap.Messages = msgs
			ev.Snapshot = &snap
		}
	}
	return ev
}

func msgWithoutImageData(m core.WireMessage) (core.WireMessage, bool) {
	blocks, changed := blocksWithoutImageData(m.Content)
	if changed {
		m.Content = blocks
	}
	return m, changed
}

// blocksWithoutImageData strips image Data from blocks (recursing into
// tool_result content). Returns the input slice untouched, false when
// nothing held data.
func blocksWithoutImageData(blocks []core.WireBlock) ([]core.WireBlock, bool) {
	var out []core.WireBlock
	for i, b := range blocks {
		nb := b
		changed := false
		if b.Type == "image" && len(b.Data) > 0 {
			nb.Data = nil
			changed = true
		}
		if inner, ch := blocksWithoutImageData(b.Content); ch {
			nb.Content = inner
			changed = true
		}
		if changed && out == nil {
			out = append([]core.WireBlock(nil), blocks...)
		}
		if out != nil {
			out[i] = nb
		}
	}
	if out == nil {
		return blocks, false
	}
	return out, true
}
