package ctrlproto

import "terva.sh/terva/packages/core"

// Method names a command carried in a [KindCmd] frame. Names are grouped by
// method group; the group a method belongs to gates whether a peer may call it
// (see [Contract]).
type Method string

const (
	// --- conversation group ---

	MethodPrompt        Method = "prompt"         // params PromptParams
	MethodQueue         Method = "queue"          // params QueueParams
	MethodQueueSet      Method = "queue.set"      // params QueueSetParams (edit/cancel pending queue)
	MethodCancel        Method = "cancel"         // no params
	MethodCompact       Method = "compact"        // no params; summarize+replace the transcript
	MethodClear         Method = "clear"          // no params; wipe the transcript (no summary)
	MethodMessageEdit   Method = "message.edit"   // params MessageEditParams; edit a message in place
	MethodMessageDelete Method = "message.delete" // params MessageDeleteParams; remove a message
	MethodTurnSwipe     Method = "turn.swipe"     // params TurnSwipeParams; switch the tail span's active variant
	MethodTurnRetry     Method = "turn.retry"     // params TurnRetryParams; regenerate the last response, keeping the old
	MethodTurnContinue  Method = "turn.continue"  // params TurnContinueParams; extend the trailing assistant (optional ContinueController)
	MethodApprove       Method = "approve"        // params ApproveParams
	MethodAnswer        Method = "answer"         // params AnswerParams
	MethodSubscribe     Method = "subscribe"      // no params; streams events for the frame's sess
	MethodUnsubscribe   Method = "unsubscribe"    // no params

	// --- session group ---

	MethodSessionsList  Method = "sessions.list"   // result SessionsResult
	MethodSessionCreate Method = "sessions.create" // params CreateOpts, result SessionResult
	MethodSessionResume Method = "sessions.resume" // result SessionResult (sess in frame)
	MethodSessionFork   Method = "sessions.fork"   // params SessionForkParams, result SessionResult (parent sess in frame)
	MethodSessionRename Method = "sessions.rename" // params RenameParams (sess in frame)
	// MethodSessionGenerateTitle regenerates sess's title from its transcript
	// with a one-shot model call — the on-demand sibling of the automatic
	// auto_title pass, and the backfill for old untitled sessions. BLOCKS on
	// the model (same synchronous posture as compact). An explicit request
	// overwrites whatever title exists, manual renames included.
	MethodSessionGenerateTitle Method = "sessions.generate_title" // result GenerateTitleResult (sess in frame)
	MethodSessionDelete        Method = "sessions.delete"         // no params (sess in frame)
	// MethodSessionArchive compresses sess's transcript into the directory's
	// archive: it leaves every listing without leaving the disk. The middle
	// verb between keeping a session in the picker forever and deleting it.
	MethodSessionArchive      Method = "sessions.archive"       // result ArchivedSessionInfo (sess in frame)
	MethodSessionsArchived    Method = "sessions.archived"      // result ArchivedSessionsResult; read-only
	MethodSessionRestore      Method = "sessions.restore"       // params RestoreSessionParams, result SessionInfo
	MethodSessionDiscardDraft Method = "sessions.discard_draft" // no params (sess in frame); served by an optional DraftController
	MethodUsageGet            Method = "usage.get"              // result UsageResult (sess in frame)
	MethodUsageSnapshot       Method = "usage.snapshot"         // params UsageSnapshotParams, result UsageSnapshotResult (sess in frame)
	MethodResetsList          Method = "usage.resets.list"      // result ResetsListResult (sess in frame); read-only
	MethodContextGet          Method = "context.get"            // result ContextResult (sess in frame)
	MethodContextNode         Method = "context.node"           // params ContextNodeParams, result ContextNodeResult (sess in frame)
	MethodSurfacesList        Method = "surfaces.list"          // result SurfacesResult (sess in frame)
	MethodSurfaceGet          Method = "surface.get"            // params SurfaceGetParams, result SurfaceResult
	MethodSurfaceAction       Method = "surface.action"         // params SurfaceActionParams (sess in frame)
	MethodI18nCatalog         Method = "i18n.catalog"           // params I18nCatalogParams, result I18nCatalogResult (session-independent)
	MethodFilesList           Method = "files.list"             // params FilesListParams, result FilesListResult (session-independent)

	// The workflow dashboard (optional; served only by a WorkflowsController).
	// Read-only and session-independent: a run is a workspace artifact, not a
	// session, and observing one needs no authority — which is why these sit
	// here rather than in the control group. See workflows.go.
	MethodWorkflowsList Method = "workflows.list" // result WorkflowRunsResult; read-only
	MethodWorkflowsGet  Method = "workflows.get"  // params WorkflowGetParams, result WorkflowRunView; read-only

	// MethodConversationReveal returns the turns a compaction checkpoint
	// summarized away. They are not gone: a compaction row is an append-only
	// checkpoint the loader honors, never a rewrite of the rows above it, so the
	// superseded messages are still in the session file. This reads them back so
	// a client can keep them in the scrollback above the compaction divider —
	// dimmed, out of context, but READABLE — instead of the conversation simply
	// appearing to have lost its history. See docs/proposals/scrollback-history.md.
	//
	// The span it returns EXCLUDES the tail the checkpoint kept verbatim, so it
	// cannot overlap the live transcript: a client prepends it and is done. No
	// dedup, no reordering, no identity reconciliation. That property is what
	// keeps this cheap, and it is pinned by a test.
	//
	// In the SESSION group, not the conversation group, for the reason
	// MethodAuthProviders gives: it grants no authority. It hands back the same
	// transcript a subscriber already streams, only older. Every verb in the
	// conversation group MUTATES a turn; this one reads.
	//
	// Requires a persisted session (an ephemeral one has no file to read):
	// CodeNotFound, which the client renders as "earlier turns unavailable"
	// rather than an error.
	MethodConversationReveal Method = "conversation.reveal" // params RevealParams, result RevealResult (sess in frame)

	// MethodConversationHistory pages BACKWARD through the live transcript — the
	// part a windowed snapshot did not carry (see FeatureHistoryWindow).
	//
	// Distinct from conversation.reveal, and the distinction is the whole shape of
	// this system: history walks within the transcript the model still has, served
	// from memory; reveal goes BEHIND a compaction, to turns the model no longer has,
	// served from the session file. A client scrolling up uses history until Base
	// reaches 0, then finds the compaction divider and uses reveal.
	//
	// Refuses a stale epoch (CodeConflict) rather than serving indexes into a
	// transcript that has since been replaced — which, silently, would hand back
	// somebody else's messages.
	MethodConversationHistory Method = "conversation.history" // params HistoryParams, result HistoryResult (sess in frame)

	// MethodAuthProviders reports the workspace's MODEL-PROVIDER credential
	// state: who terva can log into, who it is logged into, and how. Read-only
	// and session-independent; it reveals state, never material (see
	// ProvidersView — nothing it returns is a secret).
	//
	// It sits in the SESSION group, not the control group, because it grants no
	// authority: a client that can read your transcripts already learns which
	// provider you use from the first usage event. The verbs that CHANGE a
	// credential are a separate group, at a higher authority, and do not exist
	// yet — see docs/proposals/web-provider-login.md.
	MethodAuthProviders Method = "auth.providers" // result ProvidersView (session-independent)

	// --- auth group (optional; advertised only when the daemon will serve a
	// login). These CHANGE the credential terva uses to reach a model provider,
	// which is why they are not in the session group with the read verb above.
	MethodAuthLoginStart  Method = "auth.login.start"  // params AuthLoginStartParams, result AuthFlowStep
	MethodAuthLoginSubmit Method = "auth.login.submit" // params AuthLoginSubmitParams; carries the secret
	MethodAuthLoginCancel Method = "auth.login.cancel" // params AuthFlowRef
	MethodAuthLogout      Method = "auth.logout"       // params AuthLogoutParams
	// MethodAuthEndpointRemove forgets a named openai-compatible endpoint. In the
	// auth group, not the session one: defining or forgetting a backend the agent
	// will talk to is at least as privileged as holding its key.
	MethodAuthEndpointRemove Method = "auth.endpoint.remove" // params AuthEndpointRemoveParams

	// Per-model overrides (models.json): context window, max tokens, temperature.
	// In the control group — they change what the running agent talks to, and a
	// base URL among them points it at a different machine entirely.
	MethodModelParams      Method = "models.params"       // params ModelParamsParams, result ModelParamsView
	MethodModelParamsSet   Method = "models.params.set"   // params ModelParamsSetParams
	MethodModelParamsReset Method = "models.params.reset" // params ModelParamsParams

	// Side chat: an ephemeral, tool-less completion against a FROZEN snapshot of
	// a session, leaving no trace in its transcript. Backs the /btw overlay.
	MethodSideChatOpen  Method = "sidechat.open"  // result SideChatOpenResult (sess in frame)
	MethodSideChatAsk   Method = "sidechat.ask"   // params SideChatAskParams, result SideChatAskResult
	MethodSideChatClose Method = "sidechat.close" // params SideChatCloseParams

	// Suggest a reply (optional; served only by a SuggestController). Drafts the
	// player's next message from the session transcript + user persona; a composer
	// aid that never writes the transcript. Session rides the frame's sess.
	MethodSuggestReply Method = "suggest.reply" // params SuggestParams, result SuggestResult (sess in frame)

	// --- control group ---

	MethodModelsList    Method = "models.list"     // result ModelsResult
	MethodModelSwitch   Method = "models.switch"   // params ModelSwitchParams (sess in frame)
	MethodModelFavorite Method = "models.favorite" // params FavoriteParams
	// MethodModelSetDefault persists a model as the default for NEW sessions.
	// Distinct from MethodModelSwitch, which changes one live session and
	// touches no config: this writes to disk and outlives the daemon, which is
	// why it sits in the control group rather than the session one.
	MethodModelSetDefault Method = "models.set_default" // params SetDefaultParams
	MethodTrust           Method = "control.trust"      // params TrustParams; grant Workspace Trust to cwd
	MethodUntrust         Method = "control.untrust"    // no params; revoke Workspace Trust for cwd
	MethodRestart         Method = "control.restart"    // no params; re-execs the daemon (Tier-1 self-restart)
	// MethodResetsConsume redeems a usage-reset credit — irreversible and spends
	// a scarce provider grant, so it sits in the control group (categorically
	// higher authority than the read-only usage.resets.list in the session
	// group). The host still requires explicit user confirmation before calling.
	MethodResetsConsume Method = "usage.resets.consume" // params ResetConsumeParams, result ResetConsumeResult (sess in frame)

	// The character-card library (optional; served only by a CardsController).
	// In the control group: a card is workspace-wide state that outlives any one
	// session, like the model defaults above.
	MethodCardsList     Method = "cards.list"     // result CardsListResult
	MethodCardsGet      Method = "cards.get"      // params CardGetParams, result CardView
	MethodCardsImport   Method = "cards.import"   // params CardImportParams, result CardView
	MethodCardsEdit     Method = "cards.edit"     // params CardEditParams, result CardView
	MethodCardsDelete   Method = "cards.delete"   // params CardDeleteParams
	MethodCardsExport   Method = "cards.export"   // params CardExportParams, result CardExport
	MethodCardsLint     Method = "cards.lint"     // params CardLintParams, result CardLintResult
	MethodCardsFavorite Method = "cards.favorite" // params CardFavoriteParams
	MethodCardsHistory  Method = "cards.history"  // params CardHistoryParams, result CardHistoryResult
	MethodCardsRestore  Method = "cards.restore"  // params CardRestoreParams, result CardView
	MethodCardsRevision Method = "cards.revision" // params CardRevisionParams, result CardRevisionView

	// The LLM card doctor (optional; served only by a DoctorController). Reads a
	// card + its deterministic lint and proposes structured per-field edits.
	MethodCardsDoctor Method = "cards.doctor" // params DoctorParams, result DoctorResult

	// MethodSessionsDoctor runs the session doctor (Dramaturgi) over a live
	// immersive session — typed proposals in the card doctor's negotiation
	// shape. Session in the frame, like suggest.
	MethodSessionsDoctor Method = "sessions.doctor" // params SessionDoctorParams, result SessionDoctorResult (sess in frame)

	// MethodSessionsNextScene starts the next scene of a played session (SD5):
	// propose drafts the recap + cold open, commit creates the scene carrying
	// this session's live World state. Session in the frame.
	MethodSessionsNextScene Method = "sessions.next_scene" // params NextSceneParams, result NextSceneResult (sess in frame)

	// MethodSessionsRealize turns a cartographer conversation into a playable
	// world (creator C3): propose extracts the structure with a Kartoittaja
	// call, commit seeds a play session from it. Session in the frame.
	MethodSessionsRealize Method = "sessions.realize" // params RealizeParams, result RealizeResult (sess in frame)

	// MethodSessionsExport serializes a session for something outside terva.
	//
	// Deliberately ONE verb with a format discriminator rather than a verb per
	// format, matching cards.export: "markdown" renders the played scene as a
	// readable story, "tervasession" is the lossless raw-JSONL round-trip
	// docs/proposals/archive/tui-ctrlproto-parity.md reserved this name for.
	// Those are different audiences for the same act, not different acts.
	MethodSessionsExport Method = "sessions.export" // params SessionExportParams, result SessionExport (sess in frame)

	// The persona library (optional; served only by a PersonasController). In the
	// control group with cards; create/edit are trusted-tier (gated in the
	// workspace), so they change what the running agent IS, like the model
	// defaults and per-model overrides above.
	MethodPersonasList   Method = "personas.list"   // result PersonasListResult
	MethodPersonasGet    Method = "personas.get"    // params PersonaGetParams, result PersonaView
	MethodPersonasCreate Method = "personas.create" // params PersonaWriteParams, result PersonaView
	MethodPersonasEdit   Method = "personas.edit"   // params PersonaWriteParams, result PersonaView
	MethodPersonasDelete Method = "personas.delete" // params PersonaDeleteParams

	// Scene backdrops (optional; served only by a BackgroundsController). The
	// store is global like cards; bind is per-session (sess in frame).
	MethodBackgroundsList    Method = "backgrounds.list"     // result BackgroundsListResult
	MethodBackgroundsImport  Method = "backgrounds.import"   // params BackgroundImportParams, result BackgroundView
	MethodBackgroundsDelete  Method = "backgrounds.delete"   // params BackgroundDeleteParams
	MethodBackgroundBind     Method = "backgrounds.bind"     // params BackgroundBindParams (sess in frame)
	MethodBackgroundGenerate Method = "backgrounds.generate" // params BackgroundGenerateParams, result BackgroundView (sess in frame)

	// The author's note (optional; served only by a NoteController). Per-session
	// steering injected into the uncached tail — set live, no cache bust.
	MethodNoteSet Method = "note.set" // params NoteSetParams (sess in frame)

	// The user persona (optional; served only by a UserController). Binds who the
	// user is in the story — the description rides the free tail, the name rebuilds
	// the cached prefix.
	MethodUserBind Method = "user.bind" // params UserBindParams (sess in frame)

	// Saved user personas (optional; served only by a UserPersonasController). The
	// reusable user identities a chat can pre-fill or swap between.
	MethodUserPersonasList      Method = "userpersonas.list"        // result UserPersonasListResult
	MethodUserPersonaSave       Method = "userpersonas.save"        // params UserPersonaView, result UserPersonaView
	MethodUserPersonaDelete     Method = "userpersonas.delete"      // params UserPersonaRef
	MethodUserPersonaSetDefault Method = "userpersonas.set_default" // params UserPersonaRef

	// The play cast (optional; served only by a CastController). Edit a play
	// session's ensemble mid-scene; each change rebuilds the actor_spawn tool.
	MethodCastAdd    Method = "cast.add"    // params CastMemberParams (sess in frame)
	MethodCastRemove Method = "cast.remove" // params CastMemberParams (sess in frame)
	MethodCastSpeak  Method = "cast.speak"  // params CastSpeakParams (sess in frame); runs a turn

	// World lore (optional; served only by a WorldController). Session-scoped
	// shared lore (Worlds L1) — like the author's note it rides the uncached
	// per-turn tail, edited live with no cache bust. State reads ride
	// SessionInfo.WorldLore.
	MethodWorldLorePut    Method = "world.lore.put"    // params WorldLorePutParams (sess in frame)
	MethodWorldLoreDelete Method = "world.lore.delete" // params WorldLoreDeleteParams (sess in frame)
	// World settings (W3): today, the meta-narrator coordination mode. State
	// reads ride SessionInfo.Coordination.
	MethodWorldSet Method = "world.set" // params WorldSetParams (sess in frame)
	// Saved Worlds (W5): list the library, promote/update a session's World
	// into it, remove one. Membership reads ride SessionInfo.World. W5b adds
	// sessionless metadata edits (rename/description/cover) and the bundle
	// verbs — export a World with its cards embedded, import one elsewhere.
	MethodWorldsList             Method = "worlds.list"                // result WorldsListResult
	MethodWorldSave              Method = "worlds.save"                // params WorldSaveParams, result WorldView (sess in frame)
	MethodWorldDelete            Method = "worlds.delete"              // params WorldDeleteParams
	MethodWorldUpdate            Method = "worlds.update"              // params WorldUpdateParams, result WorldView
	MethodWorldSetCharacterModel Method = "worlds.set_character_model" // params WorldSetCharacterModelParams, result WorldView
	MethodWorldsExport           Method = "worlds.export"              // params WorldExportParams, result WorldExport
	MethodWorldsImport           Method = "worlds.import"              // params WorldImportParams, result WorldView

	// Card groups (optional; served only by a CardGroupsController). A group is a
	// terva-owned membership bucket for browsing the card library — workspace-wide
	// state like the worlds above, and distinct from a card's embedded CCv2 tags.
	MethodCardGroupsList      Method = "cardgroups.list"        // result CardGroupsResult
	MethodCardGroupSave       Method = "cardgroups.save"        // params CardGroupSaveParams, result GroupView
	MethodCardGroupDelete     Method = "cardgroups.delete"      // params CardGroupDeleteParams
	MethodCardGroupSetMembers Method = "cardgroups.set_members" // params CardGroupSetMembersParams, result GroupView

	// Session groups (optional; served only by a SessionGroupsController). The
	// same membership bucket over session ids, shown on both the Stage library and
	// the control panel. Workspace-wide state, like the card groups above.
	MethodSessionGroupsList      Method = "sessiongroups.list"        // result SessionGroupsResult
	MethodSessionGroupSave       Method = "sessiongroups.save"        // params SessionGroupSaveParams, result GroupView
	MethodSessionGroupDelete     Method = "sessiongroups.delete"      // params SessionGroupDeleteParams
	MethodSessionGroupSetMembers Method = "sessiongroups.set_members" // params SessionGroupSetMembersParams, result GroupView

	// Per-card default model (optional; served only by a CardModelController). A
	// card's default is terva-owned metadata like a group — a seed for a session
	// started from the card, NOT stored on the card. models.default_for is the one
	// authority that resolves the effective default for any context (Card → World →
	// Workspace); cardmodel.set writes or clears a card's rung.
	MethodModelDefaultFor Method = "models.default_for" // params DefaultForParams, result DefaultForResult
	MethodCardModelSet    Method = "cardmodel.set"      // params CardModelSetParams

	// Directed authorship (optional; served only by a DirectController). Posts an
	// approved character/narrator line INTO the transcript — the commit half of
	// Stage's suggest→review→post loop (Phase 6). Mutates the transcript (like an
	// edit), so it sits in the conversation group; the session rides the frame's
	// sess.
	MethodPostLine Method = "post.line" // params PostLineParams (sess in frame); appends an attributed message
	// MethodDirectTurn runs one turn steered by an out-of-character direction —
	// the "apply-as-direction" disposition (Phase 6b). Runs the model (a turn), so
	// it sits in the conversation group like a prompt.
	MethodDirectTurn Method = "direct.turn" // params DirectTurnParams (sess in frame); runs a steered turn
	// MethodTurnAdvance runs one turn on the transcript as it stands — Stage's
	// "▶ Advance". No params: the whole point is that nothing is injected. Runs the
	// model, so it sits in the conversation group like a prompt.
	MethodTurnAdvance Method = "turn.advance" // no params (sess in frame); runs a turn on the transcript as-is

	// Message-scoped variant cleanup (optional; served only by a VariantsController).
	MethodVariantsPrune Method = "variants.prune" // params VariantsPruneParams (sess in frame)
	MethodVariantsDrop  Method = "variants.drop"  // params VariantsDropParams (sess in frame)

	// --- replay group (optional; served only by a ReplayController) ---

	MethodReplayControl Method = "replay.control" // params ReplayControlParams, result ReplayStateResult (sess in frame)
	MethodReplayState   Method = "replay.state"   // result ReplayStateResult (sess in frame)
)

