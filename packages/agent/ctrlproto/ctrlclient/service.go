package ctrlclient

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// Service is a [ctrlproto.WorkspaceService] view over a [Client]: the same
// interface the in-process carrier satisfies, marshalled over the wire. It is
// the seam that lets a frontend written against WorkspaceService (the TUI)
// drive a remote daemon without knowing it — the inverse of ServeConn's
// dispatch switch, method for method.
//
// Every method inherits the client's reliability contract: fail-fast
// [ErrNotConnected] when down, [ErrDisconnected] for calls in flight across a
// drop, and server failures as *[ctrlproto.Error] with wire codes preserved.
type Service struct {
	c *Client
}

// Service returns the client's WorkspaceService view.
func (c *Client) Service() *Service { return &Service{c: c} }

var _ ctrlproto.WorkspaceService = (*Service)(nil)

// --- conversation group ---

func (s *Service) Prompt(ctx context.Context, sess, text string, images []ctrlproto.Image) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodPrompt, ctrlproto.PromptParams{Text: text, Images: images}, nil)
}

func (s *Service) Queue(ctx context.Context, sess, text string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodQueue, ctrlproto.QueueParams{Text: text}, nil)
}

func (s *Service) SetQueue(ctx context.Context, sess string, texts []string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodQueueSet, ctrlproto.QueueSetParams{Texts: texts}, nil)
}

func (s *Service) Cancel(ctx context.Context, sess string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodCancel, nil, nil)
}

func (s *Service) Compact(ctx context.Context, sess string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodCompact, nil, nil)
}

func (s *Service) Clear(ctx context.Context, sess string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodClear, nil, nil)
}

func (s *Service) EditMessage(ctx context.Context, sess string, epoch uint64, index int, text string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodMessageEdit, ctrlproto.MessageEditParams{Epoch: epoch, Index: index, Text: text}, nil)
}

func (s *Service) DeleteMessage(ctx context.Context, sess string, epoch uint64, index int) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodMessageDelete, ctrlproto.MessageDeleteParams{Epoch: epoch, Index: index}, nil)
}

func (s *Service) SwipeTurn(ctx context.Context, sess string, epoch uint64, variant int) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodTurnSwipe, ctrlproto.TurnSwipeParams{Epoch: epoch, Variant: variant}, nil)
}

func (s *Service) SwipeMessage(ctx context.Context, sess string, epoch uint64, index, variant int) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodTurnSwipe, ctrlproto.TurnSwipeParams{Epoch: epoch, Variant: variant, Index: &index}, nil)
}

func (s *Service) RetryTurn(ctx context.Context, sess string, epoch uint64) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodTurnRetry, ctrlproto.TurnRetryParams{Epoch: epoch}, nil)
}

func (s *Service) ForkSession(ctx context.Context, sess string, fromIndex int) (ctrlproto.SessionInfo, error) {
	var r ctrlproto.SessionResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSessionFork, ctrlproto.SessionForkParams{FromIndex: fromIndex}, &r)
	return r.Session, err
}

func (s *Service) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodApprove, ctrlproto.ApproveParams{
		CallID: callID, Decision: ctrlproto.DecisionFromCore(d),
	}, nil)
}

func (s *Service) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodAnswer, ctrlproto.AnswerParams{
		AskID: askID, Answer: ctrlproto.AnswerFromCore(a),
	}, nil)
}

func (s *Service) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	return s.c.Subscribe(ctx, sess)
}

// --- session group ---

func (s *Service) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	var r ctrlproto.SessionsResult
	err := s.c.Call(ctx, "", ctrlproto.MethodSessionsList, nil, &r)
	return r.Sessions, err
}

func (s *Service) CreateSession(ctx context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	var r ctrlproto.SessionResult
	err := s.c.Call(ctx, "", ctrlproto.MethodSessionCreate, opts, &r)
	return r.Session, err
}

func (s *Service) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	var r ctrlproto.SessionResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSessionResume, nil, &r)
	return r.Session, err
}

func (s *Service) RenameSession(ctx context.Context, sess, title string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodSessionRename, ctrlproto.RenameParams{Title: title}, nil)
}

func (s *Service) GenerateSessionTitle(ctx context.Context, sess string) (string, error) {
	var r ctrlproto.GenerateTitleResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSessionGenerateTitle, nil, &r)
	return r.Title, err
}

