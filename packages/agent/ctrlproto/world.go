package ctrlproto

import (
	"context"
	"encoding/json"
)

// The World on the wire (the Worlds proposal, W2): session-scoped shared lore —
// authored keyed-context entries every character on stage sees (L1). Like the
// author's note, entries ride the UNCACHED per-turn tail (see
// build.WorldLoreRecord), so an edit takes effect next turn with no cache bust.
// Session-scoped (the session rides the frame) and served by an OPTIONAL
// controller, so the verbs do not ripple to every WorkspaceService implementer.
// State reads ride SessionInfo.WorldLore — there is no list verb, matching how
// Note and Cast travel.
type WorldController interface {
	// WorldLorePut adds or updates one World lore entry. Upserts by
	// p.Entry.Name; a rename passes the old name as p.Replace. Only meaningful
	// for an immersive (chat/play) session.
	WorldLorePut(ctx context.Context, sess string, p WorldLorePutParams) error
	// WorldLoreDelete removes the World lore entry named p.Name.
	WorldLoreDelete(ctx context.Context, sess string, p WorldLoreDeleteParams) error
	// WorldSet updates the World's settings — today, coordination (the W3
	// meta-narrator): "" auto, "off", or "focus:<roster name>". Takes effect on
	// the next turn.
	WorldSet(ctx context.Context, sess string, p WorldSetParams) error
	// WorldsList lists the saved Worlds (W5), with each World's member-session
	// count resolved so a shelf renders without N extra calls.
	WorldsList(ctx context.Context) (WorldsListResult, error)
	// WorldSave promotes or updates (W5): it lifts sess's live World state —
	// roster, pins, lore, coordination — into the saved-Worlds library. A
	// session not yet in a World creates one (p.Name required) and is stamped a
	// member; a member session updates its World in place (last-wins; p.Name
	// optionally renames). Explicit save-back, never live sync.
	WorldSave(ctx context.Context, sess string, p WorldSaveParams) (WorldView, error)
	// WorldDelete removes a saved World. Member sessions keep their embedded
	// copies and lose only the grouping.
	WorldDelete(ctx context.Context, p WorldDeleteParams) error
	// WorldUpdate edits a saved World's metadata — name, description, cover —
	// without a session (the Library sheet's verbs). The id never changes, so
	// member-session grouping survives a rename.
	WorldUpdate(ctx context.Context, p WorldUpdateParams) (WorldView, error)
	// WorldSetCharacterModel sets (or clears) one roster character's World-scoped
	// default model — the pin a new session in this World seeds its cast from.
	// Sessionless, like WorldUpdate. Empty Provider AND Model clears the pin (the
	// character inherits again); p.Character must be on the roster.
	WorldSetCharacterModel(ctx context.Context, p WorldSetCharacterModelParams) (WorldView, error)
	// WorldSetModel sets (or clears) the World's OWN default model — the middle
	// rung of the Card → World → Workspace ladder models.default_for resolves.
	// Distinct from WorldSetCharacterModel: that pins one actor's voice, this
	// says what any scene, doctor run, or unpinned character in this World falls
	// back to. Empty Provider AND Model clears it (the workspace default shows
	// through again).
	WorldSetModel(ctx context.Context, p WorldSetModelParams) (WorldView, error)
	// WorldsLorePut adds or updates one lore entry on a SAVED World — the
	// sessionless twin of WorldLorePut, upserting by p.Entry.Name with p.Replace
	// naming the entry a rename supersedes. Both share one upsert rule
	// (workspace.putWorldLore), so a saved book and a session's book cannot drift
	// apart on what a put means.
	WorldsLorePut(ctx context.Context, p WorldsLorePutParams) (WorldView, error)
	// WorldsLoreDelete removes a lore entry from a saved World.
	WorldsLoreDelete(ctx context.Context, p WorldsLoreDeleteParams) (WorldView, error)
	// WorldsSet updates a saved World's coordination mode (W3) — the seed a new
	// session in this World starts from, validated against the saved roster.
	WorldsSet(ctx context.Context, p WorldsSetParams) (WorldView, error)
	// WorldsAddCharacter puts a character on a saved World's roster: the
	// sessionless cast.add. p.Ref must resolve in the card library, because a
	// roster entry that resolves to nothing is a scene that cannot be cast.
	WorldsAddCharacter(ctx context.Context, p WorldsAddCharacterParams) (WorldView, error)
	// WorldsRemoveCharacter takes a character off the roster, clearing their
	// model pin with them — the pin is keyed by roster name, so leaving it would
	// silently re-apply to a different character who later took the name.
	WorldsRemoveCharacter(ctx context.Context, p WorldsRemoveCharacterParams) (WorldView, error)
	// WorldsEditCharacter edits a roster character's card WITHOUT the change
	// escaping this World.
	//
	// It exists because a roster holds a plain card ref and cards.edit rewrites a
	// card in place, so one library card is shared by every World, every session,
	// and the shelf. Editing a character here through cards.edit would rewrite
	// the character every other World is still playing. Instead the card is
	// FORKED (CardStore.Fork) and the roster re-pointed at the fork, so the
	// original is never opened for writing.
	//
	// AlsoLibrary opts into the old behaviour explicitly, for the case the fork
	// is there to protect against: a fix that genuinely belongs to the character
	// everywhere, not to this World's take on them.
	WorldsEditCharacter(ctx context.Context, p WorldsEditCharacterParams) (WorldsEditCharacterResult, error)
	// WorldsCreateCharacter imports a NEW card and rosters it into this World in
	// one operation, recording that the character was born here.
	//
	// One verb rather than the cards.import + worlds.add_character pair the
	// world doctor used to send, for two reasons. The pair can half-apply — the
	// import lands, the roster call fails, and the author is left with a
	// character in their library that no World asked for and nothing explains.
	// And provenance written by the CLIENT is provenance a client can forget:
	// the origin record has to be written where the card is created, or an
	// unbadged card is one missed call away.
	//
	// Distinct from WorldsAddCharacter, which puts an EXISTING library card on
	// the roster and deliberately claims no provenance — borrowing a character
	// into a World is not the same as the World inventing them.
	WorldsCreateCharacter(ctx context.Context, p WorldsCreateCharacterParams) (WorldsCreateCharacterResult, error)
	// WorldsExport serializes a saved World for download (W5b): a single JSON
	// bundle carrying the WorldDoc, every roster character's card (each in its
	// ordinary export form), and the cover image.
	WorldsExport(ctx context.Context, p WorldExportParams) (WorldExport, error)
	// WorldsImport ingests a World bundle: each embedded card lands in the
	// card library (idempotent by content), the roster is remapped to the ids
	// this library assigned, and a FRESH World id is minted — a bundle never
	// collides with a local World.
	WorldsImport(ctx context.Context, p WorldImportParams) (WorldView, error)
}

