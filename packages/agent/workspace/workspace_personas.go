package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The persona library on the wire. Reads (list/get) are open; create/edit are
// TRUSTED-tier — a persona charter shapes identity in the cached prefix, so
// authoring one is a privileged write, gated here exactly like other trusted
// operations (permissions.ResolveTrustState). Writes land in the user library
// ($TERVA_HOME/personas); a built-in is read-only and copies to edit.
var _ ctrlproto.PersonasController = (*Workspace)(nil)

// libraryTrusted reports whether this workspace may perform a trusted-tier
// library write (persona create/edit). Resolved live from the trust store, so a
// control.trust grant takes effect without a restart.
func (w *Workspace) libraryTrusted() bool {
	return permissions.ResolveTrustState(w.cwd, false).IsTrusted()
}

// PersonasList returns the merged roster with provenance.
func (w *Workspace) PersonasList(_ context.Context) (ctrlproto.PersonasListResult, error) {
	all := persona.All()
	out := make([]ctrlproto.PersonaSummary, 0, len(all))
	for _, p := range all {
		out = append(out, personaSummary(p))
	}
	return ctrlproto.PersonasListResult{Personas: out}, nil
}

// PersonasGet returns one persona in full, including how many sessions were
// created with it (see PersonaView.SessionsUsing) — the cost of that scan is
// why it is reported here and not on the list.
func (w *Workspace) PersonasGet(_ context.Context, p ctrlproto.PersonaGetParams) (ctrlproto.PersonaView, error) {
	q := p.Query()
	found, ok := persona.Lookup(q)
	if !ok {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("persona %q not found", q))
	}
	v := personaView(found)
	// Counted by the persona's own name rather than the requested ref: a session
	// records what SetCreationSpec was given, and a caller may have asked for the
	// same persona by a namespaced ref or a differently-cased stem.
	v.SessionsUsing = len(core.SessionsUsingPersona(w.root, found.Name))
	return v, nil
}

// PersonasCreate writes a NEW persona to the user library (trusted-tier).
//
// A created persona is top-level. Nothing offers authoring INTO a namespace,
// and a namespace is not a folder the user picks: it is the identity of a team
// the personas already belong to, which is why the only writes that carry one
// are edits of a persona that already has it.
func (w *Workspace) PersonasCreate(_ context.Context, p ctrlproto.PersonaWriteParams) (ctrlproto.PersonaView, error) {
	if err := w.guardPersonaWrite(p.Name); err != nil {
		return ctrlproto.PersonaView{}, err
	}
	if _, exists := persona.UserPath(p.Name); exists {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("persona %q already exists — edit it instead", p.Name))
	}
	return w.writePersona(p, persona.Persona{})
}

// PersonasEdit overwrites an existing persona; copy-to-edit for a built-in
// (trusted-tier).
//
// The edit is filed as the persona it resolved (persona.Overwrite), and that is
// what makes copy-to-edit true rather than nearly true. A user file shadows a
// built-in by Key — "<namespace>:<stem>" — and a form off the wire carries
// neither half, so rebuilding both from the display name landed the copy beside
// the original instead of over it: the roster grew a second Vartija, every
// qualified reference kept resolving to the built-in, and the library reported
// the edit as saved. Ten of the sixteen shipped personas were affected.
//
// Which persona is being edited comes from the REF, not from the name in the
// form — they are separate fields for that reason (PersonaEditParams). The name
// is content an edit may change, and resolving by it meant the editor opened a
// persona by ref and saved it by name, so with two personas sharing a name it
// could write over the one you were not looking at. A client that sends no ref
// identifies by name, as it always did.
func (w *Workspace) PersonasEdit(_ context.Context, p ctrlproto.PersonaEditParams) (ctrlproto.PersonaView, error) {
	if err := w.guardPersonaWrite(p.Name); err != nil {
		return ctrlproto.PersonaView{}, err
	}
	target := p.Target()
	found, ok := persona.Lookup(target)
	if !ok {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("persona %q not found — create it instead", target))
	}
	return w.writePersona(p.PersonaWriteParams, found)
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
	q := p.Query()
	if err := w.guardPersonaWrite(q); err != nil {
		return err
	}
	removed, err := persona.Delete(q)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "delete persona: %v", err)
	}
	if !removed {
		// Distinguish the ways there is nothing to delete: a name you never had,
		// versus a built-in you cannot remove (and did not customize).
		if found, ok := persona.Lookup(q); ok {
			// 🪤 A third way, and it used to be the common one. The roster
			// resolves a file of YOURS that Delete could not find, and the
			// refusal below then told you your own persona was not yours —
			// naming its origin, "user", in the sentence refusing you. That was
			// UserPath ignoring namespaces while the roster honoured them, so
			// every persona under a team subdirectory was undeletable. It is
			// fixed at the source; this says so rather than reprinting the lie
			// if the two ever drift apart again.
			if personaEditable(found) {
				return ctrlproto.Errorf(ctrlproto.CodeInternal,
					"%s", i18n.T("persona %q resolves to your file %s, but the library could not find it to delete", q, found.Source))
			}
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
				"%s", i18n.T("persona %q is %s, not yours to delete — duplicate it under a new name instead", q, personaOrigin(found)))
		}
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("persona %q not found", q))
	}
	w.broadcastLibraryChanged()
	return nil
}

