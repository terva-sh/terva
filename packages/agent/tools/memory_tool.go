package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// MemoryTool curates the agent's durable memory: facts worth carrying into a
// future session, in a project scope and a cross-project user scope, each split
// across two tiers.
//
// The tiers are a CACHE split, not a filing system. Active entries ride the
// cached system prefix and are present on every turn; archived entries are
// absent until the conversation's own words match their keys, and then ride the
// per-turn tail. That is why the archive can be two orders of magnitude larger
// than the active block without costing anything until it fires — and why what
// is rationed is being always-on, not being remembered.
//
// It writes durable files and nothing else — no workspace, no network. That is
// the honest authority classification: it mutates, so it is not read-only, but
// the only thing it can touch is terva's own state, which is why it stays
// available in plan and read-only modes. A model that cannot record what it
// learned in a mode meant for investigation would lose exactly the findings that
// mode exists to produce. The archive keeps that property: retrieval is a
// keyword match over local files, and deriving keys with a model call would
// forfeit it, which is why the curator supplies them.
type MemoryTool struct {
	Project *memory.Store
	User    *memory.Store

	// ProjectArchive / UserArchive are the keyed tiers for the two scopes. nil
	// when there is nothing to bind them to; every archive action then refuses
	// rather than silently writing somewhere nothing will look again.
	ProjectArchive *memory.Archive
	UserArchive    *memory.Archive

	// recallMu guards lastRecall, which the per-turn tail writes (turn goroutine)
	// and the /memory pane reads (a surface call).
	recallMu   sync.Mutex
	lastRecall []memory.RecallFired
}

// SetLastRecall records which archived entries matched on the turn just built.
//
// It lives on the tool rather than on Resolved because the tool is already the
// one per-session pointer BOTH sides hold: the tail closure reads its archives
// to do the matching, and the pane reads them to list what is stored. A second
// shared record on Resolved would be a second thing every host has to allocate,
// which is the shape that has repeatedly gone wrong here.
//
// Only the recording tail calls this. The sizing twin behind /context must not,
// or opening a size view would overwrite the trace of the turn that actually ran
// with one computed against a transcript nobody sent — the same contract
// LoreFiredRecord keeps.
func (t *MemoryTool) SetLastRecall(fired []memory.RecallFired) {
	if t == nil {
		return
	}
	t.recallMu.Lock()
	t.lastRecall = fired
	t.recallMu.Unlock()
}

// LastRecall returns the most recent turn's activation trace — nil before the
// first turn, or after a turn on which nothing matched.
func (t *MemoryTool) LastRecall() []memory.RecallFired {
	if t == nil {
		return nil
	}
	t.recallMu.Lock()
	defer t.recallMu.Unlock()
	return append([]memory.RecallFired(nil), t.lastRecall...)
}

type memoryArgs struct {
	Action        string   `json:"action"`
	Scope         string   `json:"scope"`
	Text          string   `json:"text"`
	Match         string   `json:"match"`
	Name          string   `json:"name"`
	Keys          []string `json:"keys"`
	SecondaryKeys []string `json:"secondary_keys"`
}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Description() string {
	return "Curate durable memory that survives this session, in two scopes and two tiers.\n\n" +
		"scope: project (default; facts about this repo — conventions, gotchas, where things live) or user (cross-project facts about the person you work with — preferences, environment, how they like to work).\n\n" +
		"ACTIVE tier — always in your context, so it stays small and terse. add (append `text`), replace (swap the entry containing `match` for `text`), remove (delete the entry containing `match`).\n\n" +
		"ARCHIVED tier — not in your context until its keys match the conversation, so it can be long and detailed. archive (store `text`, or move the active entry matching `match`, with `keys`), search (find archived entries matching `text`), recall (read the archived entry named by `match`), promote (move an archived entry back into the active tier), forget (delete an archived entry).\n\n" +
		"Choosing keys is the hard part and it decides whether the memory is ever seen again. Key on what someone would TYPE when they need the fact, not on the identifiers inside it: a note about model-catalog internals belongs on keys like \"model\", \"catalog\", \"add a model\" — the person asking does not know the function name yet, which is why they are asking. Exact error text and file paths are good keys when someone would paste them. Use `secondary_keys` to narrow a broad primary key; an entry fires when a primary matches AND at least one secondary does.\n\n" +
		"Archive what is worth keeping but not worth carrying every turn: procedures, investigations, anything longer than a line or two. Keep the active tier for what shapes every conversation. Returns the updated memory for the scope you touched, so you see your change immediately — the block shown at session start does not refresh until the next session or a compaction."
}

