package core

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// Session is a JSONL-backed conversation transcript tied to a cwd.
type Session struct {
	ID     string
	Path   string
	Meta   SessionMeta
	writer *os.File
	buf    *bufio.Writer
	// writeMu serializes every durable write (writeLine) and the messagesAppended
	// counter. A session's transcript can have concurrent writers — e.g. a web
	// client's clear/compact writing a checkpoint while a turn on another
	// connection is persisting messages — and the bufio.Writer is not
	// goroutine-safe, so unsynchronized writers would interleave bytes / corrupt
	// the JSONL. All Append*/Update*/writeLine paths take this lock.
	writeMu sync.Mutex

	// freshFile is true when the file was created by NewSession (this
	// process owns it) and false when OpenSession reopened an existing
	// transcript. Used by Close() to delete the file if the run never
	// appended any messages — prevents a flood of empty session files
	// from sessions the user opens then exits without prompting.
	freshFile bool

	// messagesAppended counts AppendMessage calls. Combined with
	// freshFile it tells Close() whether the session left any content
	// worth keeping.
	messagesAppended int

	// persistedLore is the World lorebook as the FILE currently reads it — the
	// fold of every lore row and pre-v4 meta row seen on load, plus whatever this
	// process has written since. SetWorldLore diffs the incoming book against it
	// to write only what changed, so it must track the disk rather than the live
	// Meta.WorldLore (which the caller has already replaced by the time it lands
	// here). Guarded by writeMu with the writes it describes.
	persistedLore []WorldLoreEntry

	// pendingGreeting holds a deferred, re-derivable opening set (a Stage card's
	// first_mes + alternate_greetings) seeded into the LIVE transcript but not yet
	// written to disk, so a chat the user only previews stays a meta-only draft the
	// prune gates discard. Non-nil ⟺ the greeting is deferred; the first durable
	// append flushes it (flushPendingGreeting) before its own row, so the persisted
	// transcript is greeting-then-content, identical to a seed-at-build. Guarded by
	// pgMu — a distinct lock, because the flush calls SeedGreetingVariants, which
	// takes writeMu, so it must not itself hold writeMu.
	pendingGreeting *pendingGreeting
	pgMu            sync.Mutex

	// errMu guards the lazily-opened error sidecar (errFile). Provider/turn
	// failures are recorded in a SEPARATE file alongside the transcript, never
	// in the transcript itself — the .jsonl has a fixed record vocabulary that
	// replay, resume, and compaction depend on, and an error row would be noise
	// there. Its own mutex (not writeMu) because it writes a different file and
	// is called off the turn goroutine, independent of transcript writes.
	errMu   sync.Mutex
	errFile *os.File

	// LoadWarnings describes everything OpenSession had to skip or
	// guess at while reading the file (corrupt rows, unknown block
	// types, a newer format version). Empty for clean loads. Callers
	// decide how to surface it; the data is never silently dropped.
	LoadWarnings []string

	// LoadStats records what reconstructing this session's transcript cost —
	// the fold's wall time and how much revision machinery it replayed. It is
	// cheap insurance against variant/amend accumulation becoming a silent
	// load-time tax (docs/proposals/stage-inline-editing.md §9): captured on
	// every open, surfaced by callers (the workspace logs it via diag when a
	// session carries amends), and available to a future debug surface.
	LoadStats LoadStats

	// ActiveToolGroups are the capability groups this session activated, in
	// first-activation order (from its tool_group rows). A resume re-marks them
	// on the agent — see Agent.RestoreActiveGroups — so the tools array it
	// advertises matches what the provider cached the transcript behind. Empty
	// for a session that never activated a group, and for every session written
	// before tool_group rows existed: those resume as they always did.
	ActiveToolGroups []string

	// TitleGenerated reports whether the title OpenSession loaded (the last
	// rename row, reflected into Meta.Title) was machine-generated
	// (RenameSessionGenerated) rather than a user rename. Provenance decides
	// whether automatic re-titling may replace the title — a manual rename
	// is never clobbered. Meta-line titles and legacy source-less rename
	// rows count as manual: the conservative reading.
	TitleGenerated bool
}

// sessionFormatVersion is the version of the on-disk session schema
// THIS build writes. History:
//
//	1 (implicit, format_version absent) — content blocks carry no
//	  type discriminator; readers classify by field presence.
//	2 — every content block is written with an explicit "type"
//	  ("text", "image", "tool_call", "tool_result", "reasoning").
//	  v1 files keep loading through the field-presence fallback.
//	3 — the file carries `amend` rows (transcript revision: edit,
//	  delete, truncate, retract/select variants). A v2 loader skips
//	  unknown row types SILENTLY, so it would present the un-revised
//	  transcript as if nothing happened — hence a session only declares
//	  v3 once it actually holds an amend (bumpFormatForAmend), and a
//	  pre-amend build then warns instead of misleading.
//	4 — the World lorebook rides `lore` rows (see recordLore) instead of
//	  the meta row's world_lore, and a meta row at this version carries
//	  no lore authority. Same hazard as v3, sharper: an older loader
//	  skips the lore rows and reads the last meta row's absent book as
//	  an EMPTY one, presenting a session whose secrets and scene state
//	  are gone. Declared only once a session actually holds a lore row,
//	  so a coding session — which never has a book — never claims it.
//
// A fresh session is stamped sessionFormatVersion; the amend and lore bumps
// lift it as those rows first appear. This build READS up to
// sessionFormatVersionLore without warning; a file declaring more warns
// (Session.LoadWarnings) and loads best-effort.
const (
	sessionFormatVersion      = 2
	sessionFormatVersionAmend = 3
	sessionFormatVersionLore  = 4
)

// SessionMeta is written as the first line of every session file.
type SessionMeta struct {
	ID       string    `json:"id"`
	CWD      string    `json:"cwd"`
	Model    string    `json:"model"`
	Provider string    `json:"provider"`
	Started  time.Time `json:"started"`
	// Version is the app version that created the session —
	// informational only. FormatVersion is the schema contract.
	Version string `json:"version"`
	// FormatVersion is the session-schema version (sessionFormatVersion
	// at write time). 0 means a legacy v1 file.
	FormatVersion int    `json:"format_version,omitempty"`
	Title         string `json:"title,omitempty"`

	// Parent is the ID of the session this one was forked from, or
	// empty for top-level sessions. The tree picker walks parents
	// upward and sibling files (same cwd dir, same parent ID)
	// laterally to render the branch topology.
	Parent string `json:"parent,omitempty"`
	// ForkPoint is the 0-indexed message position within the parent
	// transcript where this branch diverges. Messages 0..ForkPoint-1
	// are copied from the parent verbatim; the user's next turn on
	// the child session continues from there.
	ForkPoint int `json:"fork_point,omitempty"`

	// Persona is the per-session persona/agent name chosen at creation. It is
	// persisted so a daemon restart re-materializes the session as the chosen
	// persona rather than falling back to the workspace default.
	Persona string `json:"persona,omitempty"`
	// Reasoning is this session's thinking-level override as the user typed it
	// ("off", "high", "max", …), or "" to inherit the global level. Raw rather
	// than normalized so an explicit "off" stays distinguishable from an absent
	// override, which normalization would collapse into the same empty string.
	//
	// Persisted for the reason Persona is: a session deliberately set to a
	// different depth must come back at that depth after a daemon restart, not
	// silently drop to whatever the global default happens to be.
	Reasoning string `json:"reasoning,omitempty"`
	// Experience, Card, Cast, and Greeting persist how an immersive (Stage)
	// session was created — its --chat/--play mode, character card, declared
	// cast, and selected opening — so a restart rebuilds the same session rather
	// than the workspace default. Empty/zero for ordinary coding sessions.
	Experience string            `json:"experience,omitempty"` // "chat" | "play"
	Card       string            `json:"card,omitempty"`       // card ref (library id or path)
	Cast       map[string]string `json:"cast,omitempty"`       // actor name -> persona|card ref
	// CastModels pins specific cast members to a provider+model (Phase 7); an actor
	// with no entry inherits the session/host route. Parallel to Cast (keyed by the
	// same actor name) so the ref map stays a plain name->ref.
	CastModels map[string]CastRoute `json:"cast_models,omitempty"`
	Greeting   int                  `json:"greeting,omitempty"` // selected greeting index
	// Background is the scene backdrop bound to this session (a backgrounds-library
	// id, served over /media/backgrounds/<id>). Unlike the creation spec above it
	// is mutable mid-session (SetBackground), so it is presentation metadata the
	// client renders, not a build input.
	Background string `json:"background,omitempty"`
	// Note is the session's author's note — a live steering instruction injected
	// into the UNCACHED per-turn tail (after a card's post_history_instructions),
	// never the cached prefix. Mutable mid-session (SetNote); unlike the creation
	// spec it takes effect on the next turn without a rebuild. Empty for none.
	Note string `json:"note,omitempty"`
	// UserName and UserDescription are the session's bound user persona — who the
	// user is *in the story* (distinct from Persona, which is who the agent is).
	// The DESCRIPTION rides the uncached per-turn tail (like Note), so a change
	// takes effect next turn for free. The NAME is the card {{user}} macro, baked
	// into the cached prefix at build (threaded into build Args.As on materialize),
	// so changing it mid-session is a deliberate prefix rebuild. Both persist as a
	// last-wins meta row (SetUserPersona); empty for an unbound / coding session.
	UserName        string `json:"user_name,omitempty"`
	UserDescription string `json:"user_description,omitempty"`
	// UserGender and UserPronouns are the persona's stated identity (free-form
	// strings — the client offers an inclusive dropdown with an "Other" text
	// escape, so these are never an enum). When set, the per-turn user-persona
	// frame tells the model to use them; when unset, it steers the model away from
	// inventing them. Both empty for an unbound / coding session.
	UserGender   string `json:"user_gender,omitempty"`
	UserPronouns string `json:"user_pronouns,omitempty"`
	// WorldLore is the session's World lorebook — authored keyed-context entries
	// scoped to this session (the Worlds proposal's L1: shared lore every
	// character on stage sees). Like Note it is mutable mid-session and rides the
	// uncached per-turn tail, so an edit takes effect next turn with no cache
	// bust; a constant entry injects every turn, a keyed entry when its keywords
	// appear in recent messages. Empty for coding sessions.
	//
	// The json tag is a READ path, not a write one: from format version 4 the
	// book is persisted as `lore` rows (see recordLore) and writeMeta strips this
	// field from the row it writes. The tag stays because sessions written before
	// v4 keep their book here forever — an append-only file is never rewritten —
	// and because export re-marshals those rows through this struct, so removing
	// it would drop the lorebook out of every legacy session that got exported.
	WorldLore []WorldLoreEntry `json:"world_lore,omitempty"`
	// Coordination selects who answers a normal turn in a chat World with a
	// roster (the Worlds W3 meta-narrator): "" = auto (the router picks a
	// speaker), "off" = the bound character always answers (today's behavior),
	// "focus:<name>" = that roster character always answers. Meaningless — and
	// ignored — when the roster is empty (a World of one is invisible).
	Coordination string `json:"coordination,omitempty"`
	// World is the saved World this session belongs to (a worlds-library id,
	// W5) — stamped when the session is created in a World or when its
	// embedded World is promoted (worlds.save). Grouping metadata: the
	// session's own Cast/WorldLore/Coordination remain its working copy;
	// nothing here syncs live.
	World string `json:"world,omitempty"`
}

// SceneStateName is the reserved World-lore entry name for the pinned
// scene-state card (SD4 — docs/proposals/session-doctor.md): the compact
// always-in-context state note (in-fiction datetime, location, ledger facts,
// active routines). The entry is ordinary lore on disk and in bundles, but
// everything around it special-cases the name: world.lore.put forces it
// always-on and shared, world_note may UPDATE it (the one exception to
// append-only), the per-turn tail renders it as its own pinned block outside
// the lore token budget, and the Stage client shows it above the composer
// instead of in the lore list.
const SceneStateName = "Scene state"

// IsSceneState reports whether name addresses the pinned scene-state entry
// (case-insensitive — every writer canonicalizes to SceneStateName, but reads
// must tolerate hand-imported bundles that didn't).
func IsSceneState(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), SceneStateName)
}

// StorySoFarName is the reserved World-lore entry holding the recap a scene
// break writes (SD5): what happened in the scenes before this one, as
// always-on shared lore. Unlike the scene-state pin it is an ORDINARY lore
// entry in every respect — it lists and edits like any other, and the tail
// budgets it like any other. Only sessions.next_scene treats the name
// specially, replacing it so a fifth scene carries one cumulative recap
// rather than four stacked ones.
const StorySoFarName = "The story so far"

// IsStorySoFar reports whether name addresses the recap entry.
func IsStorySoFar(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), StorySoFarName)
}

// WorldLoreEntry is one entry of a session's World lore: minimal authoring
// surface over the lore engine (name + trigger keys + always-on + content).
// The full lore.Entry knob set (secondary keys, logic, order, recursion) stays
// with file/card lore; World entries keep the schema a steering drawer can
// edit.
type WorldLoreEntry struct {
	Name     string   `json:"name"`
	Keys     []string `json:"keys,omitempty"`
	Constant bool     `json:"constant,omitempty"`
	Content  string   `json:"content"`
	// Audience names the characters who know this entry (the L2 scoping axis):
	// empty = everyone on stage (world-shared). A named character's generation
	// is assembled with only the entries they are cleared for, so a secret they
	// don't hold is ABSENT from their context — a structural guard, not a
	// "don't mention this" instruction. The scene authority (the narrator, the
	// play director, the router) always sees everything.
	Audience []string `json:"audience,omitempty"`
	// Model marks an entry the MODEL authored (the play director's world_note
	// tool, W4b) rather than the user — the UI badges it 📝. The model's
	// authority is append-only: it can add entries and reveal them, never edit
	// or delete (those stay user verbs).
	Model bool `json:"model,omitempty"`
	// Learned is the learned-when ledger (L3): character → RFC 3339 timestamp
	// of the moment they learned this entry (a world_reveal). A character in
	// Audience but not Learned knew it from the start (an authored secret).
	Learned map[string]string `json:"learned,omitempty"`
	// PinnedAt is the message count when this entry's CONTENT was last written
	// (SD6). Meaningful only on the scene-state pin, and the reason it exists is
	// that the pin is the one entry that claims to be current: its frame tells
	// the model to trust it over disagreeing prose in the history. Nothing keeps
	// that promise automatically — the pin is only rewritten by a world_note, a
	// hand edit, or an accepted doctor proposal — so a card carried across a
	// scene break can sit unchanged while the scene plays past it, and the model
	// obeys it exactly as designed. Stamping the write lets the author see the
	// drift ("pinned 8 turns ago") and lets the doctor's evidence say how far
	// behind the card is. It is a count, not a timestamp: what makes a pin stale
	// is scene played since, not wall-clock time — a session resumed a week
	// later is not stale, and eight fast turns are.
	PinnedAt int `json:"pinned_at,omitempty"`
}

