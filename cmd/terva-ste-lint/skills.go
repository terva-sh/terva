package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/skills"
)

// builtinSkillsDir holds the skills that ship in the binary. A skill's
// frontmatter description has unusual leverage for its size: it is the ENTIRE
// trigger for whether the skill loads — the model sees one line per skill in
// the system prompt and decides from that line alone — so it is model-facing
// decision text of exactly the kind the rest of this lint governs.
//
// A SKILL.md body is normally out of scope, because it loads only after the
// decision is made, and it is instructional prose whose register the author
// owns. A PINNED body breaks that premise: it sits in the frozen prefix of
// every request, with no decision in front of it. So the bodies in
// skills.DefaultAlwaysOn are enrolled, and only those.
//
// They are enrolled under rulesStructure rather than the full set. The register
// rules misfire badly on this prose (a possessive read as a contraction, a
// banned word flagged inside the list that bans it) and would demand a baseline
// of false positives. The structural rules are the ones that keep an
// instruction unambiguous, and they land clean. The reasoning and the
// measurements are in docs/proposals/always-on-skills.md.
const builtinSkillsDir = "packages/agent/skills/builtin"

// collectSkillDescriptions extracts each builtin SKILL.md's frontmatter
// description. A missing skills directory is an error, not a skip: silently
// covering zero skills would read as "all skills clean".
func collectSkillDescriptions(root string) ([]Text, error) {
	base := filepath.Join(root, filepath.FromSlash(builtinSkillsDir))
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("builtin skills: %w", err)
	}
	var out []Text
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src, err := os.ReadFile(filepath.Join(base, e.Name(), "SKILL.md"))
		if err != nil {
			continue // a skill dir without a SKILL.md ships nothing
		}
		if desc, line := frontmatterDescription(string(src)); desc != "" {
			out = append(out, Text{
				File: builtinSkillsDir + "/" + e.Name() + "/SKILL.md",
				Line: line,
				What: "skill description (" + e.Name() + ")",
				Body: desc,
			})
		}
	}
	return out, nil
}

// collectPinnedSkillBodies extracts the prose of every body terva pins by
// default. A named skill with no SKILL.md is an error rather than a skip: the
// list names what the binary ships, so a miss means the two have drifted and
// the lint would silently cover nothing.
func collectPinnedSkillBodies(root string) ([]Text, error) {
	var out []Text
	for _, name := range skills.DefaultAlwaysOn {
		rel := builtinSkillsDir + "/" + name + "/SKILL.md"
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("pinned skill body %s: %w", name, err)
		}
		// Drop the frontmatter first. Left in, its `---` fences and `name:`
		// line read as one prose paragraph, and the description would be
		// linted twice under two different rule sets.
		body, offset := bodyAfterFrontmatter(string(src))
		for _, t := range markdownProse(rel, body) {
			t.What = "pinned skill body (" + name + ")"
			t.Rules = rulesStructure
			t.Line += offset
			out = append(out, t)
		}
	}
	return out, nil
}

// bodyAfterFrontmatter splits a SKILL.md into the prose below its frontmatter
// block, and the number of lines it dropped. Callers add that offset back so a
// finding points at the real line. A file with no frontmatter comes back whole.
func bodyAfterFrontmatter(src string) (string, int) {
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return src, 0
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n"), i + 1
		}
	}
	return src, 0
}

// frontmatterDescription pulls the `description:` value out of a SKILL.md
// frontmatter block, with the 1-based line it sits on.
func frontmatterDescription(src string) (string, int) {
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", 0
	}
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" {
			return "", 0
		}
		if rest, ok := strings.CutPrefix(t, "description:"); ok {
			return strings.TrimSpace(rest), i + 1
		}
	}
	return "", 0
}
