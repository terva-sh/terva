package skills

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

// builtinFS holds the SKILL.md files terva ships with the binary.
// They appear in the catalogue as ordinary skills — same on-demand
// load via the `skill` tool — but never need to be installed by the
// user, and they stay out of the /skills picker.
//
// A NATIVELY-installed skill of the same name (./.terva/skills or
// $TERVA_HOME/skills) shadows the built-in; a .claude/.agents compat
// skill or an extension bundle does not, since those rank below the
// built-in rung. See discoveryTiers.
//
//go:embed all:builtin
var builtinFS embed.FS

// loadBuiltins returns every SKILL.md compiled into the binary.
// Errors per file are silently dropped: built-ins are part of the
// release; if one is malformed it's a release bug we want to surface
// in tests, not panic in front of the user.
func loadBuiltins() []*Skill {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	var out []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(builtinFS, path.Join("builtin", e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		front, body := splitFrontmatter(string(raw))
		s := &Skill{
			Path:      "builtin:" + e.Name(),
			Source:    "built-in",
			Namespace: NamespaceBuiltin,
			Body:      strings.TrimSpace(body),
			Builtin:   true,
		}
		parseFrontmatter(front, s)
		s.Name = sanitizeName(s.Name)
		if s.Name == "" {
			s.Name = sanitizeName(e.Name())
		}
		out = append(out, s)
	}
	return out
}