// Group returns the method group m belongs to, or "" if unknown.
func (m Method) Group() Group {
	switch m {
	case MethodPrompt, MethodQueue, MethodQueueSet, MethodCancel, MethodCompact,
		MethodClear, MethodMessageEdit, MethodMessageDelete, MethodTurnSwipe, MethodTurnRetry, MethodTurnContinue, MethodApprove, MethodAnswer,
		MethodPostLine, MethodDirectTurn, MethodTurnAdvance, MethodVariantsPrune, MethodVariantsDrop, MethodSubscribe, MethodUnsubscribe:
		return GroupConversation
	case MethodSessionsList, MethodSessionCreate, MethodSessionResume, MethodSessionFork,
		MethodSessionRename, MethodSessionGenerateTitle, MethodSessionDelete, MethodSessionDiscardDraft,
		MethodSessionArchive, MethodSessionsArchived, MethodSessionRestore, MethodUsageGet, MethodUsageSnapshot, MethodResetsList, MethodContextGet,
		MethodContextNode, MethodSurfacesList, MethodSurfaceGet, MethodSurfaceAction, MethodI18nCatalog,
		MethodFilesList, MethodAuthProviders, MethodConversationReveal, MethodConversationHistory,
		MethodSideChatOpen, MethodSideChatAsk, MethodSideChatClose, MethodSuggestReply, MethodSessionsDoctor,
		MethodSessionsNextScene, MethodSessionsRealize, MethodSessionsExport,
		MethodWorkflowsList, MethodWorkflowsGet:
		return GroupSession
	case MethodModelsList, MethodModelSwitch, MethodModelFavorite, MethodModelSetDefault,
		MethodModelParams, MethodModelParamsSet, MethodModelParamsReset,
		MethodTrust, MethodUntrust, MethodRestart, MethodResetsConsume,
		MethodCardsList, MethodCardsGet, MethodCardsImport, MethodCardsEdit, MethodCardsDelete, MethodCardsExport, MethodCardsLint, MethodCardsFavorite, MethodCardsDoctor,
		MethodCardsHistory, MethodCardsRestore, MethodCardsRevision,
		MethodPersonasList, MethodPersonasGet, MethodPersonasCreate, MethodPersonasEdit, MethodPersonasDelete,
		MethodBackgroundsList, MethodBackgroundsImport, MethodBackgroundsDelete, MethodBackgroundBind, MethodBackgroundGenerate,
		MethodNoteSet, MethodUserBind, MethodCastAdd, MethodCastRemove, MethodCastSpeak,
		MethodWorldLorePut, MethodWorldLoreDelete, MethodWorldSet,
		MethodWorldsList, MethodWorldSave, MethodWorldDelete, MethodWorldUpdate, MethodWorldSetCharacterModel, MethodWorldsExport, MethodWorldsImport,
		MethodCardGroupsList, MethodCardGroupSave, MethodCardGroupDelete, MethodCardGroupSetMembers,
		MethodSessionGroupsList, MethodSessionGroupSave, MethodSessionGroupDelete, MethodSessionGroupSetMembers,
		MethodModelDefaultFor, MethodCardModelSet,
		MethodUserPersonasList, MethodUserPersonaSave, MethodUserPersonaDelete, MethodUserPersonaSetDefault:
		return GroupControl
	case MethodReplayControl, MethodReplayState:
		return GroupReplay
	case MethodAuthLoginStart, MethodAuthLoginSubmit, MethodAuthLoginCancel, MethodAuthLogout,
		MethodAuthEndpointRemove:
		return GroupAuth
	}
	return ""
}