// sessionLine is the on-disk row type. Messages are written in the
// typed wire form (wireMessage) so every content block carries a
// "type" discriminator; reads go through hydrateMessageObject, which
// prefers the discriminator and falls back to field presence for v1
// files.
type sessionLine struct {
	Type       string          `json:"type"`
	Meta       *SessionMeta    `json:"meta,omitempty"`
	Message    *wireMessage    `json:"message,omitempty"`
	Messages   []wireMessage   `json:"messages,omitempty"`
	Usage      *provider.Usage `json:"usage,omitempty"`
	Cumulative *provider.Usage `json:"cumulative,omitempty"`
	// Delegated marks a usage row as a SUB-AGENT's spend booked against this
	// session, not a request this session sent. Without it the two are
	// byte-identical on disk, and a child's cold prompt — large input, no cache
	// read — reads exactly like a parent cache collapse to anything analysing
	// the file. RecentUsage() and the last-turn snapshot already excluded
	// delegated spend in memory for that reason; the row did not.
	Delegated  bool                    `json:"delegated,omitempty"`
	Directive  *sessionDirective       `json:"directive,omitempty"`
	Amend      *sessionAmend           `json:"amend,omitempty"`
	Lore       *sessionLore            `json:"lore,omitempty"`
	Escalation *escalationRecord       `json:"escalation,omitempty"`
	Stall      *stallRecord            `json:"stall,omitempty"`
	Retry      *retryRecord            `json:"retry,omitempty"`
	Tail       *tailRecord             `json:"tail,omitempty"`
	Prefix     *prefixDivergenceRecord `json:"prefix,omitempty"`
	ToolGroup  *toolGroupRecord        `json:"tool_group,omitempty"`
	// Cliff rides a "cliff" row: a provider-side cache collapse opened or
	// closed. Pointer-and-omitempty like the rest, so the closing row's
	// ongoing:false is carried by the record rather than by the field's
	// absence — a bare false would be indistinguishable from "no cliff here".
	Cliff *cacheCliffRecord `json:"cliff,omitempty"`
	// Net rides a "net" row: which connection/edge the adjacent dispatch
	// physically rode (provider.TransportInfo verbatim — one source of truth,
	// the way Usage rides). Informational like prefix: never in the
	// transcript, skipped by the loader. Its `at` against the usage row either
	// side of it is what correlates a cache collapse with a re-dial.
	Net *provider.TransportInfo `json:"net,omitempty"`

	// Strategy and FallbackReason ride "compaction" rows only, and only when the
	// cache-aware summarizer is in play ("cold" is the default and stays
	// implicit on legacy rows, which decode to ""). They make the
	// cache_aware_compaction A/B analyzable straight out of the session log —
	// which arm ran, how often the warm one fell back and why — and, more
	// importantly, make the silent failure detectable: strategy "warm" whose
	// usage shows no cache reads means the prefix match MISSED and the tokens
	// were billed at full price behind a label promising otherwise.
	Strategy       string `json:"strategy,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	// Superseded is how many transcript messages this checkpoint replaced.
	//
	// Redundant on a client-summarized row — the summary above it describes the
	// same turns in prose — and the only thing a reader has on a server-compacted
	// one, where the checkpoint is an encrypted blob and the row's own messages
	// say nothing about the size of what is missing. Recording it is what lets
	// the file stay auditable without paying a second summarizer to describe a
	// transcript for a human that the model already has.
	Superseded int `json:"superseded,omitempty"`
	// Truncated marks a compaction whose summary ran into compactMaxTokens. The
	// silent version of this was measurable only by arithmetic — output_tokens
	// exactly equal to the cap, prose ending mid-word — which is not a signal any
	// reader should have to reconstruct.
	Truncated bool `json:"truncated,omitempty"`

	// At stamps WHEN this row was written. writeLineLocked fills it on every row
	// that did not set one itself, so the rule is universal: a reader asks any row
	// for its time without first knowing which kind it is. Message rows carry a
	// second, semantically different time inside the payload (wireMessage.Time,
	// when the message was made rather than when the row was flushed); the two
	// agree in the ordinary case and diverge exactly where a message is
	// re-persisted, which is worth being able to see.
	//
	// It was not always universal, and the reasons it became so are worth keeping.
	// Meta rows forced it first: they are an
	// append-only timeline of what changed, but SessionMeta carries only Started —
	// the session's birth, identical on every row — so a reader could see THAT the
	// model changed and never WHEN. Aligning a settings change against the message
	// timeline was impossible; a dogfooding review could not distinguish deliberate
	// model verification from a picker that failed to confirm what it had done.
	//
	// Usage rows need it for the same reason and one more: they are the only
	// record of what a request cost and how much of it hit the prefix cache, and
	// the questions asked of that record are about RATES — was the gap before
	// this dispatch long enough to age the cached prefix out, did the collapse
	// start before the overload in the error sidecar or after it. Both need a
	// clock, and neither is answerable from row order alone. A cache collapse
	// analyzed in 2026-08 could establish WHERE the match died but not whether
	// idle time explained it, because the usage rows carried no time.
	//
	// The diagnostic rows — net, prefix, stall, tail, retry, escalation — are what
	// made it universal. Reviewing a live session in 2026-08 meant attributing
	// tool calls to a provider era after a mid-session model switch, and with no
	// clock on those rows the only handle left was the call-id PREFIX (`toolu_`
	// against `call_`). That works by coincidence and collapses the moment two
	// providers share a wire format, which anthropic-compatible ones do.
	//
	// A pointer so an explicit stamp still wins over the funnel's, and so "unset"
	// is expressible at all: a zero time.Time is not "empty" to encoding/json.
	At *time.Time `json:"at,omitempty"`
}

// sessionDirective is an append-only mutation instruction: a JSONL line that
// tells a loader to transform the reconstructed transcript without rewriting
// earlier rows (which the append-only log can't do). The only op today is
// exclude_image — drop an image the provider rejected (content-addressed by
// sha256) so a resumed session doesn't re-send it and re-fail.
type sessionDirective struct {
	Op     string `json:"op"`               // e.g. "exclude_image"
	SHA256 string `json:"sha256,omitempty"` // content hash of the image to drop
	Reason string `json:"reason,omitempty"`
}

// sessionAmend rides an "amend" row: an append-only transcript revision applied
// in file order as the loader rebuilds the effective transcript (compaction-
// shaped, unlike the content-addressed directive above). It backs the Stage
// surface's edit/delete/regenerate interactions without rewriting earlier rows.
//
//   - "replace" swaps the message at Index for Message (edit in place).
//   - "delete" removes the message at Index (later indices shift).
//   - "truncate" cuts the transcript to Index (regenerate = truncate + new turn).
//   - "retract" sets the span at Index aside as a variant ("take") and begins a
//     new take there — regenerate that KEEPS the old response, swipeable. The
//     retracted span stays in the file as its original message rows, so takes are
//     reconstructed on every walk with no byte duplication.
//   - "select" makes stored take Variant of the tail span at Index the active
//     one — the swipe interaction, carrying no message bytes.
//
// Index is interpreted against the effective transcript at the point the row is
// applied — deterministic on replay because the writer and the walk apply the
// same rules. Out-of-range indices are ignored (best-effort, like the loader).
type sessionAmend struct {
	Op      string       `json:"op"`
	Index   int          `json:"index"`
	Variant int          `json:"variant,omitempty"` // select/mselect only: which take to activate
	Message *wireMessage `json:"message,omitempty"` // replace only
	// KeepPrior, on a "replace", tells the walk to RETAIN the overwritten message
	// as a swipeable message-scoped take at Index rather than discard it — the
	// edit-as-variant-at-any-position path (docs/proposals/stage-inline-editing.md,
	// Option C). Absent/false = the original destructive replace, so old edits stay
	// collapsed. Additive: a pre-Option-C loader ignores the field and folds the
	// replace as it always did.
	KeepPrior bool   `json:"keep_prior,omitempty"`
	Reason    string `json:"reason,omitempty"` // "edit" | "delete" | "retry" | "swipe" | ...
}

// escalationRecord rides an "escalation" row: rung 3 of the stuck-loop hatch
// (docs/proposals/stuck-loop-escalation.md) swapped, or tried to swap, the live
// model to a stronger one because the detector's nudge failed to break a loop.
// The swap itself already writes a "meta" row (via UpdateModel) that is
// byte-identical to a user /model switch, so without this row the log cannot say
// a model change was the harness escalating rather than the user choosing. It is
// purely informational — it never rewrites the transcript, and a loader that
// predates it skips it (the row-type switch has no default), so resume is
// unaffected. The core payload is EscalationRecord (escalate.go).
type escalationRecord struct {
	Reason      string `json:"reason,omitempty"`
	Tool        string `json:"tool,omitempty"`
	FromModel   string `json:"from_model,omitempty"`
	ToProvider  string `json:"to_provider,omitempty"`
	ToModel     string `json:"to_model,omitempty"`
	Auto        bool   `json:"auto,omitempty"`
	Disposition string `json:"disposition,omitempty"` // switched | declined | stopped | failed
	Detail      string `json:"detail,omitempty"`
}

// stallRecord rides a "stall" row: the stuck-loop detector nudged (rung 1 of the
// hatch) because a model repeated the same call or the same failure past the
// threshold. The nudge itself rides the ephemeral tail and leaves no other
// trace, so this row is the only durable evidence the detector fired at all —
// the sibling of escalationRecord one rung up. Informational: it never enters
// the transcript, and a loader that predates it skips it (the row-type switch
// has no default), so resume is unaffected. The core payload is StallRecord
// (stall.go).
type stallRecord struct {
	Axis   string `json:"axis,omitempty"`   // spin (same call) | churn (same failure)
	Tool   string `json:"tool,omitempty"`   // the tool the model looped on
	Detail string `json:"detail,omitempty"` // the repeated error/guard slice; empty for spin
	// Rung distinguishes the first nudge (1) from the firm hold-off that follows
	// when the loop outlives it (2). Omitted on rung 1 and on every row written
	// before rung 2 existed, so absent reads as 1 — which is what makes "the
	// nudge fired and was ignored" distinguishable from "it fired and worked"
	// without re-reading the whole transcript.
	Rung int `json:"rung,omitempty"`
}

// retryRecord rides a "retry" row: a provider call failed transiently and the
// agent waited before trying again.
//
// It exists because a retry that succeeds leaves no trace anywhere. The failed
// attempt is dropped from the transcript on purpose (an abandoned attempt must
// not be replayed), the error sidecar records only failures that were never
// recovered, and the live event is gone the moment the turn ends. So the
// healthiest possible outcome — terva absorbed a provider outage and carried on
// — was also the most completely invisible one, and a session that spent two
// minutes waiting out an overloaded backend read afterwards as a session that
// was simply slow.
//
// Phase is the field that motivated the row: the turn loop and the compaction
// ladder retry through the SAME code but cost wildly different amounts, since a
// summarization request is transcript-sized. "Which ladder" is the first
// question anyone asks of these rows.
//
// Informational on the same terms as the two above: never in the transcript,
// skipped by the loader's defaultless row-type switch, so resume is unaffected.
type retryRecord struct {
	Phase    string `json:"phase,omitempty"`    // turn | compaction
	Provider string `json:"provider,omitempty"` // empty for a bare transport failure
	Attempt  int    `json:"attempt,omitempty"`  // 1-based; the attempt that failed
	Max      int    `json:"max,omitempty"`
	DelayMS  int64  `json:"delay_ms,omitempty"` // the wait taken after this failure
	Error    string `json:"error,omitempty"`
}

// tailRecord rides a "tail" row: the composition of the ephemeral tail — what
// the harness appended to the request after the prompt-cache breakpoint — at the
// moment it changed. The generalization of stallRecord above, which records one
// of the tail's five blocks and was for a long time the only one recorded at
// all. Informational on the same terms: never in the transcript, skipped by the
// loader's defaultless row-type switch, so resume is unaffected.
//
// Written on CHANGE, so a reader reconstructs any request's tail as the last row
// at or before it. That is what affords carrying the text: the rows are rare.
type tailRecord struct {
	// Blocks is deliberately NOT omitempty. An empty composition is a
	// meaningful row — it is what ends the previous one — and a reader must be
	// able to tell "the tail became empty here" from a row that failed to encode.
	Blocks []tailBlockRow `json:"blocks"`
}

type tailBlockRow struct {
	ID string `json:"id"`
	// Bytes is the block's TRUE size, always, even when Text below is clipped.
	// Reporting the clipped length instead would make the row lie about what the
	// model was charged for, which is most of what a reader comes here to learn.
	Bytes     int    `json:"bytes"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// tailTextCap clips a single block's recorded text. Only the host block can
// realistically reach it (an extension's task card, a long lore tail); the
// harness-authored notes are bounded by their own construction. Generous,
// because these rows are written on change and a session produces a handful.
const tailTextCap = 8 << 10

// prefixDivergenceRecord rides a "prefix" row: the request's cacheable prefix
// diverged from the previous request's at a point the two shared, so the
// provider re-read everything after it at full price.
//
// The only durable trace of the single largest cost driver in a long session. A
// rebuilt prefix is invisible in the transcript — same conversation, same
// messages, different bytes — and surfaces only as a cache-read figure with no
// explanation. Informational like stall and tail: never in the transcript,
// skipped by the loader.
type prefixDivergenceRecord struct {
	// Rung is the first differing element and Label names it ("tools",
	// "system", "message 12", "truncated").
	Rung  int    `json:"rung"`
	Label string `json:"label,omitempty"`
	// Messages and PrevMessages scale it: diverging at message 12 of 340 is a
	// rewrite of ancient history; at 339 of 340 it is tail churn.
	Messages     int `json:"messages"`
	PrevMessages int `json:"prev_messages"`
	// CachedTokens is what the previous request had cached — the bill.
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// cacheCliffRecord rides a "cliff" row: the detector in cachecliff.go opened or
// closed a run of append-only dispatches whose cache reads collapsed while the
// prompt kept growing.
//
// This is the row the investigation in
// docs/reviews/2026-08-04-gpt56-post-compaction-cache-collapse.md could not
// find. The detector shipped observer-only — it raised a sticky note and left
// nothing behind — so a finished session could not answer "did this fire", and
// the collapses in that document had to be reconstructed by re-deriving the
// signature from usage rows. Worse, the one experiment that would settle the
// cause has to run WHILE a session is collapsed, and nothing announced that a
// session currently was.
//
// Two rows per run, not one per dispatch. The detector deliberately fires on
// every collapse past the threshold so the note's numbers stay current, but a
// 95-dispatch run does not want 95 rows — the same reasoning recordTail applies
// to the ephemeral tail. The opening row marks the run; the closing row carries
// the totals it reached. A run still open when the session ends leaves an
// opening row and no close, which is itself the fact worth recording.
type cacheCliffRecord struct {
	// Ongoing is true on the row that opens a run and false on the row that
	// closes it — the same discriminator the note renders on, so a reader and
	// the user are never told different things.
	Ongoing bool `json:"ongoing"`
	// Dispatches is the run length and RereadTokens the input the provider
	// re-read that the previous prompt already covered. On the closing row
	// these are the run's final totals.
	Dispatches   int `json:"dispatches"`
	RereadTokens int `json:"reread_tokens"`
}

// toolGroupRecord rides a "tool_group" row: a capability group the model
// brought into view this session (activate_tools, or a skill surfacing its
// allowed-tools). Replayed on load so a resume advertises the same tools array
// the provider has cached the transcript behind.
type toolGroupRecord struct {
	Group string `json:"group"`
}

const (
	recordDirective       = "directive"
	directiveExcludeImage = "exclude_image"
	recordEscalation      = "escalation"
	recordStall           = "stall"
	recordRetry           = "retry"
	recordAmend           = "amend"
	recordTail            = "tail"
	recordPrefix          = "prefix"
	recordToolGroup       = "tool_group"
	recordNet             = "net"
	recordCliff           = "cliff"
)

// Amend op values for [Session.AppendAmend], exported so callers that persist a
// transcript revision (the workspace's edit/delete verbs) name the op without
// magic strings. See sessionAmend for the semantics.
const (
	AmendReplace  = "replace"
	AmendDelete   = "delete"
	AmendTruncate = "truncate"
	AmendRetract  = "retract"
	AmendSelect   = "select"
	// AmendMsgSelect switches a message-scoped variant's active take (the swipe for
	// a retained-history edit) — the per-index twin of AmendSelect, which acts on
	// the tail suffix span. Carries Index + Variant, no message bytes.
	AmendMsgSelect = "mselect"
	// AmendSeal collapses the message-scoped variant at Index to take Variant and
	// CLOSES the position — the walk stops reconstructing the other takes, so the
	// swipe marker goes away (prune-to-latest). The dropped takes' bytes linger in
	// the file for audit until a compaction reclaims them, but the fold no longer
	// materializes them.
	AmendSeal = "seal"
	// AmendDropTake removes take Variant from the message-scoped variant at Index,
	// keeping the rest swipeable (per-take removal); if one take remains, the
	// position closes like AmendSeal.
	AmendDropTake = "droptake"
)

type sessionLineHead struct {
	Type string `json:"type"`
}

// wireMessage is the typed on-disk form of provider.Message. The
// outer shape (role/content/time/meta) is identical to v1; only the
// blocks gain a "type" field, so v1 readers (field presence, unknown
// fields ignored) read v2 files and vice versa.
type wireMessage struct {
	Role    provider.Role     `json:"role"`
	Content []wireBlock       `json:"content"`
	Time    time.Time         `json:"time"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// wireBlock is one typed content block. One flat struct (rather than
// per-kind types) keeps encoding/decoding a single switch; omitempty
// keeps each kind's row as small as v1's.
type wireBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	MimeType string `json:"mime_type,omitempty"`
	Data     []byte `json:"data,omitempty"`
	ImageID  string `json:"image_id,omitempty"` // ig_… generation id (assistant-emitted images), for edit replay
	// tool_call
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// RawArguments preserves argument text that never parsed. Without it the
	// row records a call with "{}" and the evidence of what the model actually
	// sent is gone — which is precisely what made the original defect hard to
	// read back out of a session.
	RawArguments string `json:"raw_arguments,omitempty"`
	// Signature is the provider's opaque token for the call (Gemini 3's
	// thoughtSignature). A resumed session replays its transcript, so losing
	// this on disk means every resumed Gemini 3 session 400s on its first turn.
	Signature string `json:"signature,omitempty"`
	// tool_result (Content nests text/image blocks)
	CallID  string      `json:"call_id,omitempty"`
	Content []wireBlock `json:"content,omitempty"`
	IsError bool        `json:"is_error,omitempty"`
	// compaction_summary — Provider names who issued the encrypted blob, and
	// only that provider can replay it. Its own field rather than borrowing
	// Name (the tool_call name): a reader of a session file should not have to
	// know which block type is being decoded to know what a field means.
	Provider string `json:"provider,omitempty"`
	// reasoning — Shape names the provider block this came off. A resumed
	// session replays its transcript, and an Anthropic thinking block is only
	// replayable to Anthropic, so losing this on disk turns a resumable turn
	// into one that is silently dropped from the request.
	ReasoningID string `json:"reasoning_id,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Encrypted   string `json:"encrypted_content,omitempty"`
	Shape       string `json:"shape,omitempty"`
}

// Block type discriminator values (wireBlock.Type).
const (
	blockText       = "text"
	blockImage      = "image"
	blockToolCall   = "tool_call"
	blockToolResult = "tool_result"
	blockReasoning  = "reasoning"
	// blockCompaction matches the provider's own wire name so a session file
	// and a request body read the same way side by side.
	blockCompaction = "compaction_summary"
)

// encodeWireMessage converts a provider.Message to its typed on-disk
// form. Unknown in-memory block kinds are impossible today (Content
// is a closed set); if one appears it is dropped here at write time,
// which is loud in tests rather than silent at read time.
func encodeWireMessage(m provider.Message) wireMessage {
	w := wireMessage{Role: m.Role, Time: m.Time, Meta: m.Meta}
	w.Content = encodeWireBlocks(m.Content)
	return w
}

func encodeWireBlocks(blocks []provider.Content) []wireBlock {
	out := make([]wireBlock, 0, len(blocks))
	for _, c := range blocks {
		switch b := c.(type) {
		case provider.TextBlock:
			out = append(out, wireBlock{Type: blockText, Text: b.Text})
		case provider.ImageBlock:
			out = append(out, wireBlock{Type: blockImage, MimeType: b.MimeType, Data: b.Data, ImageID: b.ID})
		case provider.ToolCallBlock:
			// Belt and braces on the invariant provider.FinalizeToolArguments
			// establishes. An invalid RawMessage does not corrupt one field: it
			// makes json.Marshal of the WHOLE message fail, returning zero
			// bytes, so AppendMessage errors and the assistant turn never
			// reaches disk while its tool_result does — leaving an orphan
			// result no reader can attribute. ToolCallBlock is also built
			// outside the provider package (the SDK, tests, replay), so the
			// row's writability is guaranteed here rather than assumed of every
			// producer. The original text moves to RawArguments instead of
			// being dropped, because it is the only record of what was sent.
			args, rawArgs := b.Arguments, b.RawArguments
			if len(args) == 0 || !json.Valid(args) {
				if rawArgs == "" {
					rawArgs = string(args)
				}
				args = json.RawMessage("{}")
			}
			out = append(out, wireBlock{Type: blockToolCall, ID: b.ID, Name: b.Name, Arguments: args, RawArguments: rawArgs, Signature: b.Signature})
		case provider.ToolResultBlock:
			out = append(out, wireBlock{
				Type:    blockToolResult,
				CallID:  b.CallID,
				Content: encodeWireBlocks(b.Content),
				IsError: b.IsError,
			})
		case provider.ReasoningBlock:
			out = append(out, wireBlock{Type: blockReasoning, ReasoningID: b.ID, Summary: b.Summary, Encrypted: b.Encrypted, Shape: b.Shape})
		case provider.CompactionBlock:
			// Losing this block loses the compaction itself: the blob is the
			// backend's only encoding of the turns it replaced, and terva
			// cannot rebuild one. A resume that dropped it would silently
			// resume a conversation with a hole where its history was.
			out = append(out, wireBlock{Type: blockCompaction, ID: b.ID, Encrypted: b.Encrypted, Provider: b.Provider})
		}
	}
	return out
}

// CWDHash is the stable short hash of a working directory used to key
// per-cwd storage. It is exported so other per-project storage (e.g. an
// extension's data dir) can reuse the exact value SessionsDir buckets
// by, making the two correlate by eye. Pass an absolute cwd.
func CWDHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:8])
}

