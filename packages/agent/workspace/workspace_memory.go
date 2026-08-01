package workspace

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/i18n"
)

// memoryTool returns this session's memory tool, or nil when memory is off
// (--no-memory) or the session was built without one. Reads the RETAINED
// instance, not the live registry: a tool rebuild re-registers the same pointer
// (Resolved.UseMemory), and reading the registry would work today but would go
// stale the moment something rebuilt without that call.
func (s *wsSession) memoryTool() *tools.MemoryTool {
	if s == nil {
		return nil
	}
	return s.memory
}

// hasMemory reports whether this session has durable memory, which gates the
// pane's presence in the surface list. A pane that can never populate is worse
// than no pane: it reads as broken rather than as switched off.
func (s *wsSession) hasMemory() bool { return s.memoryTool() != nil }

// memoryView renders the pane. Reads the tool's own stores, so the pane, the
// injected block, and the model's own writes are all one source of truth.
func (s *wsSession) memoryView() *ctrlproto.MemoryView {
	mt := s.memoryTool()
	if mt == nil {
		return &ctrlproto.MemoryView{}
	}
	// The trace is read ONCE and shared by both scopes, because both are matched
	// in one Select: reading it per scope could straddle a turn and show an entry
	// as fired against a trace the other scope was not measured in.
	fired := recallIndex(mt.LastRecall())
	v := &ctrlproto.MemoryView{
		User:    memoryScope(mt.User, mt.UserArchive, fired),
		Project: memoryScope(mt.Project, mt.ProjectArchive, fired),
	}
	v.ProjectBound = mt.Project != nil && mt.Project.Bound()
	return v
}

// recallIndex keys the last turn's activation trace by the entry Ref it names,
// so a scope's entries can be annotated without rescanning the trace per row.
func recallIndex(fired []memory.RecallFired) map[string]memory.RecallFired {
	if len(fired) == 0 {
		return nil
	}
	out := make(map[string]memory.RecallFired, len(fired))
	for _, f := range fired {
		out[f.Ref] = f
	}
	return out
}

func memoryScope(st *memory.Store, arch *memory.Archive, fired map[string]memory.RecallFired) ctrlproto.MemoryScope {
	out := ctrlproto.MemoryScope{}
	if st != nil {
		bytes, maxBytes, maxCount := st.Budget()
		out.Label, out.Entries = st.Label(), st.List()
		out.Bytes, out.MaxBytes, out.MaxCount = bytes, maxBytes, maxCount
	}
	if arch == nil {
		return out
	}
	out.ArchivedBytes, out.ArchivedMaxBytes = arch.Bytes(), memory.MaxArchiveBytes
	out.Problems = arch.Problems()
	for _, e := range arch.List() {
		row := ctrlproto.MemoryArchivedEntry{
			Ref: e.Ref(), Title: e.Title(),
			Keys: e.Keys, SecondaryKeys: e.SecondaryKeys,
			Bytes: len(e.Text), Text: e.Text,
		}
		if f, ok := fired[e.Ref()]; ok {
			row.Fired, row.MatchedKeys, row.DroppedForBudget = true, f.Keys, f.Dropped
		}
		out.Archived = append(out.Archived, row)
	}
	return out
}

