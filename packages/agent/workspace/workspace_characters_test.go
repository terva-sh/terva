package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// TestCharactersSurface — the library renders as a workspace-scoped pane: it is
// offered (live, no actions) and its content carries the stored cards plus the
// persona roster, reusing the verbs' own summaries.
func TestCharactersSurface(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Nova"}`)}); err != nil {
		t.Fatal(err)
	}

	metas, err := w.Surfaces(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	var meta *ctrlproto.SurfaceMeta
	for i := range metas {
		if metas[i].ID == "characters" {
			meta = &metas[i]
		}
	}
	if meta == nil {
		t.Fatal("characters pane is not offered")
	}
	if meta.Kind != "characters" || meta.Scope != "workspace" || !meta.Live || meta.Actions {
		t.Errorf("characters meta wrong: %+v", *meta)
	}

	surf, err := w.Surface(ctx, info.ID, "characters")
	if err != nil {
		t.Fatal(err)
	}
	if surf.Characters == nil {
		t.Fatal("surface carries no CharactersView")
	}
	if len(surf.Characters.Cards) != 1 || surf.Characters.Cards[0].Name != "Nova" {
		t.Errorf("cards in pane: %+v", surf.Characters.Cards)
	}
	if len(surf.Characters.Personas) == 0 {
		t.Error("personas should include the embedded crew")
	}
}