// SessionsDir returns the per-cwd sessions directory under root.
func SessionsDir(root, cwd string) string {
	return filepath.Join(root, "sessions", CWDHash(cwd))
}

// ProjectKey is a human-readable, collision-proof identifier for a
// working directory: the path flattened for readability, plus CWDHash as
// the disambiguator. The readable prefix is lossy on its own (two
// distinct paths can flatten the same), so the trailing hash is what
// guarantees uniqueness — which lets the prefix be freely collapsed and
// truncated.
//
// The key is computed from cwd verbatim (no absolutization), so its hash
// is byte-for-byte the cwd's SessionsDir bucket name and the two
// correlate. Pass the same absolute cwd you pass elsewhere (sessions
// already do); absolutizing here would both diverge from SessionsDir and
// graft the platform's volume (a Windows drive letter) into the key.
func ProjectKey(cwd string) string {
	slug := projectSlug(cwd)
	hash := CWDHash(cwd)
	if slug == "" {
		return hash
	}
	return slug + "-" + hash
}

// projectSlug flattens a path into a readable, filesystem-safe token:
// path separators become '-', any other non-alphanumeric becomes '_',
// and a run of separators collapses to the first one (so "a//b" -> "a-b",
// "a__b" -> "a_b"). Leading/trailing separators are stripped — a
// leading '-' would make the dir name look like a CLI flag — and the
// result is capped, tail-biased so the most specific path components
// survive (the hash in ProjectKey carries correctness, so truncation is
// purely cosmetic).
func projectSlug(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	prevSep := false
	for _, r := range p {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevSep = false
		case r == '/' || r == '\\':
			if !prevSep {
				b.WriteByte('-')
				prevSep = true
			}
		default:
			if !prevSep {
				b.WriteByte('_')
				prevSep = true
			}
		}
	}
	s := strings.Trim(b.String(), "-_")
	const maxLen = 80
	if len(s) > maxLen {
		s = strings.TrimLeft(s[len(s)-maxLen:], "-_")
	}
	return s
}

// NewSession creates and opens a new session file under
// SessionsDir(root, cwd) with an autogenerated, time-stamped name.
func NewSession(root, cwd, providerName, model, version string) (*Session, error) {
	dir := SessionsDir(root, cwd)
	if err := privfs.MkdirAll(dir); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102-150405"), id[:8])
	p := filepath.Join(dir, name)
	return newSessionAt(p, cwd, providerName, model, version)
}

// NewSessionAtPath creates a session at an explicit file path. Used
// by callers (notably the swarm-agent child) that need the session
// file to live at a path chosen by their parent rather than under
// SessionsDir. Returns an error if the file already exists — use
// OpenSession for that case.
func NewSessionAtPath(path, cwd, providerName, model, version string) (*Session, error) {
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return newSessionAt(path, cwd, providerName, model, version)
}

