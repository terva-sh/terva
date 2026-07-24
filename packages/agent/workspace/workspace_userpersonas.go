package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
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
//
// A Ref names the persona BEING EDITED, which is what makes a RENAME expressible.
// Without it the name alone identifies the row, so changing the name read as a
// create: the new file appeared and the old one stayed, leaving two of you in a
// library that is supposed to hold one. With it, the old row is retired once the
// new one is written, and its default status comes along.
func (w *Workspace) UserPersonaSave(_ context.Context, p ctrlproto.UserPersonaView) (ctrlproto.UserPersonaView, error) {
	if strings.TrimSpace(p.Name) == "" {
		return ctrlproto.UserPersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a user persona needs a name"))
	}
	store := w.userPersonaStore()
	up := build.UserPersona{Name: p.Name, Description: p.Description, Gender: p.Gender, Pronouns: p.Pronouns}
	// The persona being edited, if the client said which. A ref that no longer
	// resolves (deleted from another tab) falls back to a plain create rather than
	// failing the save — the author's text is worth more than the bookkeeping.
	priorRef := ""
	if ref := strings.TrimSpace(p.Ref); ref != "" {
		if existing, err := store.Get(ref); err == nil {
			priorRef = existing.Ref
			up.Default = existing.Default
		}
	}
	// The row the new NAME lands on. Landing on the persona being edited is the
	// ordinary in-place edit; landing on a DIFFERENT one would overwrite that
	// persona with this one's text and then delete this one — two rows lost to a
	// typo — so a colliding rename is refused instead of merged.
	if landing, err := store.Get(p.Name); err == nil {
		if priorRef != "" && landing.Ref != priorRef {
			return ctrlproto.UserPersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("you already have a persona named %q", landing.Name))
		}
		if priorRef == "" {
			up.Default = landing.Default
		}
	}
	saved, err := store.Save(up)
	if err != nil {
		return ctrlproto.UserPersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "save user persona: %v", err)
	}
	// A rename wrote a second file; retire the one it replaced. Done AFTER the
	// write, so a failure here leaves a duplicate rather than nothing.
	if priorRef != "" && priorRef != saved.Ref {
		if err := store.Delete(priorRef); err != nil {
			return ctrlproto.UserPersonaView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "rename user persona: %v", err)
		}
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