func (t *MemoryTool) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "replace", "remove", "archive", "search", "recall", "promote", "forget"},
				"description": "active tier: add | replace | remove — archived tier: archive | search | recall | promote | forget",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{memory.ScopeProject, memory.ScopeUser},
				"description": "project (default) | user — which memory to curate",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "the entry to add or archive, the new text for replace, or the query for search",
			},
			"match": map[string]any{
				"type":        "string",
				"description": "which entry to act on: a unique substring for the active tier, or an archived entry's id",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "archive only: a short title for the entry (it also becomes the entry's id)",
			},
			"keys": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "archive only, required: the words someone would type when they need this fact",
			},
			"secondary_keys": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "archive only: narrows a broad primary key — the entry fires when a key matches AND at least one of these does",
			},
		},
		"required": []string{"action"},
	})
	return b
}

func (t *MemoryTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a memoryArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "archive", "search", "recall", "promote", "forget":
		return t.executeArchive(action, a)
	}

	store, err := t.storeFor(a.Scope)
	if err != nil {
		return toolErr("memory: " + err.Error()), nil
	}
	switch action {
	case "add":
		err = store.Add(a.Text)
	case "replace":
		err = store.Replace(a.Match, a.Text)
	case "remove":
		err = store.Remove(a.Match)
	default:
		return toolErr("memory: action must be one of: add, replace, remove, archive, search, recall, promote, forget"), nil
	}
	if err != nil {
		// The store's refusals already carry the current listing (a cap or a
		// match miss is unactionable without it), so this passes the message
		// through rather than summarizing it away.
		return toolErr("memory: " + err.Error()), nil
	}
	return t.activeResult(store)
}

// executeArchive handles the keyed tier. Split out so the active-tier path above
// stays exactly what it was: the two tiers share a tool because they are one
// subsystem, not because their operations resemble each other.
func (t *MemoryTool) executeArchive(action string, a memoryArgs) (core.ToolResult, error) {
	arch, err := t.archiveFor(a.Scope)
	if err != nil {
		return toolErr("memory: " + err.Error()), nil
	}
	switch action {
	case "archive":
		return t.archiveEntry(arch, a)
	case "search":
		q := firstNonBlank(a.Text, a.Match)
		if q == "" {
			return toolErr("memory: search needs `text` — what you are looking for"), nil
		}
		hits := arch.Search(q, searchResultLimit)
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: memory.RenderSearchResults(arch.Label(), q, hits)}},
			Details: map[string]any{"scope": arch.Label(), "query": q, "matches": len(hits)},
		}, nil
	case "recall":
		e, err := arch.Find(a.Match)
		if err != nil {
			return toolErr("memory: " + err.Error()), nil
		}
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: memory.RenderRecalledForTool(e)}},
			Details: map[string]any{"scope": arch.Label(), "entry": e.Ref()},
		}, nil
	case "forget":
		e, err := arch.Remove(a.Match)
		if err != nil {
			return toolErr("memory: " + err.Error()), nil
		}
		return t.archiveResult(arch, fmt.Sprintf("forgot [%s] %s.", e.Ref(), e.Title()))
	case "promote":
		return t.promoteEntry(arch, a)
	}
	return toolErr("memory: unknown archive action " + action), nil
}

// archiveEntry stores a new archived memory, optionally moving an active entry
// into it.
//
// When moving, the archive is written BEFORE the active entry is removed. The
// order is the only interesting part, and it is the rule the swarm archive keeps
// for the same reason: an error must leave the memory exactly where it was,
// because the alternative is deleting a fact on behalf of an operation that
// promised to keep it.
func (t *MemoryTool) archiveEntry(arch *memory.Archive, a memoryArgs) (core.ToolResult, error) {
	text := strings.TrimSpace(a.Text)
	moved := ""
	if text == "" {
		if strings.TrimSpace(a.Match) == "" {
			return toolErr("memory: archive needs `text` (the memory to store) or `match` (an active entry to move)"), nil
		}
		store, err := t.storeFor(a.Scope)
		if err != nil {
			return toolErr("memory: " + err.Error()), nil
		}
		found, err := store.Get(a.Match)
		if err != nil {
			return toolErr("memory: " + err.Error()), nil
		}
		text, moved = found, found
	}

	stored, err := arch.Add(memory.ArchiveEntry{
		Name:          a.Name,
		Keys:          a.Keys,
		SecondaryKeys: a.SecondaryKeys,
		Text:          text,
	})
	if err != nil {
		return toolErr("memory: " + err.Error()), nil
	}

	note := fmt.Sprintf("archived as [%s]. It is out of your context now and comes back when its keys match: %s.",
		stored.Ref(), strings.Join(stored.Keys, ", "))
	if moved != "" {
		if store, err := t.storeFor(a.Scope); err == nil {
			if err := store.Remove(moved); err != nil {
				// The archived copy is already durable, so this is a duplicate,
				// not a loss — say which, because "archived" alongside an
				// unchanged active list otherwise reads as a no-op.
				note += fmt.Sprintf(" NOTE: it is still in active memory too (removing it failed: %v).", err)
			} else {
				note += " Removed from active memory."
			}
		}
	}
	return t.archiveResult(arch, note)
}

