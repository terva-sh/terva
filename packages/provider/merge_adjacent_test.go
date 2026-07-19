package provider

import (
	"reflect"
	"testing"
)

func mrole(r Role, texts ...string) Message {
	c := make([]Content, len(texts))
	for i, t := range texts {
		c[i] = TextBlock{Text: t}
	}
	return Message{Role: r, Content: c}
}

func roleSeq(msgs []Message) []Role {
	out := make([]Role, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

// TestMergeAdjacentSameRole covers the strict-alternation repair: same-role runs
// (from an edit/delete or a compaction summary) coalesce into one turn keeping
// all content, a run of three collapses to one, and an already-alternating
// transcript is returned untouched.
func TestMergeAdjacentSameRole(t *testing.T) {
	// Deleting the assistant reply between two user turns leaves user-user.
	in := []Message{mrole(RoleUser, "u0"), mrole(RoleUser, "u1"), mrole(RoleAssistant, "a0")}
	got := MergeAdjacentSameRole(in)
	if want := []Role{RoleUser, RoleAssistant}; !reflect.DeepEqual(roleSeq(got), want) {
		t.Fatalf("roles = %v, want %v", roleSeq(got), want)
	}
	if n := len(got[0].Content); n != 2 {
		t.Errorf("merged user turn has %d content blocks, want 2 (both preserved)", n)
	}

	// A run of three same-role turns collapses to one.
	run := MergeAdjacentSameRole([]Message{mrole(RoleAssistant, "a0"), mrole(RoleAssistant, "a1"), mrole(RoleAssistant, "a2")})
	if len(run) != 1 || len(run[0].Content) != 3 {
		t.Errorf("run-of-three = %d turns / %d blocks, want 1/3", len(run), len(run[0].Content))
	}

	// An alternating transcript is unchanged.
	alt := []Message{mrole(RoleUser, "u"), mrole(RoleAssistant, "a"), mrole(RoleUser, "u2")}
	if got := MergeAdjacentSameRole(alt); !reflect.DeepEqual(roleSeq(got), roleSeq(alt)) {
		t.Errorf("alternating transcript changed: %v", roleSeq(got))
	}
}

// TestMergeAdjacentSameRoleDoesNotMutateInput proves the repair is request-scoped:
// neither the input slice nor a merged message's content slice is touched, so the
// stored transcript and its cache prefix are never disturbed.
func TestMergeAdjacentSameRoleDoesNotMutateInput(t *testing.T) {
	in := []Message{mrole(RoleUser, "u0"), mrole(RoleUser, "u1")}
	_ = MergeAdjacentSameRole(in)
	if len(in) != 2 {
		t.Errorf("input slice length changed to %d", len(in))
	}
	if len(in[0].Content) != 1 {
		t.Errorf("input message content mutated: %d blocks, want 1", len(in[0].Content))
	}
}
