package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Tool implements core.Tool, exposing a `skill` tool the LLM can call
// to load the body of a discovered skill on demand. The system-prompt
// addendum lists the available names; this tool returns the full
// markdown for the requested one.
//
// The list of skills is held behind a mutex so tests / future
// /reload-skills wiring can swap in a fresh set without races.
type Tool struct {
	mu     sync.RWMutex
	skills []*Skill
}

// NewTool returns a skill loader tool seeded with the given skills.
// Pass the slice from Discover().
func NewTool(skills []*Skill) *Tool { return &Tool{skills: skills} }

// SetSkills atomically replaces the underlying skill set. Used when
// the user re-runs discovery (e.g. after editing a SKILL.md).
func (t *Tool) SetSkills(s []*Skill) {
	t.mu.Lock()
	t.skills = s
	t.mu.Unlock()
}

// Skills returns a snapshot for callers that need to render the
// current set (e.g. the /skills picker).
func (t *Tool) Skills() []*Skill {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Skill, len(t.skills))
	copy(out, t.skills)
	return out
}

// ---- core.Tool implementation ----

// Name is the LLM-facing tool name.
func (*Tool) Name() string { return "skill" }

// Description tells the model what this tool does. Kept blunt so the
// model reliably uses it instead of guessing what a "skill" is.
func (*Tool) Description() string {
	return i18n.D("tool.skill.description", "Load the instructions of a skill by name. Use this tool when the request of the user agrees with a skill in the list above.")
}

// Schema is one required string parameter: the skill name.
func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
}

// Execute returns the markdown body of the requested skill.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: "skill: invalid args: " + err.Error()}},
		}, nil
	}
	if in.Name == "" {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: "skill: name is required"}},
		}, nil
	}

	t.mu.RLock()
	s := Resolve(t.skills, in.Name)
	alts := shadowedNames(t.skills, in.Name)
	t.mu.RUnlock()
	if s == nil {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("skill: no skill named %q (run /skills in terva to see what's available)", in.Name)}},
		}, nil
	}
	// Teach the qualified syntax HERE rather than in the system prompt:
	// a shadowed skill is rare enough that standing manifest text would
	// cost tokens every turn to prepare for a turn that mostly never
	// comes. The name the model asked for resolved — this only tells it
	// what else answers to that name, in case it wanted the other one.
	var note string
	if len(alts) > 0 {
		note = fmt.Sprintf("\n\n(Note: %q also names %s. This result is %s; load one of the others by its qualified name if that is what you meant.)",
			in.Name, strings.Join(alts, ", "), s.Qualified())
	}

	header := fmt.Sprintf("# Skill: %s\n\n%s\n\n---\n\n", s.Ref(), s.Description)
	body := s.Body

	// Skill-driven tool activation (retro H2·b step 5): if the skill declares the
	// tools it depends on (allowed-tools frontmatter), surface their capability
	// groups under lazy tool visibility so they're advertised. Loading a skill is
	// itself a tool call, so the immediate post-tool refresh lands them on the
	// next model step (by default). Strictly visibility — the permission/trust
	// gate is untouched, so a hidden tool a skill reveals is still gated when
	// called, and an untrusted workspace (whose extension tools were never loaded)
	// has nothing to reveal. No-op off lazy.
	if len(s.AllowedTools) > 0 {
		if ag := core.AgentFromContext(ctx); ag != nil {
			if activated := ag.ActivateGroupsForTools(s.AllowedTools); len(activated) > 0 {
				body += fmt.Sprintf("\n\n(Activated tool group(s) for this skill: %s — available on your next step; each still requires its normal permission.)", strings.Join(activated, ", "))
			}
		}
	}

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: header + body + note}},
		Details: map[string]any{
			"skill":     s.Name,
			"qualified": s.Qualified(),
			"path":      s.Path,
		},
	}, nil
}

// shadowedNames returns the qualified names that the given reference
// ALSO matches but did not resolve to — the losers of a bare-name
// collision. Empty for the overwhelmingly common uncontested name, and
// always empty when the caller already qualified the reference (it
// asked for a specific tier, so there is nothing to disambiguate).
func shadowedNames(active []*Skill, ref string) []string {
	if _, _, qualified := splitQualified(ref); qualified {
		return nil
	}
	var out []string
	for _, s := range active {
		if s == nil || !strings.EqualFold(s.Name, strings.TrimSpace(ref)) {
			continue
		}
		for _, sh := range s.Shadowed {
			if sh != nil {
				out = append(out, sh.Qualified())
			}
		}
		break
	}
	return out
}
