package core

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// sessionWithPersona creates a session under root for cwd, created with the
// named persona, holding one message so it survives the empty-session prune.
func sessionWithPersona(t *testing.T, root, cwd, persona string) string {
	t.Helper()
	s, err := NewSession(root, cwd, "anthropic", "claude-opus-5", "0.126.19")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	if persona != "" {
		if err := s.SetCreationSpec(persona, "play", "card_kobeni", nil, 0); err != nil {
			t.Fatalf("SetCreationSpec: %v", err)
		}
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// TestSessionsUsingPersonaSpansEveryProject is the reason this scan is
// server-side and not a filter over the loaded session list. A persona lives in
// $TERVA_HOME and is global; sessions are bucketed per working directory. A
// count taken from one project's list would tell a user deleting a persona that
// nothing depends on it while another project's chats did.
func TestSessionsUsingPersonaSpansEveryProject(t *testing.T) {
	root := testsupport.TempDir(t)
	projectA, projectB := testsupport.TempDir(t), testsupport.TempDir(t)

	sessionWithPersona(t, root, projectA, "kartoittaja-c")
	sessionWithPersona(t, root, projectB, "kartoittaja-c")
	sessionWithPersona(t, root, projectB, "someone-else")
	sessionWithPersona(t, root, projectA, "") // a plain coding session

	got := SessionsUsingPersona(root, "kartoittaja-c")
	if len(got) != 2 {
		t.Fatalf("found %d sessions using the persona, want 2 (one per project): %+v", len(got), got)
	}
	dirs := map[string]bool{}
	for _, s := range got {
		dirs[filepath.Dir(s.Path)] = true
	}
	if len(dirs) != 2 {
		t.Errorf("the two hits are in %d project bucket(s), want 2 — the scan is not crossing projects", len(dirs))
	}
}

// TestSessionsUsingPersonaReadsPastTheFirstMetaRow: SetCreationSpec writes the
// SECOND meta row, so a reader that stops at the first — the cheap
// ReadSessionMeta, which is authoritative for cwd and nothing else — reports
// every session as having no persona and the count is always zero.
func TestSessionsUsingPersonaReadsPastTheFirstMetaRow(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := sessionWithPersona(t, root, cwd, "kartoittaja-c")

	if first, err := ReadSessionMeta(path); err != nil {
		t.Fatalf("ReadSessionMeta: %v", err)
	} else if first.Persona != "" {
		t.Fatal("the first meta row now carries the persona; this test's premise needs rechecking")
	}
	if got := SessionsUsingPersona(root, "kartoittaja-c"); len(got) != 1 {
		t.Errorf("found %d sessions, want 1 — the scan stopped at the first meta row", len(got))
	}
}

// TestSessionsUsingPersonaMatchesTheNameCaseInsensitively: a persona is
// addressed by a stem the user types, and the roster folds case.
func TestSessionsUsingPersonaMatchesTheNameCaseInsensitively(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	sessionWithPersona(t, root, cwd, "Kartoittaja-C")

	if got := SessionsUsingPersona(root, "kartoittaja-c"); len(got) != 1 {
		t.Errorf("found %d sessions for a differently-cased name, want 1", len(got))
	}
}

// TestSessionsUsingPersonaIgnoresAnEmptyName: an empty query must not match
// every coding session ever created, which is what a naive equality would do.
func TestSessionsUsingPersonaIgnoresAnEmptyName(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	sessionWithPersona(t, root, cwd, "")
	sessionWithPersona(t, root, cwd, "")

	for _, q := range []string{"", "   "} {
		if got := SessionsUsingPersona(root, q); len(got) != 0 {
			t.Errorf("query %q matched %d sessions, want 0", q, len(got))
		}
	}
}

// TestSessionsUsingPersonaSurvivesAMissingRoot: a fresh install has no sessions
// directory, and asking about a persona there is an ordinary question with an
// ordinary answer, not an error path.
func TestSessionsUsingPersonaSurvivesAMissingRoot(t *testing.T) {
	if got := SessionsUsingPersona(filepath.Join(testsupport.TempDir(t), "nothing-here"), "kartoittaja-c"); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// TestSessionsUsingCardSpansEveryProject: the card library is $TERVA_HOME-wide
// while sessions are bucketed per working directory, so refusing a delete on
// only this workspace's chats would let a delete here break a story someone was
// telling in another project.
func TestSessionsUsingCardSpansEveryProject(t *testing.T) {
	root := testsupport.TempDir(t)
	projectA, projectB := testsupport.TempDir(t), testsupport.TempDir(t)

	sessionWithCard(t, root, projectA, "card_kobeni")
	sessionWithCard(t, root, projectB, "card_kobeni")
	sessionWithCard(t, root, projectB, "card_someone_else")

	if got := SessionsUsingCard(root, "card_kobeni"); len(got) != 2 {
		t.Errorf("found %d sessions on the card, want 2 (one per project)", len(got))
	}
}

// TestSessionsUsingCardMatchesTheIDExactly: a card id is an opaque library
// identifier, not a name a user types, so matching must not fold case or
// prefix — two cards whose ids differ only in case are two cards.
func TestSessionsUsingCardMatchesTheIDExactly(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	sessionWithCard(t, root, cwd, "card_kobeni")

	for _, q := range []string{"CARD_KOBENI", "card_kob", "card_kobeni_2", ""} {
		if got := SessionsUsingCard(root, q); len(got) != 0 {
			t.Errorf("query %q matched %d sessions, want 0", q, len(got))
		}
	}
	if got := SessionsUsingCard(root, "card_kobeni"); len(got) != 1 {
		t.Errorf("the exact id matched %d sessions, want 1", len(got))
	}
}

// sessionWithCard creates a live session bound to a card, holding one message so
// it survives the empty-session prune.
func sessionWithCard(t *testing.T, root, cwd, cardID string) string {
	t.Helper()
	s, err := NewSession(root, cwd, "anthropic", "claude-opus-5", "0.126.19")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	if err := s.SetCreationSpec("", "chat", cardID, nil, 0); err != nil {
		t.Fatalf("SetCreationSpec: %v", err)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// TestSessionsUsingCardIgnoresAnArchivedChat pins the exclusion directly rather
// than through the workspace, because the behaviour has two independent causes
// and only one of them is a decision.
//
// The name filter is the decision: DeleteSession removes the .jsonl and never
// the .jsonl.gz, so a card counted against an archive could not be deleted by
// any sequence of actions a user can perform. The other cause is incidental —
// describeSession cannot parse gzip bytes, so an archive would fall out of the
// scan even unfiltered. describeSessionFrom exists precisely so an archive CAN
// be summarized through a gzip reader, so that accident is one wiring change
// away from evaporating; this asserts the outcome that must survive it.
func TestSessionsUsingCardIgnoresAnArchivedChat(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := sessionWithCard(t, root, cwd, "card_kobeni")
	if got := SessionsUsingCard(root, "card_kobeni"); len(got) != 1 {
		t.Fatalf("the live chat is not being counted (%d); the rest of this proves nothing", len(got))
	}

	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if _, err := ArchiveSession(root, cwd, id); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if got := SessionsUsingCard(root, "card_kobeni"); len(got) != 0 {
		t.Errorf("an archived chat still holds its card (%d) — the card can never be deleted, "+
			"since DeleteSession does not remove a .jsonl.gz", len(got))
	}
}
