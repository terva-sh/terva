package core

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func mvMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func mvTexts(ms []provider.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				out[i] = tb.Text
			}
		}
	}
	return out
}

func mvNewSession(t *testing.T, base ...provider.Message) *Session {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range base {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestMessageScopedVariants pins Option C's core: a retained-history replace at an
// older message keeps the original as a swipeable take with the downstream shared,
// stacks across repeated edits, and reconstructs from disk — SessionVariants
// reports the count and SessionMsgVariant hydrates the full take list.
func TestMessageScopedVariants(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a0"),
		mvMsg(provider.RoleUser, "u1"),
		mvMsg(provider.RoleAssistant, "a1"),
	)
	// Edit the OLDER assistant (index 1) twice: a0 → a0' → a0''.
	if err := s.AppendReplaceVariant(1, mvMsg(provider.RoleAssistant, "a0-prime"), "edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendReplaceVariant(1, mvMsg(provider.RoleAssistant, "a0-prime2"), "edit"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	// Effective shows the latest edit; the downstream (u1, a1) is untouched.
	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := mvTexts(msgs); !reflect.DeepEqual(got, []string{"u0", "a0-prime2", "u1", "a1"}) {
		t.Fatalf("effective = %v, want [u0 a0-prime2 u1 a1]", got)
	}
	// One message-scoped position at index 1 with 3 takes, latest active.
	vs, err := SessionVariants(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0] != (VariantPos{Index: 1, Count: 3, Active: 2, Span: false}) {
		t.Fatalf("variants = %+v, want one msg pos {Index:1 Count:3 Active:2}", vs)
	}
	// Lazy hydration returns the full take list in creation order.
	mv, ok, err := SessionMsgVariant(path, 1)
	if err != nil || !ok {
		t.Fatalf("SessionMsgVariant: ok=%v err=%v", ok, err)
	}
	if got := mvTexts(mv.Takes); !reflect.DeepEqual(got, []string{"a0", "a0-prime", "a0-prime2"}) || mv.Active != 2 {
		t.Fatalf("takes = %v active=%d, want [a0 a0-prime a0-prime2] active 2", got, mv.Active)
	}
}

// TestMessageScopedSelectSwipesBack proves mselect switches the active take and
// survives a reload.
func TestMessageScopedSelectSwipesBack(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a0"),
		mvMsg(provider.RoleUser, "u1"),
		mvMsg(provider.RoleAssistant, "a1"),
	)
	if err := s.AppendReplaceVariant(1, mvMsg(provider.RoleAssistant, "a0-edited"), "edit"); err != nil {
		t.Fatal(err)
	}
	// Swipe back to take 0 (the original).
	if err := s.AppendMsgSelect(1, 0, "swipe"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := mvTexts(msgs); !reflect.DeepEqual(got, []string{"u0", "a0", "u1", "a1"}) {
		t.Fatalf("after mselect=0, effective = %v, want [u0 a0 u1 a1]", got)
	}
	vs, _ := SessionVariants(path)
	if len(vs) != 1 || vs[0].Active != 0 {
		t.Fatalf("variants = %+v, want active 0 after swipe-back", vs)
	}
}

// TestMessageVariantShiftsOnDelete proves a delete keeps message-variant indices
// aligned with the shifted transcript.
func TestMessageVariantShiftsOnDelete(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a0"),
		mvMsg(provider.RoleUser, "u1"),
		mvMsg(provider.RoleAssistant, "a1"),
	)
	// Variant the last assistant (index 3), then delete an earlier message (index 0).
	if err := s.AppendReplaceVariant(3, mvMsg(provider.RoleAssistant, "a1-edited"), "edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAmend(AmendDelete, 0, nil, "delete"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	vs, err := SessionVariants(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Index != 2 {
		t.Fatalf("variants = %+v, want the position shifted to index 2", vs)
	}
	mv, ok, _ := SessionMsgVariant(path, 2)
	if !ok || !reflect.DeepEqual(mvTexts(mv.Takes), []string{"a1", "a1-edited"}) {
		t.Fatalf("shifted takes = %v (ok=%v), want [a1 a1-edited]", mvTexts(mv.Takes), ok)
	}
}

// TestDestructiveReplaceStaysCollapsed proves the gate: a plain (non-keep_prior)
// replace overwrites without creating a swipeable variant, so pre-Option-C edits
// stay collapsed.
func TestDestructiveReplaceStaysCollapsed(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a0"),
	)
	m := mvMsg(provider.RoleAssistant, "a0-destructive")
	if err := s.AppendAmend(AmendReplace, 1, &m, "edit"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, msgs, _ := OpenSession(path)
	if got := mvTexts(msgs); !reflect.DeepEqual(got, []string{"u0", "a0-destructive"}) {
		t.Fatalf("effective = %v, want [u0 a0-destructive]", got)
	}
	vs, _ := SessionVariants(path)
	if len(vs) != 0 {
		t.Fatalf("a destructive replace must not create a variant, got %+v", vs)
	}
}

// TestSealPrunesToLatest proves a seal collapses a message-scoped variant to the
// kept take and closes the position (no more swipe marker), surviving a reload.
func TestSealPrunesToLatest(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a0"),
		mvMsg(provider.RoleUser, "u1"),
		mvMsg(provider.RoleAssistant, "a1"),
	)
	if err := s.AppendReplaceVariant(1, mvMsg(provider.RoleAssistant, "a0-p1"), "edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendReplaceVariant(1, mvMsg(provider.RoleAssistant, "a0-p2"), "edit"); err != nil {
		t.Fatal(err)
	}
	// Keep take 1 (a0-p1) and close the position.
	if err := s.AppendSeal(1, 1, "prune"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := mvTexts(msgs); !reflect.DeepEqual(got, []string{"u0", "a0-p1", "u1", "a1"}) {
		t.Fatalf("after seal, effective = %v, want [u0 a0-p1 u1 a1]", got)
	}
	if vs, _ := SessionVariants(path); len(vs) != 0 {
		t.Fatalf("seal must close the position, got %+v", vs)
	}
}

// TestDropTakeRemovesOne proves a drop removes one take (adjusting active) and
// closes the position once a single take remains.
func TestDropTakeRemovesOne(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a0"),
		mvMsg(provider.RoleUser, "u1"),
		mvMsg(provider.RoleAssistant, "a1"),
	)
	for _, txt := range []string{"a0-p1", "a0-p2"} { // takes: [a0, a0-p1, a0-p2], active 2
		if err := s.AppendReplaceVariant(1, mvMsg(provider.RoleAssistant, txt), "edit"); err != nil {
			t.Fatal(err)
		}
	}
	// Drop take 0 (the original): active (2) shifts down to 1; a0-p2 stays live.
	if err := s.AppendDropTake(1, 0, "drop"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := mvTexts(msgs)[1]; got != "a0-p2" {
		t.Fatalf("after drop, message 1 = %q, want a0-p2 (active preserved)", got)
	}
	mv, ok, _ := SessionMsgVariant(path, 1)
	if !ok || !reflect.DeepEqual(mvTexts(mv.Takes), []string{"a0-p1", "a0-p2"}) || mv.Active != 1 {
		t.Fatalf("takes = %v active=%d, want [a0-p1 a0-p2] active 1", mvTexts(mv.Takes), mv.Active)
	}

	// Drop the now-active take (index 1, a0-p2) → one take left → position closes.
	s2, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.AppendDropTake(1, 1, "drop"); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
	_, msgs2, _ := OpenSession(path)
	if got := mvTexts(msgs2)[1]; got != "a0-p1" {
		t.Fatalf("after second drop, message 1 = %q, want a0-p1", got)
	}
	if vs, _ := SessionVariants(path); len(vs) != 0 {
		t.Fatalf("dropping to one take must close the position, got %+v", vs)
	}
}