// newSessionAt is the shared implementation. Both NewSession and
// NewSessionAtPath funnel through here so the meta-line layout,
// freshFile bookkeeping, and id format stay identical.
func newSessionAt(p, cwd, providerName, model, version string) (*Session, error) {
	id := uuid.NewString()
	f, err := privfs.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	s := &Session{
		ID:        id,
		Path:      p,
		Meta:      SessionMeta{ID: id, CWD: cwd, Provider: providerName, Model: model, Started: time.Now().UTC(), Version: version, FormatVersion: sessionFormatVersion},
		writer:    f,
		buf:       bufio.NewWriter(f),
		freshFile: true,
	}
	if err := s.writeMeta(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func forEachJSONLLine(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > 0 {
				if ferr := fn(line); ferr != nil {
					return ferr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// jsonlPerLineCeiling bounds the bytes forEachJSONLLineBounded will materialize
// for a SINGLE row before handing it to fn. A row longer than this is drained to
// its newline and skipped without ever being allocated whole — an oversized or
// unterminated row can't force an allocation past this bound. It sits below the
// cumulative scan ceiling (session_inspect's 64 MiB) yet well above any real
// transcript row (a base64 image block or a whole-session compaction
// checkpoint), so only pathological input trips it. A var so tests can lower it.
var jsonlPerLineCeiling int64 = 16 << 20

// errJSONLCumulative stops the bounded walk when the cumulative byte budget is
// spent. It never escapes forEachJSONLLineBounded's callers, who map it to a
// truncation flag.
var errJSONLCumulative = errors.New("jsonl: cumulative byte ceiling reached")

// forEachJSONLLineBounded is forEachJSONLLine with two input bounds enforced at
// the READ boundary — before a row is trimmed, unmarshaled, or handed to fn:
//
//   - perLineMax caps a single row's bytes. A longer row is drained to its
//     newline and skipped (onOversize is called with the raw byte count so the
//     caller can flag truncation); fn never sees it, so no oversized or
//     unterminated row is ever materialized whole. Memory stays ~perLineMax.
//   - cumulativeMax caps total row bytes read across the file, enforced even
//     mid-row so one unterminated row can't read the whole file. Reaching it
//     stops the walk with errJSONLCumulative.
//
// A max <= 0 disables that bound; onOversize may be nil. fn MUST NOT retain the
// slice it is handed (the backing buffer is reused across rows).
func forEachJSONLLineBounded(r io.Reader, perLineMax, cumulativeMax int64, onOversize func(n int64), fn func([]byte) error) error {
	br := bufio.NewReader(r)
	var cumulative, rowBytes int64
	var line []byte
	oversized := false
	for {
		frag, err := br.ReadSlice('\n')
		cumulative += int64(len(frag))
		rowBytes += int64(len(frag))
		if perLineMax > 0 && !oversized && int64(len(line))+int64(len(frag)) > perLineMax {
			oversized = true // stop retaining; drain the rest of this row
			line = line[:0]
		}
		if !oversized {
			line = append(line, frag...) // append copies frag out of br's buffer
		}

		if err == bufio.ErrBufferFull {
			// Row continues past bufio's buffer; enforce the budget mid-row.
			if cumulativeMax > 0 && cumulative > cumulativeMax {
				return errJSONLCumulative
			}
			continue
		}

		// Row complete: delimiter found, or EOF closed the file's final row.
		if oversized {
			if onOversize != nil {
				onOversize(rowBytes)
			}
		} else if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
			if ferr := fn(trimmed); ferr != nil {
				return ferr
			}
		}
		line = line[:0]
		rowBytes = 0
		oversized = false

		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err // a real read error
		}
		if cumulativeMax > 0 && cumulative > cumulativeMax {
			return errJSONLCumulative
		}
	}
}

// SessionCreation is what a session file's FIRST meta row can honestly answer:
// the three facts fixed when the file was created and never rewritten
// afterwards.
//
// It exists because the cheap read used to hand back a whole SessionMeta. Meta
// rows are an append-only last-wins timeline and writeMetaLocked emits a copy of
// the WHOLE struct on every change, so the opening row is not a header — it is
// the session as it was before anything happened to it. Reading Model off it
// reports the model the session was created with, Persona reports "" for every
// session that chose one (SetCreationSpec writes the second row), and World
// reports "" for every session that joined one. The type is the fix: a caller
// cannot read a field that stops being true, because the field is not there.
//
// The three that survive do so because nothing mutates them. ImportSession and
// BranchSession rewrite ID/CWD/Started, but they rewrite EVERY meta row in the
// file they produce, so the invariant holds through both.
// TestOnlyCreationFixedFieldsSurviveTheMetaTimeline enforces it against every
// method that writes a meta row, so a field added here has to earn its place.
type SessionCreation struct {
	ID      string
	CWD     string
	Started time.Time
}

// ReadSessionCreation reads a session file's opening meta row — the cheap
// primitive for a caller that needs a session's identity without committing to a
// full scan, e.g. recovering the full UUID that the filename truncates to eight
// characters.
//
// Per-line and cumulative bounded so a damaged or crafted first row cannot force
// unbounded work. A file with no meta row returns the zero value rather than an
// error, so a caller comparing against a known id fails closed.
//
// A caller that wants anything else — the model in play, the persona, the World,
// the parent — wants the FOLDED meta and must scan the file, because every one
// of those is stamped by a row written later. describeSessionMeta does that.
func ReadSessionCreation(path string) (SessionCreation, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionCreation{}, err
	}
	defer f.Close()
	var created SessionCreation
	werr := forEachJSONLLineBounded(f, jsonlPerLineCeiling, jsonlPerLineCeiling, nil, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		if head.Type != "meta" {
			return nil
		}
		var row struct {
			Meta SessionMeta `json:"meta"`
		}
		if err := json.Unmarshal(line, &row); err == nil {
			created = SessionCreation{ID: row.Meta.ID, CWD: row.Meta.CWD, Started: row.Meta.Started}
			return io.EOF // these three never change; there is nothing later to learn
		}
		return nil
	})
	if werr != nil && werr != io.EOF {
		return SessionCreation{}, werr
	}
	return created, nil
}

// SessionUsage returns the most recent cumulative usage row stored in
// a session file. Sessions append one usage row per completed turn; the
// latest row's cumulative field is the session total. Missing usage rows
// are valid for old/empty sessions and return the zero value.
func SessionUsage(path string) (provider.Usage, error) {
	cum, _, _, err := SessionUsageDetail(path)
	return cum, err
}

// SessionUsageDetail returns the latest cumulative usage, the per-turn usage of
// the final completed turn, and the baseline a resuming host should seed the
// context gauge with.
//
// lastTurn and resumeContext are separate because they answer different
// questions and only usually agree. lastTurn is what the final turn SPENT —
// history, and a compaction that ran afterwards has no business rewriting it.
// resumeContext is what the NEXT prompt will roughly cost, which is a claim
// about the transcript as it stands on disk. They diverge in exactly one case:
// a compaction after the newest turn, where lastTurn describes a transcript
// that no longer exists.
//
// The gauge wants resumeContext (input + cache approximates the prompt size the
// model is about to see), so the TUI rehydrates on resume instead of starting
// at 0% until the next turn lands. Anything reporting what a turn cost wants
// lastTurn.
func SessionUsageDetail(path string) (cumulative, lastTurn, resumeContext provider.Usage, err error) {
	f, ferr := os.Open(path)
	if ferr != nil {
		return provider.Usage{}, provider.Usage{}, provider.Usage{}, ferr
	}
	defer f.Close()

	// Some historical sessions logged the per-turn `usage` field as a copy
	// of `cumulative` instead of the true delta. To recover an accurate
	// last-turn snapshot (used by the status-bar context gauge on resume),
	// we always derive lastTurn from the delta between the final two
	// cumulative rows. For prompt-size purposes, cache_read/cache_write
	// reflect the most recent prompt directly, so we take those from the
	// final cumulative row as-is rather than as a delta.
	//
	// Compaction rows carry their own spend (AppendCompaction), and it is
	// folded into the running total in memory — so a turn's cumulative row
	// already contains every compaction that preceded it. Two corrections
	// follow, and both matter:
	//
	//   - A compaction BETWEEN the final two turns inflates the naive delta,
	//     because cum_N = cum_{N-1} + compaction + u_N. Left uncorrected the
	//     resumed context gauge reads roughly double (a compaction's input is
	//     transcript-sized), and the first threshold check fires a spurious
	//     auto-compact on an already-condensed transcript. Subtract it.
	//   - A compaction AFTER the last turn is in no cumulative row at all —
	//     the in-memory total has it, but nothing wrote it. Compact and then
	//     quit for the day, which is an ordinary thing to do, and the spend
	//     vanished. Add it.
	//
	// Old sessions have no usage on their compaction rows; both corrections
	// are then zero and this degrades exactly to the previous behaviour.
	var prevCum provider.Usage
	var haveCum bool
	var sinceLastTurn provider.Usage // compaction spend after the newest usage row
	var betweenLastTwo provider.Usage
	// A compaction after the newest usage row SUPERSEDES lastTurn as the resume
	// baseline — see resumeContext below. Tracked as a flag beside the estimate
	// because /clear writes AppendCompaction(nil), whose estimate is legitimately
	// 0: "the transcript is empty now" and "no compaction happened" are opposite
	// facts that a bare int cannot tell apart.
	var trailingCompaction bool
	var trailingCompactionTokens int
	if ierr := forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		switch head.Type {
		case "compaction":
			// Hydrate and re-serialize rather than measuring the raw JSON line:
			// this must produce the SAME number the in-memory re-baseline did
			// (compact.go's SetLastTurn(estimateTokens(next))), or the gauge
			// jumps at the moment you resume. Cheap — a compaction row holds the
			// post-compaction transcript, which is a handful of messages.
			if msgs, herr := hydrateCompaction(line, nil); herr == nil {
				trailingCompaction = true
				trailingCompactionTokens = estimateTokens(msgs)
			}
			var row struct {
				Usage *provider.Usage `json:"usage"`
			}
			if err := json.Unmarshal(line, &row); err != nil || row.Usage == nil {
				return nil
			}
			sinceLastTurn = sinceLastTurn.Add(*row.Usage)
		case "usage":
			var row struct {
				Usage      provider.Usage `json:"usage"`
				Cumulative provider.Usage `json:"cumulative"`
				Delegated  bool           `json:"delegated"`
			}
			if err := json.Unmarshal(line, &row); err != nil {
				return nil
			}
			// A sub-agent's spend is real but is not a TURN of this session, so
			// it takes the compaction path: folded into the total, never made
			// the baseline for lastTurn. Left on the usage path, a session whose
			// final row was a child's would resume with the CHILD's prompt size
			// as its context gauge — and a child is routinely larger than its
			// parent, so the first threshold check would auto-compact a
			// transcript that never grew.
			if row.Delegated {
				sinceLastTurn = sinceLastTurn.Add(row.Usage)
				return nil
			}
			if haveCum {
				prevCum = cumulative
			}
			betweenLastTwo = sinceLastTurn
			sinceLastTurn = provider.Usage{}
			// A real turn ran after that compaction, so its provider-reported
			// prompt size is the truth again and the estimate is superseded in
			// its turn. Same handoff as in memory, where the next completed
			// request overwrites the estimate SetLastTurn seeded.
			trailingCompaction = false
			trailingCompactionTokens = 0
			cumulative = row.Cumulative
			haveCum = true
		}
		return nil
	}); ierr != nil {
		return provider.Usage{}, provider.Usage{}, provider.Usage{}, ierr
	}
	if haveCum {
		// Charge the compactions that ran between the final two turns to the
		// baseline, not to the turn: delta(cum_N, cum_{N-1} + between) = u_N.
		prevCum = prevCum.Add(betweenLastTwo)
		// input/output are monotonic totals -> per-turn = delta.
		lastTurn.InputTokens = nonNegDelta(cumulative.InputTokens, prevCum.InputTokens)
		lastTurn.OutputTokens = nonNegDelta(cumulative.OutputTokens, prevCum.OutputTokens)
		// cache_read/write on the final row already represent the last prompt's
		// cache hit/creation, not a running total of bytes; use directly.
		lastTurn.CacheReadTokens = cumulative.CacheReadTokens - prevCum.CacheReadTokens
		if lastTurn.CacheReadTokens < 0 {
			lastTurn.CacheReadTokens = cumulative.CacheReadTokens
		}
		lastTurn.CacheWriteTokens = cumulative.CacheWriteTokens - prevCum.CacheWriteTokens
		if lastTurn.CacheWriteTokens < 0 {
			lastTurn.CacheWriteTokens = cumulative.CacheWriteTokens
		}
		lastTurn.CostUSD = cumulative.CostUSD - prevCum.CostUSD
		if lastTurn.CostUSD < 0 {
			lastTurn.CostUSD = 0
		}
	}
	// A compaction after the newest turn never reached a cumulative row. Fold
	// it into the total — but NOT into lastTurn, which reports what that turn
	// actually spent and is not the compaction's to rewrite.
	cumulative = cumulative.Add(sinceLastTurn)

	// resumeContext is what a resuming host should SEED the gauge with, and it
	// is lastTurn only while lastTurn still describes the transcript on disk.
	//
	// A compaction after the newest turn breaks that. Compacting and quitting
	// for the day leaves the gauge reporting the prompt size of a transcript
	// that no longer exists — measured on a real session, 98k against a ~5.8k
	// checkpoint, 17× high — and the first threshold check on resume then fires
	// a pointless auto-compact on an already-condensed transcript. That is the
	// same stale-high failure the corrections above defend against, arriving
	// through the one door they left open.
	//
	// Compaction spend is still never the answer: its input is transcript-sized
	// by construction, so seeding FROM the compaction's own usage would read
	// even higher. The answer is the compaction's RESULT — exactly what
	// compact.go does in memory, and this is that same re-baseline recovered
	// from the file so a resumed session does not disagree with the one that
	// wrote it.
	resumeContext = lastTurn
	if trailingCompaction {
		resumeContext = provider.Usage{InputTokens: trailingCompactionTokens}
	}
	return cumulative, lastTurn, resumeContext, nil
}

func nonNegDelta(cur, prev int) int {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// LoadStats records what reconstructing a session's transcript cost — the fold's
// wall time plus how much revision machinery it replayed. Amends counts the
// in-place transforms (replace/delete/retract/select/truncate) applied during the
// walk — the proxy for variant/edit accumulation — and TailTakes is the switchable
// tail span's take count. Used to watch for load-time growth before it is felt
// (docs/proposals/stage-inline-editing.md §9).
type LoadStats struct {
	Elapsed   time.Duration
	Messages  int
	Amends    int
	TailTakes int
}

// InterruptStub is the synthetic tool_result injected for a tool_use that was
// restored without a matching result (an interrupted or lost call). Text is the
// model-visible explanation and IsError marks it a failure. A planned restart
// reconciles its interrupted call as expected (IsError:false) rather than a
// generic abort, so the agent does not read its own successful restart as a
// failed tool call.
type InterruptStub struct {
	Text    string
	IsError bool
}

// defaultInterruptStub reconciles an unmatched tool_use as a generic abort — the
// long-standing behavior for a crash/stop or a lost result.
var defaultInterruptStub = InterruptStub{Text: "tool call was aborted; no result recorded.", IsError: true}

// OpenSession opens a session file, replaying it into a live transcript, with
// any interrupted tool call reconciled as a generic abort (see InterruptStub).
func OpenSession(path string) (*Session, []provider.Message, error) {
	return openSession(path, defaultInterruptStub)
}

// OpenSessionReconciled is OpenSession with a caller-chosen reconciliation for
// an interrupted tool call — e.g. a planned restart labels the interrupted call
// as expected rather than a failure. A zero-value (empty Text) stub falls back
// to the default abort.
func OpenSessionReconciled(path string, stub InterruptStub) (*Session, []provider.Message, error) {
	if stub.Text == "" {
		stub = defaultInterruptStub
	}
	return openSession(path, stub)
}

// sessionReplay is everything reconstructing a session file yields BEFORE the
// file is opened for writing. It exists so a read-only caller
// (ReadSessionMessages, and through it `--dump-prompt`) can replay a transcript
// without taking an O_APPEND|O_WRONLY handle on a file another process may be
// appending to right now, and without owning a buffer that Close would flush.
// An inspection command should not need write permission on its subject.
//
// It is NOT protection against Close deleting the file: that is gated on
// freshFile && messagesAppended == 0, and a resumed session is never fresh.
type sessionReplay struct {
	meta           SessionMeta
	messages       []provider.Message
	titleGenerated bool
	report         *loadReport
	lore           []WorldLoreEntry
	activeGroups   []string
	amends         int
	tailTakes      int
	elapsed        time.Duration
}

// ReadSessionMessages replays a session file into its transcript and returns
// it, opening the file read-only. It is the inspection counterpart to
// OpenSession: same replay, same repair, no writer, no Close, no chance of
// mutating or deleting the session being read.
func ReadSessionMessages(path string) ([]provider.Message, error) {
	r, err := replaySession(path, defaultInterruptStub)
	if err != nil {
		return nil, err
	}
	return r.messages, nil
}

// ReadSessionPreCompaction returns the transcript as it stood immediately
// before the LAST compaction in the file, and whether there was one.
//
// This is what makes a compaction recoverable rather than a one-way door. A
// compaction row supersedes the turns it replaces in the rebuilt transcript
// (session_walk.go: `effective = out`) but the file is append-only and the
// original rows are still in it — so a conversation compacted by a provider
// whose blob another provider cannot decrypt can be rebuilt from source and
// compacted again for the new target, instead of silently losing the assistant
// turns the blob was the only copy of.
//
// The LAST compaction, not the first: earlier ones are already folded into it,
// and re-running the whole history would undo compactions that were fine.
func ReadSessionPreCompaction(path string) ([]provider.Message, bool, error) {
	var before []provider.Message
	var found bool
	_, err := replaySessionWith(path, defaultInterruptStub, func(_ []provider.Message, prior []provider.Message) {
		// Snapshot each time: the last call wins, which is the most recent
		// compaction. Cloned because the walker keeps mutating its slice.
		before = append([]provider.Message(nil), prior...)
		found = true
	})
	if err != nil {
		return nil, false, err
	}
	return before, found, nil
}

func openSession(path string, stub InterruptStub) (*Session, []provider.Message, error) {
	r, err := replaySession(path, stub)
	if err != nil {
		return nil, nil, err
	}
	out, err := privfs.OpenFile(path, os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return nil, nil, err
	}
	s := &Session{ID: r.meta.ID, Path: path, Meta: r.meta, TitleGenerated: r.titleGenerated, writer: out, buf: bufio.NewWriter(out), LoadWarnings: r.report.warnings(path),
		persistedLore:    cloneLore(r.lore),
		ActiveToolGroups: r.activeGroups,
		LoadStats:        LoadStats{Elapsed: r.elapsed, Messages: len(r.messages), Amends: r.amends, TailTakes: r.tailTakes}}
	return s, r.messages, nil
}

func replaySession(path string, stub InterruptStub) (*sessionReplay, error) {
	return replaySessionWith(path, stub, nil)
}

// replaySessionWith is replaySession with an optional observer for each
// compaction checkpoint, receiving the transcript that compaction replaced.
// Read-only callers use it to recover history a compaction superseded.
func replaySessionWith(path string, stub InterruptStub, onCompact func(out, before []provider.Message)) (*sessionReplay, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var meta SessionMeta
	var titleGenerated bool
	var messages []provider.Message
	excludeImages := map[string]bool{}
	rep := &loadReport{}
	var walkErr error
	var amends, tailTakes int
	// book folds the World lorebook across BOTH storage forms in file order, so a
	// session that predates the lore rows, one written entirely with them, and one
	// that migrated mid-file all reconstruct the same way.
	var book []WorldLoreEntry
	// Activated capability groups, in first-activation order. Deduped because
	// the same group can be re-activated across resumes (which is exactly the
	// symptom this replay removes) and the restore path wants the set.
	var activeGroups []string
	activeGroupSeen := map[string]bool{}
	// renamed records that a rename row has been folded, so a later meta row
	// cannot revert the title it set. See the onMeta hook below.
	renamed := false
	start := time.Now()
	messages, walkErr = walkSession(f, rep, sessionWalkHooks{
		onCompaction: func(out, before []provider.Message, _ int, _ []byte) {
			if onCompact != nil {
				onCompact(out, before)
			}
		},
		onMeta: func(m SessionMeta, _ []byte) {
			// A rename row is an explicit naming act and outranks any LATER
			// meta row's Title, which is stale by construction: no rename path
			// writes the new name back into the live Session.Meta (RenameSession
			// is a path-based append with no live session to update, and the
			// workspace's setTitle only touches wsSession state). So the next
			// writeMeta emits whatever Title the session started with —
			// usually "" — and folding it last silently reverted the user's
			// name on the very next load.
			//
			// The triggers are ordinary: /model (UpdateModel), /note (SetNote),
			// a background change, StampVersion on an upgraded resume, and
			// bumpFormatForAmend on the FIRST edit or retry.
			if renamed {
				m.Title = meta.Title
				meta = m
			} else {
				meta = m
				titleGenerated = false
			}
			book = foldMetaLore(book, m)
		},
		onLore: func(op sessionLore, _ []byte) {
			book = applyLoreOp(book, op)
		},
		onRename: func(title, source string, _ []byte) {
			// The latest rename row IS the session's title; without this a
			// session renamed while cold would materialize untitled and the
			// automatic titling pass could clobber the user's name.
			if title != "" {
				meta.Title = title
				titleGenerated = source == renameSourceGenerated
				renamed = true
			}
		},
		onDirective: func(d sessionDirective, _ []byte) {
			if d.Op == directiveExcludeImage && d.SHA256 != "" {
				excludeImages[strings.ToLower(d.SHA256)] = true
			}
		},
		onToolGroup: func(group string, _ []byte) {
			if !activeGroupSeen[group] {
				activeGroupSeen[group] = true
				activeGroups = append(activeGroups, group)
			}
		},
		onAmend: func(_ string, _ int, _ []byte) { amends++ },
		onTail:  func(_ int, takes [][]provider.Message, _ int) { tailTakes = len(takes) },
	})
	if walkErr != nil {
		return nil, walkErr
	}
	// The folded book is the session's lore, whichever form carried it.
	meta.WorldLore = book
	if meta.FormatVersion > sessionFormatVersionLore {
		rep.newerFormat = meta.FormatVersion
	}
	// Apply append-only directives before repair so the rebuilt transcript
	// already reflects them (e.g. a provider-rejected image is gone, so a
	// resume can't re-send it and re-fail).
	if len(excludeImages) > 0 {
		messages = applyImageExclusions(messages, excludeImages)
	}
	messages = repairToolUseResultPairsWith(messages, stub)
	backfillZeroTimes(messages, meta.Started)
	return &sessionReplay{
		meta:           meta,
		messages:       messages,
		titleGenerated: titleGenerated,
		report:         rep,
		lore:           book,
		activeGroups:   activeGroups,
		amends:         amends,
		tailTakes:      tailTakes,
		elapsed:        time.Since(start),
	}, nil
}

// backfillZeroTimes gives a Time-less message its neighbor's timestamp — the
// previous timed row's, or the session's start for a leading row. Rows written
// before the deferred-greeting stamp fix persisted Time as the zero value, and
// session files are append-only, so the zero is permanent on disk; without
// this every loaded consumer — a resumed session's wire snapshot, the replay
// rows, an export — carries a year-one timestamp on the very first row of the
// scene. Best-guess and load-only: disk bytes are never rewritten, and a
// later persistence of the loaded transcript writing the guessed time is
// strictly better than re-persisting year one.
func backfillZeroTimes(msgs []provider.Message, started time.Time) {
	last := started
	for i := range msgs {
		if msgs[i].Time.IsZero() {
			msgs[i].Time = last
		} else {
			last = msgs[i].Time
		}
	}
}

// SessionTail reports the swipe state of a session's tail span without opening it
// for appending: start is the effective index where the last response's swipeable
// span begins, takes are its alternative spans in creation order, and active is
// the one currently live in the transcript. It walks the file exactly as
// OpenSession does — so the takes are reconstructed from the message rows, never
// stored twice — but keeps only the tail-span variant state (the sole switchable
// position). start < 0, or fewer than two takes, means there is nothing to swipe.
//
// The takes are PRE-repair spans: OpenSession runs repairToolUseResultPairs after
// the walk, so a caller seeding a live transcript must reconcile them against the
// repaired transcript (a length check against the live tail suffices — repair is
// a no-op for the tool-free chat sessions this primarily serves).
func SessionTail(path string) (start int, takes [][]provider.Message, active int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return -1, nil, 0, err
	}
	defer f.Close()
	start = -1
	rep := &loadReport{}
	if _, walkErr := walkSession(f, rep, sessionWalkHooks{
		onTail: func(ts int, tk [][]provider.Message, ac int) {
			start, takes, active = ts, tk, ac
		},
	}); walkErr != nil {
		return -1, nil, 0, walkErr
	}
	return start, takes, active, nil
}