func (s *Service) DeleteSession(ctx context.Context, sess string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodSessionDelete, nil, nil)
}

func (s *Service) Usage(ctx context.Context, sess string) (core.WireUsage, error) {
	var r ctrlproto.UsageResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodUsageGet, nil, &r)
	return r.Usage, err
}

func (s *Service) UsageSnapshot(ctx context.Context, sess string, refresh bool) (ctrlproto.UsageInfo, error) {
	var r ctrlproto.UsageSnapshotResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodUsageSnapshot, ctrlproto.UsageSnapshotParams{Refresh: refresh}, &r)
	return r.Usage, err
}

func (s *Service) ListResets(ctx context.Context, sess string) (ctrlproto.ResetsListResult, error) {
	var r ctrlproto.ResetsListResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodResetsList, nil, &r)
	return r, err
}

func (s *Service) ConsumeReset(ctx context.Context, sess, id string) (ctrlproto.ResetConsumeResult, error) {
	var r ctrlproto.ResetConsumeResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodResetsConsume, ctrlproto.ResetConsumeParams{ID: id}, &r)
	return r, err
}

func (s *Service) SideChatOpen(ctx context.Context, sess string) (string, error) {
	var r ctrlproto.SideChatOpenResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSideChatOpen, nil, &r)
	return r.ID, err
}

func (s *Service) SideChatAsk(ctx context.Context, sess, id string, prior []ctrlproto.SideChatTurn, question string) (string, error) {
	var r ctrlproto.SideChatAskResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSideChatAsk, ctrlproto.SideChatAskParams{
		ID: id, Prior: prior, Question: question,
	}, &r)
	return r.Text, err
}

func (s *Service) SideChatClose(ctx context.Context, sess, id string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodSideChatClose, ctrlproto.SideChatCloseParams{ID: id}, nil)
}

func (s *Service) Context(ctx context.Context, sess string) (ctrlproto.ContextBreakdown, error) {
	var r ctrlproto.ContextResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodContextGet, nil, &r)
	return r.Breakdown, err
}

func (s *Service) Node(ctx context.Context, sess, id, op string) (ctrlproto.ContextNode, error) {
	var r ctrlproto.ContextNodeResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodContextNode, ctrlproto.ContextNodeParams{ID: id, Op: op}, &r)
	return r.Node, err
}

func (s *Service) History(ctx context.Context, sess string, before, limit int, epoch uint64) (ctrlproto.HistoryResult, error) {
	var r ctrlproto.HistoryResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodConversationHistory,
		ctrlproto.HistoryParams{Before: before, Limit: limit, Epoch: epoch}, &r)
	return r, err
}

func (s *Service) Reveal(ctx context.Context, sess string, ordinal int) (ctrlproto.RevealResult, error) {
	var r ctrlproto.RevealResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodConversationReveal, ctrlproto.RevealParams{Ordinal: ordinal}, &r)
	return r, err
}

func (s *Service) Surfaces(ctx context.Context, sess string) ([]ctrlproto.SurfaceMeta, error) {
	var r ctrlproto.SurfacesResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSurfacesList, nil, &r)
	return r.Surfaces, err
}

func (s *Service) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	var r ctrlproto.SurfaceResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodSurfaceGet, ctrlproto.SurfaceGetParams{ID: id}, &r)
	return r.Surface, err
}

func (s *Service) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodSurfaceAction, ctrlproto.SurfaceActionParams{
		ID: id, Action: action, Args: args,
	}, nil)
}

func (s *Service) Catalog(ctx context.Context, lang string) (ctrlproto.CatalogView, error) {
	var r ctrlproto.I18nCatalogResult
	err := s.c.Call(ctx, "", ctrlproto.MethodI18nCatalog, ctrlproto.I18nCatalogParams{Lang: lang}, &r)
	return r.Catalog, err
}

func (s *Service) ListFiles(ctx context.Context, opts ctrlproto.FilesListParams) (ctrlproto.FilesListResult, error) {
	var r ctrlproto.FilesListResult
	err := s.c.Call(ctx, "", ctrlproto.MethodFilesList, opts, &r)
	return r, err
}

// AuthProviders asks the DAEMON which providers it holds credentials for. An
// attached client has none of its own: credentials live on the host that runs
// the agent, which is the whole reason the panel needs to be told about them.
func (s *Service) AuthProviders(ctx context.Context) (ctrlproto.ProvidersView, error) {
	var r ctrlproto.ProvidersView
	err := s.c.Call(ctx, "", ctrlproto.MethodAuthProviders, nil, &r)
	return r, err
}