// WorldLoreEntry is one World lore entry on the wire — the minimal authoring
// surface (name + trigger keywords + always-on + content + audience) a
// steering drawer edits. Mirrors core.WorldLoreEntry.
type WorldLoreEntry struct {
	Name string `json:"name"`
	// Keys are the trigger keywords: the entry injects when one appears in
	// recent messages. Ignored when Constant — a constant entry injects every
	// turn. An entry needs Keys or Constant, else it could never activate.
	Keys     []string `json:"keys,omitempty"`
	Constant bool     `json:"constant,omitempty"`
	Content  string   `json:"content"`
	// Audience names the characters who know this entry (L2): empty = everyone
	// on stage. A named character's generation only sees entries they are
	// cleared for; the narrator/director always sees everything.
	Audience []string `json:"audience,omitempty"`
	// Model marks an entry the model authored (the play director's world_note,
	// W4b) — the UI badges it 📝. Read-only on the wire: a user edit through
	// world.lore.put takes ownership and clears it.
	Model bool `json:"model,omitempty"`
	// Learned is the learned-when ledger (L3): character → RFC 3339 moment they
	// learned this entry via world_reveal. Read-only on the wire; preserved
	// through user edits.
	Learned map[string]string `json:"learned,omitempty"`
}

// WorldLorePutParams carries one entry to add or update. Replace names the
// entry this one supersedes (a rename edits in place); empty means upsert by
// Entry.Name.
type WorldLorePutParams struct {
	Entry   WorldLoreEntry `json:"entry"`
	Replace string         `json:"replace,omitempty"`
}

