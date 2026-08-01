package modes

import (
	"context"
	"errors"
	"fmt"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// slashMemory opens the durable-memory panel: what the agent has decided is
// worth carrying into a future session, in both scopes, with the caps.
func (i *Interactive) slashMemory() {
	if i.cfg.Carrier == nil {
		i.setStatusErr(i18n.T("memory is unavailable in this mode"))
		return
	}
	scopes, rows, ok := i.memorySnapshot()
	if !ok {
		return
	}
	i.memoryDialog.Open(scopes, rows)
	i.invalidate()
}

// memorySnapshot fetches the pane and flattens it into the dialog's rows. It
// reports false (having already set a status) when memory is off or the fetch
// failed, so callers can simply return.
func (i *Interactive) memorySnapshot() ([]dialogs.MemoryScopeInfo, []dialogs.MemoryRow, bool) {
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "memory")
	if err != nil {
		// A session with memory switched off (--no-memory) answers NotFound —
		// a real "not available here", so keep the terse message. Anything else
		// is surfaced with context, so a broken carrier does not masquerade as
		// an intentional limitation.
		var ce *ctrlproto.Error
		if errors.As(err, &ce) && (ce.Code == ctrlproto.CodeNotFound || ce.Code == ctrlproto.CodeUnsupported) {
			i.setStatusErr(i18n.T("memory is switched off for this session"))
		} else {
			i.setStatusErr(i18n.T("memory unavailable: %s", err.Error()))
		}
		return nil, nil, false
	}
	if sf.Memory == nil {
		i.setStatusErr(i18n.T("memory is switched off for this session"))
		return nil, nil, false
	}
	return memoryRows(sf.Memory)
}

// memoryRows flattens the two scopes into headers + rows, user first — the same
// order the injected block uses, so the panel reads the way the model reads it.
//
// Within a scope the ACTIVE entries come first and the archived follow. That is
// the order of decreasing presence: what the model is reading right now, then
// what it could read if the conversation asked. Reading straight down answers
// "what does terva think it knows" without the reader having to hold a split.
func memoryRows(v *ctrlproto.MemoryView) ([]dialogs.MemoryScopeInfo, []dialogs.MemoryRow, bool) {
	var scopes []dialogs.MemoryScopeInfo
	var rows []dialogs.MemoryRow
	add := func(scope string, s ctrlproto.MemoryScope, bound bool) {
		scopes = append(scopes, dialogs.MemoryScopeInfo{
			Scope: scope, Label: s.Label, Count: len(s.Entries),
			Bytes: s.Bytes, MaxBytes: s.MaxBytes, Bound: bound,
			ArchivedCount: len(s.Archived), ArchivedBytes: s.ArchivedBytes,
			Problems: s.Problems,
		})
		for _, e := range s.Entries {
			rows = append(rows, dialogs.MemoryRow{Scope: scope, Text: e})
		}
		for _, a := range s.Archived {
			// Title, not body: archived entries are multi-line and up to 8 KiB,
			// so the list shows what an entry IS and the triggers under the
			// cursor say whether it can be reached.
			rows = append(rows, dialogs.MemoryRow{
				Scope: scope, Text: a.Title, Archived: true, Ref: a.Ref,
				Keys: a.Keys, Fired: a.Fired, Dropped: a.DroppedForBudget,
			})
		}
	}
	// The user scope is always bound when memory is on at all — it hangs off the
	// terva home, not off a project.
	add("user", v.User, true)
	add("project", v.Project, v.ProjectBound)
	return scopes, rows, true
}

// applyMemoryAction performs a panel action and refreshes the dialog from the
// server's answer rather than from a local guess, so a refused mutation leaves
// the panel showing what is actually stored.
func (i *Interactive) applyMemoryAction(a dialogs.MemoryAction) {
	if i.cfg.Carrier == nil {
		return
	}
	args := map[string]string{"scope": a.Scope}
	verb := ""
	switch {
	case a.Remove:
		verb, args["entry"] = "remove", a.Entry
	case a.Forget:
		verb, args["entry"] = "forget", a.Entry
	case a.Clear:
		verb = "clear"
	case a.Reload:
		verb = "reload"
	default:
		return
	}
	if err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "memory", verb, args); err != nil {
		i.memoryDialog.SetStatus(err.Error())
		i.invalidate()
		return
	}
	if scopes, rows, ok := i.memorySnapshot(); ok {
		i.memoryDialog.SetItems(scopes, rows)
	}
	i.invalidate()
}

// memoryGlance is the status-bar segment: how many facts terva is carrying, so
// a memory that is filling up (or one that is unexpectedly empty) is visible
// without opening a panel. Reads the cache; never fetches mid-frame.
func (i *Interactive) memoryGlance() string {
	i.mu.Lock()
	var v *ctrlproto.MemoryView
	if i.carrierMemorySession == i.cfg.CarrierSession {
		v = i.carrierMemory
	}
	i.mu.Unlock()
	if v == nil {
		return ""
	}
	n := len(v.User.Entries) + len(v.Project.Entries)
	// Archived entries are counted, and counted SEPARATELY. Counted, because a
	// glance that reports 4 while terva is carrying 40 facts understates what it
	// knows; separately, because the first number is what rides every request and
	// the second costs nothing until it fires, and adding them would hide exactly
	// the distinction the tier exists to make.
	arch := len(v.User.Archived) + len(v.Project.Archived)
	switch {
	case n == 0 && arch == 0:
		return ""
	case arch == 0:
		return fmt.Sprintf("🧠 %d", n)
	}
	return fmt.Sprintf("🧠 %d+%d", n, arch)
}

// refreshCarrierMemory fetches the memory surface off the pump goroutine and
// replaces the cache, then repaints. A NotFound (memory off in this session)
// clears the cache so the glance vanishes rather than freezing at its last
// value — the same contract the task-board cache keeps.
func (i *Interactive) refreshCarrierMemory() {
	if i.cfg.Carrier == nil {
		return
	}
	sess := i.carrierSession()
	sf, err := i.cfg.Carrier.Surface(context.Background(), sess, "memory")
	i.mu.Lock()
	if i.cfg.CarrierSession != sess {
		// A session switch landed while this fetch was in flight — its result
		// belongs to the previous binding. Drop it rather than letting a delayed
		// A response overwrite B's newer state.
		i.mu.Unlock()
		return
	}
	if err != nil || sf.Memory == nil {
		i.carrierMemory = nil
	} else {
		i.carrierMemory = sf.Memory
	}
	i.carrierMemorySession = sess
	i.mu.Unlock()
	i.invalidate()
}