// The attached carrier serves the auth group too — over the wire, to the DAEMON's
// credential store, which is the only one that matters. `terva attach`'s /login
// therefore works, and works on the right machine.
var _ ctrlproto.AuthController = (*Service)(nil)

// --- auth group (optional; served only when the daemon advertised GroupAuth) ---
//
// These drive a login on the DAEMON. The secret the user types travels one way,
// in: nothing on this wire ever hands a credential back.

func (s *Service) AuthLoginStart(ctx context.Context, p ctrlproto.AuthLoginStartParams) (ctrlproto.AuthFlowStep, error) {
	var r ctrlproto.AuthFlowStep
	err := s.c.Call(ctx, "", ctrlproto.MethodAuthLoginStart, p, &r)
	return r, err
}

func (s *Service) AuthLoginSubmit(ctx context.Context, p ctrlproto.AuthLoginSubmitParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodAuthLoginSubmit, p, nil)
}

func (s *Service) AuthLoginCancel(ctx context.Context, p ctrlproto.AuthFlowRef) error {
	return s.c.Call(ctx, "", ctrlproto.MethodAuthLoginCancel, p, nil)
}

func (s *Service) AuthLogout(ctx context.Context, p ctrlproto.AuthLogoutParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodAuthLogout, p, nil)
}

func (s *Service) AuthEndpointRemove(ctx context.Context, p ctrlproto.AuthEndpointRemoveParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodAuthEndpointRemove, p, nil)
}

// --- model params (models.json overrides) ---

var _ ctrlproto.ModelParamsController = (*Service)(nil)

func (s *Service) ModelParams(ctx context.Context, p ctrlproto.ModelParamsParams) (ctrlproto.ModelParamsView, error) {
	var r ctrlproto.ModelParamsView
	err := s.c.Call(ctx, "", ctrlproto.MethodModelParams, p, &r)
	return r, err
}

func (s *Service) ModelParamsSet(ctx context.Context, p ctrlproto.ModelParamsSetParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodModelParamsSet, p, nil)
}

func (s *Service) ModelParamsReset(ctx context.Context, p ctrlproto.ModelParamsParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodModelParamsReset, p, nil)
}

// --- card library ---

var _ ctrlproto.CardsController = (*Service)(nil)

func (s *Service) CardsList(ctx context.Context) (ctrlproto.CardsListResult, error) {
	var r ctrlproto.CardsListResult
	err := s.c.Call(ctx, "", ctrlproto.MethodCardsList, nil, &r)
	return r, err
}

func (s *Service) CardsGet(ctx context.Context, p ctrlproto.CardGetParams) (ctrlproto.CardView, error) {
	var r ctrlproto.CardView
	err := s.c.Call(ctx, "", ctrlproto.MethodCardsGet, p, &r)
	return r, err
}

func (s *Service) CardsImport(ctx context.Context, p ctrlproto.CardImportParams) (ctrlproto.CardView, error) {
	var r ctrlproto.CardView
	err := s.c.Call(ctx, "", ctrlproto.MethodCardsImport, p, &r)
	return r, err
}

func (s *Service) CardsEdit(ctx context.Context, p ctrlproto.CardEditParams) (ctrlproto.CardView, error) {
	var r ctrlproto.CardView
	err := s.c.Call(ctx, "", ctrlproto.MethodCardsEdit, p, &r)
	return r, err
}

func (s *Service) CardsDelete(ctx context.Context, p ctrlproto.CardDeleteParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodCardsDelete, p, nil)
}

func (s *Service) CardsExport(ctx context.Context, p ctrlproto.CardExportParams) (ctrlproto.CardExport, error) {
	var r ctrlproto.CardExport
	err := s.c.Call(ctx, "", ctrlproto.MethodCardsExport, p, &r)
	return r, err
}

func (s *Service) CardsLint(ctx context.Context, p ctrlproto.CardLintParams) (ctrlproto.CardLintResult, error) {
	var r ctrlproto.CardLintResult
	err := s.c.Call(ctx, "", ctrlproto.MethodCardsLint, p, &r)
	return r, err
}

// --- persona library ---

