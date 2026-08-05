package ctrlproto

import (
	"context"
	"encoding/json"
	"time"
)

// The character-card library on the wire. Cards were path-only before Stage;
// this brings the store (packages/agent/build.CardStore) into the control plane
// so any controller — the Stage app, the panel, the TUI — inspects, imports,
// edits, and deletes the same set. A card is data: cards.edit never changes what
// a card can DO (the authority model is untouched), so these verbs are ungated
// beyond the workspace's own auth — unlike personas.*, which are trusted-tier.
//
// CardsController is OPTIONAL, like ModelParamsController: a carrier with no
// library (a replay session, a test fake) simply does not implement it and the
// verb answers "unsupported" rather than failing deeper. Only the real Workspace
// and the ctrlclient forwarder serve it, so adding these verbs does NOT ripple
// to every WorkspaceService implementer.
type CardsController interface {
	// CardsList returns a summary of every stored card.
	CardsList(ctx context.Context) (CardsListResult, error)
	// CardsGet returns one card in full, including its raw JSON for round-trip.
	CardsGet(ctx context.Context, p CardGetParams) (CardView, error)
	// CardsImport ingests a PNG or JSON card (by server-local path, raw bytes, or
	// a remote URL) into the library, keeping a PNG's pixels as the avatar.
	// Idempotent by content: re-importing the same card returns the same id.
	CardsImport(ctx context.Context, p CardImportParams) (CardView, error)
	// CardsDuplicate copies a stored card under a new name, portrait included,
	// as a card of its own. The rename is required and is what makes the copy a
	// separate card at all: ids are content-addressed, so a name that would land
	// back on the original — or on some other card already holding these exact
	// contents — is refused rather than silently merged into it.
	CardsDuplicate(ctx context.Context, p CardDuplicateParams) (CardView, error)
	// CardsEdit replaces a stored card's fields with an edited document,
	// re-serializing it; the id and avatar are unchanged.
	CardsEdit(ctx context.Context, p CardEditParams) (CardView, error)
	// CardsDelete removes a card and its avatar from the library.
	CardsDelete(ctx context.Context, p CardDeleteParams) error
	// CardsExport serializes a stored card for download: a CCv2 PNG (the current
	// card JSON embedded in the retained avatar) when it has one, else the CCv2
	// JSON. The bytes ride the wire base64-encoded.
	CardsExport(ctx context.Context, p CardExportParams) (CardExport, error)
	// CardsLint runs the deterministic card lint over a stored card — a static,
	// model-free pass (malformed macros, oversized fields, missing greeting, …)
	// that the Stage card doctor reads first to orient. Read-only.
	CardsLint(ctx context.Context, p CardLintParams) (CardLintResult, error)
	// CardFavorite sets or clears a card's favorite flag (highlight + pin to top).
	// A per-library preference; it never touches the card JSON.
	CardFavorite(ctx context.Context, p CardFavoriteParams) error
	// CardsHistory lists a card's retained earlier revisions, newest first.
	CardsHistory(ctx context.Context, p CardHistoryParams) (CardHistoryResult, error)
	// CardsRestore puts a retained revision back. It is an ordinary edit under
	// the skin, so the state it replaces is itself recorded and a restore can be
	// undone in turn.
	CardsRestore(ctx context.Context, p CardRestoreParams) (CardView, error)
	// CardsRevision reads one retained revision in full, for comparing against
	// the card as it stands. Read-only: it changes nothing.
	CardsRevision(ctx context.Context, p CardRevisionParams) (CardRevisionView, error)
}

// Card revision history. Every write to a card goes through cards.edit — the
// editor, the doctor's apply, enrich — and the store snapshots the OUTGOING
// card there, so a pass that rewrote fields you never typed is recoverable
// without the client having to remember to ask.
//
// A card keeps a bounded number of revisions: its original plus its most recent
// changes. Restore records what it replaced, so trying one is safe.
//
// Two reads, deliberately split. cards.history is the LIST and stays small: it
// carries which fields each revision differs in, but not their contents, so a
// card with a large lorebook does not ship ten copies of it to render a list.
// cards.revision fetches ONE revision in full, for the comparison a user asked
// to see.

// CardHistoryParams names the card whose revisions to list.
type CardHistoryParams struct {
	ID string `json:"id"`
}

// CardRevision is one retained earlier revision. Ref is opaque — the only thing
// a client does with it is hand it back to cards.restore or cards.revision.
// Name is the card's name AT that revision, so a list still reads correctly
// after a rename.
type CardRevision struct {
	Ref   string    `json:"ref"`
	Saved time.Time `json:"saved"`
	Bytes int       `json:"bytes"`
	Name  string    `json:"name,omitempty"`
	// Fields are the CCv2 field names this revision differs from the card as it
	// stands in — what restoring it would change. Computed against the SAVED
	// card, so an editor holding unsaved edits is comparing against the store,
	// not against its own draft. Empty means the revision matches the card.
	Fields []string `json:"fields,omitempty"`
	// Portrait is whether restoring this revision would also change the card's
	// picture. The portrait lives outside the card document, so it is reported
	// separately — without it a revision that replaced ONLY the image would read
	// as identical to the card it plainly differs from.
	Portrait bool `json:"portrait,omitempty"`
}

// CardRevisionParams names one revision to read in full.
type CardRevisionParams struct {
	ID  string `json:"id"`
	Ref string `json:"ref"`
}

