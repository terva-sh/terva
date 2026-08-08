package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reply this file exists because of: a list verb answering with private
// prose when the question was about structure.
const shapeFixture = `{
  "worlds": [
    {
      "id": "emma-825e53a0",
      "name": "Emma",
      "sessions": 2,
      "lore": [
        {"name": "Secret History", "content": "SEVENTEEN-SYLLABLE-SECRET", "keys": ["secret", "family"]},
        {"name": "Scene state", "content": "ANOTHER-PRIVATE-THING", "constant": true}
      ]
    },
    {
      "id": "kobeni-3f8a02e6",
      "name": "Kobeni",
      "sessions": 2,
      "cover_url": "/media/worlds/kobeni",
      "lore": []
    }
  ]
}`

func decodeFixture(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// THE property, stated as an absence. A redactor has to guess which fields are
// sensitive and fails open on the one it did not think of; this never holds a
// value, so the assertion is that no input string survives to the output at all.
//
// Asserted over every string in the fixture rather than a chosen one, so a
// future field cannot be added to the fixture and quietly skipped here.
func TestShapeLinesNeverPrintsAValue(t *testing.T) {
	out := strings.Join(shapeLines(decodeFixture(t, shapeFixture)), "\n")
	for _, secret := range []string{
		"SEVENTEEN-SYLLABLE-SECRET", "ANOTHER-PRIVATE-THING",
		"Emma", "Kobeni", "emma-825e53a0", "kobeni-3f8a02e6",
		"secret", "family", "/media/worlds/kobeni", "Secret History", "Scene state",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("--shape printed the value %q:\n%s", secret, out)
		}
	}
	// ...while still answering the question it was asked.
	for _, want := range []string{"worlds", "array(2)", "worlds[].lore[].content", "string("} {
		if !strings.Contains(out, want) {
			t.Errorf("--shape did not report %q:\n%s", want, out)
		}
	}
}

// Keys are UNIONED across array elements, not sampled from the first. Sampling
// would hide every field element zero happened to omit — for a World list that
// means a cover, a model, or a lorebook simply missing from the shape.
func TestShapeLinesUnionsAcrossArrayElements(t *testing.T) {
	out := strings.Join(shapeLines(decodeFixture(t, shapeFixture)), "\n")
	// cover_url exists only on the SECOND World.
	if !strings.Contains(out, "worlds[].cover_url") {
		t.Errorf("a field present only on a later element vanished from the shape:\n%s", out)
	}
	// constant exists only on the second lore entry of the first World.
	if !strings.Contains(out, "worlds[].lore[].constant") {
		t.Errorf("a field present only on a later nested element vanished:\n%s", out)
	}
}

// Sizes are the reason to look at a shape at all: "is the lorebook 4KB or 400KB"
// is the whole budget question, and a range plus an average answers it where a
// single number does not.
func TestShapeLinesReportsSizeSpread(t *testing.T) {
	lines := shapeLines(decodeFixture(t, shapeFixture))
	var content string
	for _, l := range lines {
		if strings.HasPrefix(l, "worlds[].lore[].content") {
			content = l
		}
	}
	if content == "" {
		t.Fatalf("no content line in:\n%s", strings.Join(lines, "\n"))
	}
	// Two entries of different lengths: a range and an average, not one number.
	if !strings.Contains(content, "..") || !strings.Contains(content, "avg") {
		t.Errorf("differing sizes should report a spread, got %q", content)
	}
	if !strings.Contains(content, "×2") {
		t.Errorf("the observation count says how many rows the spread is over, got %q", content)
	}
}

// An empty array is a different fact from an absent one, and the shape has to
// keep them apart or "lore: array(0)" reads as "the shape below is unknown".
func TestShapeLinesKeepsEmptyArraysDistinct(t *testing.T) {
	out := strings.Join(shapeLines(decodeFixture(t, `{"a": [], "b": [1]}`)), "\n")
	if !strings.Contains(out, "a") || !strings.Contains(out, "array(0)") {
		t.Errorf("an empty array must still be reported:\n%s", out)
	}
	if strings.Contains(out, "a[]") {
		t.Errorf("an empty array must not claim a shape below it:\n%s", out)
	}
}

