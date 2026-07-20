package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// The persona library on the wire. Reads (list/get) are open; create/edit are
// TRUSTED-tier — a persona charter shapes identity in the cached prefix, so
// authoring one is a privileged write, gated here exactly like other trusted
// operations (build.ResolveTrustState). Writes land in the user library
// ($TERVA_HOME/personas); a built-in is read-only and copies to edit.
var _ ctrlproto.PersonasController = (*Workspace)(nil)

// libraryTrusted reports whether this workspace may perform a trusted-tier
// library write (persona create/edit). Resolved live from the trust store, so a
// control.trust grant takes effect without a restart.
func (w *Workspace) libraryTrusted() bool {
	return build.ResolveTrustState(build.Args{CWD: w.cwd}).IsTrusted()
}

// PersonasList returns the merged roster with provenance.
func (w *Workspace) PersonasList(_ context.Context) (ctrlproto.PersonasListResult, error) {
	all := build.AllPersonas()
	out := make([]ctrlproto.PersonaSummary, 0, len(all))
	for _, p := range all {
		out = append(out, personaSummary(p))
	}
	return ctrlproto.PersonasListResult{Personas: out}, nil
}

// PersonasGet returns one persona in full.
func (w *Workspace) PersonasGet(_ context.Context, p ctrlproto.PersonaGetParams) (ctrlproto.PersonaView, error) {
	found, ok := build.LookupPersona(p.Name)
	if !ok {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "persona %q not found", p.Name)
	}
	return personaView(found), nil
}

// PersonasCreate writes a NEW persona to the user library (trusted-tier).
func (w *Workspace) PersonasCreate(_ context.Context, p ctrlproto.PersonaWriteParams) (ctrlproto.PersonaView, error) {
	if err := w.guardPersonaWrite(p.Name); err != nil {
		return ctrlproto.PersonaView{}, err
	}
	if _, exists := build.UserPersonaPath(p.Name); exists {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeConflict, "persona %q already exists — edit it instead", p.Name)
	}
	return w.writePersona(p)
}

// PersonasEdit overwrites an existing persona; copy-to-edit for a built-in
// (trusted-tier).
func (w *Workspace) PersonasEdit(_ context.Context, p ctrlproto.PersonaWriteParams) (ctrlproto.PersonaView, error) {
	if err := w.guardPersonaWrite(p.Name); err != nil {
		return ctrlproto.PersonaView{}, err
	}
	if _, ok := build.LookupPersona(p.Name); !ok {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "persona %q not found — create it instead", p.Name)
	}
	return w.writePersona(p)
}

// PersonasDelete removes a persona from the user library (trusted-tier).
//
// Only a user on-disk file can go. A built-in or extension persona is not ours
// to remove, and the request is REFUSED rather than quietly succeeding — a
// silent no-op would leave the roster unchanged with nothing to explain why.
// Deleting a user file that shadows a built-in un-shadows it, so the built-in
// reappears in the roster; that is the only way back from a copy-to-edit, and
// the reason the refusal message names it.
func (w *Workspace) PersonasDelete(_ context.Context, p ctrlproto.PersonaDeleteParams) error {
	if err := w.guardPersonaWrite(p.Name); err != nil {
		return err
	}
	removed, err := build.DeletePersona(p.Name)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "delete persona: %v", err)
	}
	if !removed {
		// Distinguish the two ways there is nothing to delete: a name you never
		// had, versus a built-in you cannot remove (and did not customize).
		if found, ok := build.LookupPersona(p.Name); ok {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
				"persona %q is %s, not yours to delete — duplicate it under a new name instead", p.Name, personaOrigin(found))
		}
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "persona %q not found", p.Name)
	}
	w.broadcastLibraryChanged()
	return nil
}

// guardPersonaWrite enforces the two preconditions every persona write shares:
// a trusted workspace, and a non-empty name.
func (w *Workspace) guardPersonaWrite(name string) error {
	if strings.TrimSpace(name) == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a persona needs a name")
	}
	if !w.libraryTrusted() {
		return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "creating or editing a persona requires a trusted workspace")
	}
	return nil
}

func (w *Workspace) writePersona(p ctrlproto.PersonaWriteParams) (ctrlproto.PersonaView, error) {
	if _, err := build.WritePersona(paramsToPersona(p)); err != nil {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "write persona: %v", err)
	}
	// Re-read through the roster so the returned view carries the resolved
	// provenance (the on-disk file now wins over any shadowed built-in).
	saved, ok := build.LookupPersona(p.Name)
	if !ok {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "persona %q vanished after write", p.Name)
	}
	w.broadcastLibraryChanged()
	return personaView(saved), nil
}

func paramsToPersona(p ctrlproto.PersonaWriteParams) build.Persona {
	return build.Persona{
		Name:              p.Name,
		Pronunciation:     p.Pronunciation,
		Specialty:         p.Specialty,
		Summary:           p.Summary,
		Emoji:             p.Emoji,
		AccentColor:       p.AccentColor,
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Immersive:         p.Immersive,
		Introduction:      p.Introduction,
		Charter:           p.Charter,
	}
}

func personaSummary(p build.Persona) ctrlproto.PersonaSummary {
	return ctrlproto.PersonaSummary{
		Name:        p.Name,
		Ref:         p.Ref(),
		Namespace:   p.Namespace,
		Specialty:   p.Specialty,
		Summary:     p.Summary,
		Emoji:       p.Emoji,
		AccentColor: p.AccentColor,
		Immersive:   p.Immersive,
		Origin:      personaOrigin(p),
		Editable:    personaEditable(p),
	}
}

func personaView(p build.Persona) ctrlproto.PersonaView {
	return ctrlproto.PersonaView{
		PersonaSummary:    personaSummary(p),
		Pronunciation:     p.Pronunciation,
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Introduction:      p.Introduction,
		Charter:           p.Charter,
	}
}

// personaOrigin maps a persona's source to a short provenance tag the library
// renders: built-in (embedded crew), extension (bundle), or user (on-disk).
func personaOrigin(p build.Persona) string {
	switch {
	case p.Builtin():
		return "built-in"
	case p.FromExtension():
		return "extension"
	default:
		return "user"
	}
}

// personaEditable reports whether a persona can be written in place. Only a
// user on-disk file is editable; a built-in or extension persona copies to edit
// (a new user file that shadows it).
func personaEditable(p build.Persona) bool {
	return !p.Builtin() && !p.FromExtension() && strings.TrimSpace(p.Source) != ""
}
