package workspace

// The model's World hands (Worlds W4b) — the play director's bounded authority
// over the World's knowledge:
//
//	world_note   — append a NEW lore entry (a fact the scene established, a
//	               character's memory). Append-only: an existing name is an
//	               error, never an overwrite.
//	world_reveal — a character LEARNS an existing targeted entry: they join
//	               its audience and the learned-when ledger records the moment
//	               (L3b — the model-driven learning source).
//
// Play-only by design: chat is pure conversation (no tools from anywhere —
// see MergeExtensionTools), so these register with the director's other tools
// (actor_spawn) via injectExtraTools. Edits and deletion stay user verbs
// (world.lore.put/delete); promotion stays a user action. Both tools reuse
// setWorldLore, so the persisted meta, the live tail record, and every open
// steering drawer stay in step.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// worldNoteTool appends one World lore entry, model-flagged.
type worldNoteTool struct{ s *wsSession }

func (t *worldNoteTool) Name() string { return "world_note" }

func (t *worldNoteTool) Description() string {
	// The world_reveal cross-reference is load-bearing: live play showed the
	// director writing a fresh entry for a fact an existing entry already
	// covered (a secret someone just confessed), because only the exact-name
	// collision path mentioned the other tool. The duplicate splits one fact
	// across two entries with two audiences, and the ledger loses the moment
	// it was learned.
	//
	// The scene-state sentence is the SD4 write path: the proposal's "model-
	// writable through world_note" is exactly this carve-out, and the
	// description is the only place the director learns the pin exists.
	return "Record a NEW piece of world knowledge the scene just established — a fact, an event, something a character now remembers. It becomes a World lore entry injected into future generations when relevant. Append-only: pick a fresh name; you cannot edit or remove existing entries. Scope it with `audience` when only some characters would know it. If an existing entry already covers this fact and the scene just exposed it to someone new, do not re-record it under a fresh name — use world_reveal to add them to that entry's audience instead. One exception to append-only: the entry named \"" + core.SceneStateName + "\" is the pinned scene-state card (in-fiction date and time, location, ledger facts, active routines) — writing that name REPLACES its content, so keep it current and compact whenever the scene's clock, place, or ledger moves."
}

func (t *worldNoteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "A short, fresh title for this entry (e.g. \"The guildmaster's betrayal\"). Must not collide with an existing entry."
    },
    "content": {
      "type": "string",
      "description": "The knowledge itself, one tight paragraph, written as scene truth (not as narration)."
    },
    "keys": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Trigger keywords: the entry injects when one appears in recent messages. Omit for always-on knowledge."
    },
    "audience": {
      "type": "array",
      "items": {"type": "string"},
      "description": "The characters who know this, by name. Omit for world-shared knowledge everyone on stage sees."
    }
  },
  "required": ["name", "content"]
}`)
}

type worldNoteArgs struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Keys     []string `json:"keys,omitempty"`
	Audience []string `json:"audience,omitempty"`
}

func (t *worldNoteTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a worldNoteArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	name := strings.TrimSpace(a.Name)
	content := strings.TrimSpace(a.Content)
	if name == "" || content == "" {
		return worldToolErr("world_note: name and content are required"), nil
	}
	entries := t.s.sess.Meta.WorldLore
	// The pinned scene-state card (SD4) is the one exception to append-only:
	// writing its reserved name replaces the card's content in place. Keys and
	// audience are discarded — the pin is always-on and shared by definition
	// (see worldLoreFromWire, which normalizes user puts the same way).
	if core.IsSceneState(name) {
		entry := core.WorldLoreEntry{Name: core.SceneStateName, Constant: true, Content: content, Model: true}
		next := make([]core.WorldLoreEntry, 0, len(entries)+1)
		updated := false
		for _, e := range entries {
			if core.IsSceneState(e.Name) {
				if !updated {
					next = append(next, entry)
					updated = true
				}
				continue
			}
			next = append(next, e)
		}
		if !updated {
			next = append(next, entry)
		}
		if err := t.s.setWorldLore(next); err != nil {
			return core.ToolResult{}, err
		}
		return worldToolOK("Updated the pinned scene state."), nil
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return worldToolErr(fmt.Sprintf("world_note: an entry named %q already exists — entries are append-only, pick a fresh name (or reveal the existing one with world_reveal)", e.Name)), nil
		}
	}
	entry := core.WorldLoreEntry{
		Name:     name,
		Keys:     trimList(a.Keys),
		Audience: dedupeNames(a.Audience),
		Content:  content,
		Model:    true,
	}
	entry.Constant = len(entry.Keys) == 0
	next := append(append([]core.WorldLoreEntry(nil), entries...), entry)
	if err := t.s.setWorldLore(next); err != nil {
		return core.ToolResult{}, err
	}
	scope := "everyone on stage"
	if len(entry.Audience) > 0 {
		scope = "only " + strings.Join(entry.Audience, ", ")
	}
	return worldToolOK(fmt.Sprintf("Noted %q (known to %s).", name, scope)), nil
}

// worldRevealTool moves a character onto an entry's audience — they learn it.
type worldRevealTool struct{ s *wsSession }

func (t *worldRevealTool) Name() string { return "world_reveal" }

func (t *worldRevealTool) Description() string {
	return "A character LEARNS an existing piece of targeted world knowledge — use it the moment the scene exposes a secret to someone new. They join the entry's audience (their future generations can draw on it) and the moment is recorded. Only targeted entries can be revealed; world-shared knowledge is already known to everyone."
}

func (t *worldRevealTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "entry": {
      "type": "string",
      "description": "The name of the existing World lore entry being learned."
    },
    "character": {
      "type": "string",
      "description": "The character who just learned it, by name."
    }
  },
  "required": ["entry", "character"]
}`)
}

