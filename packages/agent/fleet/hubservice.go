package fleet

import (
	"context"
	"errors"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// HubService adapts an [Aggregate] to the ctrlproto.WorkspaceService that
// web.Serve takes, so a browser on the hub talks to a fleet through the same
// interface it already uses for one workspace.
//
// Every method is written out. None is inherited. Embedding a workspace here
// would be the bug this whole milestone guards against: an unrouted method
// would answer from the hub's own workspace for a member-addressed session id,
// silently. Written out, a method added to the interface later breaks the build
// instead, and somebody has to decide what it means for a fleet.
//
// Three kinds of method, three answers.
//
// Session-addressed READS route to the member that owns the session. That is
// what read-only aggregation is for: a browser has to fetch a transcript and
// fill the panels around it, or a member's sessions are dead tiles in a list.
//
// Session-addressed COMMANDS refuse with [ErrRemoteNotRouted] when the session
// lives on a member, and pass through when it is the hub's own. Routed writes
// belong to fleet-control.
//
// Methods that carry no session id are HUB-SCOPED. See [HubService.Restart].
//
// The read set is an allowlist and that direction matters. A method added to
// the interface later refuses until somebody promotes it deliberately. The
// opposite default would route new methods to members, which is how a write
// reaches a machine nobody meant to expose.
type HubService struct {
	agg *Aggregate
}

// NewHubService wraps agg for web.Serve.
func NewHubService(agg *Aggregate) (*HubService, error) {
	if agg == nil {
		return nil, errors.New("fleet: a hub service needs an Aggregate to read the fleet through")
	}
	return &HubService{agg: agg}, nil
}

// This is the completeness check. If ctrlproto.WorkspaceService grows a method
// and nobody writes it here, the build fails at this line rather than at a
// member's session months later.
var _ ctrlproto.WorkspaceService = (*HubService)(nil)

// read resolves a session-addressed read to the workspace that owns it, which
// may be a member.
func (s *HubService) read(federated string) (ctrlproto.WorkspaceService, string, error) {
	src, id, err := s.agg.Route(federated)
	if err != nil {
		return nil, "", err
	}
	return src.Svc, id, nil
}

// command resolves a session-addressed command, refusing one that lands on a
// member. The zero workspace on refusal is deliberate, matching
// [Aggregate.RouteCommand]: a caller that ignores the error holds nothing it
// can drive.
func (s *HubService) command(federated string) (ctrlproto.WorkspaceService, string, error) {
	src, id, err := s.agg.RouteCommand(federated)
	if err != nil {
		return nil, "", err
	}
	return src.Svc, id, nil
}

// hub is the workspace for a method that carries no session id.
func (s *HubService) hub() ctrlproto.WorkspaceService { return s.agg.local }

// --- reads that route to their member ---

func (s *HubService) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	return s.agg.Sessions(ctx)
}

func (s *HubService) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	return s.agg.Subscribe(ctx, sess)
}

func (s *HubService) Usage(ctx context.Context, sess string) (core.WireUsage, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return core.WireUsage{}, err
	}
	return svc.Usage(ctx, id)
}

// UsageSnapshot routes, with one caveat worth knowing. UsageSnapshotParams
// documents Refresh as pulling from the provider's usage endpoint and blocking
// on the fetch, so a hub asking for a refreshed snapshot spends the member's
// rate limit. It touches no session state, so it stays a read. If the fleet
// ever wants a budget boundary between hub and member, this method finds it
// first.
func (s *HubService) UsageSnapshot(ctx context.Context, sess string, refresh bool) (ctrlproto.UsageInfo, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return ctrlproto.UsageInfo{}, err
	}
	return svc.UsageSnapshot(ctx, id, refresh)
}

func (s *HubService) ListResets(ctx context.Context, sess string) (ctrlproto.ResetsListResult, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return ctrlproto.ResetsListResult{}, err
	}
	return svc.ListResets(ctx, id)
}

func (s *HubService) Context(ctx context.Context, sess string) (ctrlproto.ContextBreakdown, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return ctrlproto.ContextBreakdown{}, err
	}
	return svc.Context(ctx, id)
}

