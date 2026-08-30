package tools

import (
	"strings"
	"testing"
)

// The recorded churn: the model archived a correction under the name it had
// already used, got a -2 it did not notice, and spent 21 turns on
// archive -> recall -> archive -> forget. The suffix is deliberate. Saying
// nothing about it is what cost the turns.
func TestMemoryArchiveAnnouncesTheSuffix(t *testing.T) {
	mt := memTool()

	if out, isErr := call(t, mt, map[string]any{
		"action": "archive", "name": "build gotchas",
		"keys": []string{"build"}, "text": "the first fact, about how the build resolves its tags",
	}); isErr {
		t.Fatalf("first archive failed: %s", out)
	}

	out, isErr := call(t, mt, map[string]any{
		"action": "archive", "name": "Build Gotchas!",
		"keys": []string{"compile"}, "text": "an entirely separate fact concerning linker flags on windows",
	})
	if isErr {
		t.Fatalf("second archive failed: %s", out)
	}
	// It must name what was taken, what it stored instead, and the way back.
	for _, want := range []string{"build-gotchas", "already taken", "forget"} {
		if !strings.Contains(out, want) {
			t.Errorf("the suffix went unannounced; want %q in:\n%s", want, out)
		}
	}
}

// A free name must not grow a paragraph about a collision that did not happen.
func TestMemoryArchiveSaysNothingWhenTheNameIsFree(t *testing.T) {
	mt := memTool()
	out, isErr := call(t, mt, map[string]any{
		"action": "archive", "name": "build gotchas",
		"keys": []string{"build"}, "text": "a fact about how the build resolves its tags",
	})
	if isErr {
		t.Fatalf("archive failed: %s", out)
	}
	if strings.Contains(out, "already taken") {
		t.Errorf("a free name reported a collision:\n%s", out)
	}
}

// The finding's first half: the cap was real, refusing, and undocumented, so a
// ~750-token composition was discarded whole and the model could not have
// budgeted for it.
func TestMemoryDescriptionStatesTheEntryLimits(t *testing.T) {
	desc := (&MemoryTool{}).Description()
	for _, want := range []string{"1024", "8192", "refuses"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the description does not state the limit (%q missing):\n%s", want, desc)
		}
	}
}
