package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The session state sidecar on the wire (workspace_state.go). core is tested on
// the FILE (packages/core/session_state_test.go); these are the guards for what
// the daemon adds on top: id resolution, the tenant-scoped write, and the one
// error a user can act on.

func stateWorkspace(t *testing.T) *Workspace {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

// stateSession creates a session and returns its id.
func stateSession(t *testing.T, w *Workspace) string {
	t.Helper()
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// The whole point of serving the draft from the daemon: what one front end
// typed, another reads back.
func TestAComposerDraftSurvivesTheRoundTrip(t *testing.T) {
	w := stateWorkspace(t)
	ctx := context.Background()
	id := stateSession(t, w)

	if err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: "half a question about the parser"}); err != nil {
		t.Fatal(err)
	}
	got, err := w.SessionState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Composer == nil {
		t.Fatal("no draft came back")
	}
	if got.Composer.Text != "half a question about the parser" {
		t.Errorf("text = %q", got.Composer.Text)
	}
	// An unset source means the user's own words — the reading that cannot
	// hand the machine's line back as though a person wrote it.
	if got.Composer.Source != ctrlproto.ComposerSourceUser {
		t.Errorf("source = %q, want %q", got.Composer.Source, ctrlproto.ComposerSourceUser)
	}
	// The stamp is server-set: the caller sent none.
	if got.Composer.UpdatedAt == nil || got.Composer.UpdatedAt.IsZero() {
		t.Errorf("updated_at = %v, want a server stamp", got.Composer.UpdatedAt)
	}
}

// A stored suggestion must come back LABELLED as one, because that label is
// what makes the front end offer it as a ghost instead of dropping the model's
// words into the composer as the user's own.
func TestASuggestionComesBackTaggedAsASuggestion(t *testing.T) {
	w := stateWorkspace(t)
	ctx := context.Background()
	id := stateSession(t, w)

	err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{
		Text:   "Shall I run the tests?",
		Source: ctrlproto.ComposerSourceSuggestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.SessionState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Composer == nil || got.Composer.Source != ctrlproto.ComposerSourceSuggestion {
		t.Fatalf("draft = %+v, want source %q", got.Composer, ctrlproto.ComposerSourceSuggestion)
	}
}

// Emptying the composer is how a front end says "there is no draft any more",
// so blank text clears rather than storing an empty draft that a later restore
// would put into the composer and call a restore.
func TestBlankTextClearsTheDraft(t *testing.T) {
	w := stateWorkspace(t)
	ctx := context.Background()
	id := stateSession(t, w)

	if err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: "typed then thought better of"}); err != nil {
		t.Fatal(err)
	}
	if err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: "   \n  "}); err != nil {
		t.Fatal(err)
	}
	got, err := w.SessionState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Composer != nil {
		t.Errorf("draft = %+v, want none", got.Composer)
	}
	// An empty document is no document: leaving a state file that says nothing
	// litters the sessions directory.
	if _, err := os.Stat(w.statePath(id)); !os.IsNotExist(err) {
		t.Errorf("state file still present after the last tenant was cleared (stat err = %v)", err)
	}
}

