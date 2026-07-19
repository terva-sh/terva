package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestOpenSessionLoadStats pins the §9 load telemetry: reconstructing a session
// that carries a tail variant reports the transcript size, the amend count (the
// revision-accumulation proxy), and the tail's take count. A plain session reports
// zero amends.
func TestOpenSessionLoadStats(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	// A completed exchange, then a tail variant: retract the response and append an
	// alternative — the shape an edit-as-variant (or a retry) leaves on disk.
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0"}}},
	} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendAmend(AmendRetract, 1, nil, "edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0-edited"}}}); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	st := reopened.LoadStats
	if st.Messages != len(msgs) || st.Messages != 2 {
		t.Errorf("Messages = %d (transcript %d), want 2", st.Messages, len(msgs))
	}
	if st.Amends < 1 {
		t.Errorf("Amends = %d, want >= 1 (the retract)", st.Amends)
	}
	if st.TailTakes != 2 {
		t.Errorf("TailTakes = %d, want 2 (original + edited)", st.TailTakes)
	}

	// A plain session (no revision) reports no amends and no switchable tail.
	p, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	plainPath := p.Path
	_ = p.Close()
	plain, _, err := OpenSession(plainPath)
	if err != nil {
		t.Fatalf("reopen plain: %v", err)
	}
	defer plain.Close()
	if plain.LoadStats.Amends != 0 || plain.LoadStats.TailTakes != 0 {
		t.Errorf("plain LoadStats = %+v, want 0 amends / 0 tail takes", plain.LoadStats)
	}
}