func (s *HubService) History(ctx context.Context, sess string, before, limit int, epoch uint64) (ctrlproto.HistoryResult, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return ctrlproto.HistoryResult{}, err
	}
	return svc.History(ctx, id, before, limit, epoch)
}

func (s *HubService) Reveal(ctx context.Context, sess string, ordinal int) (ctrlproto.RevealResult, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return ctrlproto.RevealResult{}, err
	}
	return svc.Reveal(ctx, id, ordinal)
}

func (s *HubService) Surfaces(ctx context.Context, sess string) ([]ctrlproto.SurfaceMeta, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return nil, err
	}
	return svc.Surfaces(ctx, id)
}

func (s *HubService) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	svc, sid, err := s.read(sess)
	if err != nil {
		return ctrlproto.Surface{}, err
	}
	return svc.Surface(ctx, sid, id)
}

func (s *HubService) ToolDisplays(ctx context.Context, sess string) (map[string]ctrlproto.ToolDisplay, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return nil, err
	}
	return svc.ToolDisplays(ctx, id)
}

func (s *HubService) Models(ctx context.Context, sess string) (ctrlproto.ModelsResult, error) {
	svc, id, err := s.read(sess)
	if err != nil {
		return ctrlproto.ModelsResult{}, err
	}
	return svc.Models(ctx, id)
}

// --- commands, refused when the session lives on a member ---

func (s *HubService) Prompt(ctx context.Context, sess string, p ctrlproto.PromptParams) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Prompt(ctx, id, p)
}

func (s *HubService) Queue(ctx context.Context, sess, text string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Queue(ctx, id, text)
}

func (s *HubService) SetQueue(ctx context.Context, sess string, texts []string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SetQueue(ctx, id, texts)
}

func (s *HubService) Cancel(ctx context.Context, sess string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Cancel(ctx, id)
}

func (s *HubService) Compact(ctx context.Context, sess string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Compact(ctx, id)
}

func (s *HubService) Clear(ctx context.Context, sess string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Clear(ctx, id)
}

func (s *HubService) EditMessage(ctx context.Context, sess string, epoch uint64, index int, text string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.EditMessage(ctx, id, epoch, index, text)
}

func (s *HubService) DeleteMessage(ctx context.Context, sess string, epoch uint64, index int) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.DeleteMessage(ctx, id, epoch, index)
}

func (s *HubService) SwipeTurn(ctx context.Context, sess string, epoch uint64, variant int) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SwipeTurn(ctx, id, epoch, variant)
}

func (s *HubService) SwipeMessage(ctx context.Context, sess string, epoch uint64, index, variant int) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SwipeMessage(ctx, id, epoch, index, variant)
}

func (s *HubService) RetryTurn(ctx context.Context, sess string, p ctrlproto.TurnRetryParams) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.RetryTurn(ctx, id, p)
}

func (s *HubService) ResumeTurn(ctx context.Context, sess string, p ctrlproto.TurnResumeParams) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.ResumeTurn(ctx, id, p)
}

func (s *HubService) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Approve(ctx, id, callID, d)
}

func (s *HubService) Answer(ctx context.Context, sess, askID string, answers []core.UserAnswer) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.Answer(ctx, id, askID, answers)
}

func (s *HubService) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	svc, id, err := s.command(sess)
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	return svc.ResumeSession(ctx, id)
}

func (s *HubService) ForkSession(ctx context.Context, sess string, fromIndex int) (ctrlproto.SessionInfo, error) {
	svc, id, err := s.command(sess)
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	return svc.ForkSession(ctx, id, fromIndex)
}

func (s *HubService) RenameSession(ctx context.Context, sess, title string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.RenameSession(ctx, id, title)
}

// GenerateSessionTitle reads like a getter and is not. It asks a model to write
// a title and stores the result, so it is a command.
func (s *HubService) GenerateSessionTitle(ctx context.Context, sess string) (string, error) {
	svc, id, err := s.command(sess)
	if err != nil {
		return "", err
	}
	return svc.GenerateSessionTitle(ctx, id)
}

func (s *HubService) DeleteSession(ctx context.Context, sess string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.DeleteSession(ctx, id)
}