// WorldLoreDeleteParams names the entry to remove.
type WorldLoreDeleteParams struct {
	Name string `json:"name"`
}

// WorldSetParams carries the World's settings. Coordination selects who
// answers a normal turn in a chat World with a roster: "" (auto — the
// meta-narrator picks), "off" (the bound character always answers), or
// "focus:<roster name>" (that character always answers).
type WorldSetParams struct {
	Coordination string `json:"coordination"`
}

// WorldView is one saved World on the wire (W5).
type WorldView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Characters is the roster (name → card ref); Lore the World's lorebook.
	Characters map[string]string `json:"characters,omitempty"`
	// CharacterModels is the per-character default model (name → provider+model),
	// the World-scoped model pin a new session in this World seeds its cast from
	// (workspace.go's create path passes it to SetCast). Empty for a character =
	// that actor inherits the session/host model. Edited via
	// worlds.set_character_model.
	CharacterModels map[string]CastRoute `json:"character_models,omitempty"`
	// Model is the World's own default model — what a scene started here, a
	// doctor run against it, and any character without a pin of their own fall
	// back to. Empty means the World states no preference; ask
	// models.default_for to see what actually shows through. Edited via
	// worlds.set_model.
	Model CastRoute        `json:"model,omitempty"`
	Lore  []WorldLoreEntry `json:"lore,omitempty"`
	// Coordination is the World's saved meta-narrator mode (W3): "" (auto),
	// "off", or "focus:<roster name>" — the seed a new session in this World
	// starts from. Carried on the view because worlds.set writes it and a
	// surface that could not read it back would be steering blind.
	Coordination string `json:"coordination,omitempty"`
	// Sessions is how many sessions belong to this World (SessionMeta.World).
	Sessions int    `json:"sessions,omitempty"`
	Created  string `json:"created,omitempty"` // RFC 3339
	Updated  string `json:"updated,omitempty"`
	// CoverURL is the media route for the World's cover image ("" when it has
	// none) — same contract as CardSummary.AvatarURL.
	CoverURL string `json:"cover_url,omitempty"`
}

// WorldsListResult is the payload of worlds.list.
type WorldsListResult struct {
	Worlds []WorldView `json:"worlds"`
}

// WorldSaveParams drives worlds.save: Name names a NEW World (required on
// first save; optional rename after), Description is optional flavor. The
// session rides the frame.
type WorldSaveParams struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// WorldDeleteParams names the saved World to remove.
type WorldDeleteParams struct {
	ID string `json:"id"`
}

// WorldUpdateParams edits a saved World's metadata. Name "" keeps the current
// name (a World always has one); Description is applied VERBATIM — the sheet
// editing it holds the full WorldView, so it sends the current text back when
// leaving it unchanged (and "" clears it). Cover sets a new cover image (PNG
// bytes, base64 on the wire); RemoveCover deletes the current one. Setting
// both is a bad request.
type WorldUpdateParams struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description"`
	Cover       []byte `json:"cover,omitempty"`
	RemoveCover bool   `json:"remove_cover,omitempty"`
}

