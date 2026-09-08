package testsupport

import (
	"strings"
	"testing"
)

// Reading the justfile, for the gates in this package that assert a recipe
// still runs the command it exists to run.
//
// These two live in their own file because more than one gate needs them and
// the release EXCLUDES list drops some of those gates. A helper declared beside
// an excluded gate takes every surviving gate in the package down with it, at
// compile time. TestSurvivingFilesDoNotUseExcludedSymbols is what catches that,
// and it caught this.

// recipeBlock returns the COMMANDS of one justfile recipe, by name, with every
// comment line dropped.
//
// Dropping the comments is the point. Recipes here carry long prose explaining
// what a gate is for, so a substring search that read those comments would
// report a command present in a recipe that stopped running it.
func recipeBlock(t *testing.T, body, name string) string {
	t.Helper()

	var out []string
	inRecipe := false
	for _, line := range strings.Split(body, "\n") {
		if !inRecipe {
			if recipeHeader(line) == name {
				inRecipe = true
			}
			continue
		}
		// A recipe body is indented, so any non-empty line at column 0 is the
		// next recipe or the comment block introducing it.
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}

	if len(out) == 0 {
		t.Fatalf("no recipe named %q in the justfile, or it has an empty body.\n"+
			"This scan anchors on a line starting %q at column 0. If the recipe was renamed, "+
			"re-anchor this test.", name, name+":")
	}
	return strings.Join(out, "\n")
}

// recipeHeader returns the recipe a justfile line declares, or "".
//
// It accepts every header form the justfile uses: `name:` alone, `name: dep dep`
// for dependencies, and `name ARG='default':` for a parameterised recipe. The
// last one is why this is not a prefix match. `ticket-check LANE='local':` is a
// recipe header, and a scan anchored on `name:` walks straight past it.
func recipeHeader(line string) string {
	if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "" // a body line, not a header
	}
	head, _, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	fields := strings.Fields(head)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
		return "" // a comment that happens to contain a colon
	}
	return fields[0]
}
