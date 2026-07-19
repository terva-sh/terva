package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/imagegen"
)

// Scene backdrops on the wire. The store is global (like cards), but a
// background is BOUND per session — backgrounds.bind writes SessionMeta.Background
// (a durable meta row) and broadcasts a snapshot so the open view re-renders.
// Backgrounds are inert images, so there is no trust gate.
var _ ctrlproto.BackgroundsController = (*Workspace)(nil)

func (w *Workspace) bgStore() *build.BackgroundStore { return build.NewBackgroundStore() }

// BackgroundsList returns every stored backdrop.
func (w *Workspace) BackgroundsList(_ context.Context) (ctrlproto.BackgroundsListResult, error) {
	bgs, err := w.bgStore().List()
	if err != nil {
		return ctrlproto.BackgroundsListResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "list backgrounds: %v", err)
	}
	out := make([]ctrlproto.BackgroundView, 0, len(bgs))
	for _, b := range bgs {
		out = append(out, backgroundView(b.ID))
	}
	return ctrlproto.BackgroundsListResult{Backgrounds: out}, nil
}

// BackgroundsImport stores an image (bytes win over a server path).
func (w *Workspace) BackgroundsImport(_ context.Context, p ctrlproto.BackgroundImportParams) (ctrlproto.BackgroundView, error) {
	var (
		b   build.Background
		err error
	)
	switch {
	case len(p.Bytes) > 0:
		b, err = w.bgStore().ImportBytes(p.Bytes)
	case p.Path != "":
		b, err = w.bgStore().ImportPath(p.Path)
	default:
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "backgrounds.import needs bytes or a path")
	}
	if err != nil {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "import background: %v", err)
	}
	return backgroundView(b.ID), nil
}

// BackgroundsDelete removes a backdrop from the store.
func (w *Workspace) BackgroundsDelete(_ context.Context, p ctrlproto.BackgroundDeleteParams) error {
	if err := w.bgStore().Delete(p.ID); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	return nil
}

// BackgroundBind sets (or clears, with "") the backdrop bound to a session.
func (w *Workspace) BackgroundBind(_ context.Context, sess string, p ctrlproto.BackgroundBindParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if p.Background != "" && w.bgStore().Path(p.Background) == "" {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "background %q not found", p.Background)
	}
	if err := s.sess.SetBackground(p.Background); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "bind background: %v", err)
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// BackgroundsGenerate paints a scene from a prompt via the session's image
// backend, stores it, and binds it live — generate-and-set. The registry is the
// same one the generate_image tool uses; when none is configured this is a bad
// request rather than a silent no-op, so the client can say so.
func (w *Workspace) BackgroundsGenerate(ctx context.Context, sess string, p ctrlproto.BackgroundGenerateParams) (ctrlproto.BackgroundView, error) {
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "backgrounds.generate needs a prompt")
	}
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.BackgroundView{}, err
	}
	if s.imageRegistry == nil || s.imageRegistry.Len() == 0 {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "no image backend configured — set one up to generate scenes")
	}
	backend, err := s.imageRegistry.Resolve(strings.TrimSpace(p.Backend))
	if err != nil {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	res, err := backend.Generate(ctx, imagegen.Request{
		Prompt:         prompt,
		NegativePrompt: strings.TrimSpace(p.NegativePrompt),
		Size:           strings.TrimSpace(p.Size),
		N:              1,
	})
	if err != nil {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "generate scene: %v", err)
	}
	if len(res.Images) == 0 || len(res.Images[0].Data) == 0 {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "generate scene: the backend returned no image")
	}
	bg, err := w.bgStore().ImportBytes(res.Images[0].Data)
	if err != nil {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "store scene: %v", err)
	}
	if err := s.sess.SetBackground(bg.ID); err != nil {
		return ctrlproto.BackgroundView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "bind scene: %v", err)
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return backgroundView(bg.ID), nil
}

func backgroundView(id string) ctrlproto.BackgroundView {
	return ctrlproto.BackgroundView{ID: id, URL: "/media/backgrounds/" + id}
}