// promoteEntry moves an archived entry back into the active tier — the recall
// verb that costs something. It re-enters the cached system prefix, so on a long
// transcript it invalidates the prompt cache at the next refresh; the cheap
// escape hatch is recall, which reads an entry without moving it.
func (t *MemoryTool) promoteEntry(arch *memory.Archive, a memoryArgs) (core.ToolResult, error) {
	store, err := t.storeFor(a.Scope)
	if err != nil {
		return toolErr("memory: " + err.Error()), nil
	}
	e, err := arch.Find(a.Match)
	if err != nil {
		return toolErr("memory: " + err.Error()), nil
	}
	// Add first, remove second — the same rule as archiving. A promotion that
	// fails the active tier's caps must leave the entry archived, not vanished.
	if err := store.Add(e.Text); err != nil {
		return toolErr(fmt.Sprintf("memory: [%s] cannot be promoted: %v\n"+
			"(it is still archived. The active tier is one terse line per entry because it rides every "+
			"request; shorten it and add it directly, or leave it archived and rely on its keys)", e.Ref(), err)), nil
	}
	if _, err := arch.Remove(e.ID); err != nil {
		return toolErr(fmt.Sprintf("memory: promoted [%s] into active memory, but could not delete the "+
			"archived copy: %v — it is now in both tiers", e.Ref(), err)), nil
	}
	return t.activeResult(store)
}

// searchResultLimit caps how many archived entries a search returns. Bodies are
// multi-line here, so an uncapped search over a large archive would answer a
// question with the archive.
const searchResultLimit = 5

// activeResult is the standard reply for an active-tier mutation: the scope's
// full list, so the model sees its own change without the block re-injecting.
func (t *MemoryTool) activeResult(store *memory.Store) (core.ToolResult, error) {
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: memory.RenderForTool(store.Label(), store.List())}},
		Details: map[string]any{"scope": store.Label(), "entries": len(store.List())},
	}, nil
}

// archiveResult is the standard reply for an archive mutation: what happened,
// then the scope's index with each entry's triggers.
//
// The index rides along for the reason the byte-cap refusal carries its listing:
// the archive is invisible by construction, so an answer that does not show it
// leaves the model guessing about the one tier it cannot see.
func (t *MemoryTool) archiveResult(arch *memory.Archive, note string) (core.ToolResult, error) {
	items := arch.List()
	text := note + "\n\n" + memory.RenderArchiveList(arch.Label(), items)
	if probs := arch.Problems(); len(probs) > 0 {
		// An unparseable file is INERT — on disk, never firing, with no other
		// symptom at all. Surfacing it here is the only place anyone finds out.
		text += "\n\nunreadable archive files (these can never fire):\n  " + strings.Join(probs, "\n  ")
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Details: map[string]any{"scope": arch.Label(), "archived": len(items)},
	}, nil
}

// storeFor resolves the scope argument. An unrecognised scope is refused rather
// than defaulting: silently writing a user-scoped fact into project memory (or
// the reverse) puts it where nothing will look for it again.
func (t *MemoryTool) storeFor(scope string) (*memory.Store, error) {
	switch normalizeScope(scope) {
	case memory.ScopeProject:
		if t.Project == nil {
			return nil, fmt.Errorf("no project memory is bound to this session")
		}
		return t.Project, nil
	case memory.ScopeUser:
		if t.User == nil {
			return nil, fmt.Errorf("no user memory is available")
		}
		return t.User, nil
	default:
		return nil, fmt.Errorf("scope must be %q or %q", memory.ScopeProject, memory.ScopeUser)
	}
}

// archiveFor resolves the scope argument to that scope's archive.
func (t *MemoryTool) archiveFor(scope string) (*memory.Archive, error) {
	switch normalizeScope(scope) {
	case memory.ScopeProject:
		if t.ProjectArchive == nil {
			return nil, fmt.Errorf("no project memory archive is bound to this session")
		}
		return t.ProjectArchive, nil
	case memory.ScopeUser:
		if t.UserArchive == nil {
			return nil, fmt.Errorf("no user memory archive is available")
		}
		return t.UserArchive, nil
	default:
		return nil, fmt.Errorf("scope must be %q or %q", memory.ScopeProject, memory.ScopeUser)
	}
}

// normalizeScope maps the wire value to a scope constant, defaulting to project.
// One function so the store and the archive cannot disagree about what an
// omitted scope means — they are resolved by different calls on every archive
// action that touches both tiers.
func normalizeScope(scope string) string {
	switch s := strings.ToLower(strings.TrimSpace(scope)); s {
	case "", memory.ScopeProject:
		return memory.ScopeProject
	default:
		return s
	}
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
