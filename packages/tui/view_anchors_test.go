package tui

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// BuildWithAnchors must stay internally consistent: it sizes an internal
// per-message slice from len(v.Messages) and later re-ranges the transcript
// for the anchor pass. It now snapshots v.Messages once so those reads can't
// disagree — a regression where the transcript was read at two different
// lengths panicked with "index out of range" when it grew between the reads.
// This exercises a range of lengths and asserts the anchor set matches.
func TestBuildWithAnchorsConsistent(t *testing.T) {
	v := &View{Theme: Dark}
	for n := 0; n <= 40; n++ {
		msgs := make([]provider.Message, n)
		for i := range msgs {
			role := provider.RoleUser
			if i%2 == 1 {
				role = provider.RoleAssistant
			}
			msgs[i] = provider.Message{
				Role:    role,
				Content: []provider.Content{provider.TextBlock{Text: "line one\nline two"}},
			}
		}
		v.Messages = msgs
		v.InvalidateRenderCache()

		out, anchors := v.BuildWithAnchors(80)
		if len(anchors) != n {
			t.Fatalf("n=%d: got %d anchors, want %d", n, len(anchors), n)
		}
		for _, a := range anchors {
			if a.MessageIdx < 0 || a.MessageIdx >= n {
				t.Fatalf("n=%d: anchor MessageIdx %d out of [0,%d)", n, a.MessageIdx, n)
			}
			if a.Row < 0 || a.Row > len(out) {
				t.Fatalf("n=%d: anchor Row %d out of [0,%d]", n, a.Row, len(out))
			}
		}
	}
}
