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
	// One literal, not a concatenation: the catalog extractor needs the English
	// default to be a single literal or it cannot pull the string.
	return i18n.P("memory.policy",
		"You have a durable memory shown to you here at session start, in two scopes. PROJECT memory: facts about this repo — conventions, gotchas, architecture decisions, where things live. USER memory: cross-project facts about the person you work with — their preferences, environment, and how they like to work. Curate both with the memory tool (scope defaults to project; pass scope=user for facts about the user). Save non-obvious, durable facts proactively after discovering something a future session would otherwise re-learn; skip the trivial or obvious; keep entries to one or two lines; remove stale ones. The tool returns the updated list so you see your change immediately; this block refreshes at the next session and after a compaction.")
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
	writeSection(&b, i18n.P("memory.section.project", "Project memory (durable facts about this repo):"), project)
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