// MsgVariants is the reconstructed message-scoped variant set at one position: the
// alternative single-message takes in creation order and which one is active
// (live in the effective transcript).
type MsgVariants struct {
	Takes  []provider.Message
	Active int
}

// VariantPos summarizes a switchable position for a snapshot's per-position swipe
// markers: its effective-transcript Index, how many takes it has, and which is
// active. Span distinguishes the tail suffix span (retry/greeting variants, whole
// tail) from a message-scoped variant (an edited single message with shared
// downstream).
type VariantPos struct {
	Index  int
	Count  int
	Active int
	Span   bool
}

// SessionVariants reports every switchable position in a session without opening
// it for appending — the tail suffix span (as SessionTail) plus each message-
// scoped variant (Option C) — as the cheap per-position counts a snapshot needs to
// draw swipe markers across the whole transcript. The full take lists are NOT
// returned; a non-tail position is hydrated on demand with SessionMsgVariant.
func SessionVariants(path string) ([]VariantPos, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []VariantPos
	rep := &loadReport{}
	if _, walkErr := walkSession(f, rep, sessionWalkHooks{
		onTail: func(ts int, tk [][]provider.Message, ac int) {
			if ts >= 0 && len(tk) >= 2 {
				out = append(out, VariantPos{Index: ts, Count: len(tk), Active: ac, Span: true})
			}
		},
		onMsgVariants: func(m map[int]MsgVariants) {
			for idx, mv := range m {
				if len(mv.Takes) >= 2 {
					out = append(out, VariantPos{Index: idx, Count: len(mv.Takes), Active: mv.Active})
				}
			}
		},
	}); walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// SessionMsgVariant reconstructs the full message-scoped variant set at one
// position — the lazy hydration a client does when it actually swipes a non-tail
// edited message (SessionVariants gave only the count). ok is false when Index has
// no message-scoped variants.
func SessionMsgVariant(path string, index int) (mv MsgVariants, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return MsgVariants{}, false, err
	}
	defer f.Close()
	rep := &loadReport{}
	if _, walkErr := walkSession(f, rep, sessionWalkHooks{
		onMsgVariants: func(m map[int]MsgVariants) {
			if v, has := m[index]; has {
				mv, ok = v, true
			}
		},
	}); walkErr != nil {
		return MsgVariants{}, false, walkErr
	}
	return mv, ok, nil
}

// applyImageExclusions replaces every ImageBlock whose content sha256 is in the
// excluded set with the standard rejected-image note — directly in a message
// and nested in a tool result. Content-addressed, so one exclude_image
// directive covers every copy of the image (tool result + codex mirror) and
// survives reordering. Mutates and returns msgs.
func applyImageExclusions(msgs []provider.Message, excluded map[string]bool) []provider.Message {
	isExcluded := func(b provider.ImageBlock) bool { return excluded[imageSHA256(b.Data)] }
	for mi := range msgs {
		content := msgs[mi].Content
		for ci := range content {
			switch v := content[ci].(type) {
			case provider.ImageBlock:
				if isExcluded(v) {
					content[ci] = provider.TextBlock{Text: imageRejectedNote}
				}
			case provider.ToolResultBlock:
				changed := false
				for ii := range v.Content {
					if ib, ok := v.Content[ii].(provider.ImageBlock); ok && isExcluded(ib) {
						v.Content[ii] = provider.TextBlock{Text: imageRejectedNote}
						changed = true
					}
				}
				if changed {
					content[ci] = v
				}
			}
		}
	}
	return msgs
}

// repairToolUseResultPairs walks a restored transcript and
// synthesises stub tool_result blocks for any assistant
// tool_use blocks that aren't paired with a matching result in
// the next message. Anthropic (and OpenAI via the responses API)
// reject any request whose transcript leaves a tool_use without
// its matching tool_result immediately after, with errors like:
//
//	messages.8: `tool_use` ids were found without `tool_result`
//	blocks immediately after
//
// Corruption gets into the transcript two ways we know of:
//
//   - Older terva builds that persisted the assistant tool_use row
//     before the tool_result row, then crashed between the two.
//   - Abort paths in older builds that didn't drop the mid-turn
//     assistant message cleanly.
//
// Rather than change runtime semantics (which would risk hiding a
// real bug), we scrub on load: any unmatched tool_use gets a stub
// tool_result injected as a RoleTool message so the next
// outbound request passes the provider's validity check. The stub
// reads "tool call was aborted; no result recorded." so the
// model can see what happened and decide whether to retry.
//
// Runs once per OpenSession call. No cost on the hot path.
func repairToolUseResultPairs(msgs []provider.Message) []provider.Message {
	return repairToolUseResultPairsWith(msgs, defaultInterruptStub)
}

// repairToolUseResultPairsWith is repairToolUseResultPairs with a caller-chosen
// stub for the synthesized results — so a planned restart reconciles its
// interrupted call as expected text (non-error) rather than a generic abort.
func repairToolUseResultPairsWith(msgs []provider.Message, stub InterruptStub) []provider.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]provider.Message, 0, len(msgs)+2)
	for i, m := range msgs {
		out = append(out, m)
		if m.Role != provider.RoleAssistant {
			continue
		}
		// Collect tool_use ids in this assistant message.
		var ids []string
		for _, c := range m.Content {
			if tc, ok := c.(provider.ToolCallBlock); ok {
				ids = append(ids, tc.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		// Look at the next message (if any) and collect tool_result
		// CallIDs it covers.
		have := map[string]bool{}
		if i+1 < len(msgs) && msgs[i+1].Role == provider.RoleTool {
			for _, c := range msgs[i+1].Content {
				if tr, ok := c.(provider.ToolResultBlock); ok {
					have[tr.CallID] = true
				}
			}
		}
		// Build stubs for any missing id.
		var stubs []provider.Content
		for _, id := range ids {
			if have[id] {
				continue
			}
			stubs = append(stubs, provider.ToolResultBlock{
				CallID:  id,
				Content: []provider.Content{provider.TextBlock{Text: stub.Text}},
				IsError: stub.IsError,
			})
		}
		if len(stubs) == 0 {
			continue
		}
		// Merge into the next tool-role message if present,
		// otherwise insert a synthetic one right after the
		// assistant message. Merging keeps the tool-role row
		// count stable; inserting handles the common case where
		// no tool message was persisted at all.
		if i+1 < len(msgs) && msgs[i+1].Role == provider.RoleTool {
			msgs[i+1].Content = append(msgs[i+1].Content, stubs...)
			// We already appended m to out; the modified next
			// message will be appended on the following iteration.
			continue
		}
		out = append(out, provider.Message{
			Role:    provider.RoleTool,
			Content: stubs,
			Time:    m.Time,
		})
	}
	return out
}

// LatestSession returns the most recent session file for cwd, or "".
func LatestSession(root, cwd string) string {
	paths := ListSessions(root, cwd)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// SessionSummary describes one on-disk session at a glance for UI pickers.
type SessionSummary struct {
	Path          string
	Started       time.Time
	Model         string
	Provider      string
	MessageCount  int
	FirstUserText string
	TotalCost     float64
	Title         string
	// TitleGenerated reports machine provenance for Title (see
	// Session.TitleGenerated); false for user renames and meta-line titles.
	TitleGenerated bool
	// Experience/Card/Background/World carry the immersive spec from meta so a
	// cold session (a disk scan, never opened this run) still badges as
	// chat/play and groups under its character — and its saved World (W5) —
	// without waking it.
	Experience string
	Card       string
	Background string
	World      string
	// Persona is the agent persona the session was created with, read from meta
	// like the fields above. It is what a materialize replays, so it is also what
	// makes a session depend on a persona continuing to exist — see
	// SessionsUsingPersona.
	Persona string
	// Live/Busy are the session's live state, set only on the attached-TUI path
	// (from ctrlproto.SessionInfo via SessionSummariesFromInfos): Live = the
	// session is materialized in memory, Busy = a turn is in flight. A disk scan
	// (DescribeSessions) and an old daemon both leave them false — honestly cold,
	// since a session you are not attached to has no live state to show.
	Live bool
	Busy bool
}

// renameSourceGenerated marks a rename row written by machine titling
// (settleTitle / sessions.generate_title / the post-compaction refresh).
// User renames carry no source. The distinction is provenance: automatic
// re-titling may replace a generated title, never a manual one.
const renameSourceGenerated = "generated"

// RenameSession appends a USER rename line to the session file. This is
// safe even for the currently active session because it opens the
// file independently and appends (doesn't rewrite).
func RenameSession(path, title string) error {
	return appendRename(path, title, "")
}

// RenameSessionGenerated appends a machine-generated rename line — same row
// as RenameSession plus the provenance marker automatic re-titling keys on.
func RenameSessionGenerated(path, title string) error {
	return appendRename(path, title, renameSourceGenerated)
}

func appendRename(path, title, source string) error {
	f, err := privfs.OpenFile(path, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	defer f.Close()
	row := map[string]string{"type": "rename", "title": title}
	if source != "" {
		row["source"] = source
	}
	line, _ := json.Marshal(row)
	line = append(line, '\n')
	_, err = f.Write(line)
	return err
}

// DescribeSessions returns lightweight summaries for every session in
// cwd, newest first. Parses only the first few lines and the last usage
// line so it's cheap to run on every dialog open.
func DescribeSessions(root, cwd string) []SessionSummary {
	paths := ListSessions(root, cwd)
	summaries := make([]SessionSummary, 0, len(paths))
	for _, p := range paths {
		summaries = append(summaries, describeSession(p))
	}
	return summaries
}

// SessionsMatching returns summaries for every live session under root — across
// ALL project directories, not just one cwd — that keep satisfies, newest first.
//
// It exists for the questions a library DELETE has to ask: what did this card,
// or this persona, leave behind? Those live in $TERVA_HOME and are global, while
// sessions are bucketed per working directory, so a per-project answer is the
// wrong answer — it reports that nothing depends on a thing another project's
// chats are built on.
//
// Deliberately not folded into DescribeSessions: this reads every project's
// transcripts, which is right for a question asked once before a delete and
// wrong for one asked on every dialog open.
//
// Archived sessions (.jsonl.gz) are not scanned — see SessionsUsingCard for why
// that matters and what it costs.
func SessionsMatching(root string, keep func(SessionSummary) bool) []SessionSummary {
	buckets, err := os.ReadDir(filepath.Join(root, "sessions"))
	if err != nil {
		return nil
	}
	var out []SessionSummary
	for _, b := range buckets {
		if !b.IsDir() {
			continue
		}
		dir := filepath.Join(root, "sessions", b.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isSessionTranscriptName(e.Name()) {
				continue
			}
			if s := describeSession(filepath.Join(dir, e.Name())); keep(s) {
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// SessionsUsingPersona returns every live session created with the named
// persona. A session replays its persona on every materialize, so deleting one
// changes how each of these opens (it falls back to the workspace default and
// says so) — the count is what lets a caller say how many before doing it.
func SessionsUsingPersona(root, persona string) []SessionSummary {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		return nil
	}
	return SessionsMatching(root, func(s SessionSummary) bool {
		return strings.EqualFold(strings.TrimSpace(s.Persona), persona)
	})
}

// SessionsUsingCard returns every live session bound to the given card id.
//
// Unlike a persona, a card cannot be fallen back from: it IS the character, and
// a session re-resolves SessionMeta.Card on every materialize, so evicting the
// card stops those chats opening for good. That is why the count gates the
// delete rather than merely annotating it.
//
// Archived sessions are excluded, and the exclusion is a decision rather than an
// oversight: DeleteSession removes the .jsonl and never the .jsonl.gz, so
// counting archives would make a card with one permanently undeletable. The cost
// is that archiving a chat releases its card, and restoring that archive later
// finds the character gone — a trade the user makes explicitly, unlike the one
// this scan exists to prevent.
func SessionsUsingCard(root, cardID string) []SessionSummary {
	cardID = strings.TrimSpace(cardID)
	if cardID == "" {
		return nil
	}
	return SessionsMatching(root, func(s SessionSummary) bool {
		return strings.TrimSpace(s.Card) == cardID
	})
}

func describeSession(path string) SessionSummary {
	s, _ := describeSessionMeta(path)
	return s
}

// describeSessionMeta is describeSession plus the FOLDED meta — the last-wins
// result of the same pass, for a caller that needs a meta field the summary does
// not carry.
//
// It exists so that caller does not open the file a second time to read the
// FIRST meta row, which is what /session tree did: it took Parent from the
// opening row, so a session whose lineage was stamped after creation
// (Session.SetParent, the next-scene path) rendered as a parentless root. One
// pass, and the answer is the one the loader would give.
func describeSessionMeta(path string) (SessionSummary, SessionMeta) {
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{Path: path}, SessionMeta{}
	}
	defer f.Close()
	return describeSessionFromMeta(path, f)
}

// describeSessionFrom summarises a transcript from an arbitrary reader.
//
// Split out from describeSession so an ARCHIVED session — the same JSONL behind
// a gzip reader — produces the same summary as a live one, rather than the
// archive browser growing a second, drifting description of what a session is.
func describeSessionFrom(path string, r io.Reader) SessionSummary {
	s, _ := describeSessionFromMeta(path, r)
	return s
}

// describeSessionFromMeta is describeSessionFrom's body, also returning the
// folded meta. See describeSessionMeta for why the meta comes back from this
// pass rather than from a second read of the opening row.
func describeSessionFromMeta(path string, r io.Reader) (SessionSummary, SessionMeta) {
	s := SessionSummary{Path: path}
	var meta SessionMeta
	// See replaySessionWith: a rename outranks a later meta row's stale Title.
	renamed := false
	_ = forEachJSONLLine(r, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		switch head.Type {
		case "meta":
			var row struct {
				Meta SessionMeta `json:"meta"`
			}
			if err := json.Unmarshal(line, &row); err == nil {
				// Last row wins, matching the loader — see the Experience block
				// below and replaySessionWith.
				meta = row.Meta
				s.Started = row.Meta.Started
				s.Model = row.Meta.Model
				s.Provider = row.Meta.Provider
				// A rename outranks a LATER meta row's Title, which is stale by
				// construction — no rename path writes back into Session.Meta.
				// This fold is byte-identical to replaySessionWith's and had the
				// same bug, so the listing and the opened session agreed on the
				// wrong answer, which is the worst way for two readers to agree.
				if !renamed {
					s.Title = row.Meta.Title
					s.TitleGenerated = false
				}
				// Meta is a last-wins timeline, so the final row's spec wins.
				s.Experience = row.Meta.Experience
				s.Card = row.Meta.Card
				s.Background = row.Meta.Background
				s.World = row.Meta.World
				s.Persona = row.Meta.Persona
			}
		case "message":
			s.MessageCount++
			if s.FirstUserText == "" {
				s.FirstUserText = firstUserText(line)
			}
		case "compaction":
			if compacted, err := hydrateCompaction(line, nil); err == nil {
				s.MessageCount = len(compacted)
				if s.FirstUserText == "" && len(compacted) > 0 {
					s.FirstUserText = firstTextFromMessage(compacted[0])
				}
			}
		case "rename":
			var row struct {
				Title  string `json:"title"`
				Source string `json:"source"`
			}
			if err := json.Unmarshal(line, &row); err == nil && row.Title != "" {
				s.Title = row.Title
				s.TitleGenerated = row.Source == renameSourceGenerated
				renamed = true
			}
		case "usage":
			var row struct {
				Cumulative provider.Usage `json:"cumulative"`
			}
			if err := json.Unmarshal(line, &row); err == nil {
				s.TotalCost = row.Cumulative.CostUSD
			}
		}
		return nil
	})
	return s, meta
}

func firstUserText(line []byte) string {
	var row struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return ""
	}
	if row.Message.Role != "user" {
		return ""
	}
	for _, c := range row.Message.Content {
		if c.Text != "" {
			return c.Text
		}
	}
	return ""
}

func firstTextFromMessage(msg provider.Message) string {
	for _, c := range msg.Content {
		if tb, ok := c.(provider.TextBlock); ok && tb.Text != "" {
			return tb.Text
		}
	}
	return ""
}

// PruneEmptySessions deletes session files in cwd's session directory
// that contain only a meta line (no messages were ever appended).
// Cleans up the backlog of empty stubs created by old terva versions
// that wrote a meta line at NewSession time and never followed up.
// Errors are swallowed; the caller treats this as best-effort.
func PruneEmptySessions(root, cwd string) {
	dir := SessionsDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !isSessionTranscriptName(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if sessionHasNoMessages(p) {
			_ = os.Remove(p)
		}
	}
}

// isSessionTranscriptName reports whether a directory entry name is a
// session transcript. Sidecars (see sessionSidecars — the error log at
// <session>.errors.jsonl is one) share the transcript's base name, and some
// share its .jsonl extension so they sort next to it, but they are NOT
// sessions: listing them would surface blank entries in /sessions and
// /continue, and pruning them would silently destroy the record they hold
// (sidecar rows carry no "message" lines, so sessionHasNoMessages reads them
// as empty). Every scan of a sessions directory must use this filter, not a
// bare .jsonl check.
func isSessionTranscriptName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") && !isSessionSidecarName(name)
}

// sessionHasNoMessages returns true when the file at path contains
// no lines of type "message". Meta-only / usage-only files count as
// empty. Used by PruneEmptySessions and the Describe path.
func sessionHasNoMessages(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hasMessage := false
	_ = forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		if head.Type == "message" {
			hasMessage = true
			return io.EOF
		}
		return nil
	})
	return !hasMessage
}

// ListSessions returns session file paths for cwd, most-recently-
// modified first. Sorting on filesystem ModTime instead of the
// timestamp embedded in the filename means a long-running session
// the user actually returned to recently floats to the top of
// /sessions, /continue, and the resume picker, even when it was
// originally created days earlier than newer but idle sessions.
// Files with identical ModTime fall back to filename desc so the
// order stays stable across calls.
func ListSessions(root, cwd string) []string {
	dir := SessionsDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type rec struct {
		path string
		mod  time.Time
	}
	var files []rec
	for _, e := range entries {
		if e.IsDir() || !isSessionTranscriptName(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, rec{path: p, mod: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.After(files[j].mod)
		}
		return files[i].path > files[j].path
	})
	out := make([]string, 0, len(files))
	for _, r := range files {
		out = append(out, r.path)
	}
	return out
}

// AppendMessage writes a message to the session.
func (s *Session) AppendMessage(m provider.Message) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	w := encodeWireMessage(m)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.writeLineLocked(sessionLine{Type: "message", Message: &w}); err != nil {
		return err
	}
	s.messagesAppended++
	return nil
}

