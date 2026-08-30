package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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

// memoryDesc is the English default for tool.memory.description. A const so
// the i18n extractor can resolve it: a concatenation passed inline as the
// argument does not resolve, and an unresolvable default cannot be
// overridden or translated.
const memoryDesc = "Keep a memory that stays after this session. The memory has two scopes and two tiers.\n\n" +
	"The scope is project or user. The default is project, which holds facts about this repository: its conventions, its dangers, and the location of each part. The user scope holds facts about the person that you work with, in all projects: their preferences, their environment, and their method of work.\n\n" +
	"The active tier is always in your context. Therefore keep it small and short. Use add to append `text`. Use replace to put `text` in the position of the entry that contains `match`. Use remove to delete the entry that contains `match`.\n\n" +
	"The archived tier is not in your context until its keys agree with the conversation. Therefore an archived entry can be long and can hold much detail. Use archive to store `text` with `keys`, or to move the active entry that agrees with `match`. Use search to find the archived entries that agree with `text`. Use recall to read the archived entry with the name in `match`. Use promote to move an archived entry into the active tier. Use forget to delete an archived entry.\n\n" +
	"One active entry holds at most 1024 characters. One archived entry holds at most 8192 bytes. The tool refuses a longer entry, and it saves nothing. Compose within the limit, or archive a pointer to a file.\n\n" +
	"The choice of keys is difficult, and it decides if you find the memory again. Use the words that a person types when they need the fact. Do not use the identifiers in the fact itself. For example, put a note about the internal parts of the model catalog on the keys \"model\", \"catalog\", and \"add a model\". The person who asks does not know the name of the function yet, and this is the reason for the question. Exact error text and file paths are good keys, because a person copies them into a question.\n\n" +
	"Use `secondary_keys` to make a wide primary key more narrow. An entry fires when a primary key agrees and at least one secondary key also agrees.\n\n" +
	"Archive the facts that are worth storage but not worth space in each turn: procedures, investigations, and any text longer than two lines. Keep in the active tier the facts that apply to each conversation. The tool returns the new memory for the scope that you changed, so that you see your change immediately. The memory block at the start of a session does not change until the next session or a compaction."

func (t *MemoryTool) Description() string {
	return i18n.D("tool.memory.description", memoryDesc)
}

func (t *MemoryTool) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "replace", "remove", "archive", "search", "recall", "promote", "forget"},
				"description": "The operation to do. For the active tier, use add, replace, or remove. For the archived tier, use archive, search, recall, promote, or forget.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{memory.ScopeProject, memory.ScopeUser},
				"description": "The memory to change. Use project for this repository, or user for the person. The default is project.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "For add and archive, the entry. For replace, the new text. For search, the query.",
			},
			"match": map[string]any{
				"type":        "string",
				"description": "The entry to operate on. For the active tier, give a unique fragment of its text. For the archived tier, give the id of the entry.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "For archive only. A short title for the entry. The title becomes the id of the entry.",
			},
			"keys": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "For archive only. This field is necessary. Give the words that a person types when they need this fact.",
			},
			"secondary_keys": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "For archive only. These keys make a wide primary key more narrow. The entry fires when a primary key agrees and at least one of these keys also agrees.",
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
	if taken := stored.CollidedWith(); taken != "" {
		// The suffix is deliberate — two different facts that slugify alike must
		// both stay addressable — but staying quiet about it is what cost the
		// recorded session 21 turns. The tool cannot tell a correction from a
		// second fact, so it states what it did and names the verb that undoes
		// it, rather than guessing at intent.
		note += fmt.Sprintf(" NOTE: the name %q was already taken, so this was stored as %q and both are kept."+
			" If you meant to replace the older one, forget %q.", taken, stored.ID, taken)
	}
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