var _ ctrlproto.PersonasController = (*Service)(nil)

func (s *Service) PersonasList(ctx context.Context) (ctrlproto.PersonasListResult, error) {
	var r ctrlproto.PersonasListResult
	err := s.c.Call(ctx, "", ctrlproto.MethodPersonasList, nil, &r)
	return r, err
}

func (s *Service) PersonasGet(ctx context.Context, p ctrlproto.PersonaGetParams) (ctrlproto.PersonaView, error) {
	var r ctrlproto.PersonaView
	err := s.c.Call(ctx, "", ctrlproto.MethodPersonasGet, p, &r)
	return r, err
}

func (s *Service) PersonasCreate(ctx context.Context, p ctrlproto.PersonaWriteParams) (ctrlproto.PersonaView, error) {
	var r ctrlproto.PersonaView
	err := s.c.Call(ctx, "", ctrlproto.MethodPersonasCreate, p, &r)
	return r, err
}

func (s *Service) PersonasEdit(ctx context.Context, p ctrlproto.PersonaWriteParams) (ctrlproto.PersonaView, error) {
	var r ctrlproto.PersonaView
	err := s.c.Call(ctx, "", ctrlproto.MethodPersonasEdit, p, &r)
	return r, err
}

func (s *Service) PersonasDelete(ctx context.Context, p ctrlproto.PersonaDeleteParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodPersonasDelete, p, nil)
}

// --- backgrounds ---

var _ ctrlproto.BackgroundsController = (*Service)(nil)

func (s *Service) BackgroundsList(ctx context.Context) (ctrlproto.BackgroundsListResult, error) {
	var r ctrlproto.BackgroundsListResult
	err := s.c.Call(ctx, "", ctrlproto.MethodBackgroundsList, nil, &r)
	return r, err
}

func (s *Service) BackgroundsImport(ctx context.Context, p ctrlproto.BackgroundImportParams) (ctrlproto.BackgroundView, error) {
	var r ctrlproto.BackgroundView
	err := s.c.Call(ctx, "", ctrlproto.MethodBackgroundsImport, p, &r)
	return r, err
}

func (s *Service) BackgroundsDelete(ctx context.Context, p ctrlproto.BackgroundDeleteParams) error {
	return s.c.Call(ctx, "", ctrlproto.MethodBackgroundsDelete, p, nil)
}

func (s *Service) BackgroundBind(ctx context.Context, sess string, p ctrlproto.BackgroundBindParams) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodBackgroundBind, p, nil)
}

func (s *Service) BackgroundsGenerate(ctx context.Context, sess string, p ctrlproto.BackgroundGenerateParams) (ctrlproto.BackgroundView, error) {
	var r ctrlproto.BackgroundView
	err := s.c.Call(ctx, sess, ctrlproto.MethodBackgroundGenerate, p, &r)
	return r, err
}

// --- control group ---

func (s *Service) Models(ctx context.Context, sess string) ([]ctrlproto.ModelInfo, error) {
	var r ctrlproto.ModelsResult
	err := s.c.Call(ctx, sess, ctrlproto.MethodModelsList, nil, &r)
	return r.Models, err
}

func (s *Service) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	return s.c.Call(ctx, sess, ctrlproto.MethodModelSwitch, ctrlproto.ModelSwitchParams{
		Provider: providerName, Model: modelID,
	}, nil)
}

func (s *Service) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	return s.c.Call(ctx, "", ctrlproto.MethodModelFavorite, ctrlproto.FavoriteParams{
		Provider: provider, Model: model, On: on,
	}, nil)
}

func (s *Service) SetDefaultModel(ctx context.Context, provider, model string, scope ctrlproto.DefaultScope) error {
	return s.c.Call(ctx, "", ctrlproto.MethodModelSetDefault, ctrlproto.SetDefaultParams{
		Provider: provider, Model: model, Scope: scope,
	}, nil)
}

func (s *Service) Trust(ctx context.Context, parent bool) error {
	return s.c.Call(ctx, "", ctrlproto.MethodTrust, ctrlproto.TrustParams{Parent: parent}, nil)
}

func (s *Service) Untrust(ctx context.Context) error {
	return s.c.Call(ctx, "", ctrlproto.MethodUntrust, nil, nil)
}

func (s *Service) Restart(ctx context.Context) error {
	return s.c.Call(ctx, "", ctrlproto.MethodRestart, nil, nil)
}