// --- command params ---

// PromptParams is the payload of [MethodPrompt].
type PromptParams struct {
	Text   string  `json:"text"`
	Images []Image `json:"images,omitempty"`
	// Attachments name files the client already staged with the carrier (see
	// [FeatureAttachments]). Unlike Images, no bytes ride this frame: the daemon
	// resolves each id to a path and tells the model where to read it.
	Attachments []AttachmentRef `json:"attachments,omitempty"`
}

// AttachmentRef names one staged file by the id its upload returned.
//
// The id is all a client sends. Name, type, and size are resolved daemon-side
// from what is actually on disk, so a client cannot misdescribe a file to the
// model — and cannot name a path at all, which is what keeps the reference from
// being a way to point the agent at an arbitrary file.
//
// A struct rather than a bare id string so the reference can gain fields later
// without a wire break.
type AttachmentRef struct {
	ID string `json:"id"`
}

// QueueParams is the payload of [MethodQueue].
type QueueParams struct {
	Text string `json:"text"`
}

// QueueSetParams is the payload of [MethodQueueSet]: the complete new pending
// queue, replacing whatever is currently queued. Used to edit or cancel queued
// messages before they are injected. An empty slice clears the queue.
type QueueSetParams struct {
	Texts []string `json:"texts"`
}