// memoryAction handles the pane's mutations. The verbs are deliberately few:
// this is a curated list, and the model is the curator — the human surface
// exists to prune and to reset, not to author.
//
// Every mutation reloads and re-renders the system prompt afterwards, because a
// user deleting a fact from the pane should not leave the model still reading it
// out of the cached prefix for the rest of the session. That is the one place
// memory's frozen block is deliberately refreshed outside a session boundary:
// the block is frozen against the MODEL's writes (it sees those in the tool's
// reply), not against the user's, which it has no other way to learn about.
func (s *wsSession) memoryAction(action string, args map[string]string) error {
	mt := s.memoryTool()
	if mt == nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("memory is not enabled for this session"))
	}
	store, err := memoryScopeArg(mt, args)
	if err != nil {
		return err
	}
	arch := memoryArchiveArg(mt, args)
	switch action {
	case "forget":
		// The archived tier's delete. A separate verb rather than a flag on
		// remove: the two tiers address entries differently (a substring of the
		// text vs an id), and one verb taking either would have to guess which
		// was meant on a string that could plausibly be both.
		if arch == nil {
			return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no memory archive in this session"))
		}
		ref := args["entry"]
		if strings.TrimSpace(ref) == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("forget needs the archived entry to delete"))
		}
		if _, err := arch.Remove(ref); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", err.Error())
		}
	case "remove":
		entry := args["entry"]
		if strings.TrimSpace(entry) == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("remove needs the entry to delete"))
		}
		// Matched on the FULL entry text the pane displayed, which is an
		// unambiguous substring of itself — the pane must never send a prefix
		// and hope, because the store's match is a substring search and a short
		// one could resolve to a different row than the user clicked.
		if err := store.Remove(entry); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", err.Error())
		}
	case "clear":
		// The ACTIVE tier only, deliberately. Clearing is already the one
		// irreversible action here, and the archived tier is the half that is
		// expensive to rebuild — long procedures with hand-tuned triggers, up to
		// two orders of magnitude more of it. Bulk-deleting that behind the same
		// keystroke that empties a dozen one-line facts is not a proportionate
		// default; archived entries go one at a time, which is also how anyone
		// would want to review them.
		if err := store.Clear(); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", err.Error())
		}
	case "reload":
		// Pull writes another instance (or a hand-edit) made since this session
		// loaded. The file is the interface; someone editing memory.md in an
		// editor is a supported workflow, not a workaround — and that goes double
		// for the archive, whose entries are whole markdown files.
		if err := store.Reload(); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", err.Error())
		}
		// Both archives, not just this scope's: reload is the "show me what is
		// actually on disk" verb, and reloading half of what the pane displays
		// would leave the other half stale in the same view.
		if err := mt.UserArchive.Reload(); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", err.Error())
		}
		if err := mt.ProjectArchive.Reload(); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", err.Error())
		}
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", fmt.Sprintf(i18n.T("unknown memory action %q"), action))
	}
	// A user deleting a fact must not leave the model still reading it out of the
	// cached prefix for the rest of the session. This is the one place memory's
	// frozen block is refreshed outside a session boundary: it is frozen against
	// the MODEL's writes (which it sees in the tool's reply), not the user's,
	// which it has no other way to learn about. rebuildTools re-Resolves and
	// SetSystem's the result, which re-renders MemoryBlock from these stores.
	s.rebuildTools("memory")
	s.broadcast(ctrlproto.SurfaceUpdatedEvent("memory"))
	return nil
}

// memoryScopeArg resolves the scope argument to a store. Defaults to project,
// matching the tool, and refuses an unrecognised value rather than guessing:
// clearing the wrong scope is not recoverable from the pane.
func memoryScopeArg(mt *tools.MemoryTool, args map[string]string) (*memory.Store, error) {
	scope := args["scope"]
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", memory.ScopeProject:
		if mt.Project == nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no project memory in this session"))
		}
		return mt.Project, nil
	case memory.ScopeUser:
		if mt.User == nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no user memory available"))
		}
		return mt.User, nil
	default:
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", fmt.Sprintf(i18n.T("unknown memory scope %q"), scope))
	}
}

// memoryArchiveArg resolves the scope argument to that scope's archive. Returns
// nil rather than an error, because only the archive verbs need one and the
// active-tier verbs must keep working when no archive is bound.
func memoryArchiveArg(mt *tools.MemoryTool, args map[string]string) *memory.Archive {
	if strings.EqualFold(strings.TrimSpace(args["scope"]), memory.ScopeUser) {
		return mt.UserArchive
	}
	return mt.ProjectArchive
}