// AppendCompaction writes a checkpoint that replaces all earlier
// transcript rows when the session is resumed. The old rows remain in
// the JSONL file for audit/export, while loaders use the latest
// compaction row as the effective transcript.
//
// res.Usage is the summarization call's own spend, recorded on THIS row rather
// than as a "usage" row on purpose. SessionUsageDetail derives the resumed
// context gauge from usage rows alone, and a compaction's input count is
// transcript-sized by construction — as a usage row it would seed the gauge
// stale-high and fire a spurious auto-compact on an already-condensed
// transcript, which is the exact bug CostTracker.AddTotalOnly avoids in
// memory. Compaction spend is cost, never context; the row type is what
// carries that distinction across the file boundary.
//
// res.Strategy records which summarizer produced it, because usage alone cannot
// say — and a cache-aware compaction that MISSED the cache is indistinguishable
// from one that hit, except in these numbers. A zero CompactResult is what
// /clear writes: a floor marker that summarized nothing and cost nothing.
func (s *Session) AppendCompaction(messages []provider.Message, res CompactResult) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	wires := make([]wireMessage, 0, len(messages))
	for _, m := range messages {
		wires = append(wires, encodeWireMessage(m))
	}
	u := res.Usage
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.writeLineLocked(sessionLine{
		Type:           "compaction",
		Messages:       wires,
		Usage:          &u,
		Strategy:       string(res.Strategy),
		FallbackReason: res.FallbackReason,
		Truncated:      res.Truncated,
		Superseded:     res.SupersededMessages,
	}); err != nil {
		return err
	}
	s.messagesAppended = len(messages)
	return nil
}

// UpdateModel records a provider/model switch in the session file.
// The reader keeps the most recent meta entry, so the session resumes
// with the updated model.
//
// A switch that changes nothing writes nothing. Startup applies the resolved
// model unconditionally, so without this a session opened three times over
// began with three byte-identical meta rows saying the same thing — noise in a
// file whose meta rows are supposed to read as a timeline of what changed.
func (s *Session) UpdateModel(providerName, model string) error {
	if s == nil {
		return nil
	}
	if s.Meta.Provider == providerName && s.Meta.Model == model {
		return nil
	}
	s.Meta.Provider = providerName
	s.Meta.Model = model
	return s.writeMeta()
}

// UpdateReasoning records this session's reasoning override in the session
// file, so a daemon restart re-materializes the session at the level the user
// chose rather than dropping it back to the global default.
//
// level is the RAW ladder token ("off", "high", "max", …) rather than the
// normalized form, because normalization folds "off" into "" and this field
// needs to tell an explicit off apart from an absent override. "" means
// inherit the global level — the same two-state distinction core.Agent carries
// as Reasoning plus ReasoningSet, spelled with one field because a session file
// is read by things that cannot see a Go bool.
//
// Writes nothing when the level is unchanged, for the reason UpdateModel does:
// meta rows are a timeline of what changed, and re-applying the same value on
// every resume turns that timeline into noise.
func (s *Session) UpdateReasoning(level string) error {
	if s == nil {
		return nil
	}
	if s.Meta.Reasoning == level {
		return nil
	}
	s.Meta.Reasoning = level
	return s.writeMeta()
}

// writeMeta appends the session's current metadata as a timeline row, stamped
// with the moment it changed.
//
// Every meta writer funnels through here rather than building the row itself, so
// the stamp cannot be forgotten by the next one — and so the lorebook can be
// stripped in exactly one place. See sessionLine.At for why the rows needed a
// time of their own.
func (s *Session) writeMeta() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeMetaLocked()
}

// writeMetaLocked is writeMeta's body; the caller must hold writeMu. Used by
// SetWorldLore, which bumps the format version and appends its lore rows as one
// critical section.
//
// The row is a COPY of s.Meta so the strip below can't disturb the live session.
func (s *Session) writeMetaLocked() error {
	now := time.Now().UTC()
	row := s.Meta
	if row.FormatVersion >= sessionFormatVersionLore {
		// The book has its own rows from here on. Leaving it here as well would
		// restore the duplication the rows exist to remove, and would put a second
		// copy on disk that can disagree with the first.
		//
		// Conditional on the version because it is also what tells a READER whether
		// this row's absent book means "stored elsewhere" or "cleared". A session
		// carrying a pre-v4 book that has not been edited yet keeps re-emitting it
		// here, unchanged, until the first lore edit migrates it — stripping
		// unconditionally would make the next SetNote silently erase the lorebook.
		row.WorldLore = nil
	}
	return s.writeLineLocked(sessionLine{Type: "meta", Meta: &row, At: &now})
}

// StampVersion records the running build in the session file, so a session that
// spans an upgrade can say which terva wrote which part of it.
//
// The meta rows already form a timeline: they are append-only, the loader keeps
// the last one, and UpdateModel writes a fresh one on every model switch — which
// is what lets a reader say "these turns ran on codex, those on anthropic".
// Version was the one field that never joined it. It was stamped once by
// NewSession and then re-emitted verbatim forever, so every row a resumed
// session wrote CLAIMED the build that had created the session, however many
// upgrades ago. Attribution across an upgrade was not missing so much as wrong.
//
// Callers are the paths that RESUME a session to keep talking in it. Opening a
// session to read it — an inspector, a sub-agent lifting its transcript — must
// not stamp: it isn't writing any of the rows it would be claiming.
//
// A no-op when the version is unchanged, which is the common case, so a session
// that never crosses an upgrade grows no extra rows.
func (s *Session) StampVersion(version string) error {
	if s == nil || version == "" || s.Meta.Version == version {
		return nil
	}
	s.Meta.Version = version
	return s.writeMeta()
}

// SetCreationSpec records the per-session creation parameters — the chosen
// persona and the immersive (Stage) fields (experience/card/cast/greeting) — on
// the session metadata and writes a fresh meta row. Meta rows are an append-only
// timeline whose last entry wins, so this makes the spec durable: a resume after
// a daemon restart re-materializes the session as it was created rather than as
// the workspace default. A no-op on a nil receiver, mirroring the other stampers.
func (s *Session) SetCreationSpec(persona, experience, card string, cast map[string]string, greeting int) error {
	if s == nil {
		return nil
	}
	s.Meta.Persona = persona
	s.Meta.Experience = experience
	s.Meta.Card = card
	s.Meta.Cast = cast
	s.Meta.Greeting = greeting
	return s.writeMeta()
}

// SetBackground binds (or, with "", clears) the session's scene backdrop and
// writes a fresh meta row. Like the creation spec it rides the last-wins meta
// timeline, so a rebind is durable across a restart; unlike it, a background can
// change any number of times during a session. A no-op on a nil receiver.
func (s *Session) SetBackground(id string) error {
	if s == nil {
		return nil
	}
	s.Meta.Background = id
	return s.writeMeta()
}

// SetNote sets (or, with "", clears) the session's author's note and writes a
// fresh meta row. Like SetBackground it rides the last-wins meta timeline, so the
// note is durable across a restart and can change any number of times during a
// session. Unlike a background, the note is a build input — it is injected into
// the uncached per-turn tail — but it never touches the cached prefix, so a change
// takes effect on the next turn without a rebuild. A no-op on a nil receiver.
func (s *Session) SetNote(text string) error {
	if s == nil {
		return nil
	}
	s.Meta.Note = text
	return s.writeMeta()
}

// SetUserPersona binds (or, with empty strings, clears) the session's user
// persona — who the user is in the story. Persisted as one last-wins meta row
// carrying both halves; the caller applies the live effects (the description
// updates the per-turn tail for free, the name rebuilds the cached prefix).
func (s *Session) SetUserPersona(name, description, gender, pronouns string) error {
	if s == nil {
		return nil
	}
	s.Meta.UserName = name
	s.Meta.UserDescription = description
	s.Meta.UserGender = gender
	s.Meta.UserPronouns = pronouns
	return s.writeMeta()
}

// SetCoordination sets who answers a normal turn in a chat World (the W3
// meta-narrator setting): "" auto, "off", or "focus:<name>". A last-wins meta
// row like the other stampers; takes effect on the next turn (the route
// decision reads it fresh each prompt). A no-op on a nil receiver.
func (s *Session) SetCoordination(mode string) error {
	if s == nil {
		return nil
	}
	s.Meta.Coordination = mode
	return s.writeMeta()
}

// SetWorld stamps (or, with "", clears) the saved World this session belongs
// to — membership metadata for grouping, a last-wins meta row like the other
// stampers. A no-op on a nil receiver.
func (s *Session) SetWorld(id string) error {
	if s == nil {
		return nil
	}
	s.Meta.World = id
	return s.writeMeta()
}

// SetParent records which session this one came from, for a successor that was
// not produced by a fork — today the next scene of a story (SD5), which builds a
// fresh session rather than branching one. BranchSession stamps Parent at
// creation; this is the stamper for the paths that create first and learn their
// lineage after. A no-op on a nil receiver.
//
// Parent means "came from", not "is a branch of": ForkPoint stays empty here,
// which is what distinguishes a successor from a branch — a fork shares a
// transcript prefix, a next scene shares only its world.
func (s *Session) SetParent(id string) error {
	if s == nil {
		return nil
	}
	s.Meta.Parent = id
	return s.writeMeta()
}

// CastRoute pins a cast member to a specific provider+model (Phase 7 per-generation
// routing). Empty fields mean "inherit the session/host route" — the default for an
// unpinned actor.
type CastRoute struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// SetCast replaces (or, with a nil/empty map, clears) a play session's cast —
// the actor-name → persona/card refs the director can voice via actor_spawn, plus
// the parallel per-actor model pins (CastRoute; empty map = no pins). Like the
// other stampers it rides the last-wins meta timeline, so a mid-scene cast change
// is durable across a restart. The caller rebuilds the actor_spawn tool + cast
// addendum to apply it live (unlike the creation spec, which bakes the cast at
// build time). A no-op on a nil receiver.
func (s *Session) SetCast(cast map[string]string, models map[string]CastRoute) error {
	if s == nil {
		return nil
	}
	s.Meta.Cast = cast
	s.Meta.CastModels = models
	return s.writeMeta()
}

