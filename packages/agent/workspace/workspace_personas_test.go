package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/testsupport"
)

// editParams is an edit that names its target the pre-ref way, by the name in
// the form. Most of these tests have one persona of the name in play, so the
// two identifiers agree and the shorter call reads better — and it keeps the
// name-fallback path exercised by everything, not by one test that could be
// deleted without anyone noticing the path went with it.
//
// The tests where they DISAGREE pass a ref explicitly; that is the whole point
// of the field, and it is where the target is spelled out.
func editParams(p ctrlproto.PersonaWriteParams) ctrlproto.PersonaEditParams {
	return ctrlproto.PersonaEditParams{PersonaWriteParams: p}
}

func newPersonaWorkspace(t *testing.T) *Workspace {
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

// TestWorkspacePersonasListAndGet — reads are open and the embedded crew is
// present, built-in, and not editable in place.
func TestWorkspacePersonasListAndGet(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()

	r, err := w.PersonasList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Personas) == 0 {
		t.Fatal("roster should include the embedded crew")
	}
	var mieli *ctrlproto.PersonaSummary
	for i := range r.Personas {
		if r.Personas[i].Name == "Mieli" || r.Personas[i].Ref == "mieli" {
			mieli = &r.Personas[i]
		}
	}
	if mieli == nil {
		t.Fatal("Mieli not found in the roster")
	}
	if mieli.Origin != "built-in" || mieli.Editable {
		t.Errorf("a built-in persona must be non-editable: %+v", *mieli)
	}

	got, err := w.PersonasGet(ctx, ctrlproto.PersonaGetParams{Name: "mieli"})
	if err != nil {
		t.Fatalf("get mieli: %v", err)
	}
	if got.Charter == "" {
		t.Error("get should carry the charter")
	}
	if _, err := w.PersonasGet(ctx, ctrlproto.PersonaGetParams{Name: "nobody-here"}); err == nil {
		t.Error("get on a missing persona should error")
	}
}

// TestWorkspacePersonaWriteRequiresTrust — the trusted-tier gate: an untrusted
// workspace cannot author a persona, and a refused write leaves nothing behind.
func TestWorkspacePersonaWriteRequiresTrust(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()

	_, err := w.PersonasCreate(ctx, ctrlproto.PersonaWriteParams{Name: "Untrusted", Charter: "x"})
	var werr *ctrlproto.Error
	if !errors.As(err, &werr) || werr.Code != ctrlproto.CodeUnauthorized {
		t.Fatalf("create in an untrusted workspace: err = %v, want CodeUnauthorized", err)
	}
	if r, _ := w.PersonasList(ctx); containsPersona(r.Personas, "Untrusted") {
		t.Error("a refused create must not write a persona")
	}
}

// TestWorkspacePersonaCreateAndEditWhenTrusted — the full authoring loop once
// the workspace is trusted, including the create/edit precondition split.
func TestWorkspacePersonaCreateAndEditWhenTrusted(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}

	created, err := w.PersonasCreate(ctx, ctrlproto.PersonaWriteParams{
		Name: "Custom", Summary: "mine", Charter: "You are Custom.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Origin != "user" || !created.Editable {
		t.Errorf("created persona should be user + editable: %+v", created.PersonaSummary)
	}

	if _, err := w.PersonasCreate(ctx, ctrlproto.PersonaWriteParams{Name: "Custom", Charter: "y"}); err == nil {
		t.Error("re-creating an existing persona should conflict")
	}

	edited, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{Name: "Custom", Summary: "updated", Charter: "New charter."}))
	if err != nil {
		t.Fatal(err)
	}
	if edited.Summary != "updated" {
		t.Errorf("edit not applied: %q", edited.Summary)
	}

	if _, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{Name: "Ghost", Charter: "x"})); err == nil {
		t.Error("editing a nonexistent persona should error (create it instead)")
	}
}

// TestWorkspacePersonaCopyToEditBuiltin — editing a built-in writes a shadowing
// user file rather than mutating the embedded crew.
func TestWorkspacePersonaCopyToEditBuiltin(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}

	edited, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{
		Name: "Mieli", Summary: "my mieli", Charter: "Custom Mieli.",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if edited.Origin != "user" || !edited.Editable {
		t.Errorf("copy-to-edit should yield a user persona: %+v", edited.PersonaSummary)
	}
	if _, exists := persona.UserPath("Mieli"); !exists {
		t.Error("copy-to-edit should have written a user file")
	}
}