type worldRevealArgs struct {
	Entry     string `json:"entry"`
	Character string `json:"character"`
}

func (t *worldRevealTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a worldRevealArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	entryName := strings.TrimSpace(a.Entry)
	who := strings.TrimSpace(a.Character)
	if entryName == "" || who == "" {
		return worldToolErr("world_reveal: entry and character are required"), nil
	}
	entries := append([]core.WorldLoreEntry(nil), t.s.sess.Meta.WorldLore...)
	idx := -1
	for i, e := range entries {
		if strings.EqualFold(e.Name, entryName) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return worldToolErr(fmt.Sprintf("world_reveal: no entry named %q (existing: %s)", entryName, strings.Join(worldEntryNames(entries), ", "))), nil
	}
	e := entries[idx]
	if len(e.Audience) == 0 {
		return worldToolErr(fmt.Sprintf("world_reveal: %q is world-shared — everyone already knows it", e.Name)), nil
	}
	if audienceHas(e.Audience, who) {
		return worldToolOK(fmt.Sprintf("%s already knows %q.", who, e.Name)), nil
	}
	e.Audience = append(append([]string(nil), e.Audience...), who)
	if e.Learned == nil {
		e.Learned = map[string]string{}
	} else {
		learned := make(map[string]string, len(e.Learned)+1)
		for k, v := range e.Learned {
			learned[k] = v
		}
		e.Learned = learned
	}
	e.Learned[who] = time.Now().UTC().Format(time.RFC3339)
	entries[idx] = e
	if err := t.s.setWorldLore(entries); err != nil {
		return core.ToolResult{}, err
	}
	return worldToolOK(fmt.Sprintf("%s now knows %q.", who, e.Name)), nil
}

func worldToolOK(msg string) core.ToolResult {
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: msg}}}
}

func worldToolErr(msg string) core.ToolResult {
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: msg}}, IsError: true}
}

func worldEntryNames(entries []core.WorldLoreEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func trimList(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// dedupeNames trims, drops empties, and dedupes case-insensitively (the same
// normalization world.lore.put applies to an audience).
func dedupeNames(in []string) []string {
	var out []string
	for _, s := range in {
		t := strings.TrimSpace(s)
		if t == "" || audienceHas(out, t) {
			continue
		}
		out = append(out, t)
	}
	return out
}
