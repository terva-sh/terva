package core

import (
	"encoding/json"
	"io"

	"terva.sh/terva/packages/provider"
)

// sessionWalkHooks observe a session's rows as walkSession replays them in file
// order. Every hook is optional (nil is skipped). The effective-transcript
// bookkeeping — append a message row, reset on a compaction checkpoint — lives
// in walkSession itself, so the several readers that reconstruct a transcript
// (OpenSession, BranchSession, RevealCompaction, ReadReplayRows) share ONE
// implementation of it. That is the point of this seam: a future transcript
// transform (an amend row) is taught to walkSession alone, and every reader
// inherits it instead of drifting.
//
// A hook's []byte line is the raw JSONL bytes of the row; a []provider.Message
// slice passed to a hook is only valid for the duration of the call (walkSession
// may reuse or replace the backing array afterward), so a hook that retains one
// must copy it.
type sessionWalkHooks struct {
	// onMessage fires for each non-empty message row AFTER it is appended to the
	// effective transcript; idx is its position there (the new last index).
	onMessage func(m provider.Message, idx int, line []byte)
	// onCompaction fires for each compaction checkpoint BEFORE the effective
	// transcript is reset to out. before is the transcript the checkpoint
	// summarized (its input); out is the checkpoint's stored output; ordinal is
	// the 0-based checkpoint index within the file.
	onCompaction func(out, before []provider.Message, ordinal int, line []byte)
	// onUsage fires for each usage row; effLen is the effective-transcript length
	// at that point (usage rows do not change it). delegated marks a SUB-AGENT's
	// spend booked against this session rather than a request it sent — a
	// distinction any cost or cache-hit analysis has to make, because a fresh
	// child's usage looks exactly like the parent's cache collapsing.
	onUsage func(u, cum provider.Usage, effLen int, delegated bool, line []byte)
	// onDirective fires for each append-only directive row (e.g. exclude_image).
	onDirective func(d sessionDirective, line []byte)
	// onToolGroup fires for each tool_group row — a capability group activated
	// during the session, which a resume must re-mark to keep the tools array
	// (and so the provider's cached prefix) identical.
	onToolGroup func(group string, line []byte)
	// onAmend fires for each amend row AFTER it is applied to the effective
	// transcript, with the op and the (as-written) index.
	onAmend func(op string, index int, line []byte)
	// onTail fires ONCE at the end of the walk with the current tail span's
	// variant state: takes are its alternative spans (creation order), active
	// indexes the one currently live in effective[tailStart:]. tailStart < 0 (and
	// len(takes) < 2) means the last response has no alternatives to swipe.
	onTail func(tailStart int, takes [][]provider.Message, active int)
	// onMsgVariants fires ONCE at the end of the walk with every message-scoped
	// variant position (Option C): index → its retained-history takes and active
	// take. Independent of onTail (which is the tail suffix span). Empty/unset when
	// no edit-as-variant was recorded.
	onMsgVariants func(vars map[int]MsgVariants)
	// onMeta fires for each meta row, with the parsed metadata.
	onMeta func(m SessionMeta, line []byte)
	// onLore fires for each World-lore row, with the parsed op. The reader folds
	// it (applyLoreOp); the walk keeps no book of its own, because the pre-v4
	// half of the fold lives on the meta rows and only the reader sees both.
	onLore func(op sessionLore, line []byte)
	// onRename fires for each rename row, with the new title and its source.
	onRename func(title, source string, line []byte)
}

// msgVarState tracks one position's message-scoped variant set during a walk: the
// retained-history takes (creation order) and which is active in effective.
type msgVarState struct {
	takes  []provider.Message
	active int
}