func containsPersona(ps []ctrlproto.PersonaSummary, name string) bool {
	for _, p := range ps {
		if p.Name == name {
			return true
		}
	}
	return false
}

// TestWorkspacePersonaDelete — a user persona can be removed, and the roster
// stops carrying it.
func TestWorkspacePersonaDelete(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PersonasCreate(ctx, ctrlproto.PersonaWriteParams{Name: "Scratch", Charter: "temp."}); err != nil {
		t.Fatal(err)
	}
	if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: "Scratch"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := persona.UserPath("Scratch"); exists {
		t.Error("the user file should be gone")
	}
	r, err := w.PersonasList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if containsPersona(r.Personas, "Scratch") {
		t.Error("a deleted persona should leave the roster")
	}
}

// TestWorkspacePersonaDeleteRefusesABuiltin — the embedded crew is not ours to
// remove. The refusal must be an ERROR, not a silent success: a no-op would
// leave the roster unchanged with nothing to explain why, which reads as a bug.
func TestWorkspacePersonaDeleteRefusesABuiltin(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: "Mieli"})
	if err == nil {
		t.Fatal("deleting a built-in should be refused")
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("the refusal should say WHY it cannot go: %v", err)
	}
	if _, ok := persona.Lookup("Mieli"); !ok {
		t.Error("the built-in must survive a refused delete")
	}
}

// TestWorkspacePersonaDeleteUnshadowsABuiltin — 🔑 the way back from a
// copy-to-edit. Editing a built-in writes a user file that shadows it by slug;
// deleting that file must REVEAL the built-in again rather than leave a hole,
// which is the whole reason delete beats a tombstone.
func TestWorkspacePersonaDeleteUnshadowsABuiltin(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{Name: "Mieli", Summary: "mine", Charter: "Custom."})); err != nil {
		t.Fatal(err)
	}
	if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: "Mieli"}); err != nil {
		t.Fatal(err)
	}
	restored, ok := persona.Lookup("Mieli")
	if !ok {
		t.Fatal("deleting the shadow should reveal the built-in, not remove the persona")
	}
	if restored.Summary == "mine" {
		t.Error("the roster still resolves the deleted user copy")
	}
	if origin := personaOrigin(restored); origin != "built-in" {
		t.Errorf("origin after un-shadow = %q, want built-in", origin)
	}
}

// TestWorkspacePersonaDeleteRequiresTrust — delete is a trusted-tier mutation
// like create/edit; an untrusted workspace must not be able to remove identity.
func TestWorkspacePersonaDeleteRequiresTrust(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PersonasCreate(ctx, ctrlproto.PersonaWriteParams{Name: "Scratch", Charter: "temp."}); err != nil {
		t.Fatal(err)
	}
	if err := config.UntrustPath(w.cwd); err != nil {
		t.Fatal(err)
	}
	if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: "Scratch"}); err == nil {
		t.Fatal("an untrusted workspace must not delete a persona")
	}
	if _, exists := persona.UserPath("Scratch"); !exists {
		t.Error("the persona must survive a refused delete")
	}
}

// TestWorkspacePersonaDeleteUnknown — a name that was never there is a
// not-found, distinct from the built-in refusal above.
func TestWorkspacePersonaDeleteUnknown(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: "Ghost"}); err == nil {
		t.Error("deleting an unknown persona should error")
	}
}

// The control-plane leg of the round trip. A persona write REPLACES the file,
// so `extends` has to survive create → get → edit or the library silently
// deletes an inheritance the user never touched — and the deletion looks
// exactly like a persona that never had one.
func TestWorkspacePersonaExtendsSurvivesTheEditRoundTrip(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}

	created, err := w.PersonasCreate(ctx, ctrlproto.PersonaWriteParams{
		Name: "Assistant", Extends: "mieli", Charter: "Track what is in flight.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Extends != "mieli" {
		t.Errorf("create dropped extends: %+v", created)
	}

	got, err := w.PersonasGet(ctx, ctrlproto.PersonaGetParams{Name: created.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if got.Extends != "mieli" {
		t.Fatalf("get does not return extends, so no editor could send it back: %+v", got)
	}

	// The editor sends the view back, whole. That is the flow this guards.
	edited, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{
		Name: got.Name, Extends: got.Extends, Charter: "Track what is in flight, and what is blocked.",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if edited.Extends != "mieli" {
		t.Errorf("edit erased extends: %+v", edited)
	}
}
