package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/testsupport"
)

func minPNGBytes() []byte { return append([]byte("\x89PNG\r\n\x1a\n"), 0, 1, 2, 3) }

// fakeImageBackend returns a fixed image without touching a real provider, so
// the generate path is testable with no image-gen spend.
type fakeImageBackend struct{ data []byte }

func (fakeImageBackend) ID() string { return "fake" }
func (b fakeImageBackend) Generate(context.Context, imagegen.Request) (imagegen.Result, error) {
	return imagegen.Result{Images: []imagegen.Image{{Data: b.data, MimeType: "image/png"}}, Backend: "fake"}, nil
}

// TestWorkspaceBackgroundsAndBind — import/list/delete plus the per-session bind
// that writes SessionMeta.Background and surfaces on SessionInfo.
func TestWorkspaceBackgroundsAndBind(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	view, err := w.BackgroundsImport(ctx, ctrlproto.BackgroundImportParams{Bytes: minPNGBytes()})
	if err != nil {
		t.Fatal(err)
	}
	if view.URL != "/media/backgrounds/"+view.ID {
		t.Errorf("background url = %q", view.URL)
	}
	if r, _ := w.BackgroundsList(ctx); len(r.Backgrounds) != 1 {
		t.Fatalf("list after import: %+v", r.Backgrounds)
	}

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.BackgroundBind(ctx, info.ID, ctrlproto.BackgroundBindParams{Background: view.ID}); err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live.sess.Meta.Background != view.ID {
		t.Errorf("meta background = %q, want %q", live.sess.Meta.Background, view.ID)
	}
	if live.info().Background != view.ID {
		t.Errorf("SessionInfo.Background = %q, want %q", live.info().Background, view.ID)
	}

	// A nonexistent (but well-formed) id is a clean not-found.
	if err := w.BackgroundBind(ctx, info.ID, ctrlproto.BackgroundBindParams{Background: "deadbeef"}); err == nil {
		t.Error("binding a nonexistent background should error")
	}

	// Clearing (empty id) is allowed.
	if err := w.BackgroundBind(ctx, info.ID, ctrlproto.BackgroundBindParams{Background: ""}); err != nil {
		t.Errorf("clearing the background should succeed: %v", err)
	}
	if w.live(info.ID).sess.Meta.Background != "" {
		t.Error("background not cleared")
	}

	if err := w.BackgroundsDelete(ctx, ctrlproto.BackgroundDeleteParams{ID: view.ID}); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.BackgroundsList(ctx); len(r.Backgrounds) != 0 {
		t.Errorf("list after delete: %+v", r.Backgrounds)
	}
}

// TestBackgroundsGenerate — backgrounds.generate paints a scene via the image
// backend, stores it, and binds it live; a missing backend and an empty prompt
// are clean errors.
func TestBackgroundsGenerate(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "play"})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)

	// No image backend configured → a clear bad request, not a silent no-op.
	if _, err := w.BackgroundsGenerate(ctx, info.ID, ctrlproto.BackgroundGenerateParams{Prompt: "a misty pass"}); err == nil {
		t.Error("generate without an image backend should error")
	}

	// Inject a fake backend; generate stores the image and binds it live.
	reg := imagegen.NewRegistry()
	reg.Add(fakeImageBackend{data: minPNGBytes()})
	live.imageRegistry = reg

	view, err := w.BackgroundsGenerate(ctx, info.ID, ctrlproto.BackgroundGenerateParams{Prompt: "a misty mountain pass at dusk"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if view.ID == "" || view.URL != "/media/backgrounds/"+view.ID {
		t.Fatalf("bad view: %+v", view)
	}
	if r, _ := w.BackgroundsList(ctx); len(r.Backgrounds) != 1 || r.Backgrounds[0].ID != view.ID {
		t.Errorf("generated background not stored: %+v", r.Backgrounds)
	}
	if got := live.info().Background; got != view.ID {
		t.Errorf("generated background not bound: %q, want %q", got, view.ID)
	}

	// An empty prompt is refused.
	if _, err := w.BackgroundsGenerate(ctx, info.ID, ctrlproto.BackgroundGenerateParams{Prompt: "  "}); err == nil {
		t.Error("an empty prompt should error")
	}
}

// TestBackgroundBoundAtCreate — CreateOpts.Background binds a backdrop up front.
func TestBackgroundBoundAtCreate(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	view, err := w.BackgroundsImport(ctx, ctrlproto.BackgroundImportParams{Bytes: minPNGBytes()})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Background: view.ID})
	if err != nil {
		t.Fatal(err)
	}
	if w.live(info.ID).sess.Meta.Background != view.ID {
		t.Errorf("bound-at-create background = %q, want %q", w.live(info.ID).sess.Meta.Background, view.ID)
	}
}