// guardPersonaWrite enforces the two preconditions every persona write shares:
// a trusted workspace, and a non-empty name.
func (w *Workspace) guardPersonaWrite(name string) error {
	if strings.TrimSpace(name) == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a persona needs a name"))
	}
	if !w.libraryTrusted() {
		return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "%s", i18n.T("creating or editing a persona requires a trusted workspace"))
	}
	return nil
}

// writePersona writes the form, filed as `into` — the persona being overwritten,
// or the zero value for a create, which files it under its own new name.
func (w *Workspace) writePersona(p ctrlproto.PersonaWriteParams, into persona.Persona) (ctrlproto.PersonaView, error) {
	dest, err := persona.Write(persona.Overwrite(paramsToPersona(p), into))
	if err != nil {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "write persona: %v", err)
	}
	// Re-read through the roster so the returned view carries the resolved
	// provenance (the on-disk file now wins over any shadowed built-in) — by
	// FILE, because a write targeted by ref can land on a persona the form's
	// name does not resolve to, and answering by name would then describe a
	// persona this call never touched.
	saved, ok := persona.ByFile(dest)
	if !ok {
		return ctrlproto.PersonaView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("persona %q vanished after write", p.Name))
	}
	w.broadcastLibraryChanged()
	return personaView(saved), nil
}

func paramsToPersona(p ctrlproto.PersonaWriteParams) persona.Persona {
	return persona.Persona{
		Name:              p.Name,
		Pronunciation:     p.Pronunciation,
		Specialty:         p.Specialty,
		Summary:           p.Summary,
		Emoji:             p.Emoji,
		AccentColor:       p.AccentColor,
		Group:             p.Group,
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Immersive:         p.Immersive,
		Introduction:      p.Introduction,
		Charter:           p.Charter,
		Extends:           p.Extends,
	}
}

func personaSummary(p persona.Persona) ctrlproto.PersonaSummary {
	return ctrlproto.PersonaSummary{
		Name:        p.Name,
		Ref:         p.Ref(),
		Namespace:   p.Namespace,
		Specialty:   p.Specialty,
		Summary:     p.Summary,
		Emoji:       p.Emoji,
		AccentColor: p.AccentColor,
		// The EFFECTIVE shelf, not the raw field: a persona that inherits its
		// group from a team directory or an extension should file under it in
		// the roster exactly as a declared one does.
		Group:     p.GroupLabel(),
		Immersive: p.Immersive,
		Origin:    personaOrigin(p),
		Editable:  personaEditable(p),
	}
}

func personaView(p persona.Persona) ctrlproto.PersonaView {
	return ctrlproto.PersonaView{
		PersonaSummary:    personaSummary(p),
		Pronunciation:     p.Pronunciation,
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Introduction:      p.Introduction,
		Charter:           p.Charter,
		Extends:           p.Extends,
	}
}

// personaOrigin maps a persona's source to a short provenance tag the library
// renders: built-in (embedded crew), extension (bundle), or user (on-disk).
func personaOrigin(p persona.Persona) string {
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
func personaEditable(p persona.Persona) bool {
	return !p.Builtin() && !p.FromExtension() && strings.TrimSpace(p.Source) != ""
}