// Paths that cross the same array come back as ROWS, keyed by the array they
// came from — one object per element carrying every selected field of it.
func TestSelectPathsReturnsRows(t *testing.T) {
	got, err := selectPaths(decodeFixture(t, shapeFixture), []string{"worlds.id", "worlds.name"})
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got["worlds"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("expected one row per element, got %#v", got)
	}
	first, _ := rows[0].(map[string]any)
	if first["name"] != "Emma" || first["id"] != "emma-825e53a0" {
		t.Errorf("a row must carry its own fields together, got %#v", first)
	}
	// The keys are the path BELOW the array, not the whole dotted path — inside
	// a row "worlds.name" would repeat what the row set already says.
	if _, stutter := first["worlds.name"]; stutter {
		t.Errorf("row keys should be the leaf path, got %#v", first)
	}
}

// THE bug rows exist for. Parallel arrays shortened silently when a field was
// missing from some elements: `sessions.world` answered with 2 entries against
// 14 sessions and read as "the first two sessions", with nothing saying
// otherwise. A row carries its own association, so a missing field is a key the
// row does not have and no alignment can be misread.
func TestSelectPathsKeepsRowsAlignedWhenAFieldIsMissing(t *testing.T) {
	got, err := selectPaths(decodeFixture(t, shapeFixture), []string{"worlds.id", "worlds.cover_url"})
	if err != nil {
		t.Fatal(err)
	}
	rows := got["worlds"].([]any)
	if len(rows) != 2 {
		t.Fatalf("every element gets a row even when it lacks a selected field, got %d", len(rows))
	}
	a, b := rows[0].(map[string]any), rows[1].(map[string]any)
	if _, has := a["cover_url"]; has {
		t.Error("the element without a cover must not have the key")
	}
	if b["cover_url"] != "/media/worlds/kobeni" {
		t.Errorf("the element WITH a cover must carry it, got %#v", b)
	}
	// ...and each row still names which element it is, which the short parallel
	// array could not do.
	if a["id"] != "emma-825e53a0" || b["id"] != "kobeni-3f8a02e6" {
		t.Errorf("rows lost their identity: %#v / %#v", a, b)
	}
}

// A path below the row array still maps — a World's lorebook is an array inside
// the row, and asking for its names answers with all of them.
func TestSelectPathsMapsBelowTheRowArray(t *testing.T) {
	got, err := selectPaths(decodeFixture(t, shapeFixture), []string{"worlds.name", "worlds.lore.content"})
	if err != nil {
		t.Fatal(err)
	}
	rows := got["worlds"].([]any)
	first := rows[0].(map[string]any)
	contents, ok := first["lore.content"].([]any)
	if !ok || len(contents) != 2 {
		t.Fatalf("a nested array should answer with every entry, got %#v", first["lore.content"])
	}
}

// A typo returning {} looks exactly like a daemon that answered with nothing,
// and sends the caller to the wrong conclusion — about their data rather than
// their spelling.
func TestSelectPathsRefusesAPathThatMatchesNothing(t *testing.T) {
	if _, err := selectPaths(decodeFixture(t, shapeFixture), []string{"worlds.nmae"}); err == nil {
		t.Error("a path matching nothing must be an error, not an empty result")
	}
	if _, err := selectPaths(decodeFixture(t, shapeFixture), []string{}); err == nil {
		t.Error("selecting nothing must be refused")
	}
	// The case a bad-path-alone test does NOT cover, and the one that actually
	// bites: mixed with a path that DOES match, a silently-dropped typo leaves a
	// plausible-looking answer that is quietly missing a column. Caught by
	// mutation — dropping the error left the alone-case passing via the
	// "selected nothing" guard.
	if _, err := selectPaths(decodeFixture(t, shapeFixture), []string{"worlds.name", "worlds.nmae"}); err == nil {
		t.Error("one bad path among good ones must fail the whole selection")
	}
}

// A field missing from SOME elements is not a bad path — a roster where one card
// has no avatar still answers for the ones that do.
func TestSelectPathsSkipsElementsWithoutTheField(t *testing.T) {
	got, err := selectPaths(decodeFixture(t, shapeFixture), []string{"worlds.cover_url"})
	if err != nil {
		t.Fatalf("a field present on only some elements must still select: %v", err)
	}
	rows := got["worlds"].([]any)
	if len(rows) != 2 {
		t.Fatalf("every element still gets a row, got %d", len(rows))
	}
	if _, has := rows[1].(map[string]any)["cover_url"]; !has {
		t.Error("the element that HAS the field must carry it")
	}
}