// ApproveParams is the payload of [MethodApprove].
type ApproveParams struct {
	CallID   string   `json:"call_id"`
	Decision Decision `json:"decision"`
}

// AnswerParams is the payload of [MethodAnswer].
type AnswerParams struct {
	AskID  string `json:"ask_id"`
	Answer Answer `json:"answer"`
}

// RenameParams is the payload of [MethodSessionRename].
type RenameParams struct {
	Title string `json:"title"`
}

// GenerateTitleResult is the result of [MethodSessionGenerateTitle]: the
// generated (and already-persisted) title. The requesting client updates its
// row from it; a live session's other clients converge via session_updated.
type GenerateTitleResult struct {
	Title string `json:"title"`
}

// ModelSwitchParams is the payload of [MethodModelSwitch]. Provider qualifies
// Model (ids are not globally unique across providers); empty resolves across
// all providers, preserving the old wire behavior.
type ModelSwitchParams struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
}

// SurfaceGetParams is the payload of [MethodSurfaceGet].
type SurfaceGetParams struct {
	ID string `json:"id"`
}

// SurfaceActionParams is the payload of [MethodSurfaceAction]: an action on a
// surface, e.g. forwarding a keypress to an extension panel. Args carries
// action-specific fields (e.g. "key"/"text" for a panel key).
type SurfaceActionParams struct {
	ID     string            `json:"id"`
	Action string            `json:"action"`
	Args   map[string]string `json:"args,omitempty"`
}

