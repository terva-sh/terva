package ctrlproto

import (
	"context"
	"strings"
)

// The persona library on the wire — the identity half of the content library.
// Personas were CLI-only (terva persona list/validate/init); this brings them
// into the control plane so any controller inspects and authors them.
//
// Trust is the hard line, unlike cards: a persona charter shapes identity in the
// cached prefix, so create/edit is a TRUSTED-tier mutation — the workspace gates
// it (writes land in the user library, $TERVA_HOME/personas; the embedded crew is
// read-only and copy-to-edit). Reads (list/get) are ungated.
//
// PersonasController is OPTIONAL, like CardsController: a carrier without a
// persona library simply does not implement it. Adding these verbs does NOT
// ripple to every WorkspaceService implementer.
type PersonasController interface {
	// PersonasList returns the merged, deduped roster with provenance.
	PersonasList(ctx context.Context) (PersonasListResult, error)
	// PersonasGet returns one persona in full, including its charter.
	PersonasGet(ctx context.Context, p PersonaGetParams) (PersonaView, error)
	// PersonasCreate writes a NEW persona to the user library. Trusted-tier;
	// errors if a user persona of that name already exists (edit it instead).
	PersonasCreate(ctx context.Context, p PersonaWriteParams) (PersonaView, error)
	// PersonasEdit overwrites the persona its Ref names (copy-to-edit for a
	// built-in). Trusted-tier; errors if no persona of that ref exists (create it
	// instead).
	PersonasEdit(ctx context.Context, p PersonaEditParams) (PersonaView, error)
	// PersonasDelete removes a persona from the USER library. Trusted-tier.
	//
	// Only an on-disk user file can be deleted — the embedded crew and extension
	// bundles are not ours to remove, and a request naming one is refused rather
	// than silently doing nothing. Deleting a user file that SHADOWS a built-in
	// is the un-shadow: the built-in becomes visible again, which is the only way
	// back from a copy-to-edit and the reason this is a delete rather than a
	// tombstone.
	PersonasDelete(ctx context.Context, p PersonaDeleteParams) error
}

// Every persona verb identifies a persona by its REF — "<namespace>:<stem>",
// what personas.list publishes and what Persona.Ref() prints. A bare stem or
// display name still resolves, because the library's matcher accepts both.
//
// It is spelled `ref` and not `name` because on a WRITE the two are different
// things: the ref says which persona is being overwritten, the name is content
// the write may change. They were the same field, and the conflation is not
// theoretical — the editor opened a persona by ref and saved it by name, so
// with two personas sharing a name it could write over the wrong one.
//
// `name` is still accepted wherever it used to be, so a client that predates
// this keeps working; personaQuery is the single place that prefers one.
func personaQuery(ref, name string) string {
	if r := strings.TrimSpace(ref); r != "" {
		return r
	}
	return strings.TrimSpace(name)
}

// PersonaDeleteParams names the persona to remove from the user library.
type PersonaDeleteParams struct {
	Ref string `json:"ref,omitempty"`
	// Name is the pre-ref spelling of Ref. Deprecated; still honoured.
	Name string `json:"name,omitempty"`
}

// Query is the persona this call names.
func (p PersonaDeleteParams) Query() string { return personaQuery(p.Ref, p.Name) }

// PersonaGetParams names a persona to read.
type PersonaGetParams struct {
	Ref string `json:"ref,omitempty"`
	// Name is the pre-ref spelling of Ref. Deprecated; still honoured.
	Name string `json:"name,omitempty"`
}

// Query is the persona this call names.
func (p PersonaGetParams) Query() string { return personaQuery(p.Ref, p.Name) }

