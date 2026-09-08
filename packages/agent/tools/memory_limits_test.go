package tools

import (
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/memory"
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
//
// The numbers come from the constants, not from literals. Written as literals
// this test asserted 1024 and 8192 and passed for as long as the description
// said so, which is exactly as long as the description was right. The first cap
// raise then failed it, and the failure named the test rather than the drift it
// exists to catch. Sourced from memory, a description that falls behind the cap
// it describes is the only way this can fail.
func TestMemoryDescriptionStatesTheEntryLimits(t *testing.T) {
	desc := (&MemoryTool{}).Description()
	want := []string{
		strconv.Itoa(memory.MaxEntryLen),
		strconv.Itoa(memory.MaxArchiveEntryBytes),
		"refuses",
	}
	for _, w := range want {
		if !strings.Contains(desc, w) {
			t.Errorf("the description does not state the limit (%q missing):\n%s", w, desc)
		}
	}
}