// AppendAmend writes an append-only transcript revision (see sessionAmend): a
// "replace"/"delete"/"truncate" applied in file order when the session is
// reloaded. The original message rows stay in the file for audit; the loader
// folds the amend into the rebuilt transcript. Amend rows are NOT counted as
// appended messages, so a session that is only a seeded greeting plus an edit
// still prunes correctly on Close. msg is required for "replace" and ignored
// otherwise.
func (s *Session) AppendAmend(op string, index int, msg *provider.Message, reason string) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	if err := s.bumpFormatForAmend(); err != nil {
		return err
	}
	amend := &sessionAmend{Op: op, Index: index, Reason: reason}
	if msg != nil {
		w := encodeWireMessage(*msg)
		amend.Message = &w
	}
	return s.writeLine(sessionLine{Type: recordAmend, Amend: amend})
}

// bumpFormatForAmend raises the session's declared on-disk format version to the
// amend-aware version the first time a revision row is written, by appending a
// fresh meta row (meta rows are an append-only timeline whose last entry wins).
// A pre-amend build then WARNS on load instead of silently presenting the
// un-revised transcript. Idempotent: a no-op once the session already declares
// the version, so a heavily-edited session grows exactly one extra meta row.
func (s *Session) bumpFormatForAmend() error {
	if s == nil || s.Meta.FormatVersion >= sessionFormatVersionAmend {
		return nil
	}
	s.Meta.FormatVersion = sessionFormatVersionAmend
	return s.writeMeta()
}

// AppendSelect writes a "select" amend (see sessionAmend): make take `variant` of
// the tail span starting at `index` the active one — the swipe interaction. It
// carries no message bytes (the take is reconstructed from the file) and, like
// every amend, is not counted as an appended message.
func (s *Session) AppendSelect(index, variant int, reason string) error {
	if s == nil {
		return nil
	}
	if err := s.bumpFormatForAmend(); err != nil {
		return err
	}
	return s.writeLine(sessionLine{Type: recordAmend, Amend: &sessionAmend{
		Op: AmendSelect, Index: index, Variant: variant, Reason: reason,
	}})
}

// AppendReplaceVariant writes a retained-history replace: the message at index is
// swapped for msg, but the walk keeps the overwritten message as a swipeable
// message-scoped take at that index (unlike AppendAmend(AmendReplace,…), which
// discards it). This is edit-as-variant at any position (Option C) — the prior
// version stays reachable via AppendMsgSelect. The bytes cost nothing extra: the
// prior message is reconstructed from its own earlier row on every walk.
func (s *Session) AppendReplaceVariant(index int, msg provider.Message, reason string) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	if err := s.bumpFormatForAmend(); err != nil {
		return err
	}
	w := encodeWireMessage(msg)
	return s.writeLine(sessionLine{Type: recordAmend, Amend: &sessionAmend{
		Op: AmendReplace, Index: index, Message: &w, KeepPrior: true, Reason: reason,
	}})
}

// AppendMsgSelect writes a message-scoped select (see AmendMsgSelect): make take
// `variant` of the retained-history message-variant at Index the active one — the
// swipe-back for an edited message, carrying no message bytes.
func (s *Session) AppendMsgSelect(index, variant int, reason string) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	if err := s.bumpFormatForAmend(); err != nil {
		return err
	}
	return s.writeLine(sessionLine{Type: recordAmend, Amend: &sessionAmend{
		Op: AmendMsgSelect, Index: index, Variant: variant, Reason: reason,
	}})
}

// AppendSeal writes a seal (see AmendSeal): collapse the message-scoped variant at
// index to take `keep` and close the position — prune-to-latest, carrying no
// message bytes.
func (s *Session) AppendSeal(index, keep int, reason string) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	if err := s.bumpFormatForAmend(); err != nil {
		return err
	}
	return s.writeLine(sessionLine{Type: recordAmend, Amend: &sessionAmend{
		Op: AmendSeal, Index: index, Variant: keep, Reason: reason,
	}})
}

// AppendDropTake writes a drop-take (see AmendDropTake): remove one take from the
// message-scoped variant at index, keeping the rest swipeable.
func (s *Session) AppendDropTake(index, drop int, reason string) error {
	if s == nil {
		return nil
	}
	if err := s.flushPendingGreeting(); err != nil {
		return err
	}
	if err := s.bumpFormatForAmend(); err != nil {
		return err
	}
	return s.writeLine(sessionLine{Type: recordAmend, Amend: &sessionAmend{
		Op: AmendDropTake, Index: index, Variant: drop, Reason: reason,
	}})
}

// SeedGreetingVariants seeds a set of opening messages as message-0 swipe
// variants (takes) on a fresh session, with take `active` live — so a card's
// first_mes + alternate_greetings all pre-seed and the user can swipe between
// openings. Each message becomes its own single-message take at index 0: the
// first is appended, then each subsequent one retracts the current opening as a
// take and appends the next, and finally the active take is selected. The walker
// (session_walk.go) reconstructs the takes from these rows exactly as it does a
// retry's. A single greeting seeds one message with no variants; empty is a
// no-op. Returns the active message so the caller can prime the live transcript.
func (s *Session) SeedGreetingVariants(greetings []provider.Message, active int) (provider.Message, error) {
	if s == nil || len(greetings) == 0 {
		return provider.Message{}, nil
	}
	if active < 0 || active >= len(greetings) {
		active = 0
	}
	if err := s.AppendMessage(greetings[0]); err != nil {
		return provider.Message{}, err
	}
	for i := 1; i < len(greetings); i++ {
		if err := s.AppendAmend(AmendRetract, 0, nil, "greeting-variant"); err != nil {
			return provider.Message{}, err
		}
		if err := s.AppendMessage(greetings[i]); err != nil {
			return provider.Message{}, err
		}
	}
	// After the loop the last-appended greeting is live; select the intended one
	// unless it is already active (avoids a redundant amend for the common case).
	if active != len(greetings)-1 {
		if err := s.AppendSelect(0, active, "greeting-variant"); err != nil {
			return provider.Message{}, err
		}
	}
	return greetings[active], nil
}

// pendingGreeting is a deferred opening set held in memory until the first
// durable append flushes it — see Session.pendingGreeting.
type pendingGreeting struct {
	msgs   []provider.Message
	active int
}

// DeferGreetingVariants seeds the opening set (first_mes + alternate_greetings)
// as a PENDING greeting held in memory only — no disk write — and returns the
// active message so the caller can prime the live transcript. The file stays
// meta-only (messagesAppended == 0), so Close/PruneEmptySessions treat it as an
// empty draft: a chat the user opens to preview but never sends into is discarded
// rather than cluttering the list. The first durable append flushes it
// (flushPendingGreeting) before writing its own row, so the on-disk shape is
// identical to a seed-at-build — only *when* it is written moves. Mirrors
// SeedGreetingVariants' active clamp and return; empty is a no-op.
func (s *Session) DeferGreetingVariants(greetings []provider.Message, active int) (provider.Message, error) {
	if s == nil || len(greetings) == 0 {
		return provider.Message{}, nil
	}
	if active < 0 || active >= len(greetings) {
		active = 0
	}
	s.pgMu.Lock()
	s.pendingGreeting = &pendingGreeting{msgs: append([]provider.Message(nil), greetings...), active: active}
	s.pgMu.Unlock()
	return greetings[active], nil
}

// flushPendingGreeting writes a deferred greeting to disk (via
// SeedGreetingVariants) if one is pending, then clears it. It runs at the top of
// every durable-content appender BEFORE that appender takes writeMu, so the first
// real turn persists the greeting ahead of its own row. Niling the pending set
// BEFORE the write makes it recursion-safe: SeedGreetingVariants re-enters the
// appenders, but they find nothing pending and no-op. Cheap (a lock + nil check)
// when nothing is pending — the common path for every non-draft append.
func (s *Session) flushPendingGreeting() error {
	if s == nil {
		return nil
	}
	s.pgMu.Lock()
	pg := s.pendingGreeting
	s.pendingGreeting = nil
	s.pgMu.Unlock()
	if pg == nil {
		return nil
	}
	_, err := s.SeedGreetingVariants(pg.msgs, pg.active)
	return err
}

// HasPendingGreeting reports whether the greeting is still deferred (in memory,
// not on disk) — i.e. the session is an unpromoted draft with no real turn yet.
func (s *Session) HasPendingGreeting() bool {
	if s == nil {
		return false
	}
	s.pgMu.Lock()
	defer s.pgMu.Unlock()
	return s.pendingGreeting != nil
}

// SetPendingGreetingActive updates which take of a deferred greeting is active (a
// pre-first-turn swipe) with no disk write, so swiping a preview does not promote
// the draft. Returns the new active message and true when a greeting was pending.
func (s *Session) SetPendingGreetingActive(active int) (provider.Message, bool) {
	if s == nil {
		return provider.Message{}, false
	}
	s.pgMu.Lock()
	defer s.pgMu.Unlock()
	pg := s.pendingGreeting
	if pg == nil {
		return provider.Message{}, false
	}
	if active < 0 || active >= len(pg.msgs) {
		active = 0
	}
	pg.active = active
	return pg.msgs[active], true
}

// ReplacePendingGreeting swaps the whole deferred opening set — for a pre-first-
// turn user-persona rename, which re-derives the greeting with the new {{user}}.
// A no-op unless a greeting is still pending; returns the new active message and
// whether it applied.
func (s *Session) ReplacePendingGreeting(greetings []provider.Message, active int) (provider.Message, bool) {
	if s == nil || len(greetings) == 0 {
		return provider.Message{}, false
	}
	s.pgMu.Lock()
	defer s.pgMu.Unlock()
	if s.pendingGreeting == nil {
		return provider.Message{}, false
	}
	if active < 0 || active >= len(greetings) {
		active = 0
	}
	s.pendingGreeting = &pendingGreeting{msgs: append([]provider.Message(nil), greetings...), active: active}
	return greetings[active], true
}

// AppendImageExclusion writes a directive telling future loads to drop the
// image whose raw bytes hash to sha256Hex — every copy of it (the tool result
// and the codex mirror both match) — replacing it with a short note. Append
// only: the original image rows stay in the file for audit, but the loader
// applies the exclusion when it rebuilds the transcript, so a resumed session
// never re-sends a provider-rejected image and pays the recovery only once.
func (s *Session) AppendImageExclusion(sha256Hex, reason string) error {
	if s == nil || sha256Hex == "" {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordDirective, Directive: &sessionDirective{
		Op: directiveExcludeImage, SHA256: sha256Hex, Reason: reason,
	}})
}

// AppendEscalation records that the stuck-loop hatch escalated, or tried to (see
// escalationRecord). It accompanies the "meta" row the swap itself produced
// (UpdateModel), carrying the "why" that meta row cannot: a harness escalation is
// otherwise indistinguishable from a user /model switch. Append only and purely
// informational — the loader skips it, so it never affects the rebuilt
// transcript or resume. Written only when an escalation target is configured
// (the observer fires nowhere else).
func (s *Session) AppendEscalation(rec EscalationRecord) error {
	if s == nil {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordEscalation, Escalation: &escalationRecord{
		Reason:      rec.Reason,
		Tool:        rec.Tool,
		FromModel:   rec.FromModel,
		ToProvider:  rec.ToProvider,
		ToModel:     rec.ToModel,
		Auto:        rec.Auto,
		Disposition: string(rec.Disposition),
		Detail:      rec.Detail,
	}})
}

// AppendStall records that the stuck-loop detector nudged (see stallRecord). It
// is the durable trace of rung 1 — the nudge itself only rides the ephemeral
// tail — sitting inline where the loop happened. Append only and informational;
// the loader skips it, so it never affects the rebuilt transcript or resume.
func (s *Session) AppendStall(rec StallRecord) error {
	if s == nil {
		return nil
	}
	row := &stallRecord{Axis: rec.Axis, Tool: rec.Tool, Detail: rec.Detail}
	if rec.Rung > 1 {
		row.Rung = rec.Rung // omitted for rung 1, which is what absent already means
	}
	return s.writeLine(sessionLine{Type: recordStall, Stall: row})
}

// AppendRetry records that a provider call was retried after a transient
// failure (see retryRecord). The durable half of EvRetry, and the only trace a
// SUCCESSFUL retry leaves — the recovered attempt is dropped from the
// transcript, and the error sidecar only ever sees failures nothing recovered.
//
// Append only and informational; the loader skips it, so it never affects the
// rebuilt transcript or resume.
func (s *Session) AppendRetry(rec RetryRecord) error {
	if s == nil {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordRetry, Retry: &retryRecord{
		Phase:    string(rec.Phase),
		Provider: rec.Provider,
		Attempt:  rec.Attempt,
		Max:      rec.Max,
		DelayMS:  rec.Delay.Milliseconds(),
		Error:    rec.Err,
	}})
}

// AppendTail records the ephemeral tail's composition at the moment it changed
// (see tailRecord). The agent fires this on change, not per request, so this
// writes a handful of rows per session and can afford to carry each block's
// text — which is the part that matters, since the review that motivated the row
// turned on a note's WORDING and no size would have shown it.
//
// Append only and informational; the loader skips it, so it never affects the
// rebuilt transcript or resume.
func (s *Session) AppendTail(rec TailRecord) error {
	if s == nil {
		return nil
	}
	// Non-nil even when empty, so the row encodes as [] rather than null: an
	// empty composition is the row that ends the previous one, and must not read
	// as a row whose payload went missing.
	rows := make([]tailBlockRow, 0, len(rec.Blocks))
	for _, b := range rec.Blocks {
		row := tailBlockRow{ID: b.ID, Bytes: len(b.Text), Text: b.Text}
		if len(b.Text) > tailTextCap {
			// Bytes above already holds the true size; only Text is clipped.
			row.Text, row.Truncated = b.Text[:tailTextCap], true
		}
		rows = append(rows, row)
	}
	return s.writeLine(sessionLine{Type: recordTail, Tail: &tailRecord{Blocks: rows}})
}

// SessionIDFromPath turns a session file path into its stable id — the file
// name without the .jsonl extension.
//
// Lives here, rather than in the build package that long owned it, because the
// tool layer needs it too and build imports tools. A second copy over there
// would have been a twin of the kind this repository keeps paying for.
func SessionIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// AppendPrefixDivergence records that the cacheable prefix was rebuilt rather
// than extended (see prefixDivergenceRecord). Append only and informational; the
// loader skips it, so it never affects the rebuilt transcript or resume.
func (s *Session) AppendPrefixDivergence(d PrefixDivergence) error {
	if s == nil {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordPrefix, Prefix: &prefixDivergenceRecord{
		Rung:         d.Rung,
		Label:        d.Label,
		Messages:     d.MsgCount,
		PrevMessages: d.PrevMsgCount,
		CachedTokens: d.CachedTokens,
	}})
}

// AppendCacheCliff records that a provider-side cache collapse opened
// (ongoing) or closed (see cacheCliffRecord). Append only and informational;
// the loader skips it, so it never affects the rebuilt transcript or resume.
//
// Takes ongoing explicitly rather than reading cc.Ongoing: the closing row
// reports the totals the run REACHED, and the detector's end-of-run event
// carries the zero CacheCliff by contract. The caller holds the last ongoing
// event and passes it back here with ongoing=false.
func (s *Session) AppendCacheCliff(cc CacheCliff, ongoing bool) error {
	if s == nil {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordCliff, Cliff: &cacheCliffRecord{
		Ongoing:      ongoing,
		Dispatches:   cc.Dispatches,
		RereadTokens: cc.RereadTokens,
	}})
}

