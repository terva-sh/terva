package memory

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// blockMaxBytes bounds the injected block. The per-scope file caps already keep
// the two sections small (project ~6 KiB, user ~1.5 KiB, minus headers), so this
// is a belt-and-suspenders clamp against the combined policy + sections.
const blockMaxBytes = 24000

// Policy is the standing curation heuristic the model reads at the head of the
// injected block. It is model-facing prompt text, so it goes through the prompts
// catalog like every other injection.
//
// It states that the block refreshes at a session boundary and after a
// compaction, because that is true and load-bearing: memory is a FROZEN block in
// the cached system prefix, not an ephemeral per-turn note. A mid-session write
// updates the file immediately but does not re-inject — the model sees its own
// change through the tool's return value instead, and the provider prompt cache
// survives. Saying so stops the model from re-reading memory to check that a
// write landed.
func Policy() string {
	// The curation commands lead and the scope taxonomy follows (the position
	// rule). This wording also names the archive tier, which the old text
	// never did — a policy cannot be followed on a tier it does not mention.
	// Measured against the old text on Haiku (scripts/eval, tier-A prompts
	// run, 2026-08): behaviourally neutral everywhere, including the archive
	// sentence (tier choice 2/20 in both arms — the sentence is carried for
	// completeness, not for a measured effect; wording is not the lever there).
	//
	// One literal, not a concatenation: the catalog extractor needs the English
	// default to be a single literal or it cannot pull the string.
	return i18n.P("memory.policy",
		"You have a durable memory. terva shows it to you here at session start. Save non-obvious, durable facts proactively when you discover something that a future session would have to learn again. Do not save trivial or obvious facts. Keep each entry to one or two lines. Remove stale entries.\n\nA fact too long for two lines belongs in the archive tier. Pass action=archive for it, and retrieve it later by keyword. There are two scopes. Project memory holds facts about this repository: conventions, gotchas, architecture decisions, where things live. User memory holds cross-project facts about the person you work with: their preferences, environment, and how they like to work.\n\nCurate both scopes with the memory tool. Scope defaults to project. Pass scope=user for a fact about the user. The tool returns the updated list, so you see your change immediately. This block refreshes at the next session and after a compaction.")
}

// RenderBlock is the single static block injected into the cached system prompt
// at session start and after a compaction. It leads with the standing policy,
// then a User section and a Project section, each omitted when empty.
//
// The user section comes first: it is short and stable, so if the clamp ever
// fires it trims the longer project tail rather than the user's own facts.
//
// Returns "" when both scopes are empty — a policy telling the model to curate a
// memory it has never written is a live instruction, but paying for it in the
// cached prefix of every request before there is anything to show is not worth
// it. The tool description carries the essentials either way.
func RenderBlock(user, project []string) string {
	if len(user) == 0 && len(project) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(Policy())
	writeSection(&b, i18n.P("memory.section.user", "User memory (cross-project facts about the person you work with):"), user)
	writeSection(&b, i18n.P("memory.section.project", "Project memory (durable facts about this repository):"), project)
	out := b.String()
	if len(out) > blockMaxBytes {
		out = strings.ToValidUTF8(out[:blockMaxBytes], "") + "\n…(truncated)"
	}
	return out
}

func writeSection(b *strings.Builder, header string, entries []string) {
	if len(entries) == 0 {
		return
	}
	b.WriteString("\n\n")
	b.WriteString(header)
	b.WriteString("\n")
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
}

// RenderForTool is what a memory tool call returns inline, so the model sees the
// result of its own write within the session without re-injecting (which would
// bust the prompt cache). label names the mutated scope.
func RenderForTool(label string, entries []string) string {
	if len(entries) == 0 {
		return label + " is empty."
	}
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = fmt.Sprintf("%2d. %s", i+1, e)
	}
	noun := "entries"
	if len(entries) == 1 {
		noun = "entry"
	}
	return fmt.Sprintf("%s (%d %s):\n%s", label, len(entries), noun, strings.Join(lines, "\n"))
}