// PersonaWriteParams is the editable persona form. Name is required; Charter is
// the behavioral body. Immersive makes the charter own the whole system prompt
// (roleplay) rather than layering on terva's identity.
type PersonaWriteParams struct {
	Name              string   `json:"name"`
	Pronunciation     string   `json:"pronunciation,omitempty"`
	Specialty         string   `json:"specialty,omitempty"`
	Summary           string   `json:"summary,omitempty"`
	Emoji             string   `json:"emoji,omitempty"`
	AccentColor       string   `json:"accent_color,omitempty"`
	Group             string   `json:"group,omitempty"`
	RecommendedSkills []string `json:"recommended_skills,omitempty"`
	GoodFor           []string `json:"good_for,omitempty"`
	AvoidFor          []string `json:"avoid_for,omitempty"`
	Immersive         bool     `json:"immersive,omitempty"`
	Introduction      string   `json:"introduction,omitempty"`
	Charter           string   `json:"charter,omitempty"`
	// Extends names a persona whose charter this one builds on. A write
	// REPLACES the whole file, so a client that renders a persona and saves it
	// back must round-trip this field or it silently deletes the inheritance —
	// the same class of quiet loss `extends` exists to fix.
	Extends string `json:"extends,omitempty"`
}

// PersonaEditParams is a write plus the persona it OVERWRITES.
//
// Split from PersonaWriteParams rather than being one more field on it, because
// Ref is not part of the persona: everything in a write params is content that
// must survive the file round trip, and a workspace test enrolls those fields
// and requires exactly that. A ref would have had to be argued out of it.
//
// The split says the same thing in the type system, and says one thing more —
// a create has nothing to overwrite, so it cannot be handed a ref to obey by
// mistake. That is why personas.create keeps taking the bare write params.
//
// Ref is identity and Name is content, which is the distinction the single
// `name` field could not express: an edit that changes the name still targets
// the persona Ref resolves to. An empty Ref falls back to Name, so a client
// written before this keeps working.
type PersonaEditParams struct {
	Ref string `json:"ref,omitempty"`
	PersonaWriteParams
}

// Target is the persona this edit overwrites.
func (p PersonaEditParams) Target() string { return personaQuery(p.Ref, p.Name) }

// PersonasListResult is the payload of personas.list.
type PersonasListResult struct {
	Personas []PersonaSummary `json:"personas"`
}

// PersonaSummary is the roster entry: identity + provenance. Origin is
// "built-in" | "extension" | "user"; Editable is false for built-in/extension
// (those copy-to-edit into the user library).
type PersonaSummary struct {
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	Namespace   string `json:"namespace,omitempty"`
	Specialty   string `json:"specialty,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	AccentColor string `json:"accent_color,omitempty"`
	// Group is the shelf a roster files this persona under — its declared
	// `group`, else the namespace it came from. Purely organisational: it is
	// not part of Ref, so a client must never key anything on it.
	Group     string `json:"group,omitempty"`
	Immersive bool   `json:"immersive,omitempty"`
	Origin    string `json:"origin"`
	Editable  bool   `json:"editable,omitempty"`
}

// PersonaView is one persona in full: its summary plus the fields an editor
// renders (charter, pronunciation, the good-for/avoid-for guidance).
type PersonaView struct {
	PersonaSummary
	Pronunciation     string   `json:"pronunciation,omitempty"`
	RecommendedSkills []string `json:"recommended_skills,omitempty"`
	GoodFor           []string `json:"good_for,omitempty"`
	AvoidFor          []string `json:"avoid_for,omitempty"`
	Introduction      string   `json:"introduction,omitempty"`
	Charter           string   `json:"charter,omitempty"`
	Extends           string   `json:"extends,omitempty"`
	// SessionsUsing counts the sessions — across every project, not just this
	// one — that were CREATED with this persona and replay it on every
	// materialize. It answers the question a delete raises: how much else does
	// this name hold up? Those sessions stay openable if the persona goes (the
	// build falls back to the default and says so), but they lose the voice they
	// were written in, so a client offering to delete should say how many.
	//
	// Reported by PersonasGet only. Deriving it reads every project's
	// transcripts, which is right for a question asked once before a delete and
	// wrong for one asked on every library open — so PersonasList omits it.
	SessionsUsing int `json:"sessions_using,omitempty"`
}