func (s *HubService) ConsumeReset(ctx context.Context, sess, id string) (ctrlproto.ResetConsumeResult, error) {
	svc, sid, err := s.command(sess)
	if err != nil {
		return ctrlproto.ResetConsumeResult{}, err
	}
	return svc.ConsumeReset(ctx, sid, id)
}

func (s *HubService) SideChatOpen(ctx context.Context, sess string) (string, error) {
	svc, id, err := s.command(sess)
	if err != nil {
		return "", err
	}
	return svc.SideChatOpen(ctx, id)
}

func (s *HubService) SideChatAsk(ctx context.Context, sess, id string, prior []ctrlproto.SideChatTurn, question string) (string, error) {
	svc, sid, err := s.command(sess)
	if err != nil {
		return "", err
	}
	return svc.SideChatAsk(ctx, sid, id, prior, question)
}

func (s *HubService) SideChatClose(ctx context.Context, sess, id string) error {
	svc, sid, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SideChatClose(ctx, sid, id)
}

// Node refuses despite looking like a read. ContextNodeParams carries an Op
// string with no documented value set, so I cannot show it is inert. Promoting
// it to a read is a one-line change once somebody checks what Op does.
func (s *HubService) Node(ctx context.Context, sess, id, op string) (ctrlproto.ContextNode, error) {
	svc, sid, err := s.command(sess)
	if err != nil {
		return ctrlproto.ContextNode{}, err
	}
	return svc.Node(ctx, sid, id, op)
}

func (s *HubService) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	svc, sid, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SurfaceAction(ctx, sid, id, action, args)
}

func (s *HubService) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SwitchModel(ctx, id, providerName, modelID)
}

func (s *HubService) SetSessionReasoning(ctx context.Context, sess, level string) error {
	svc, id, err := s.command(sess)
	if err != nil {
		return err
	}
	return svc.SetSessionReasoning(ctx, id, level)
}

// --- hub-scoped: no session id, so no member to route to ---

// CreateSession creates on the hub's own workspace.
//
// This is the hub-scoped answer that will feel wrong first. A person looking at
// a fleet board and asking for a new session probably means "on that machine",
// and this gives them one on the hub instead. Naming a target member needs a
// parameter that CreateOpts does not have, and a wire change belongs with
// fleet-control rather than here. The honest reading today is that the hub's
// own workspace is where the hub creates sessions.
func (s *HubService) CreateSession(ctx context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	return s.hub().CreateSession(ctx, opts)
}

func (s *HubService) Catalog(ctx context.Context, lang string) (ctrlproto.CatalogView, error) {
	return s.hub().Catalog(ctx, lang)
}

// ListFiles lists the hub's own working directory. A fleet file browser would
// have to say which machine it is browsing, and FilesListParams has no field
// for that.
func (s *HubService) ListFiles(ctx context.Context, opts ctrlproto.FilesListParams) (ctrlproto.FilesListResult, error) {
	return s.hub().ListFiles(ctx, opts)
}

// AuthProviders reports the hub's credentials. A member authenticates itself
// and the hub never sees or brokers those credentials.
func (s *HubService) AuthProviders(ctx context.Context) (ctrlproto.ProvidersView, error) {
	return s.hub().AuthProviders(ctx)
}

func (s *HubService) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	return s.hub().SetFavoriteModel(ctx, provider, model, on)
}

func (s *HubService) SetModelHidden(ctx context.Context, provider, model string, on bool) error {
	return s.hub().SetModelHidden(ctx, provider, model, on)
}

func (s *HubService) SetDefaultModel(ctx context.Context, provider, model string, scope ctrlproto.DefaultScope) error {
	return s.hub().SetDefaultModel(ctx, provider, model, scope)
}

// Trust applies to the hub's own workspace. Trusting a member's workspace is
// that member's decision, taken on that machine.
func (s *HubService) Trust(ctx context.Context, parent bool) error {
	return s.hub().Trust(ctx, parent)
}

func (s *HubService) Untrust(ctx context.Context) error {
	return s.hub().Untrust(ctx)
}

// Restart restarts the hub process and nothing else.
//
// This is the hub-scoped rule at its sharpest, so it is the one with a test. A
// restart that reached the fleet would take down every member from a button
// that says nothing about them. Members restart on their own machines.
func (s *HubService) Restart(ctx context.Context) error {
	return s.hub().Restart(ctx)
}