// FavoriteParams is the payload of [MethodModelFavorite].
type FavoriteParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	On       bool   `json:"on"`
}

// DefaultScope is where a model default is written — the two scopes the TUI's
// model picker offers under ctrl+d.
type DefaultScope string

const (
	// ScopeGlobal writes provider+model to the user's config: the default
	// everywhere, for every workspace.
	ScopeGlobal DefaultScope = "global"
	// ScopeProject writes them to the workspace's project config, which only
	// takes effect while that workspace is trusted.
	ScopeProject DefaultScope = "project"
)

// SetDefaultParams is the payload of [MethodModelSetDefault].
type SetDefaultParams struct {
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Scope    DefaultScope `json:"scope"`
}

// TrustParams is the payload of [MethodTrust]: Parent also trusts descendant
// directories (marking "trust everything under this parent"), matching the
// interactive /trust parent form.
type TrustParams struct {
	Parent bool `json:"parent,omitempty"`
}

// I18nCatalogParams is the payload of [MethodI18nCatalog]: an optional BCP-47
// tag, empty for the daemon's active language.
type I18nCatalogParams struct {
	Lang string `json:"lang,omitempty"`
}

// FilesListParams is the payload of [MethodFilesList]: list workspace files
// for a client's @-file picker, walked daemon-side under the workspace's
// working directory with the picker's usual semantics (gitignore filtering,
// .git pruning, entry/depth caps). Dir selects a subdirectory in flat mode
// ("" = the cwd itself; ".." escapes are rejected) and is ignored when
// Recursive is set — a recursive listing always covers the whole tree.
type FilesListParams struct {
	Dir              string `json:"dir,omitempty"`
	Recursive        bool   `json:"recursive,omitempty"`
	RespectGitignore bool   `json:"respect_gitignore,omitempty"`
}

