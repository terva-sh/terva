package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/skills"
)

// A pinned skill is absent from the model's manifest on purpose, because its
// whole body already sits in the system prompt. Rendered like any other row,
// that reads as a skill the model keeps ignoring, which is the opposite of what
// is happening.
func TestPinnedSkillRowSaysSo(t *testing.T) {
	pinned := &skills.Skill{
		Name:        "house-style",
		Namespace:   skills.NamespaceBuiltin,
		Description: "Apply before you write or edit prose.",
		Source:      "built-in",
		Builtin:     true,
		Pinned:      true,
	}
	row := formatSkillRow(pinned, 120)
	if !strings.Contains(row, "pinned") {
		t.Errorf("a pinned skill renders no marker:\n%s", row)
	}

	ordinary := *pinned
	ordinary.Pinned = false
	if plain := formatSkillRow(&ordinary, 120); strings.Contains(plain, "pinned") {
		t.Errorf("an unpinned skill claims to be pinned:\n%s", plain)
	}
}