// walkSession replays a session JSONL from r in file order and returns the
// effective transcript a resume would reconstruct: message rows append, a
// compaction checkpoint replaces everything before it. Message hydration, the
// drop of empty-content messages, and best-effort skipping of corrupt rows all
// match the loader; rep accumulates the corrupt-line count. Hooks (all optional)
// observe rows and compaction boundaries as they pass.
func walkSession(r io.Reader, rep *loadReport, h sessionWalkHooks) ([]provider.Message, error) {
	var effective []provider.Message
	ordinal := 0
	// Tail-span variant tracking (for the swipe UX). tailStart < 0 means no
	// tracked span; takes are the tail span's alternative spans in creation order;
	// active indexes the one currently live in effective[tailStart:]. Maintained by
	// retract/select amends (which is why it runs even when onTail is unset — a
	// select needs the takes to reconstruct effective) and reset by a new user turn.
	tailStart := -1
	var takes [][]provider.Message
	active := 0
	cloneSpan := func(s []provider.Message) []provider.Message {
		return append([]provider.Message(nil), s...)
	}
	sealActiveTake := func() {
		if tailStart >= 0 && tailStart <= len(effective) && active >= 0 && active < len(takes) {
			takes[active] = cloneSpan(effective[tailStart:])
		}
	}
	beginTail := func(index int) {
		if tailStart != index { // seed take 0 from the span already at index
			tailStart = index
			takes = [][]provider.Message{cloneSpan(effective[index:])}
			active = 0
		}
	}
	resetTail := func() {
		tailStart, takes, active = -1, nil, 0
	}
	// Message-scoped variants (Option C), by effective index. Independent of the
	// tail suffix span above and NOT reset by a new user turn — an edited message
	// stays swipeable at its position. Compaction/truncate collapse it (below).
	msgVars := map[int]*msgVarState{}
	err := forEachJSONLLine(r, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			rep.corruptLines++
			return nil
		}
		switch head.Type {
		case "message":
			msg, err := hydrateMessage(line, rep)
			if err != nil {
				rep.corruptLines++
				return nil
			}
			if len(msg.Content) == 0 {
				return nil
			}
			effective = append(effective, msg)
			// A new user turn commits the previous response, so its takes are no
			// longer the tail to swipe. Tool results (RoleTool) do NOT reset, so a
			// tool-using take stays intact.
			if msg.Role == provider.RoleUser {
				resetTail()
			}
			if h.onMessage != nil {
				h.onMessage(msg, len(effective)-1, line)
			}
		case "compaction":
			out, err := hydrateCompaction(line, rep)
			if err != nil {
				rep.corruptLines++
				return nil
			}
			if h.onCompaction != nil {
				h.onCompaction(out, effective, ordinal, line)
			}
			effective = out
			ordinal++
			// A checkpoint snapshots the already-folded transcript, so amend chains
			// never cross it — variant history (tail and message-scoped) collapses.
			msgVars = map[int]*msgVarState{}
		case "usage":
			if h.onUsage != nil {
				var row struct {
					Usage      provider.Usage `json:"usage"`
					Cumulative provider.Usage `json:"cumulative"`
					Delegated  bool           `json:"delegated"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					rep.corruptLines++
					return nil
				}
				h.onUsage(row.Usage, row.Cumulative, len(effective), row.Delegated, line)
			}
		case recordDirective:
			if h.onDirective != nil {
				var row struct {
					Directive sessionDirective `json:"directive"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					rep.corruptLines++
					return nil
				}
				h.onDirective(row.Directive, line)
			}
		case recordToolGroup:
			if h.onToolGroup != nil {
				var row struct {
					ToolGroup toolGroupRecord `json:"tool_group"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					rep.corruptLines++
					return nil
				}
				if row.ToolGroup.Group != "" {
					h.onToolGroup(row.ToolGroup.Group, line)
				}
			}
		case recordAmend:
			// An amend revises the effective transcript in place, in file order —
			// the fold every reader now inherits from here. Out-of-range indices
			// are ignored (best-effort, like the loader's other skips).
			var row struct {
				Amend struct {
					Op        string          `json:"op"`
					Index     int             `json:"index"`
					Variant   int             `json:"variant"`
					Message   json.RawMessage `json:"message"`
					KeepPrior bool            `json:"keep_prior"`
				} `json:"amend"`
			}
			if err := json.Unmarshal(line, &row); err != nil {
				rep.corruptLines++
				return nil
			}
			a := row.Amend
			switch a.Op {
			case AmendReplace:
				if a.Index >= 0 && a.Index < len(effective) && len(a.Message) > 0 {
					if m, err := hydrateMessageObject(a.Message, rep); err == nil && len(m.Content) > 0 {
						if a.KeepPrior {
							// Retained-history replace: keep the overwritten message as
							// a swipeable take at this index (seed take 0 from the prior
							// on the first edit), then make the new one active.
							mv := msgVars[a.Index]
							if mv == nil {
								mv = &msgVarState{takes: []provider.Message{effective[a.Index]}}
								msgVars[a.Index] = mv
							}
							mv.takes = append(mv.takes, m)
							mv.active = len(mv.takes) - 1
						}
						effective[a.Index] = m
					}
				}
			case AmendMsgSelect:
				// Switch a message-scoped variant's active take (swipe-back).
				if mv := msgVars[a.Index]; mv != nil && a.Index >= 0 && a.Index < len(effective) &&
					a.Variant >= 0 && a.Variant < len(mv.takes) {
					mv.active = a.Variant
					effective[a.Index] = mv.takes[a.Variant]
				}
			case AmendSeal:
				// Collapse a message-scoped variant to take Variant and close it
				// (prune-to-latest): the other takes stop being reconstructed.
				if mv := msgVars[a.Index]; mv != nil && a.Index >= 0 && a.Index < len(effective) &&
					a.Variant >= 0 && a.Variant < len(mv.takes) {
					effective[a.Index] = mv.takes[a.Variant]
					delete(msgVars, a.Index)
				}
			case AmendDropTake:
				// Remove one take from a message-scoped variant, keeping the rest
				// swipeable; close the position when a single take remains. Guarded to
				// >= 2 takes (a live position always has that) so a drop can't empty it.
				if mv := msgVars[a.Index]; mv != nil && a.Index >= 0 && a.Index < len(effective) &&
					len(mv.takes) >= 2 && a.Variant >= 0 && a.Variant < len(mv.takes) {
					mv.takes = append(mv.takes[:a.Variant], mv.takes[a.Variant+1:]...)
					switch {
					case a.Variant < mv.active:
						mv.active--
					case a.Variant == mv.active && mv.active >= len(mv.takes):
						mv.active = len(mv.takes) - 1 // dropped the last take; fall back
					}
					if len(mv.takes) <= 1 {
						effective[a.Index] = mv.takes[0]
						delete(msgVars, a.Index)
					} else {
						effective[a.Index] = mv.takes[mv.active]
					}
				}
			case AmendDelete:
				if a.Index >= 0 && a.Index < len(effective) {
					effective = append(effective[:a.Index], effective[a.Index+1:]...)
					// Keep message-variant indices aligned with the shifted transcript.
					if len(msgVars) > 0 {
						shifted := make(map[int]*msgVarState, len(msgVars))
						for idx, mv := range msgVars {
							if idx == a.Index {
								continue // gone with the deleted message
							}
							if idx > a.Index {
								idx--
							}
							shifted[idx] = mv
						}
						msgVars = shifted
					}
				}
			case AmendTruncate:
				if a.Index >= 0 && a.Index <= len(effective) {
					effective = effective[:a.Index]
					for idx := range msgVars {
						if idx >= a.Index {
							delete(msgVars, idx) // truncated away
						}
					}
				}
			case AmendRetract:
				// Set the current span aside as a take and begin a new one at Index.
				if a.Index >= 0 && a.Index <= len(effective) {
					beginTail(a.Index) // seeds take 0 from the existing span (if new)
					sealActiveTake()   // sync the take that was growing in effective
					takes = append(takes, nil)
					active = len(takes) - 1
					effective = effective[:a.Index]
				}
			case AmendSelect:
				// Make stored take Variant active, restoring it into effective.
				if a.Index >= 0 && a.Index <= len(effective) {
					beginTail(a.Index)
					sealActiveTake()
					if a.Variant >= 0 && a.Variant < len(takes) {
						active = a.Variant
						effective = append(effective[:a.Index:a.Index], takes[a.Variant]...)
					}
				}
			}
			if h.onAmend != nil {
				h.onAmend(a.Op, a.Index, line)
			}
		case "meta":
			if h.onMeta != nil {
				var row struct {
					Meta SessionMeta `json:"meta"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					rep.corruptLines++
					return nil
				}
				h.onMeta(row.Meta, line)
			}
		case recordLore:
			if h.onLore != nil {
				var row struct {
					Lore sessionLore `json:"lore"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					rep.corruptLines++
					return nil
				}
				h.onLore(row.Lore, line)
			}
		case "rename":
			if h.onRename != nil {
				var row struct {
					Title  string `json:"title"`
					Source string `json:"source"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					rep.corruptLines++
					return nil
				}
				h.onRename(row.Title, row.Source, line)
			}
		}
		return nil
	})
	if h.onTail != nil {
		sealActiveTake() // sync the live active take before reporting
		h.onTail(tailStart, takes, active)
	}
	if h.onMsgVariants != nil && len(msgVars) > 0 {
		out := make(map[int]MsgVariants, len(msgVars))
		for idx, mv := range msgVars {
			out[idx] = MsgVariants{Takes: mv.takes, Active: mv.active}
		}
		h.onMsgVariants(out)
	}
	return effective, err
}
