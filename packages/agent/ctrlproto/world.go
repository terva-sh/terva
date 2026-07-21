package ctrlproto

import "context"

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
	Lore            []WorldLoreEntry     `json:"lore,omitempty"`
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
