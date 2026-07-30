package ctrlproto

import "context"

// dispatch is every verb this server answers, keyed by [Method].
//
// ONE map literal on purpose: duplicate constant keys in a Go map literal are a
// compile error, so two entries claiming the same verb cannot reach a test. That
// check is free and the switch never had it — it would happily have taken a
// second `case MethodX:` as unreachable code.
//
// Entries are grouped by method group, in the order methods.go declares them. The
// shape helpers (do/get/act/ask) are in dispatch.go, as is why the unsupported
// message is a func.
//
// subscribe/unsubscribe are absent by design: serveState answers them, because
// they manage this connection's event pumps rather than calling the service. See
// handle().
var dispatch = map[Method]handler{

	// ---------------------------------------------------------------- conversation

	MethodPrompt: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p PromptParams) error {
		return svc.Prompt(ctx, f.Sess, p)
	}),
	MethodQueue: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p QueueParams) error {
		return svc.Queue(ctx, f.Sess, p.Text)
	}),
	MethodQueueSet: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p QueueSetParams) error {
		return svc.SetQueue(ctx, f.Sess, p.Texts)
	}),
	MethodCancel: do(base, func(svc WorkspaceService, ctx context.Context, f Frame) error {
		return svc.Cancel(ctx, f.Sess)
	}),
	MethodCompact: do(base, func(svc WorkspaceService, ctx context.Context, f Frame) error {
		return svc.Compact(ctx, f.Sess)
	}),
	MethodClear: do(base, func(svc WorkspaceService, ctx context.Context, f Frame) error {
		return svc.Clear(ctx, f.Sess)
	}),
	MethodMessageEdit: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p MessageEditParams) error {
		return svc.EditMessage(ctx, f.Sess, p.Epoch, p.Index, p.Text)
	}),
	MethodMessageDelete: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p MessageDeleteParams) error {
		return svc.DeleteMessage(ctx, f.Sess, p.Epoch, p.Index)
	}),

	// The one verb with two destinations: Index absent swipes the tail span,
	// Index present swipes the message-scoped variant at that position. Pinned
	// by TestTurnSwipeRoutesByIndexPresence.
	MethodTurnSwipe: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p TurnSwipeParams) error {
		if p.Index != nil {
			return svc.SwipeMessage(ctx, f.Sess, p.Epoch, *p.Index, p.Variant)
		}
		return svc.SwipeTurn(ctx, f.Sess, p.Epoch, p.Variant)
	}),

	MethodTurnRetry: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p TurnRetryParams) error {
		return svc.RetryTurn(ctx, f.Sess, p)
	}),
	MethodTurnContinue: act(noContinue, func(c ContinueController, ctx context.Context, f Frame, p TurnContinueParams) error {
		return c.ContinueTurn(ctx, f.Sess, p.Epoch)
	}),
	MethodApprove: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p ApproveParams) error {
		return svc.Approve(ctx, f.Sess, p.CallID, p.Decision.Core())
	}),
	MethodAnswer: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p AnswerParams) error {
		return svc.Answer(ctx, f.Sess, p.AskID, p.Core())
	}),

	// -------------------------------------------------------------------- session

	MethodSessionsList: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (SessionsResult, error) {
		list, err := svc.Sessions(ctx)
		return SessionsResult{Sessions: list}, err
	}),
	MethodSessionCreate: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, o CreateOpts) (SessionResult, error) {
		info, err := svc.CreateSession(ctx, o)
		return SessionResult{Session: info}, err
	}),
	MethodSessionResume: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (SessionResult, error) {
		info, err := svc.ResumeSession(ctx, f.Sess)
		return SessionResult{Session: info}, err
	}),
	MethodSessionFork: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p SessionForkParams) (SessionResult, error) {
		info, err := svc.ForkSession(ctx, f.Sess, p.FromIndex)
		return SessionResult{Session: info}, err
	}),
	MethodSessionRename: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p RenameParams) error {
		return svc.RenameSession(ctx, f.Sess, p.Title)
	}),
	// Blocks on the model — the same synchronous posture as MethodCompact.
	MethodSessionGenerateTitle: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (GenerateTitleResult, error) {
		title, err := svc.GenerateSessionTitle(ctx, f.Sess)
		return GenerateTitleResult{Title: title}, err
	}),
	MethodSessionDelete: do(base, func(svc WorkspaceService, ctx context.Context, f Frame) error {
		return svc.DeleteSession(ctx, f.Sess)
	}),

	MethodUsageGet: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (UsageResult, error) {
		u, err := svc.Usage(ctx, f.Sess)
		return UsageResult{Usage: u}, err
	}),
	MethodUsageSnapshot: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p UsageSnapshotParams) (UsageSnapshotResult, error) {
		u, err := svc.UsageSnapshot(ctx, f.Sess, p.Refresh)
		return UsageSnapshotResult{Usage: u}, err
	}),
	MethodResetsList: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (ResetsListResult, error) {
		return svc.ListResets(ctx, f.Sess)
	}),
	MethodResetsConsume: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p ResetConsumeParams) (ResetConsumeResult, error) {
		return svc.ConsumeReset(ctx, f.Sess, p.ID)
	}),

	MethodSideChatOpen: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (SideChatOpenResult, error) {
		id, err := svc.SideChatOpen(ctx, f.Sess)
		return SideChatOpenResult{ID: id}, err
	}),
	// Blocks on the model — the same synchronous posture as MethodCompact. The
	// in-process TUI carrier bypasses this loop entirely (it calls the
	// WorkspaceService method directly, on the dialog's own goroutine), so /btw
	// never stalls the session stream; a networked caller pays the same
	// per-connection block compact already has.
	MethodSideChatAsk: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p SideChatAskParams) (SideChatAskResult, error) {
		text, err := svc.SideChatAsk(ctx, f.Sess, p.ID, p.Prior, p.Question)
		return SideChatAskResult{Text: text}, err
	}),
	MethodSideChatClose: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p SideChatCloseParams) error {
		return svc.SideChatClose(ctx, f.Sess, p.ID)
	}),

	MethodContextGet: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (ContextResult, error) {
		b, err := svc.Context(ctx, f.Sess)
		return ContextResult{Breakdown: b}, err
	}),
	MethodContextNode: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p ContextNodeParams) (ContextNodeResult, error) {
		n, err := svc.Node(ctx, f.Sess, p.ID, p.Op)
		return ContextNodeResult{Node: n}, err
	}),
	MethodConversationReveal: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p RevealParams) (RevealResult, error) {
		return svc.Reveal(ctx, f.Sess, p.Ordinal)
	}),
	MethodConversationHistory: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p HistoryParams) (HistoryResult, error) {
		return svc.History(ctx, f.Sess, p.Before, p.Limit, p.Epoch)
	}),

	MethodSurfacesList: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (SurfacesResult, error) {
		list, err := svc.Surfaces(ctx, f.Sess)
		return SurfacesResult{Surfaces: list}, err
	}),
	MethodSurfaceGet: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p SurfaceGetParams) (SurfaceResult, error) {
		sf, err := svc.Surface(ctx, f.Sess, p.ID)
		return SurfaceResult{Surface: sf}, err
	}),
	MethodSurfaceAction: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p SurfaceActionParams) error {
		return svc.SurfaceAction(ctx, f.Sess, p.ID, p.Action, p.Args)
	}),

	MethodI18nCatalog: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p I18nCatalogParams) (I18nCatalogResult, error) {
		cv, err := svc.Catalog(ctx, p.Lang)
		return I18nCatalogResult{Catalog: cv}, err
	}),
	MethodFilesList: ask(base, func(svc WorkspaceService, ctx context.Context, f Frame, p FilesListParams) (FilesListResult, error) {
		return svc.ListFiles(ctx, p)
	}),
	// Session-independent: f.Sess is ignored on purpose. A credential belongs to
	// the daemon, not to a conversation.
	MethodAuthProviders: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (ProvidersView, error) {
		return svc.AuthProviders(ctx)
	}),

	// -------------------------------------------------------------------- control

	MethodModelsList: get(base, func(svc WorkspaceService, ctx context.Context, f Frame) (ModelsResult, error) {
		m, err := svc.Models(ctx, f.Sess)
		return ModelsResult{Models: m}, err
	}),
	MethodModelSwitch: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p ModelSwitchParams) error {
		return svc.SwitchModel(ctx, f.Sess, p.Provider, p.Model)
	}),
	MethodModelFavorite: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p FavoriteParams) error {
		return svc.SetFavoriteModel(ctx, p.Provider, p.Model, p.On)
	}),
	MethodModelSetDefault: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p SetDefaultParams) error {
		return svc.SetDefaultModel(ctx, p.Provider, p.Model, p.Scope)
	}),
	MethodTrust: act(base, func(svc WorkspaceService, ctx context.Context, f Frame, p TrustParams) error {
		return svc.Trust(ctx, p.Parent)
	}),
	MethodUntrust: do(base, func(svc WorkspaceService, ctx context.Context, f Frame) error {
		return svc.Untrust(ctx)
	}),
	MethodRestart: do(base, func(svc WorkspaceService, ctx context.Context, f Frame) error {
		return svc.Restart(ctx)
	}),

	// ----------------------------------------------------------------------- auth
	//
	// Optional, and gated by GroupAuth as well as by the controller. Every one of
	// these CHANGES a credential; none of them returns one. The group is separate
	// from `control` precisely so a carrier can grant one without the other.

	MethodAuthLoginStart: ask(noLogin, func(c AuthController, ctx context.Context, f Frame, p AuthLoginStartParams) (AuthFlowStep, error) {
		return c.AuthLoginStart(ctx, p)
	}),
	MethodAuthLoginSubmit: act(noLogin, func(c AuthController, ctx context.Context, f Frame, p AuthLoginSubmitParams) error {
		return c.AuthLoginSubmit(ctx, p)
	}),
	MethodAuthLoginCancel: act(noLogin, func(c AuthController, ctx context.Context, f Frame, p AuthFlowRef) error {
		return c.AuthLoginCancel(ctx, p)
	}),
	MethodAuthLogout: act(noLogin, func(c AuthController, ctx context.Context, f Frame, p AuthLogoutParams) error {
		return c.AuthLogout(ctx, p)
	}),
	MethodAuthEndpointRemove: act(noLogin, func(c AuthController, ctx context.Context, f Frame, p AuthEndpointRemoveParams) error {
		return c.AuthEndpointRemove(ctx, p)
	}),

	// --------------------------------------------------------------------- replay

	MethodReplayControl: ask(noReplay, func(c ReplayController, ctx context.Context, f Frame, p ReplayControlParams) (ReplayStateResult, error) {
		st, err := c.ReplayControl(ctx, f.Sess, p)
		return ReplayStateResult{State: st}, err
	}),
	MethodReplayState: get(noReplay, func(c ReplayController, ctx context.Context, f Frame) (ReplayStateResult, error) {
		st, err := c.ReplayState(ctx, f.Sess)
		return ReplayStateResult{State: st}, err
	}),

	// ---------------------------------------------------------------- model params
	//
	// Optional, like the auth group: a carrier with no models.json to write simply
	// does not implement it, and says so rather than failing deeper.

	MethodModelParams: ask(noModelParams, func(c ModelParamsController, ctx context.Context, f Frame, p ModelParamsParams) (ModelParamsView, error) {
		return c.ModelParams(ctx, p)
	}),
	MethodModelParamsSet: act(noModelParams, func(c ModelParamsController, ctx context.Context, f Frame, p ModelParamsSetParams) error {
		return c.ModelParamsSet(ctx, p)
	}),
	MethodModelParamsReset: act(noModelParams, func(c ModelParamsController, ctx context.Context, f Frame, p ModelParamsParams) error {
		return c.ModelParamsReset(ctx, p)
	}),

	// ----------------------------------------------------------------------- cards

	MethodCardsList: get(noCards, func(c CardsController, ctx context.Context, f Frame) (CardsListResult, error) {
		return c.CardsList(ctx)
	}),
	MethodCardsGet: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardGetParams) (CardView, error) {
		return c.CardsGet(ctx, p)
	}),
	MethodCardsImport: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardImportParams) (CardView, error) {
		return c.CardsImport(ctx, p)
	}),
	MethodCardsEdit: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardEditParams) (CardView, error) {
		return c.CardsEdit(ctx, p)
	}),
	MethodCardsExport: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardExportParams) (CardExport, error) {
		return c.CardsExport(ctx, p)
	}),
	MethodCardsLint: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardLintParams) (CardLintResult, error) {
		return c.CardsLint(ctx, p)
	}),
	MethodCardsDelete: act(noCards, func(c CardsController, ctx context.Context, f Frame, p CardDeleteParams) error {
		return c.CardsDelete(ctx, p)
	}),
	MethodCardsFavorite: act(noCards, func(c CardsController, ctx context.Context, f Frame, p CardFavoriteParams) error {
		return c.CardFavorite(ctx, p)
	}),
	MethodCardsHistory: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardHistoryParams) (CardHistoryResult, error) {
		return c.CardsHistory(ctx, p)
	}),
	MethodCardsRestore: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardRestoreParams) (CardView, error) {
		return c.CardsRestore(ctx, p)
	}),
	MethodCardsRevision: ask(noCards, func(c CardsController, ctx context.Context, f Frame, p CardRevisionParams) (CardRevisionView, error) {
		return c.CardsRevision(ctx, p)
	}),

	// ---------------------------------------------------------------------- doctor
	//
	// One controller, four verbs — and each answered "unsupported" in its own
	// words. The messages stay per-verb for that reason.

	MethodCardsDoctor: ask(noCardDoctor, func(c DoctorController, ctx context.Context, f Frame, p DoctorParams) (DoctorResult, error) {
		return c.CardsDoctor(ctx, p)
	}),
	MethodSessionsDoctor: ask(noSessionDoctor, func(c DoctorController, ctx context.Context, f Frame, p SessionDoctorParams) (SessionDoctorResult, error) {
		return c.SessionsDoctor(ctx, f.Sess, p)
	}),
	MethodSessionsNextScene: ask(noNextScene, func(c DoctorController, ctx context.Context, f Frame, p NextSceneParams) (NextSceneResult, error) {
		return c.SessionsNextScene(ctx, f.Sess, p)
	}),
	MethodSessionsRealize: ask(noRealize, func(c DoctorController, ctx context.Context, f Frame, p RealizeParams) (RealizeResult, error) {
		return c.SessionsRealize(ctx, f.Sess, p)
	}),

	// ------------------------------------------------------------- export, suggest

	MethodSessionsExport: ask(noExport, func(c ExportController, ctx context.Context, f Frame, p SessionExportParams) (SessionExport, error) {
		return c.SessionsExport(ctx, f.Sess, p)
	}),
	MethodSuggestReply: ask(noSuggest, func(c SuggestController, ctx context.Context, f Frame, p SuggestParams) (SuggestResult, error) {
		return c.SuggestReply(ctx, f.Sess, p)
	}),

	// -------------------------------------------------------------------- personas

	MethodPersonasList: get(noPersonas, func(c PersonasController, ctx context.Context, f Frame) (PersonasListResult, error) {
		return c.PersonasList(ctx)
	}),
	MethodPersonasGet: ask(noPersonas, func(c PersonasController, ctx context.Context, f Frame, p PersonaGetParams) (PersonaView, error) {
		return c.PersonasGet(ctx, p)
	}),
	MethodPersonasCreate: ask(noPersonas, func(c PersonasController, ctx context.Context, f Frame, p PersonaWriteParams) (PersonaView, error) {
		return c.PersonasCreate(ctx, p)
	}),
	MethodPersonasEdit: ask(noPersonas, func(c PersonasController, ctx context.Context, f Frame, p PersonaWriteParams) (PersonaView, error) {
		return c.PersonasEdit(ctx, p)
	}),
	// The verb the outer-case trap actually bit: reached only through the group's
	// list, it ran as PersonasEdit and overwrote the persona with an empty one.
	MethodPersonasDelete: act(noPersonas, func(c PersonasController, ctx context.Context, f Frame, p PersonaDeleteParams) error {
		return c.PersonasDelete(ctx, p)
	}),

	// ----------------------------------------------------------------- backgrounds

	MethodBackgroundsList: get(noBackgrounds, func(c BackgroundsController, ctx context.Context, f Frame) (BackgroundsListResult, error) {
		return c.BackgroundsList(ctx)
	}),
	MethodBackgroundGenerate: ask(noBackgrounds, func(c BackgroundsController, ctx context.Context, f Frame, p BackgroundGenerateParams) (BackgroundView, error) {
		return c.BackgroundsGenerate(ctx, f.Sess, p)
	}),
	MethodBackgroundsImport: ask(noBackgrounds, func(c BackgroundsController, ctx context.Context, f Frame, p BackgroundImportParams) (BackgroundView, error) {
		return c.BackgroundsImport(ctx, p)
	}),
	MethodBackgroundsDelete: act(noBackgrounds, func(c BackgroundsController, ctx context.Context, f Frame, p BackgroundDeleteParams) error {
		return c.BackgroundsDelete(ctx, p)
	}),
	MethodBackgroundBind: act(noBackgrounds, func(c BackgroundsController, ctx context.Context, f Frame, p BackgroundBindParams) error {
		return c.BackgroundBind(ctx, f.Sess, p)
	}),

	// -------------------------------------------------------------- note, user, draft

	MethodNoteSet: act(noNote, func(c NoteController, ctx context.Context, f Frame, p NoteSetParams) error {
		return c.NoteSet(ctx, f.Sess, p)
	}),
	MethodUserBind: act(noUser, func(c UserController, ctx context.Context, f Frame, p UserBindParams) error {
		return c.UserBind(ctx, f.Sess, p)
	}),
	MethodSessionDiscardDraft: do(noDraft, func(c DraftController, ctx context.Context, f Frame) error {
		return c.DiscardDraft(ctx, f.Sess)
	}),

	// --------------------------------------------------------------------- archive

	MethodSessionArchive: get(noArchive, func(c SessionArchiveController, ctx context.Context, f Frame) (ArchivedSessionInfo, error) {
		return c.ArchiveSession(ctx, f.Sess)
	}),
	MethodSessionsArchived: get(noArchive, func(c SessionArchiveController, ctx context.Context, f Frame) (ArchivedSessionsResult, error) {
		list, err := c.ArchivedSessions(ctx)
		return ArchivedSessionsResult{Sessions: list}, err
	}),
	MethodSessionRestore: ask(noArchive, func(c SessionArchiveController, ctx context.Context, f Frame, p RestoreSessionParams) (SessionInfo, error) {
		return c.RestoreSession(ctx, p)
	}),

	// ------------------------------------------------------------------- workflows

	MethodWorkflowsList: get(noWorkflows, func(c WorkflowsController, ctx context.Context, f Frame) (WorkflowRunsResult, error) {
		list, err := c.WorkflowRuns(ctx)
		return WorkflowRunsResult{Runs: list}, err
	}),
	MethodWorkflowsGet: ask(noWorkflows, func(c WorkflowsController, ctx context.Context, f Frame, p WorkflowGetParams) (WorkflowRunView, error) {
		return c.WorkflowRun(ctx, p)
	}),

	// --------------------------------------------------------------- user personas

	MethodUserPersonasList: get(noUserPers, func(c UserPersonasController, ctx context.Context, f Frame) (UserPersonasListResult, error) {
		return c.UserPersonasList(ctx)
	}),
	MethodUserPersonaSave: ask(noUserPers, func(c UserPersonasController, ctx context.Context, f Frame, p UserPersonaView) (UserPersonaView, error) {
		return c.UserPersonaSave(ctx, p)
	}),
	MethodUserPersonaDelete: act(noUserPers, func(c UserPersonasController, ctx context.Context, f Frame, p UserPersonaRef) error {
		return c.UserPersonaDelete(ctx, p)
	}),
	MethodUserPersonaSetDefault: act(noUserPers, func(c UserPersonasController, ctx context.Context, f Frame, p UserPersonaRef) error {
		return c.UserPersonaSetDefault(ctx, p)
	}),

	// ------------------------------------------------------------------------ cast
	//
	// Add and remove shared one binding in the switch (both take CastMemberParams);
	// each names its own type here, which is what stops a third verb joining the
	// group and silently removing a cast member.

	MethodCastAdd: act(noCast, func(c CastController, ctx context.Context, f Frame, p CastMemberParams) error {
		return c.CastAdd(ctx, f.Sess, p)
	}),
	MethodCastRemove: act(noCast, func(c CastController, ctx context.Context, f Frame, p CastMemberParams) error {
		return c.CastRemove(ctx, f.Sess, p)
	}),
	MethodCastSpeak: act(noCast, func(c CastController, ctx context.Context, f Frame, p CastSpeakParams) error {
		return c.CastSpeak(ctx, f.Sess, p)
	}),

	// ----------------------------------------------------------------------- world

	MethodWorldLorePut: act(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldLorePutParams) error {
		return c.WorldLorePut(ctx, f.Sess, p)
	}),
	MethodWorldLoreDelete: act(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldLoreDeleteParams) error {
		return c.WorldLoreDelete(ctx, f.Sess, p)
	}),
	MethodWorldSet: act(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldSetParams) error {
		return c.WorldSet(ctx, f.Sess, p)
	}),
	MethodWorldsList: get(noWorld, func(c WorldController, ctx context.Context, f Frame) (WorldsListResult, error) {
		return c.WorldsList(ctx)
	}),
	MethodWorldSave: ask(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldSaveParams) (WorldView, error) {
		return c.WorldSave(ctx, f.Sess, p)
	}),
	MethodWorldUpdate: ask(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldUpdateParams) (WorldView, error) {
		return c.WorldUpdate(ctx, p)
	}),
	MethodWorldSetCharacterModel: ask(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldSetCharacterModelParams) (WorldView, error) {
		return c.WorldSetCharacterModel(ctx, p)
	}),
	MethodWorldsExport: ask(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldExportParams) (WorldExport, error) {
		return c.WorldsExport(ctx, p)
	}),
	MethodWorldsImport: ask(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldImportParams) (WorldView, error) {
		return c.WorldsImport(ctx, p)
	}),
	MethodWorldDelete: act(noWorld, func(c WorldController, ctx context.Context, f Frame, p WorldDeleteParams) error {
		return c.WorldDelete(ctx, p)
	}),

	// ---------------------------------------------------------------------- groups

	MethodCardGroupsList: get(noCardGroups, func(c CardGroupsController, ctx context.Context, f Frame) (CardGroupsResult, error) {
		return c.CardGroupsList(ctx)
	}),
	MethodCardGroupSave: ask(noCardGroups, func(c CardGroupsController, ctx context.Context, f Frame, p CardGroupSaveParams) (GroupView, error) {
		return c.CardGroupSave(ctx, p)
	}),
	MethodCardGroupSetMembers: ask(noCardGroups, func(c CardGroupsController, ctx context.Context, f Frame, p CardGroupSetMembersParams) (GroupView, error) {
		return c.CardGroupSetMembers(ctx, p)
	}),
	MethodCardGroupDelete: act(noCardGroups, func(c CardGroupsController, ctx context.Context, f Frame, p CardGroupDeleteParams) error {
		return c.CardGroupDelete(ctx, p)
	}),

	MethodSessionGroupsList: get(noSessGroups, func(c SessionGroupsController, ctx context.Context, f Frame) (SessionGroupsResult, error) {
		return c.SessionGroupsList(ctx)
	}),
	MethodSessionGroupSave: ask(noSessGroups, func(c SessionGroupsController, ctx context.Context, f Frame, p SessionGroupSaveParams) (GroupView, error) {
		return c.SessionGroupSave(ctx, p)
	}),
	MethodSessionGroupSetMembers: ask(noSessGroups, func(c SessionGroupsController, ctx context.Context, f Frame, p SessionGroupSetMembersParams) (GroupView, error) {
		return c.SessionGroupSetMembers(ctx, p)
	}),
	MethodSessionGroupDelete: act(noSessGroups, func(c SessionGroupsController, ctx context.Context, f Frame, p SessionGroupDeleteParams) error {
		return c.SessionGroupDelete(ctx, p)
	}),

	// ------------------------------------------------------------------ card model

	MethodModelDefaultFor: ask(noCardModel, func(c CardModelController, ctx context.Context, f Frame, p DefaultForParams) (DefaultForResult, error) {
		return c.ModelDefaultFor(ctx, p)
	}),
	MethodCardModelSet: act(noCardModel, func(c CardModelController, ctx context.Context, f Frame, p CardModelSetParams) error {
		return c.CardModelSet(ctx, p)
	}),

	// ------------------------------------------------------------ directed authorship

	// No params by design: advance injects nothing.
	MethodTurnAdvance: do(noDirect, func(c DirectController, ctx context.Context, f Frame) error {
		return c.AdvanceTurn(ctx, f.Sess)
	}),
	MethodDirectTurn: act(noDirect, func(c DirectController, ctx context.Context, f Frame, p DirectTurnParams) error {
		return c.DirectTurn(ctx, f.Sess, p)
	}),
	MethodPostLine: act(noDirect, func(c DirectController, ctx context.Context, f Frame, p PostLineParams) error {
		return c.PostLine(ctx, f.Sess, p)
	}),

	// -------------------------------------------------------------------- variants
	//
	// The two positional call sites the dispatch table was built for: epoch, index
	// and variant are three numbers in a row, and swapping any two is invisible to
	// the compiler.

	MethodVariantsPrune: act(noVariants, func(c VariantsController, ctx context.Context, f Frame, p VariantsPruneParams) error {
		return c.PruneVariants(ctx, f.Sess, p.Epoch, p.Index)
	}),
	MethodVariantsDrop: act(noVariants, func(c VariantsController, ctx context.Context, f Frame, p VariantsDropParams) error {
		return c.DropVariant(ctx, f.Sess, p.Epoch, p.Index, p.Variant)
	}),
}