// CardRevisionView is one revision's stored card document, with the same
// metadata its list entry carried. Raw is the normalized card JSON, in the
// shape cards.get returns, so a client diffs two documents of one shape.
type CardRevisionView struct {
	Ref      string          `json:"ref"`
	Saved    time.Time       `json:"saved"`
	Name     string          `json:"name,omitempty"`
	Fields   []string        `json:"fields,omitempty"`
	Portrait bool            `json:"portrait,omitempty"`
	Raw      json.RawMessage `json:"raw"`
}

// CardHistoryResult is the payload of cards.history, newest first. A card that
// has never been edited has an empty list, which is a normal state.
type CardHistoryResult struct {
	Revisions []CardRevision `json:"revisions"`
}

// CardRestoreParams names the card and the revision to put back.
type CardRestoreParams struct {
	ID  string `json:"id"`
	Ref string `json:"ref"`
}

// CardFavoriteParams names the card and the desired favorite state.
type CardFavoriteParams struct {
	ID       string `json:"id"`
	Favorite bool   `json:"favorite"`
}

// CardLintParams names the card to lint by its library id.
type CardLintParams struct {
	ID string `json:"id"`
}

// CardLintFinding is one deterministic lint result (mirrors card.Finding).
// Severity is "warn" (a real problem) or "info" (a fact worth surfacing); Field
// names the offending card field and Detail carries the offending snippet.
type CardLintFinding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
}

// CardLintResult is the payload of cards.lint.
type CardLintResult struct {
	Findings []CardLintFinding `json:"findings"`
}

// CardGetParams / CardDeleteParams name a card by its library id.
type CardGetParams struct {
	ID string `json:"id"`
}

// CardDeleteParams names the card to remove.
type CardDeleteParams struct {
	ID string `json:"id"`
}

// CardImportParams carries a card to import. Exactly one source is used, in
// precedence order: Bytes (raw file bytes, base64 on the wire — how a browser
// uploads) over Path (a server-local file — how the TUI/CLI imports something
// on the daemon's disk) over URL (a remote card, e.g. a chub.ai download link —
// fetched through the SSRF-guarded egress client, so it cannot reach the
// daemon's loopback/private network).
type CardImportParams struct {
	Path  string `json:"path,omitempty"`
	Bytes []byte `json:"bytes,omitempty"`
	URL   string `json:"url,omitempty"`
}

// CardDuplicateParams names the card to copy and what to call the copy. Name is
// REQUIRED and must differ from the original's, because an id is a stem of the
// name plus a hash of the contents — an unchanged name over unchanged contents
// is not a second card, it is the first one, and copying onto it would report
// success while creating nothing.
//
// The caller picks the free name (the Stage sheet proposes "<name> (copy)" and
// counts upward), so this carries a name rather than deriving one: the client
// holds the library listing the name has to be free WITHIN, and it is the client
// that has to show the author what the copy will be called before they commit.
type CardDuplicateParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CardEditParams replaces a card's data. Card is a full edited card document
// (CCv2 {spec,data} wrapper or a flat object); the server validates and
// re-serializes it, round-tripping any `extensions` the editor left untouched.
type CardEditParams struct {
	ID   string          `json:"id"`
	Card json.RawMessage `json:"card"`
}

// CardExportParams names the card to export and, optionally, the format: "png"
// (a CCv2 PNG — requires a retained avatar), "json" (the CCv2 document), or ""
// to auto-pick PNG when the card has an avatar, else JSON.
type CardExportParams struct {
	ID     string `json:"id"`
	Format string `json:"format,omitempty"`
}

// CardExport is a card serialized for download: the suggested filename, its MIME
// type, and the file bytes (base64 on the wire).
type CardExport struct {
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Bytes    []byte `json:"bytes"`
}

// CardsListResult is the payload of cards.list.
type CardsListResult struct {
	Cards []CardSummary `json:"cards"`
}

// CardSummary is the at-a-glance library entry: identity plus the inventory a
// grid needs (how many greetings, how big a lorebook, whether it carries
// post-history instructions) without shipping the whole card.
type CardSummary struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Creator          string   `json:"creator,omitempty"`
	CharacterVersion string   `json:"character_version,omitempty"`
	SpecVersion      string   `json:"spec_version,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	// AvatarURL is the media route for the card's portrait, or "" if it has none.
	AvatarURL string `json:"avatar_url,omitempty"`
	// Greetings counts the openings a session could seed (first_mes plus
	// alternate_greetings); BookEntries counts the embedded lorebook; HasPHI is
	// whether post_history_instructions is set.
	Greetings   int  `json:"greetings"`
	BookEntries int  `json:"book_entries,omitempty"`
	HasPHI      bool `json:"has_phi,omitempty"`
	// Added is when the card entered the library (its directory mtime) — the sort
	// key for "recently added". Zero/omitted if unknown.
	Added time.Time `json:"added,omitempty"`
	// Favorite is whether the card is favorited: the client highlights it and
	// pins it to the top of the library. A per-library preference toggled by
	// cards.favorite, never part of the card JSON.
	Favorite bool `json:"favorite,omitempty"`
}

// CardView is one card in full: its summary plus the normalized card JSON, so a
// detail/edit screen renders every field and round-trips extensions it doesn't.
type CardView struct {
	CardSummary
	Raw json.RawMessage `json:"raw"`
	// Warnings are non-fatal notes from an import — a portrait that was
	// downscaled or dropped. The card imported fine; these say what was done to
	// it. Empty on a read (cards.get), populated only by cards.import.
	Warnings []string `json:"warnings,omitempty"`
}