// --- command results ---

// SessionsResult is the payload of a [MethodSessionsList] response.
type SessionsResult struct {
	Sessions []SessionInfo `json:"sessions"`
}

// SessionResult is the payload of a [MethodSessionCreate] / [MethodSessionResume]
// / [MethodSessionFork] response.
type SessionResult struct {
	Session SessionInfo `json:"session"`
}

// SessionForkParams is the payload of [MethodSessionFork]: branch the session in
// the frame at FromIndex into a NEW parent-linked session. The child keeps the
// parent's messages 0..FromIndex (inclusive) and diverges after; the parent is
// untouched. FromIndex is a message index in the transcript the client sees —
// branch at a user-turn boundary (a mid-response index still works, but the
// child's load-time tool-pair repair may drop a dangling call). Returns the
// child's descriptor.
type SessionForkParams struct {
	FromIndex int `json:"from_index"`
}

// UsageResult is the payload of a [MethodUsageGet] response.
type UsageResult struct {
	Usage core.WireUsage `json:"usage"`
}

// ModelsResult is the payload of a [MethodModelsList] response.
type ModelsResult struct {
	Models []ModelInfo `json:"models"`
}

// UsageSnapshotParams is the payload of [MethodUsageSnapshot]. Refresh=true
// pulls from the provider's usage endpoint and blocks on the fetch.
type UsageSnapshotParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// UsageSnapshotResult is the payload of a [MethodUsageSnapshot] response.
type UsageSnapshotResult struct {
	Usage UsageInfo `json:"usage"`
}

// --- side chat ---

// SideChatTurn is one completed question/answer exchange inside a side chat.
// Carried on every [MethodSideChatAsk] so the daemon holds no per-turn state:
// the frozen base is the daemon's, the conversation on top of it is the
// client's, and a failed or cancelled ask simply never becomes a turn.
type SideChatTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// SideChatOpenResult is the payload of a [MethodSideChatOpen] response. ID
// names the frozen snapshot for the asks that follow.
type SideChatOpenResult struct {
	ID string `json:"id"`
}

// SideChatAskParams is the payload of [MethodSideChatAsk]. The daemon builds
// the request as: the frozen system prompt, the frozen transcript, then Prior
// (oldest first), then Question. No tools — a side chat is conversational.
type SideChatAskParams struct {
	ID       string         `json:"id"`
	Prior    []SideChatTurn `json:"prior,omitempty"`
	Question string         `json:"question"`
}

// SideChatAskResult is the payload of a [MethodSideChatAsk] response.
type SideChatAskResult struct {
	Text string `json:"text"`
}

// SideChatCloseParams is the payload of [MethodSideChatClose]: release the
// frozen snapshot. Closing an unknown id is not an error — a client that
// crashed and reconnected must not have to reason about what it leaked.
type SideChatCloseParams struct {
	ID string `json:"id"`
}

// ContextResult is the payload of a [MethodContextGet] response.
type ContextResult struct {
	Breakdown ContextBreakdown `json:"breakdown"`
}