// The reason the setter is tenant-scoped and there is no sessions.set_state: a
// write from a client that has never heard of another tenant must not delete
// it. Otherwise an older web build would erase a newer TUI's state on every
// draft save.
func TestAComposerWriteKeepsATenantItDoesNotKnowAbout(t *testing.T) {
	w := stateWorkspace(t)
	ctx := context.Background()
	id := stateSession(t, w)
	path := w.statePath(id)

	// A tenant from an imagined future binary, written by hand.
	seed := `{"version":1,"scroll":{"anchor":"msg-17"}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: "a draft from this binary"}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["composer"]; !ok {
		t.Error("the composer tenant was not written")
	}
	if got := string(doc["scroll"]); got != `{"anchor":"msg-17"}` {
		t.Errorf("the unknown tenant did not survive the write: %q", got)
	}
}

// Over the cap the write is REFUSED and nothing changes. Half a draft looks
// like a whole one, so the user has to be told rather than quietly given a
// prefix — and the draft they had before must still be there.
func TestAnOverCapDraftIsRefusedAndLeavesTheOldOne(t *testing.T) {
	w := stateWorkspace(t)
	ctx := context.Background()
	id := stateSession(t, w)

	if err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: "the keeper"}); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", core.MaxSessionStateBytes+1)
	err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: huge})
	if err == nil {
		t.Fatal("an over-cap draft was accepted")
	}
	// A coded refusal, not an internal error: the user did this on purpose and
	// can act on it (shorten it, or send it).
	var wire *ctrlproto.Error
	if !errors.As(err, &wire) || wire.Code != ctrlproto.CodeBadRequest {
		t.Errorf("err = %v, want a %s wire error", err, ctrlproto.CodeBadRequest)
	}

	got, gerr := w.SessionState(ctx, id)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Composer == nil || got.Composer.Text != "the keeper" {
		t.Errorf("draft = %+v, want the previous one intact", got.Composer)
	}
}

// An id that names no session is refused, and — the part that matters — writes
// nothing. A state file whose transcript never existed is litter nothing
// collects: the sidecar table reaps a state file when its TRANSCRIPT is
// deleted, pruned or archived.
func TestAnIdThatNamesNoSessionIsRefusedAndWritesNothing(t *testing.T) {
	w := stateWorkspace(t)
	ctx := context.Background()
	// One real session, so the sessions directory exists and the sweep below
	// distinguishes "nothing was written" from "nothing could have been".
	stateSession(t, w)

	for _, id := range []string{
		"never-existed",
		"../escape",              // path traversal: it would become a filename
		strings.Repeat("x", 200), // a plausible-looking stem that is still no session
	} {
		err := w.SetComposerDraft(ctx, id, ctrlproto.ComposerDraft{Text: "stray"})
		if !errors.Is(err, ctrlproto.ErrNoSession) {
			t.Errorf("SetComposerDraft(%q) err = %v, want ErrNoSession", id, err)
		}
		if _, err := w.SessionState(ctx, id); !errors.Is(err, ctrlproto.ErrNoSession) {
			t.Errorf("SessionState(%q) err = %v, want ErrNoSession", id, err)
		}
	}

	// Nothing was created anywhere under the sessions directory, and nothing
	// escaped it either.
	entries, err := os.ReadDir(w.sessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".state.json") {
			t.Errorf("a refused write left %s behind", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.sessionsDir()), "escape.state.json")); !os.IsNotExist(err) {
		t.Errorf("a traversing id wrote outside the sessions directory (stat err = %v)", err)
	}
}

// A draft with no stamp sends no time. A hand-written or hand-edited state file
// can carry a composer with no updated_at, and forwarding time.Time's zero
// value would put year 1 on the wire as though it were a fact — a client would
// render "written 2025 years ago" and believe it.
func TestADraftWithNoStampSendsNoTime(t *testing.T) {
	w := stateWorkspace(t)
	id := stateSession(t, w)

	seed := `{"version":1,"composer":{"text":"typed by hand into the file","source":"user"}}`
	if err := os.WriteFile(w.statePath(id), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := w.SessionState(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Composer == nil {
		t.Fatal("no draft came back")
	}
	if got.Composer.UpdatedAt != nil {
		t.Errorf("updated_at = %v, want none for a draft that never carried one", got.Composer.UpdatedAt)
	}
}

// Reading a session that has no state file is an ANSWER, not a failure: the
// sidecar is a convenience and the transcript is the data, so a session must
// never fail to open over a draft it does not have.
func TestASessionWithNoStateReadsAsEmpty(t *testing.T) {
	w := stateWorkspace(t)
	id := stateSession(t, w)

	got, err := w.SessionState(context.Background(), id)
	if err != nil {
		t.Fatalf("err = %v, want a session with no state to read cleanly", err)
	}
	if got.Composer != nil {
		t.Errorf("draft = %+v, want none", got.Composer)
	}
}
