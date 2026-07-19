package workspace

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

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

	edited, err := w.PersonasEdit(ctx, ctrlproto.PersonaWriteParams{Name: "Custom", Summary: "updated", Charter: "New charter."})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Summary != "updated" {
		t.Errorf("edit not applied: %q", edited.Summary)
	}

	if _, err := w.PersonasEdit(ctx, ctrlproto.PersonaWriteParams{Name: "Ghost", Charter: "x"}); err == nil {
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

	edited, err := w.PersonasEdit(ctx, ctrlproto.PersonaWriteParams{
		Name: "Mieli", Summary: "my mieli", Charter: "Custom Mieli.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Origin != "user" || !edited.Editable {
		t.Errorf("copy-to-edit should yield a user persona: %+v", edited.PersonaSummary)
	}
	if _, exists := build.UserPersonaPath("Mieli"); !exists {
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
