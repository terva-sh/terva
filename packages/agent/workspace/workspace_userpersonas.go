package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
)

var _ ctrlproto.UserPersonasController = (*Workspace)(nil)

func (w *Workspace) userPersonaStore() *build.UserPersonaStore { return build.NewUserPersonaStore() }

func toUserPersonaView(p build.UserPersona) ctrlproto.UserPersonaView {
	return ctrlproto.UserPersonaView{Ref: p.Ref, Name: p.Name, Description: p.Description, Gender: p.Gender, Pronouns: p.Pronouns, Default: p.Default}
}

// UserPersonasList returns every saved user persona.
func (w *Workspace) UserPersonasList(_ context.Context) (ctrlproto.UserPersonasListResult, error) {
	list, err := w.userPersonaStore().List()
	if err != nil {
		return ctrlproto.UserPersonasListResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", err)
	}
	out := make([]ctrlproto.UserPersonaView, len(list))
	for i, p := range list {
		out[i] = toUserPersonaView(p)
	}
	return ctrlproto.UserPersonasListResult{Personas: out}, nil
}

// UserPersonaSave upserts a saved persona, preserving its default status — the
// default is owned by set_default alone, so editing a persona never disturbs
// which one is default.
func (w *Workspace) UserPersonaSave(_ context.Context, p ctrlproto.UserPersonaView) (ctrlproto.UserPersonaView, error) {
	if strings.TrimSpace(p.Name) == "" {
		return ctrlproto.UserPersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a user persona needs a name")
	}
	store := w.userPersonaStore()
	up := build.UserPersona{Name: p.Name, Description: p.Description, Gender: p.Gender, Pronouns: p.Pronouns}
	if existing, err := store.Get(p.Name); err == nil {
		up.Default = existing.Default
	}
	saved, err := store.Save(up)
	if err != nil {
		return ctrlproto.UserPersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "save user persona: %v", err)
	}
	return toUserPersonaView(saved), nil
}

// UserPersonaDelete removes a saved persona (a missing one is a no-op).
func (w *Workspace) UserPersonaDelete(_ context.Context, p ctrlproto.UserPersonaRef) error {
	if err := w.userPersonaStore().Delete(p.Ref); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "delete user persona: %v", err)
	}
	return nil
}

// UserPersonaSetDefault marks one saved persona the default (clearing the rest);
// an empty ref clears the default.
func (w *Workspace) UserPersonaSetDefault(_ context.Context, p ctrlproto.UserPersonaRef) error {
	if err := w.userPersonaStore().SetDefault(p.Ref); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	return nil
}
