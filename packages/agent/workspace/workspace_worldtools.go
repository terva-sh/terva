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
// setWorldLore, so the persisted lorebook, the live tail record, and every open
// steering drawer stay in step.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// worldNoteDesc is the English default for tool.world_note.description. The
// single %s is the reserved scene-state entry name, filled at call time. A
// const because the extractor cannot resolve an inline concatenation, and a
// default it cannot resolve is one nobody can override.
const worldNoteDesc = "Record a new piece of world knowledge that the scene establishes. This can be a fact, an event, or something that a character now remembers. The knowledge becomes a World lore entry, and the tool puts it in a later generation when it is relevant.\n\n" +
	"You can only append. Select a new name, because you cannot change or remove an entry that exists. Give `audience` when only some characters know the fact.\n\n" +
	"If an entry already holds this fact, and the scene exposes the fact to a new person, do not record the fact again under a new name. Use world_reveal to add that person to the audience of the entry.\n\n" +
	"There is one exception to the append-only rule. The entry with the name \"%s\" is the pinned scene-state card. It holds the date and the time in the fiction, the location, the facts in the ledger, and the active routines. When you write to that name, the tool replaces the content of the card. Therefore keep the card correct and short each time that the clock, the place, or the ledger of the scene changes."

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
	return i18n.D("tool.world_note.description", worldNoteDesc, core.SceneStateName)
}

func (t *worldNoteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "A short, new title for this entry, for example \"The guildmaster's betrayal\". The title must be different from the title of each entry that exists."
    },
    "content": {
      "type": "string",
      "description": "The knowledge, in one short paragraph. Write it as truth in the scene, and not as narration."
    },
    "keys": {
      "type": "array",
      "items": {"type": "string"},
      "description": "The keywords that make the entry appear. The tool puts the entry in the context when one keyword occurs in a recent message. Omit this field for knowledge that is always in the context."
    },
    "audience": {
      "type": "array",
      "items": {"type": "string"},
      "description": "The names of the characters who know this. Omit this field for knowledge that all the characters in the scene know."
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
	return i18n.D("tool.world_reveal.description", "A character learns a piece of world knowledge that has a target audience. Use this tool immediately when the scene shows a secret to a new person. The character joins the audience of the entry, and a later generation for that character can then use the knowledge. The tool records the moment. You can reveal an entry with a target audience only, because all the characters already know the knowledge that the world shares.")
}

func (t *worldRevealTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "entry": {
      "type": "string",
      "description": "The name of the World lore entry that the character learns."
    },
    "character": {
      "type": "string",
      "description": "The name of the character who learns the knowledge."
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
