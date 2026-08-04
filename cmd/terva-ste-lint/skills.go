package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// builtinSkillsDir holds the skills that ship in the binary. A skill's
// frontmatter description has unusual leverage for its size: it is the ENTIRE
// trigger for whether the skill loads — the model sees one line per skill in
// the system prompt and decides from that line alone — so it is model-facing
// decision text of exactly the kind the rest of this lint governs. The SKILL.md
// body is deliberately out of scope: it loads only after the decision is made,
// and it is instructional prose whose register the author owns.
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
