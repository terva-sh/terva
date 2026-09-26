package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
)

// /context names each segment of the frame and what it weighs: the system
// prompt's sections under sys, the host's tail segments under xt. Each section's
// children sum to it, so the view accounts for every byte it reports. Opening it
// assembles with a peek, so it never runs the tail's per-request side effects.
func TestContextShowsEachSegmentByTagAndSize(t *testing.T) {
	asm := build.NewAssembler([]build.PromptSegment{
		{Source: build.SourceIdentityIntro, Text: "You are terva."},
		{Source: build.SourceFooter, Text: "cwd: /x"},
	})
	requested := 0
	asm.SetTail(func() string { requested++; return "the tail" }, func() string { return "the tail" })
	ag := coretest.NewAgentWithAssembler(nil, "fake", asm, core.Registry{})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag}

	sys, err := s.contextNode("sys", "")
	if err != nil {
		t.Fatalf("sys: %v", err)
	}
	if len(sys.Children) != 2 || sys.Children[0].Label != build.SourceIdentityIntro || sys.Children[1].Label != build.SourceFooter {
		t.Fatalf("sys children = %+v, want one per Stable segment, labeled by tag", sys.Children)
	}
	// The blank line between two sections is the only byte no child owns.
	if got := sys.Children[0].Bytes + sys.Children[1].Bytes + len("\n\n"); got != sys.Bytes {
		t.Errorf("sys children sum to %d, section is %d", got, sys.Bytes)
	}

	xt, err := s.contextNode("xt", "")
	if err != nil {
		t.Fatalf("xt: %v", err)
	}
	if len(xt.Children) != 1 || xt.Children[0].Label != core.TailHost || xt.Children[0].Content != "the tail" {
		t.Fatalf("xt children = %+v, want the host segment", xt.Children)
	}
	if b := s.contextBreakdown(); b.SystemBytes != sys.Bytes || b.ExtBytes != xt.Bytes {
		t.Errorf("breakdown sys=%d ext=%d, nodes sys=%d ext=%d", b.SystemBytes, b.ExtBytes, sys.Bytes, xt.Bytes)
	}
	if requested != 0 {
		t.Errorf("opening /context ran the tail's per-request side effects %d time(s)", requested)
	}
}