// ContextNodeParams is the payload of [MethodContextNode]: resolve one context
// node by its opaque id (minted by context.get). Op selects the operation —
// empty (or "expand") returns the node with its content/children populated one
// level deep; a non-empty op is a node-named reveal (e.g. "compaction", stage 3).
type ContextNodeParams struct {
	ID string `json:"id"`
	Op string `json:"op,omitempty"`
}

// MessageEditParams is the payload of [MethodMessageEdit]: replace the message at
// Index with a text message of the same role. Epoch is the client's transcript
// epoch (from the last snapshot); a stale one means the transcript shifted under
// the client — the index no longer means what it thought — and the edit is
// refused with [CodeConflict] rather than applied to the wrong message.
type MessageEditParams struct {
	Epoch uint64 `json:"epoch"`
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// MessageDeleteParams is the payload of [MethodMessageDelete]: remove the message
// at Index (later messages shift down). Epoch is checked as in [MessageEditParams].
type MessageDeleteParams struct {
	Epoch uint64 `json:"epoch"`
	Index int    `json:"index"`
}

// TurnSwipeParams is the payload of [MethodTurnSwipe]: make take Variant of the
// current tail span active (swipe-back among a regenerated turn's alternatives or
// pre-seeded greetings). Variant is an ABSOLUTE index into the tail's takes — the
// client resolves next/prev arrows against [TailInfo] and sends the destination.
// Epoch is checked as in [MessageEditParams]; a swipe to the current variant is a
// no-op.
type TurnSwipeParams struct {
	Epoch   uint64 `json:"epoch"`
	Variant int    `json:"variant"`
	// Index selects WHICH switchable position to swipe (from the snapshot's
	// VariantMarks). Absent (nil) means the tail span — the original behavior, so a
	// client that only swipes the tail need not send it. Present means the
	// message-scoped variant at that effective index (Option C); index 0 is a real
	// position, which is why this is a pointer rather than a 0-default.
	Index *int `json:"index,omitempty"`
}

// TurnRetryParams is the payload of [MethodTurnRetry]: regenerate the last
// response, keeping the current one as a swipeable take. Epoch is checked as in
// [MessageEditParams] — a stale one means the transcript shifted under the client.
// Like a prompt it starts a turn (returns once accepted; the regeneration
// streams), and [CodeBusy] when a turn is already running.
type TurnRetryParams struct {
	Epoch uint64 `json:"epoch"`

	// Guidance is an OPTIONAL note about what should be different this time —
	// "shorter", "have her refuse instead", "less purple". Empty (the zero value,
	// and what every pre-guidance client sends) is the plain regenerate: an
	// independent sample from the same prefix, unchanged in every respect.
	//
	// It steers exactly one generation and is never persisted. A regenerate's
	// takes all share the transcript prefix they were generated from, so writing
	// the guidance into that prefix would put it in front of takes that never saw
	// it. See [core.Agent.ContinueWithCue].
	Guidance string `json:"guidance,omitempty"`

	// IgnorePrior asks for a BLIND guided regenerate: follow the guidance, but do
	// not show the model the take being replaced. Ignored when Guidance is empty
	// (a plain regenerate never sees the prior take — it is meant to be an
	// independent sample).
	//
	// The default is deliberately the other way round, which is why this is phrased
	// as an opt-OUT: guidance is usually relative ("shorter", "less of that"), and
	// a model that cannot see what it is being asked to change is guessing. Set
	// this when the previous attempt went somewhere the user wants no trace of.
	IgnorePrior bool `json:"ignore_prior,omitempty"`
}

// HistoryParams is the payload of [MethodConversationHistory]: give me the Limit
// messages ENDING just before index Before, in the transcript at Epoch.
//
// Before is the client's current Base — the index of the oldest message it holds —
// so paging up is "ask for what is above what I have". Epoch is what it thinks it is
// holding; sending it back is what lets the daemon refuse rather than quietly index
// into a transcript that has since been replaced.
type HistoryParams struct {
	Before int    `json:"before"`
	Limit  int    `json:"limit,omitempty"` // 0 → HistoryWindow
	Epoch  uint64 `json:"epoch,omitempty"` // 0 → no check (a client that does not track it)
}

// HistoryResult is the payload of a [MethodConversationHistory] response: the slice
// [Base, Base+len(Messages)) of the transcript at Epoch. Base is 0 when the client
// has now reached the start of the live transcript — beyond which lies whatever a
// compaction folded away, reachable with conversation.reveal, not this.
type HistoryResult struct {
	Epoch    uint64             `json:"epoch"`
	Base     int                `json:"base"`
	Total    int                `json:"total"`
	Messages []core.WireMessage `json:"messages"`
}

// RevealParams is the payload of [MethodConversationReveal]: which compaction
// checkpoint to look behind. Ordinal is 0-based among the session's checkpoints;
// a NEGATIVE ordinal means the latest, which is what a client walking up from the
// live transcript asks for first. Not omitempty — 0 is the first checkpoint, a
// perfectly ordinary thing to ask for.
type RevealParams struct {
	Ordinal int `json:"ordinal"`
}

// RevealResult is the payload of a [MethodConversationReveal] response.
//
// Messages is the span the checkpoint folded into its summary, in transcript
// order, MINUS the tail it kept verbatim — so it abuts the client's existing
// scrollback without overlapping it.
//
// PrevOrdinal chains further back: a session compacted more than once has a
// checkpoint behind the checkpoint, and revealing one exposes the next divider
// above it. It is -1 when this is the first, which is how a client knows it has
// reached the beginning of the conversation.
//
// PrevClear says the checkpoint behind this one is a /clear, and it is the one
// place the walk is expected to STOP on its own. A clear is a deliberate act —
// "done with that, start fresh" — nearer a session boundary than a compaction,
// which only condenses a conversation you are still having. So a client does not
// chain across one; it offers the crossing as a separate, explicit choice, and
// passes the clear's own ordinal when the user takes it. Clear says the target
// WAS that crossing. Deliberate to make, deliberate to undo.
//
// None of this is redaction. The rows are still in the session file in plaintext,
// and --replay raw, export, and session_inspect all read them.
type RevealResult struct {
	Ordinal     int                `json:"ordinal"`
	PrevOrdinal int                `json:"prev_ordinal"`
	Total       int                `json:"total"`
	Messages    []core.WireMessage `json:"messages"`
	Clear       bool               `json:"clear,omitempty"`
	PrevClear   bool               `json:"prev_clear,omitempty"`
}

// ContextNodeResult is the payload of a [MethodContextNode] response: the
// requested node with its lazily-fetched content/children filled in.
type ContextNodeResult struct {
	Node ContextNode `json:"node"`
}

// SurfacesResult is the payload of a [MethodSurfacesList] response: the panes
// available for the session, for the client's surface switcher.
type SurfacesResult struct {
	Surfaces []SurfaceMeta `json:"surfaces"`
}

// SurfaceResult is the payload of a [MethodSurfaceGet] response.
type SurfaceResult struct {
	Surface Surface `json:"surface"`
}

// I18nCatalogResult is the payload of a [MethodI18nCatalog] response: the
// effective (embedded ⊕ operator overlay) web string catalog for a language,
// English-as-key. The client resolves its own strings against this, falling
// back to the English key. See [CatalogView].
type I18nCatalogResult struct {
	Catalog CatalogView `json:"catalog"`
}

// FileEntry is one listed file or directory in a [FilesListResult]. Path is
// relative to the workspace's working directory, "/"-separated on the wire.
type FileEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
}

