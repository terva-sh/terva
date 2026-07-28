package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedTurn appends one durable message. A session that never held any is pruned
// when the workspace closes, so a test about RESUMING one has to say something
// first — and a test that skips this passes for the wrong reason: the resume
// fails with "no such session" rather than the failure under test.
func seedTurn(t *testing.T, w *Workspace, id string) {
	t.Helper()
	s := w.existing(id)
	if s == nil {
		t.Fatalf("session %s is not live", id)
	}
	err := s.sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
}

// writeUserPersona puts a persona in the user library ($TERVA_HOME/personas) and
// returns a function that deletes it — the "duplicate a built-in to edit it,
// then delete the copy" workflow, which is the ordinary path into this bug.
func writeUserPersona(t *testing.T, home, name string) func() {
	t.Helper()
	dir := filepath.Join(home, "personas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir personas: %v", err)
	}
	path := filepath.Join(dir, name+".md")
	body := "---\nname: " + name + "\nsummary: a persona under test\n---\n\nSpeak plainly.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write persona: %v", err)
	}
	return func() {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove persona: %v", err)
		}
	}
}

// TestASessionOutlivesThePersonaItWasCreatedWith is the defect.
//
// A session persists the persona it was created with and REPLAYS it on every
// materialize, and resolution refuses a name it cannot find — so deleting a
// persona did not degrade those sessions, it made them unopenable. The
// transcript was intact and simply unreachable over a name, with the only way
// back being to recreate a persona spelled exactly that, which nobody would
// guess. The trigger is an ordinary workflow: duplicate a built-in to edit it,
// play, delete the copy.
func TestASessionOutlivesThePersonaItWasCreatedWith(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	removePersona := writeUserPersona(t, home, "kartoittaja-c")

	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}

	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Persona: "kartoittaja-c"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id := info.ID
	if s := w.existing(id); s == nil || s.sess.Meta.Persona != "kartoittaja-c" {
		t.Fatalf("the session did not persist its persona; meta = %+v", info)
	}
	seedTurn(t, w, id)
	w.Close()

	// The user deletes the persona they had been playing with.
	removePersona()

	// A fresh daemon picks the session back up. This is where it used to die
	// with `internal: resolve: persona "kartoittaja-c" not found`.
	w2, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace (second): %v", err)
	}
	defer w2.Close()
	var notes []string
	w2.diag = func(m string) { notes = append(notes, m) }

	s, err := w2.resolve(id)
	if err != nil {
		t.Fatalf("the session did not open after its persona was deleted: %v\n"+
			"the transcript is intact and unreachable over a name", err)
	}
	if s == nil || s.agent == nil {
		t.Fatal("session resolved without an agent")
	}
	// It must still be the same session — the fallback replaces the voice, not
	// the transcript or anything else the session persisted.
	if s.sess.Meta.Persona != "kartoittaja-c" {
		t.Errorf("the session's recorded persona changed to %q; the file should still say what it was created with",
			s.sess.Meta.Persona)
	}
	// And it has to SAY so. A session that silently changes voice is a bug
	// report about the model behaving oddly.
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "kartoittaja-c") {
		t.Errorf("nothing told the user the persona was missing; diags were:\n%s", joined)
	}
}

// TestAResolveFailureThatIsNotThePersonaStillFails: the fallback is a retry, so
// it must not turn every build failure into a session that quietly opens
// differently. A session naming a persona that DOES resolve, failing for another
// reason, still surfaces that reason.
func TestAResolveFailureThatIsNotThePersonaStillFails(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	writeUserPersona(t, home, "kartoittaja-c")

	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Persona: "kartoittaja-c"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id := info.ID
	// Point the session at a card that does not exist. The persona resolves
	// fine; the build fails on the card, and dropping the persona cannot fix it.
	seedTurn(t, w, id)
	if s := w.existing(id); s != nil {
		if err := s.sess.SetCreationSpec("kartoittaja-c", "chat", "no-such-card-anywhere", nil, 0); err != nil {
			t.Fatalf("SetCreationSpec: %v", err)
		}
	}
	w.Close()

	w2, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace (second): %v", err)
	}
	defer w2.Close()
	_, err = w2.resolve(id)
	if err == nil {
		t.Fatal("a build that fails for a reason other than the persona must still fail, " +
			"not open quietly without the persona it was created with")
	}
	// And it must fail for the REASON under test. A pruned or missing file
	// errors here too, which would make this pass without exercising anything.
	if strings.Contains(err.Error(), "no such session") {
		t.Fatalf("the session was gone before the build ran, so this proves nothing: %v", err)
	}
}
