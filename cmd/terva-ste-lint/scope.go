package main

import "strings"

// enrolledDirs are the directories whose model-visible tool text must be
// written in Simplified Technical English. Enrolment is per DIRECTORY, not per
// tool, and that is the whole point: a tool added to an enrolled package
// tomorrow is covered without anyone remembering to add it here.
//
// Why not enrol the whole repo and let the first run be the audit, which is how
// we normally write a guard? Because that pattern inverts at this scale. An
// empty self-enrolling guard over 270 markdown files and 73,000 comment lines
// does not produce an audit, it produces 8,000 findings and gets switched off.
// The enrolment list grows a directory at a time, deliberately, and each
// addition is a decision someone made.
var enrolledDirs = []string{
	"packages/agent/tools",
	"packages/agent/tools/tasks/tasktool",
	"packages/agent/skills",
	"packages/agent/workspace",
	// The prompt-bearing packages: every i18n.P English default in these dirs
	// is model-visible prompt text and is extracted like a description —
	// except the keys promptKeyExemptPrefixes carves out, which are a tier
	// decision, not an oversight.
	"packages/agent",
	"packages/agent/attach",
	"packages/agent/build",
	"packages/agent/chat",
	"packages/agent/modes",
	"packages/agent/raati",
	"packages/agent/tools/memory",
	"packages/agent/worker",
	"packages/core",
}

// exemptFiles are files inside an enrolled directory whose tool text is not
// ours to rewrite. MCP and extension tools relay a third party's description
// verbatim; holding that text to our house style would mean rewriting other
// people's words on their behalf.
var exemptFiles = map[string]bool{
	"packages/agent/mcp/manager.go":  true,
	"packages/agent/exttool/tool.go": true,
}

// promptKeyExemptPrefixes lists the i18n.P keys whose English defaults are
// deliberately NOT held to Simplified Technical English: the creative
// register, decided in the 2026-08 prompts pass (scripts/eval/README.md).
// For stage/play/lore/persona and the immersive framings, the approved-word
// list would flatten the voice this text exists to produce. Limited
// compliance only — structure, never vocabulary.
//
// The off-surface decision tier (stall/compact/swarm-child/raati rounds)
// graduated in that same pass and is no longer exempt.
//
// Removing a prefix here is how a tier graduates: delete the line, run the
// lint, convert what it flags.
var promptKeyExemptPrefixes = []string{
	"stage.", "play.", "lore.", "persona.",
	"system.conventions.craft", "system.conventions.experience",
	"system.immersive.",
	// The identity and vessel blocks are the persona's brand voice — the
	// pine-tar image is deliberate register, and the pronunciation guides
	// (MYEH-lee, TEHR-vah) read to the caps rule as shouted emphasis.
	"system.identity.", "system.vessel.",
	"chat.intro.",
}

// promptKeyExempt reports whether an i18n.P key sits in an exempted tier.
func promptKeyExempt(key string) bool {
	for _, p := range promptKeyExemptPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// enrolled reports whether a repo-relative path is in scope. Test files are
// out: their tool text is fixtures, and a fixture that reads badly is often
// reading badly on purpose.
func enrolled(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") || exemptFiles[rel] {
		return false
	}
	dir := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dir = rel[:i]
	}
	for _, d := range enrolledDirs {
		if dir == d {
			return true
		}
	}
	return false
}