// FilesListResult is the payload of a [MethodFilesList] response. Truncated
// reports a recursive walk stopped at the entry/depth caps — the list is
// still usable, just not exhaustive.
type FilesListResult struct {
	Files     []FileEntry `json:"files"`
	Truncated bool        `json:"truncated,omitempty"`
}

// --- decision / answer wire forms ---

// Decision is the wire form of [core.ConfirmDecision] carried in
// [ApproveParams].
type Decision struct {
	Allow        bool   `json:"allow"`
	Reason       string `json:"reason,omitempty"`
	RememberTool bool   `json:"remember_tool,omitempty"`
	RememberAll  bool   `json:"remember_all,omitempty"`
	PersistTool  bool   `json:"persist_tool,omitempty"`
	// PersistScopes narrows a persist_tool grant to these args patterns
	// (the Pattern fields of the offered PermissionRequest.Scopes, echoed
	// back) — one saved rule per pattern instead of a blanket tool rule.
	PersistScopes []string `json:"persist_scopes,omitempty"`
}

// Core converts a wire Decision to the core type.
func (d Decision) Core() core.ConfirmDecision {
	return core.ConfirmDecision{
		Allow:         d.Allow,
		Reason:        d.Reason,
		RememberTool:  d.RememberTool,
		RememberAll:   d.RememberAll,
		PersistTool:   d.PersistTool,
		PersistScopes: d.PersistScopes,
	}
}

// DecisionFromCore converts a core decision to its wire form.
func DecisionFromCore(c core.ConfirmDecision) Decision {
	return Decision{
		Allow:         c.Allow,
		Reason:        c.Reason,
		RememberTool:  c.RememberTool,
		RememberAll:   c.RememberAll,
		PersistTool:   c.PersistTool,
		PersistScopes: c.PersistScopes,
	}
}

// GrantScopesFromCore converts derived core grant scopes to their wire form.
func GrantScopesFromCore(scopes []core.GrantScope) []GrantScope {
	if len(scopes) == 0 {
		return nil
	}
	out := make([]GrantScope, len(scopes))
	for i, s := range scopes {
		out[i] = GrantScope{Display: s.Display, Pattern: s.Pattern}
	}
	return out
}

// CoreGrantScopes converts wire grant scopes back to the core type.
func CoreGrantScopes(scopes []GrantScope) []core.GrantScope {
	if len(scopes) == 0 {
		return nil
	}
	out := make([]core.GrantScope, len(scopes))
	for i, s := range scopes {
		out[i] = core.GrantScope{Display: s.Display, Pattern: s.Pattern}
	}
	return out
}

// Answer is the wire form of [core.UserAnswer] carried in [AnswerParams].
type Answer struct {
	Answer   string `json:"answer,omitempty"`
	Declined bool   `json:"declined,omitempty"`
}

// Core converts a wire Answer to the core type.
func (a Answer) Core() core.UserAnswer {
	return core.UserAnswer{Answer: a.Answer, Declined: a.Declined}
}

// AnswerFromCore converts a core answer to its wire form.
func AnswerFromCore(a core.UserAnswer) Answer {
	return Answer{Answer: a.Answer, Declined: a.Declined}
}