// AppendTransport records which connection and edge a dispatch rode — the
// half of a cache miss the usage row cannot see. Written per dispatch on
// providers that report it (openai-codex today); the analysis it exists for
// reads it against the usage rows either side.
func (s *Session) AppendTransport(ti provider.TransportInfo) error {
	if s == nil {
		return nil
	}
	rec := ti
	return s.writeLine(sessionLine{Type: recordNet, Net: &rec})
}

// AppendToolGroupActivation records that a capability group was activated, so a
// resume can re-mark it (Load collects these into Session.ActiveToolGroups).
//
// Written per activation rather than as a replace-the-set row: the log is
// append-only, activation is monotonic within a session, and one row per event
// keeps WHEN it happened readable against the usage rows either side of it —
// which is how the cost of an activation was measured in the first place.
func (s *Session) AppendToolGroupActivation(group string) error {
	if s == nil || group == "" {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordToolGroup, ToolGroup: &toolGroupRecord{Group: group}})
}

// AppendUsage writes a usage row to the session.
func (s *Session) AppendUsage(u, cum provider.Usage) error {
	return s.appendUsage(u, cum, false)
}

// AppendDelegatedUsage writes a usage row for spend a SUB-AGENT incurred on this
// session's behalf, marked so readers can separate it from this session's own
// requests.
//
// Same row type on purpose: the cumulative figure must stay a single coherent
// timeline, so a crash still recovers the true total. Only the attribution
// differs — and it has to be on the row, because a fresh sub-agent's usage
// (transcript-sized input, nothing cached) is otherwise indistinguishable from
// this session's cache collapsing.
func (s *Session) AppendDelegatedUsage(u, cum provider.Usage) error {
	return s.appendUsage(u, cum, true)
}

// appendUsage is the single writer behind both, so the timestamp cannot be
// forgotten by whichever one the next caller reaches for — the same reason every
// meta writer funnels through writeMeta. See sessionLine.At.
func (s *Session) appendUsage(u, cum provider.Usage, delegated bool) error {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	return s.writeLine(sessionLine{Type: "usage", Usage: &u, Cumulative: &cum, Delegated: delegated, At: &now})
}

// SessionError is one row of the error sidecar (see LogError). Exported
// because the sidecar is where a session's provider failures live and nothing
// else records them: a turn that died on a 401 or an overload leaves the
// transcript looking merely quiet, and the only evidence of what happened is
// here. Reading it back is what lets terva explain its own bad sessions.
type SessionError struct {
	Time     time.Time `json:"time"`
	Error    string    `json:"error"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
}

// sessionError is the internal alias LogError writes through.
type sessionError = SessionError

// sessionErrorScanCeiling bounds one sidecar read. The file holds one short
// line per provider failure, so a healthy session's is a few hundred bytes;
// this only ever trips on a pathological retry storm.
const sessionErrorScanCeiling = 1 << 20

// ReadSessionErrors reads a transcript's error sidecar in file order. A
// missing sidecar is not an error — it is the normal case, meaning the session
// had no provider failures — and returns no rows. Corrupt rows are skipped
// like everywhere else in the loader, so a partially-written tail still yields
// everything before it.
func ReadSessionErrors(transcriptPath string) ([]SessionError, error) {
	path := ErrorLogPathFor(transcriptPath)
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []SessionError
	walkErr := forEachJSONLLineBounded(f, jsonlPerLineCeiling, sessionErrorScanCeiling, nil, func(line []byte) error {
		var row SessionError
		if err := json.Unmarshal(line, &row); err == nil && strings.TrimSpace(row.Error) != "" {
			out = append(out, row)
		}
		return nil
	})
	if errors.Is(walkErr, errJSONLCumulative) {
		walkErr = nil // bounded read: everything up to the ceiling still counts
	}
	if walkErr != nil {
		return out, walkErr
	}
	return out, nil
}

// ErrorLogPath returns the path of the session's error sidecar — the
// transcript path with its .jsonl suffix replaced by .errors.jsonl, so the
// two sort together in a directory listing. Empty when the session has no
// file (live-only conversations).
func (s *Session) ErrorLogPath() string {
	if s == nil || s.Path == "" {
		return ""
	}
	return ErrorLogPathFor(s.Path)
}

// ErrorLogPathFor derives the error-sidecar path for a transcript path,
// for callers that hold only the path (e.g. deleting a session that isn't
// open). Keep in sync with ErrorLogPath; empty in, empty out.
func ErrorLogPathFor(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	return strings.TrimSuffix(transcriptPath, ".jsonl") + errorSidecarSuffix
}

// LogError records a turn/provider failure to the session's error sidecar — a
// file ALONGSIDE the transcript, never inside it (the transcript's record
// vocabulary is a contract for replay/resume/compaction). The file is created
// lazily on the first error, so a clean session never leaves an empty sidecar.
// Stamped with the session's current provider/model. Best-effort and
// non-fatal: a failure to record an error must not compound the original one,
// so the write error is returned for callers that care but is safe to ignore.
func (s *Session) LogError(errText string) error {
	if s == nil || s.Path == "" || strings.TrimSpace(errText) == "" {
		return nil
	}
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.errFile == nil {
		f, err := privfs.OpenFile(s.ErrorLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY)
		if err != nil {
			return err
		}
		s.errFile = f
	}
	// Redact secret-shaped substrings and bound the length before persisting:
	// provider/auth errors can embed Authorization headers, tokened callback
	// URLs, or whole response bodies, and the sidecar is a durable local file.
	row := sessionError{Time: time.Now().UTC(), Error: redactErrorForSidecar(errText), Provider: s.Meta.Provider, Model: s.Meta.Model}
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	// Direct write + newline (no bufio): errors are rare and must survive a
	// crash that skips Close, so we never leave a half-recorded failure buffered.
	if _, err := s.errFile.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// Close flushes and closes the session file. If the session was
// freshly created in this process and never had any messages
// appended (the user opened terva, looked around, and exited without
// prompting), the file is deleted on close so the sessions list
// doesn't fill up with empty meta-only stubs.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	// Hold writeMu so a Close racing a late Append (multi-connection web) neither
	// flushes a half-written line nor reads messagesAppended mid-update.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	flushErr := s.buf.Flush()
	closeErr := s.writer.Close()
	// Close the error sidecar if this session ever opened one (its writes are
	// unbuffered, so there's nothing to flush — just release the handle).
	s.errMu.Lock()
	if s.errFile != nil {
		_ = s.errFile.Close()
		s.errFile = nil
	}
	s.errMu.Unlock()
	if s.freshFile && s.messagesAppended == 0 {
		// Best-effort cleanup. We deliberately don't propagate the
		// remove error: if it fails (file already gone, perms changed)
		// the worst case is one stale empty file in the listing.
		_ = os.Remove(s.Path)
		// Keep the sidecars paired with the transcript: if the empty transcript
		// is discarded, drop them too rather than orphan them. The list is
		// sessionSidecars, so a new sidecar is dropped here without editing this.
		for _, sc := range SessionSidecarPaths(s.Path) {
			_ = os.Remove(sc)
		}
	}
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (s *Session) writeLine(row sessionLine) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeLineLocked(row)
}

// marshalLine encodes a session row, filling its write time when it did not set
// one itself.
//
// Every path that puts a sessionLine on disk goes through here: writeLineLocked
// for a live session, and the import/branch writers that build a new file by
// hand. Those writers are the reason it is a helper rather than four lines
// inside writeLineLocked — a second writer that formats its own rows is exactly
// how a format rule quietly stops being universal, and the guard reading a live
// session would never have seen it.
//
// A row that arrives already stamped keeps its own time. Import and branch copy
// message rows whose payload time is the ORIGINAL moment; the stamp added here
// is when this file was materialized, and the two are meant to differ.
func marshalLine(row sessionLine) ([]byte, error) {
	if row.At == nil {
		now := time.Now().UTC()
		row.At = &now
	}
	return json.Marshal(row)
}

// writeLineLocked is writeLine's body; the caller must hold writeMu. Used by the
// Append* methods that also mutate messagesAppended under the same lock, so the
// buffer write and the counter update are one atomic critical section.
func (s *Session) writeLineLocked(row sessionLine) error {
	b, err := marshalLine(row)
	if err != nil {
		return err
	}
	if _, err := s.buf.Write(b); err != nil {
		return err
	}
	if err := s.buf.WriteByte('\n'); err != nil {
		return err
	}
	return s.buf.Flush()
}

// ---- content (de)serialization ----
//
// provider.Content is an interface; encoding/json drops type
// information. v2 files carry an explicit "type" on every block
// (wireBlock); v1 files are rebuilt from discriminated fields. Both
// paths run through hydrateMessageObject, which reports (not
// swallows) corrupt and unknown blocks via loadReport.

// loadReport accumulates everything OpenSession had to skip or guess
// at, so callers can surface it instead of silently losing data.
type loadReport struct {
	corruptLines  int            // whole rows that failed to parse
	corruptBlocks int            // content blocks that failed to parse
	unknownBlocks map[string]int // typed blocks with an unrecognized type
	newerFormat   int            // file's format_version when > ours
}

func (r *loadReport) noteUnknown(blockType string) {
	if r == nil {
		return
	}
	if r.unknownBlocks == nil {
		r.unknownBlocks = make(map[string]int)
	}
	r.unknownBlocks[blockType]++
}

func (r *loadReport) noteCorruptBlock() {
	if r != nil {
		r.corruptBlocks++
	}
}

// warnings renders the report as human-readable lines, empty when
// nothing was skipped.
func (r *loadReport) warnings(path string) []string {
	if r == nil {
		return nil
	}
	var out []string
	base := filepath.Base(path)
	if r.newerFormat > 0 {
		out = append(out, fmt.Sprintf("session %s: written by a newer terva (format v%d, this build reads v%d); loaded best-effort", base, r.newerFormat, sessionFormatVersionAmend))
	}
	if r.corruptLines > 0 {
		out = append(out, fmt.Sprintf("session %s: %d corrupt line(s) skipped", base, r.corruptLines))
	}
	if r.corruptBlocks > 0 {
		out = append(out, fmt.Sprintf("session %s: %d corrupt content block(s) skipped", base, r.corruptBlocks))
	}
	for typ, n := range r.unknownBlocks {
		out = append(out, fmt.Sprintf("session %s: %d block(s) of unknown type %q skipped", base, n, typ))
	}
	return out
}

func hydrateCompaction(lineBytes []byte, rep *loadReport) ([]provider.Message, error) {
	var row struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(lineBytes, &row); err != nil {
		return nil, err
	}
	messages := make([]provider.Message, 0, len(row.Messages))
	for _, raw := range row.Messages {
		msg, err := hydrateMessageObject(raw, rep)
		if err != nil {
			rep.noteCorruptBlock()
			continue
		}
		if len(msg.Content) > 0 {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}

func hydrateMessage(lineBytes []byte, rep *loadReport) (provider.Message, error) {
	var row struct {
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(lineBytes, &row); err != nil {
		return provider.Message{}, err
	}
	return hydrateMessageObject(row.Message, rep)
}

// decodeWireBlock rebuilds one v2 typed block. ok=false means the
// type is unrecognized (written by a newer terva) — the caller records
// it and skips, rather than degrading it to an empty text block.
func decodeWireBlock(b wireBlock) (provider.Content, bool) {
	switch b.Type {
	case blockText:
		return provider.TextBlock{Text: b.Text}, true
	case blockImage:
		return provider.ImageBlock{MimeType: b.MimeType, Data: b.Data, ID: b.ImageID}, true
	case blockToolCall:
		return provider.ToolCallBlock{ID: b.ID, Name: b.Name, Arguments: b.Arguments, RawArguments: b.RawArguments, Signature: b.Signature}, true
	case blockToolResult:
		block := provider.ToolResultBlock{CallID: b.CallID, IsError: b.IsError}
		for _, inner := range b.Content {
			if c, ok := decodeWireBlock(inner); ok {
				block.Content = append(block.Content, c)
			}
		}
		return block, true
	case blockReasoning:
		return provider.NormalizeLegacyReasoningShape(provider.ReasoningBlock{
			ID: b.ReasoningID, Summary: b.Summary, Encrypted: b.Encrypted, Shape: b.Shape,
		}), true
	case blockCompaction:
		return provider.CompactionBlock{ID: b.ID, Encrypted: b.Encrypted, Provider: b.Provider}, true
	}
	return nil, false
}

func hydrateMessageObject(rawMessage []byte, rep *loadReport) (provider.Message, error) {
	var row struct {
		Role    provider.Role     `json:"role"`
		Content []json.RawMessage `json:"content"`
		Time    time.Time         `json:"time"`
		Meta    map[string]string `json:"meta,omitempty"`
	}
	if err := json.Unmarshal(rawMessage, &row); err != nil {
		return provider.Message{}, err
	}
	msg := provider.Message{Role: row.Role, Time: row.Time, Meta: row.Meta}
	for _, raw := range row.Content {
		// v2 path: an explicit type discriminator decides the block.
		var typed wireBlock
		if err := json.Unmarshal(raw, &typed); err == nil && typed.Type != "" {
			c, ok := decodeWireBlock(typed)
			if !ok {
				rep.noteUnknown(typed.Type)
				continue
			}
			msg.Content = append(msg.Content, c)
			continue
		}

		// v1 fallback: discriminate by field presence.
		var head struct {
			Text        string `json:"text"`
			MimeType    string `json:"mime_type"`
			Data        []byte `json:"data"`
			ID          string `json:"id"`
			Name        string `json:"name"`
			CallID      string `json:"call_id"`
			ReasoningID string `json:"reasoning_id"`
			Summary     string `json:"summary"`
			Encrypted   string `json:"encrypted_content"`
			Shape       string `json:"shape"`
			// ToolCallBlock also has Arguments, ToolResultBlock has Content + IsError
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			rep.noteCorruptBlock()
			continue
		}
		switch {
		case head.ReasoningID != "" || head.Encrypted != "":
			msg.Content = append(msg.Content, provider.NormalizeLegacyReasoningShape(provider.ReasoningBlock{
				ID:        head.ReasoningID,
				Summary:   head.Summary,
				Encrypted: head.Encrypted,
				Shape:     head.Shape,
			}))
		case head.Name != "" && head.ID != "":
			var tc struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(raw, &tc)
			msg.Content = append(msg.Content, provider.ToolCallBlock{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		case head.CallID != "":
			var tr struct {
				CallID  string            `json:"call_id"`
				Content []json.RawMessage `json:"content"`
				IsError bool              `json:"is_error"`
			}
			_ = json.Unmarshal(raw, &tr)
			block := provider.ToolResultBlock{CallID: tr.CallID, IsError: tr.IsError}
			for _, c := range tr.Content {
				var inner struct {
					Text     string `json:"text"`
					MimeType string `json:"mime_type"`
					Data     []byte `json:"data"`
				}
				_ = json.Unmarshal(c, &inner)
				if inner.MimeType != "" {
					block.Content = append(block.Content, provider.ImageBlock{MimeType: inner.MimeType, Data: inner.Data})
				} else {
					block.Content = append(block.Content, provider.TextBlock{Text: inner.Text})
				}
			}
			msg.Content = append(msg.Content, block)
		case head.MimeType != "":
			msg.Content = append(msg.Content, provider.ImageBlock{MimeType: head.MimeType, Data: head.Data})
		default:
			msg.Content = append(msg.Content, provider.TextBlock{Text: head.Text})
		}
	}
	return msg, nil
}