// WorldSetCharacterModelParams pins (or clears) one roster character's
// World-scoped default model. Empty Provider AND Model clears the pin.
type WorldSetCharacterModelParams struct {
	ID        string `json:"id"`
	Character string `json:"character"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

// WorldSetModelParams sets (or clears) the World's own default model. Empty
// Provider AND Model clears it. Provider is accepted alongside Model for the
// same reason CardModelSetParams takes one: a bare model id can exist under
// several providers (subscription vs api key) and the unqualified lookup may
// land on one this workspace holds no credential for.
type WorldSetModelParams struct {
	ID       string `json:"id"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// The sessionless World-content params (WS-1). Every one carries the saved
// World's id where the session-scoped twin takes the session from the frame —
// that is the whole difference between the `world.*` family and this one. They
// all answer with the stored WorldView so a client re-renders from the write's
// own result instead of re-listing.

// WorldsLorePutParams carries one entry to add or update on a saved World.
// Replace names the entry this one supersedes (a rename edits in place); empty
// means upsert by Entry.Name — identical to WorldLorePutParams but for the id.
type WorldsLorePutParams struct {
	ID      string         `json:"id"`
	Entry   WorldLoreEntry `json:"entry"`
	Replace string         `json:"replace,omitempty"`
}

// WorldsLoreDeleteParams names the saved World's entry to remove.
type WorldsLoreDeleteParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WorldsSetParams carries a saved World's settings. Coordination takes the same
// three shapes as the session-scoped world.set: "" (auto), "off", or
// "focus:<roster name>".
type WorldsSetParams struct {
	ID           string `json:"id"`
	Coordination string `json:"coordination"`
}

// WorldsAddCharacterParams puts Name on the roster pointing at Ref (a card
// library id). Adding a name already on the roster re-points it, which is how
// a swap is spelled.
type WorldsAddCharacterParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// WorldsRemoveCharacterParams names the roster character to drop.
type WorldsRemoveCharacterParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WorldsEditCharacterParams edits one roster character's card, World-scoped.
// Card is a full edited card document, the same shape cards.edit takes (a CCv2
// {spec,data} wrapper or a flat object).
type WorldsEditCharacterParams struct {
	ID        string `json:"id"`
	Character string `json:"character"`
	// Card is the edited document. Sending the card unchanged is a no-op that
	// returns the same card id — content-addressing makes that free, so a
	// speculative apply never litters the library with a twin.
	Card json.RawMessage `json:"card"`
	// AlsoLibrary additionally writes the edit onto the ORIGINAL library card,
	// so the change reaches every World and session using it. Off by default:
	// the default has to be the safe one, because the surface driving this is a
	// doctor proposing edits to characters the author may be playing elsewhere.
	AlsoLibrary bool `json:"also_library,omitempty"`
}

// WorldsEditCharacterResult reports where the edit landed. CardID is the roster's
// ref AFTER the edit — a new id when the edit forked, the same id when it changed
// nothing. Forked says which happened, so a client can tell the author their
// World now has its own copy rather than leaving them to infer it from an id.
type WorldsEditCharacterResult struct {
	World  WorldView `json:"world"`
	CardID string    `json:"card_id"`
	Forked bool      `json:"forked"`
}

// WorldsCreateCharacterParams imports Card and rosters it under Name. Card is a
// full card document, the same shape cards.import takes.
type WorldsCreateCharacterParams struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Card json.RawMessage `json:"card"`
}

// WorldsCreateCharacterResult answers with the stored World and the id the new
// card landed on, so the caller renders from the write's own result.
type WorldsCreateCharacterResult struct {
	World  WorldView `json:"world"`
	CardID string    `json:"card_id"`
}

// WorldExportParams names the saved World to bundle.
type WorldExportParams struct {
	ID string `json:"id"`
}

// WorldExport is a World serialized for download — the same download shape as
// CardExport (filename, MIME, base64 bytes).
type WorldExport struct {
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Bytes    []byte `json:"bytes"`
}

// WorldImportParams carries a World bundle to import: Bytes (an upload) wins
// over Path (a file on the daemon's disk); one must be present.
type WorldImportParams struct {
	Bytes []byte `json:"bytes,omitempty"`
	Path  string `json:"path,omitempty"`
}
