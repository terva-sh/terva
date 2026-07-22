package ctrlproto

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"terva.sh/terva/packages/i18n"
)

// FrameConn is a bidirectional frame transport. Each carrier — WebSocket,
// stdio, an in-memory pipe — implements it, and [ServeConn] drives the server
// side over it. ReadFrame is only ever called from ServeConn's single read
// goroutine; WriteFrame may be called concurrently (responses from the read
// loop, events from subscription pumps), but ServeConn serializes every write
// through one mutex, so a carrier's WriteFrame need not be goroutine-safe.
type FrameConn interface {
	// ReadFrame reads the next frame, blocking until one arrives, ctx is
	// cancelled, or the peer goes away (return a non-nil error for the last
	// two).
	ReadFrame(ctx context.Context) (Frame, error)
	// WriteFrame writes one frame.
	WriteFrame(ctx context.Context, f Frame) error
	// Close releases the transport.
	Close() error
}

// ServeConn runs the server side of ctrlproto over conn, backed by svc. It
// performs the hello handshake (the client sends its hello first; this replies
// with serverHello and returns the negotiated [Contract]), then reads command
// frames and dispatches them to svc, pumping each subscribed session's event
// stream back as event frames. It blocks until the read loop ends — ctx
// cancellation, the peer closing, or a transport error — and returns that
// error (nil-ish transport EOFs included; callers typically ignore the error
// on a clean disconnect).
//
// ServeConn does not close conn; the carrier owns its lifecycle.
func ServeConn(ctx context.Context, conn FrameConn, svc WorkspaceService, serverHello Hello) (Contract, error) {
	first, err := conn.ReadFrame(ctx)
	if err != nil {
		return Contract{}, err
	}
	if first.Kind != KindHello || first.Hello == nil {
		_ = conn.WriteFrame(ctx, ErrFrame(first.ID, CodeBadRequest, "expected hello frame"))
		return Contract{}, fmt.Errorf("ctrlproto: expected hello, got %q", first.Kind)
	}
	contract := Negotiate(serverHello, *first.Hello)

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wmu sync.Mutex
	write := func(f Frame) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteFrame(loopCtx, f)
	}
	if err := write(HelloFrame(serverHello)); err != nil {
		return contract, err
	}

	s := &serveState{svc: svc, contract: contract, write: write, subs: map[string]context.CancelFunc{}}
	defer s.closeSubs()

	// A client that did not negotiate FeatureWorkspaceEvents cannot subscribe to
	// the workspace, so relay its events into the session subscriptions it does
	// hold — which is exactly what the workspace itself used to do before it had
	// a hub of its own. The duplication is now a property of this client's
	// contract rather than of the workspace, which publishes once.
	if !contract.HasFeature(FeatureWorkspaceEvents) {
		s.relayWorkspaceEvents(loopCtx)
	}

	for {
		f, err := conn.ReadFrame(loopCtx)
		if err != nil {
			return contract, err
		}
		if f.Kind == KindCmd {
			s.handle(loopCtx, f)
		}
		// Client-sent hello/resp/event frames are not expected; ignore them
		// rather than tearing down the connection.
	}
}

// serveState is the per-connection dispatch state.
type serveState struct {
	svc      WorkspaceService
	contract Contract
	write    func(Frame) error

	mu   sync.Mutex
	subs map[string]context.CancelFunc // session id → pump canceller
}

// handle dispatches one command frame, enforcing method-group negotiation.
func (s *serveState) handle(ctx context.Context, f Frame) {
	if g := f.Method.Group(); g != "" && !s.contract.Has(g) {
		s.write(ErrFrame(f.ID, CodeUnsupported, "method group not negotiated: "+string(g)))
		return
	}

	switch f.Method {
	// --- conversation ---
	case MethodPrompt:
		var p PromptParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.Prompt(ctx, f.Sess, p.Text, p.Images))
	case MethodQueue:
		var p QueueParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.Queue(ctx, f.Sess, p.Text))
	case MethodQueueSet:
		var p QueueSetParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SetQueue(ctx, f.Sess, p.Texts))
	case MethodCancel:
		s.respond(f.ID, nil, s.svc.Cancel(ctx, f.Sess))
	case MethodCompact:
		s.respond(f.ID, nil, s.svc.Compact(ctx, f.Sess))
	case MethodClear:
		s.respond(f.ID, nil, s.svc.Clear(ctx, f.Sess))
	case MethodMessageEdit:
		var p MessageEditParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.EditMessage(ctx, f.Sess, p.Epoch, p.Index, p.Text))
	case MethodMessageDelete:
		var p MessageDeleteParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.DeleteMessage(ctx, f.Sess, p.Epoch, p.Index))
	case MethodTurnSwipe:
		var p TurnSwipeParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		if p.Index != nil {
			s.respond(f.ID, nil, s.svc.SwipeMessage(ctx, f.Sess, p.Epoch, *p.Index, p.Variant))
		} else {
			s.respond(f.ID, nil, s.svc.SwipeTurn(ctx, f.Sess, p.Epoch, p.Variant))
		}
	case MethodTurnRetry:
		var p TurnRetryParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.RetryTurn(ctx, f.Sess, p))

	case MethodTurnContinue:
		cc, ok := s.svc.(ContinueController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("continue is not available here")))
			return
		}
		var p TurnContinueParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, cc.ContinueTurn(ctx, f.Sess, p.Epoch))
	case MethodApprove:
		var p ApproveParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.Approve(ctx, f.Sess, p.CallID, p.Decision.Core()))
	case MethodAnswer:
		var p AnswerParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.Answer(ctx, f.Sess, p.AskID, p.Answer.Core()))
	case MethodSubscribe:
		s.subscribe(ctx, f)
	case MethodUnsubscribe:
		s.unsubscribe(f.Sess)
		s.respond(f.ID, nil, nil)

	// --- session ---
	case MethodSessionsList:
		list, err := s.svc.Sessions(ctx)
		s.respond(f.ID, SessionsResult{Sessions: list}, err)
	case MethodSessionCreate:
		var o CreateOpts
		if err := f.Bind(&o); err != nil {
			s.badReq(f.ID, err)
			return
		}
		info, err := s.svc.CreateSession(ctx, o)
		s.respond(f.ID, SessionResult{Session: info}, err)
	case MethodSessionResume:
		info, err := s.svc.ResumeSession(ctx, f.Sess)
		s.respond(f.ID, SessionResult{Session: info}, err)
	case MethodSessionFork:
		var p SessionForkParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		info, err := s.svc.ForkSession(ctx, f.Sess, p.FromIndex)
		s.respond(f.ID, SessionResult{Session: info}, err)
	case MethodSessionRename:
		var p RenameParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.RenameSession(ctx, f.Sess, p.Title))
	case MethodSessionGenerateTitle:
		// Blocks on the model — the same synchronous posture as MethodCompact.
		title, err := s.svc.GenerateSessionTitle(ctx, f.Sess)
		s.respond(f.ID, GenerateTitleResult{Title: title}, err)
	case MethodSessionDelete:
		s.respond(f.ID, nil, s.svc.DeleteSession(ctx, f.Sess))
	case MethodUsageGet:
		u, err := s.svc.Usage(ctx, f.Sess)
		s.respond(f.ID, UsageResult{Usage: u}, err)
	case MethodUsageSnapshot:
		var p UsageSnapshotParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		u, err := s.svc.UsageSnapshot(ctx, f.Sess, p.Refresh)
		s.respond(f.ID, UsageSnapshotResult{Usage: u}, err)
	case MethodResetsList:
		res, err := s.svc.ListResets(ctx, f.Sess)
		s.respond(f.ID, res, err)
	case MethodResetsConsume:
		var p ResetConsumeParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		res, err := s.svc.ConsumeReset(ctx, f.Sess, p.ID)
		s.respond(f.ID, res, err)
	case MethodSideChatOpen:
		id, err := s.svc.SideChatOpen(ctx, f.Sess)
		s.respond(f.ID, SideChatOpenResult{ID: id}, err)
	case MethodSideChatAsk:
		var p SideChatAskParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		// Blocks on the model — the same synchronous posture as MethodCompact.
		// The in-process TUI carrier bypasses this loop entirely (it calls the
		// WorkspaceService method directly, on the dialog's own goroutine), so
		// /btw never stalls the session stream; a networked caller pays the same
		// per-connection block compact already has.
		text, err := s.svc.SideChatAsk(ctx, f.Sess, p.ID, p.Prior, p.Question)
		s.respond(f.ID, SideChatAskResult{Text: text}, err)
	case MethodSideChatClose:
		var p SideChatCloseParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SideChatClose(ctx, f.Sess, p.ID))
	case MethodContextGet:
		b, err := s.svc.Context(ctx, f.Sess)
		s.respond(f.ID, ContextResult{Breakdown: b}, err)
	case MethodContextNode:
		var p ContextNodeParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		n, err := s.svc.Node(ctx, f.Sess, p.ID, p.Op)
		s.respond(f.ID, ContextNodeResult{Node: n}, err)
	case MethodConversationReveal:
		var p RevealParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		res, err := s.svc.Reveal(ctx, f.Sess, p.Ordinal)
		s.respond(f.ID, res, err)
	case MethodConversationHistory:
		var p HistoryParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		res, err := s.svc.History(ctx, f.Sess, p.Before, p.Limit, p.Epoch)
		s.respond(f.ID, res, err)
	case MethodSurfacesList:
		list, err := s.svc.Surfaces(ctx, f.Sess)
		s.respond(f.ID, SurfacesResult{Surfaces: list}, err)
	case MethodSurfaceGet:
		var p SurfaceGetParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		sf, err := s.svc.Surface(ctx, f.Sess, p.ID)
		s.respond(f.ID, SurfaceResult{Surface: sf}, err)
	case MethodSurfaceAction:
		var p SurfaceActionParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SurfaceAction(ctx, f.Sess, p.ID, p.Action, p.Args))
	case MethodI18nCatalog:
		var p I18nCatalogParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		cv, err := s.svc.Catalog(ctx, p.Lang)
		s.respond(f.ID, I18nCatalogResult{Catalog: cv}, err)
	case MethodFilesList:
		var p FilesListParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		res, err := s.svc.ListFiles(ctx, p)
		s.respond(f.ID, res, err)

	case MethodAuthProviders:
		// Session-independent: f.Sess is ignored on purpose. A credential belongs
		// to the daemon, not to a conversation.
		res, err := s.svc.AuthProviders(ctx)
		s.respond(f.ID, res, err)

	// --- control ---
	case MethodModelsList:
		m, err := s.svc.Models(ctx, f.Sess)
		s.respond(f.ID, ModelsResult{Models: m}, err)
	case MethodModelSwitch:
		var p ModelSwitchParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SwitchModel(ctx, f.Sess, p.Provider, p.Model))
	case MethodModelFavorite:
		var p FavoriteParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SetFavoriteModel(ctx, p.Provider, p.Model, p.On))
	case MethodModelSetDefault:
		var p SetDefaultParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SetDefaultModel(ctx, p.Provider, p.Model, p.Scope))
	case MethodTrust:
		var p TrustParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.Trust(ctx, p.Parent))
	case MethodUntrust:
		s.respond(f.ID, nil, s.svc.Untrust(ctx))
	case MethodRestart:
		s.respond(f.ID, nil, s.svc.Restart(ctx))

	// --- auth (optional; served only by an AuthController, and only when the
	// carrier advertised GroupAuth. Every one of these CHANGES a credential;
	// none of them returns one.) ---
	case MethodAuthLoginStart:
		ac, ok := s.svc.(AuthController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("login not supported")))
			return
		}
		var p AuthLoginStartParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		step, err := ac.AuthLoginStart(ctx, p)
		s.respond(f.ID, step, err)

	case MethodAuthLoginSubmit:
		ac, ok := s.svc.(AuthController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("login not supported")))
			return
		}
		var p AuthLoginSubmitParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, ac.AuthLoginSubmit(ctx, p))

	case MethodAuthLoginCancel:
		ac, ok := s.svc.(AuthController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("login not supported")))
			return
		}
		var p AuthFlowRef
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, ac.AuthLoginCancel(ctx, p))

	case MethodAuthLogout:
		ac, ok := s.svc.(AuthController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("login not supported")))
			return
		}
		var p AuthLogoutParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, ac.AuthLogout(ctx, p))

	case MethodModelParams, MethodModelParamsSet, MethodModelParamsReset:
		// Optional, like the auth group: a carrier with no models.json to write
		// simply does not implement it, and says so rather than failing deeper.
		mc, ok := s.svc.(ModelParamsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("editing model settings is not supported here")))
			return
		}
		switch f.Method {
		case MethodModelParams:
			var p ModelParamsParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := mc.ModelParams(ctx, p)
			s.respond(f.ID, v, err)
		case MethodModelParamsSet:
			var p ModelParamsSetParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, mc.ModelParamsSet(ctx, p))
		case MethodModelParamsReset:
			var p ModelParamsParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, mc.ModelParamsReset(ctx, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled model-params verb %q", f.Method)))
		}

	case MethodAuthEndpointRemove:
		ac, ok := s.svc.(AuthController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, i18n.T("login not supported")))
			return
		}
		var p AuthEndpointRemoveParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, ac.AuthEndpointRemove(ctx, p))

	case MethodCardsList, MethodCardsGet, MethodCardsImport, MethodCardsEdit, MethodCardsDelete, MethodCardsExport, MethodCardsLint, MethodCardsFavorite:
		// Optional, like the model-params group: a carrier with no card library
		// simply does not implement it and says so, rather than failing deeper.
		cc, ok := s.svc.(CardsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the card library is not available here"))
			return
		}
		switch f.Method {
		case MethodCardsList:
			v, err := cc.CardsList(ctx)
			s.respond(f.ID, v, err)
		case MethodCardsGet:
			var p CardGetParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cc.CardsGet(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardsImport:
			var p CardImportParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cc.CardsImport(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardsEdit:
			var p CardEditParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cc.CardsEdit(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardsExport:
			var p CardExportParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cc.CardsExport(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardsLint:
			var p CardLintParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cc.CardsLint(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardsDelete:
			var p CardDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, cc.CardsDelete(ctx, p))
		case MethodCardsFavorite:
			var p CardFavoriteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, cc.CardFavorite(ctx, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled card verb %q", f.Method)))
		}

	case MethodCardsDoctor:
		// Optional, like the card library — but also needs a usable model, so a
		// carrier without one (or without a credential) answers "unsupported".
		dc, ok := s.svc.(DoctorController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the card doctor is not available here"))
			return
		}
		var p DoctorParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := dc.CardsDoctor(ctx, p)
		s.respond(f.ID, v, err)

	case MethodSessionsDoctor:
		// Same posture as the card doctor: optional controller, needs a model.
		dc, ok := s.svc.(DoctorController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the session doctor is not available here"))
			return
		}
		var p SessionDoctorParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := dc.SessionsDoctor(ctx, f.Sess, p)
		s.respond(f.ID, v, err)

	case MethodSessionsNextScene:
		// Same posture again: the propose half is a Dramaturgi call, so a
		// carrier without a model answers "unsupported" rather than failing
		// deeper on the commit half it could technically serve.
		dc, ok := s.svc.(DoctorController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "scene breaks are not available here"))
			return
		}
		var p NextSceneParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := dc.SessionsNextScene(ctx, f.Sess, p)
		s.respond(f.ID, v, err)

	case MethodSessionsRealize:
		// Same posture: the propose half is a Kartoittaja call, so a carrier
		// without a model answers "unsupported".
		dc, ok := s.svc.(DoctorController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "realize is not available here"))
			return
		}
		var p RealizeParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := dc.SessionsRealize(ctx, f.Sess, p)
		s.respond(f.ID, v, err)

	case MethodSessionsExport:
		// An explicit arm, never the trailing `default:` idiom — that binds the
		// LAST verb's params struct to any new verb in the group, silently.
		ec, ok := s.svc.(ExportController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "session export is not available here"))
			return
		}
		var p SessionExportParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := ec.SessionsExport(ctx, f.Sess, p)
		s.respond(f.ID, v, err)

	case MethodSuggestReply:
		// Optional, like the card doctor — needs a model and the session's
		// transcript, so a carrier without one answers "unsupported".
		sc, ok := s.svc.(SuggestController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "reply suggestions are not available here"))
			return
		}
		var p SuggestParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := sc.SuggestReply(ctx, f.Sess, p)
		s.respond(f.ID, v, err)

	case MethodPersonasList, MethodPersonasGet, MethodPersonasCreate, MethodPersonasEdit, MethodPersonasDelete:
		// Optional, like the card library. Create/edit/delete are trusted-tier —
		// the workspace enforces the gate; the group only decides whether it's
		// served.
		pc, ok := s.svc.(PersonasController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the persona library is not available here"))
			return
		}
		switch f.Method {
		case MethodPersonasList:
			v, err := pc.PersonasList(ctx)
			s.respond(f.ID, v, err)
		case MethodPersonasGet:
			var p PersonaGetParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := pc.PersonasGet(ctx, p)
			s.respond(f.ID, v, err)
		case MethodPersonasCreate:
			var p PersonaWriteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := pc.PersonasCreate(ctx, p)
			s.respond(f.ID, v, err)
		case MethodPersonasEdit:
			var p PersonaWriteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := pc.PersonasEdit(ctx, p)
			s.respond(f.ID, v, err)
		case MethodPersonasDelete:
			var p PersonaDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, pc.PersonasDelete(ctx, p))
		default:
			// Every member of the outer case list is handled above. This arm is a
			// backstop for a verb added there and forgotten here: the previous
			// shape made edit the default, so a new member silently BOUND ITS
			// PARAMS AS A WRITE and overwrote the persona with an empty one.
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled persona verb %q", f.Method)))
		}

	case MethodBackgroundsList, MethodBackgroundsImport, MethodBackgroundsDelete, MethodBackgroundBind, MethodBackgroundGenerate:
		bc, ok := s.svc.(BackgroundsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "scene backgrounds are not available here"))
			return
		}
		switch f.Method {
		case MethodBackgroundsList:
			v, err := bc.BackgroundsList(ctx)
			s.respond(f.ID, v, err)
		case MethodBackgroundGenerate:
			var p BackgroundGenerateParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := bc.BackgroundsGenerate(ctx, f.Sess, p)
			s.respond(f.ID, v, err)
		case MethodBackgroundsImport:
			var p BackgroundImportParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := bc.BackgroundsImport(ctx, p)
			s.respond(f.ID, v, err)
		case MethodBackgroundsDelete:
			var p BackgroundDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, bc.BackgroundsDelete(ctx, p))
		case MethodBackgroundBind:
			var p BackgroundBindParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, bc.BackgroundBind(ctx, f.Sess, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled background verb %q", f.Method)))
		}

	case MethodNoteSet:
		nc, ok := s.svc.(NoteController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the author's note is not available here"))
			return
		}
		var p NoteSetParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, nc.NoteSet(ctx, f.Sess, p))

	case MethodUserBind:
		uc, ok := s.svc.(UserController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the user persona is not available here"))
			return
		}
		var p UserBindParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, uc.UserBind(ctx, f.Sess, p))

	case MethodSessionDiscardDraft:
		dc, ok := s.svc.(DraftController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "draft sessions are not available here"))
			return
		}
		s.respond(f.ID, nil, dc.DiscardDraft(ctx, f.Sess))

	case MethodUserPersonasList, MethodUserPersonaSave, MethodUserPersonaDelete, MethodUserPersonaSetDefault:
		upc, ok := s.svc.(UserPersonasController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "saved user personas are not available here"))
			return
		}
		switch f.Method {
		case MethodUserPersonasList:
			v, err := upc.UserPersonasList(ctx)
			s.respond(f.ID, v, err)
		case MethodUserPersonaSave:
			var p UserPersonaView
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := upc.UserPersonaSave(ctx, p)
			s.respond(f.ID, v, err)
		case MethodUserPersonaDelete:
			var p UserPersonaRef
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, upc.UserPersonaDelete(ctx, p))
		case MethodUserPersonaSetDefault:
			var p UserPersonaRef
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, upc.UserPersonaSetDefault(ctx, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled user-persona verb %q", f.Method)))
		}

	case MethodCastAdd, MethodCastRemove:
		cc, ok := s.svc.(CastController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the cast is not available here"))
			return
		}
		var p CastMemberParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		// Switch, not if/else: an else arm is the same trap as a bare default —
		// a third verb added to the outer list above would silently REMOVE a
		// cast member. The backstop makes that a loud error instead.
		switch f.Method {
		case MethodCastAdd:
			s.respond(f.ID, nil, cc.CastAdd(ctx, f.Sess, p))
		case MethodCastRemove:
			s.respond(f.ID, nil, cc.CastRemove(ctx, f.Sess, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled cast verb %q", f.Method)))
		}

	case MethodCastSpeak:
		cc, ok := s.svc.(CastController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the cast is not available here"))
			return
		}
		var p CastSpeakParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, cc.CastSpeak(ctx, f.Sess, p))

	case MethodWorldLorePut, MethodWorldLoreDelete, MethodWorldSet:
		wc, ok := s.svc.(WorldController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the World is not available here"))
			return
		}
		switch f.Method {
		case MethodWorldLorePut:
			var p WorldLorePutParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, wc.WorldLorePut(ctx, f.Sess, p))
		case MethodWorldLoreDelete:
			var p WorldLoreDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, wc.WorldLoreDelete(ctx, f.Sess, p))
		case MethodWorldSet:
			var p WorldSetParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, wc.WorldSet(ctx, f.Sess, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled world verb %q", f.Method)))
		}

	case MethodWorldsList, MethodWorldSave, MethodWorldDelete, MethodWorldUpdate, MethodWorldSetCharacterModel, MethodWorldsExport, MethodWorldsImport:
		wc, ok := s.svc.(WorldController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "the World is not available here"))
			return
		}
		switch f.Method {
		case MethodWorldsList:
			v, err := wc.WorldsList(ctx)
			s.respond(f.ID, v, err)
		case MethodWorldSave:
			var p WorldSaveParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := wc.WorldSave(ctx, f.Sess, p)
			s.respond(f.ID, v, err)
		case MethodWorldUpdate:
			var p WorldUpdateParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := wc.WorldUpdate(ctx, p)
			s.respond(f.ID, v, err)
		case MethodWorldSetCharacterModel:
			var p WorldSetCharacterModelParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := wc.WorldSetCharacterModel(ctx, p)
			s.respond(f.ID, v, err)
		case MethodWorldsExport:
			var p WorldExportParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := wc.WorldsExport(ctx, p)
			s.respond(f.ID, v, err)
		case MethodWorldsImport:
			var p WorldImportParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := wc.WorldsImport(ctx, p)
			s.respond(f.ID, v, err)
		case MethodWorldDelete:
			var p WorldDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, wc.WorldDelete(ctx, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled worlds verb %q", f.Method)))
		}

	case MethodCardGroupsList, MethodCardGroupSave, MethodCardGroupDelete, MethodCardGroupSetMembers:
		cgc, ok := s.svc.(CardGroupsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "card groups are not available here"))
			return
		}
		switch f.Method {
		case MethodCardGroupsList:
			v, err := cgc.CardGroupsList(ctx)
			s.respond(f.ID, v, err)
		case MethodCardGroupSave:
			var p CardGroupSaveParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cgc.CardGroupSave(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardGroupSetMembers:
			var p CardGroupSetMembersParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := cgc.CardGroupSetMembers(ctx, p)
			s.respond(f.ID, v, err)
		case MethodCardGroupDelete:
			var p CardGroupDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, cgc.CardGroupDelete(ctx, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled card-group verb %q", f.Method)))
		}

	case MethodSessionGroupsList, MethodSessionGroupSave, MethodSessionGroupDelete, MethodSessionGroupSetMembers:
		sgc, ok := s.svc.(SessionGroupsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "session groups are not available here"))
			return
		}
		switch f.Method {
		case MethodSessionGroupsList:
			v, err := sgc.SessionGroupsList(ctx)
			s.respond(f.ID, v, err)
		case MethodSessionGroupSave:
			var p SessionGroupSaveParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := sgc.SessionGroupSave(ctx, p)
			s.respond(f.ID, v, err)
		case MethodSessionGroupSetMembers:
			var p SessionGroupSetMembersParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			v, err := sgc.SessionGroupSetMembers(ctx, p)
			s.respond(f.ID, v, err)
		case MethodSessionGroupDelete:
			var p SessionGroupDeleteParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, sgc.SessionGroupDelete(ctx, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled session-group verb %q", f.Method)))
		}

	case MethodPostLine, MethodDirectTurn, MethodTurnAdvance:
		// Optional, like suggest — needs a live session's transcript, so a carrier
		// without one answers "unsupported".
		dc, ok := s.svc.(DirectController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "directed authorship is not available here"))
			return
		}
		// Switch, not a chain of ifs with an implicit trailing arm: the fallthrough
		// used to be post_line, so a fourth verb added to the outer list above
		// would silently POST A LINE into the transcript with its params bound as
		// PostLineParams. The backstop makes that a loud error instead.
		switch f.Method {
		case MethodTurnAdvance:
			// No params by design: advance injects nothing.
			s.respond(f.ID, nil, dc.AdvanceTurn(ctx, f.Sess))
		case MethodDirectTurn:
			var p DirectTurnParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, dc.DirectTurn(ctx, f.Sess, p))
		case MethodPostLine:
			var p PostLineParams
			if err := f.Bind(&p); err != nil {
				s.badReq(f.ID, err)
				return
			}
			s.respond(f.ID, nil, dc.PostLine(ctx, f.Sess, p))
		default:
			s.write(ErrFrame(f.ID, CodeUnsupported, fmt.Sprintf("unhandled directed-authorship verb %q", f.Method)))
		}

	// --- message-scoped variant cleanup (optional; served only by a VariantsController) ---
	case MethodVariantsPrune:
		vc, ok := s.svc.(VariantsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "variant cleanup is not available here"))
			return
		}
		var p VariantsPruneParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, vc.PruneVariants(ctx, f.Sess, p.Epoch, p.Index))
	case MethodVariantsDrop:
		vc, ok := s.svc.(VariantsController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "variant cleanup is not available here"))
			return
		}
		var p VariantsDropParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, vc.DropVariant(ctx, f.Sess, p.Epoch, p.Index, p.Variant))

	// --- replay (optional; served only by a ReplayController) ---
	case MethodReplayControl:
		rc, ok := s.svc.(ReplayController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "replay not supported"))
			return
		}
		var p ReplayControlParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		st, err := rc.ReplayControl(ctx, f.Sess, p)
		s.respond(f.ID, ReplayStateResult{State: st}, err)
	case MethodReplayState:
		rc, ok := s.svc.(ReplayController)
		if !ok {
			s.write(ErrFrame(f.ID, CodeUnsupported, "replay not supported"))
			return
		}
		st, err := rc.ReplayState(ctx, f.Sess)
		s.respond(f.ID, ReplayStateResult{State: st}, err)

	default:
		s.write(ErrFrame(f.ID, CodeBadRequest, "unknown method: "+string(f.Method)))
	}
}

// respond writes a success frame carrying result, or maps err to a coded
// failure frame. A nil result yields a bare ok response.
func (s *serveState) respond(id uint64, result any, err error) {
	if err != nil {
		s.fail(id, err)
		return
	}
	fr, merr := OKFrame(id, result)
	if merr != nil {
		s.write(ErrFrame(id, CodeInternal, merr.Error()))
		return
	}
	s.write(fr)
}

func (s *serveState) badReq(id uint64, err error) {
	s.write(ErrFrame(id, CodeBadRequest, err.Error()))
}

// fail forwards a [*Error]'s code verbatim; any other error becomes internal.
// The message prose is passed through i18n.T on a COPY at this one emission
// point: a constant message (the i18n.M-marked sentinels) translates here,
// a parameterized one translated at its construction site passes through
// unchanged (a non-key is returned verbatim), and the shared sentinel values
// are never mutated.
func (s *serveState) fail(id uint64, err error) {
	var ce *Error
	if errors.As(err, &ce) {
		s.write(Frame{Kind: KindResp, ID: id, Error: &Error{Code: ce.Code, Message: i18n.T(ce.Message)}})
		return
	}
	s.write(ErrFrame(id, CodeInternal, err.Error()))
}

// relayWorkspaceEvents is the backward-compatibility half of the workspace
// address: it stamps each workspace event with every session id this connection
// is subscribed to, reproducing the fan-out the workspace used to do itself.
//
// Only for a client that did not negotiate FeatureWorkspaceEvents. A client that
// did subscribes to AddrWorkspace and receives one copy, correctly addressed.
//
// Ordering note: workspace events now reach the socket from their own pump, so
// they are no longer interleaved with a session's conversation events in a fixed
// order. Nothing depends on that. A workspace event says "something changed, go
// re-read it" — it carries no state whose position in the stream matters.
func (s *serveState) relayWorkspaceEvents(ctx context.Context) {
	ch, err := s.svc.Subscribe(ctx, AddrWorkspace)
	if err != nil {
		// A carrier with no workspace stream (the replay carrier) has no
		// workspace events to relay. Nothing to do, and not an error.
		return
	}
	strip := !s.contract.HasFeature(FeatureImageData)
	// A window is cut for a client that asked for one, and only for that client. The
	// hub broadcasts one full snapshot; this is the per-connection edge, where image
	// data is already dropped per contract. Window BEFORE stripping so the strip walk
	// only touches what will actually be sent.
	window := 0
	if s.contract.HasFeature(FeatureHistoryWindow) {
		window = HistoryWindow
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				ev = windowSnapshot(ev, window)
				if strip {
					ev = stripImageData(ev)
				}
				for _, sess := range s.sessionSubs() {
					if err := s.write(EventFrame(sess, ev)); err != nil {
						return
					}
				}
			}
		}
	}()
}

// sessionSubs lists the real sessions this connection is subscribed to —
// reserved addresses are not sessions and never appear.
func (s *serveState) sessionSubs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.subs))
	for sess := range s.subs {
		if !IsReservedAddr(sess) {
			out = append(out, sess)
		}
	}
	return out
}

// subscribe starts (idempotently) an event pump for the frame's session,
// forwarding every event as a [KindEvent] frame until the connection or the
// subscription ends.
func (s *serveState) subscribe(ctx context.Context, f Frame) {
	sess := f.Sess

	// The reserved addresses are not sessions, and only one of them exists. A
	// client must have said it understands them (FeatureWorkspaceEvents) before
	// it may subscribe to one — otherwise it would receive workspace events
	// twice, once here and once from the compat pump that is relaying them into
	// its session subscriptions on its behalf.
	if IsReservedAddr(sess) {
		switch {
		case sess != AddrWorkspace:
			s.write(ErrFrame(f.ID, CodeNotFound, "unknown reserved address: "+sess))
			return
		case !s.contract.HasFeature(FeatureWorkspaceEvents):
			s.write(ErrFrame(f.ID, CodeUnsupported, "feature not negotiated: "+FeatureWorkspaceEvents))
			return
		}
	}

	s.mu.Lock()
	if _, ok := s.subs[sess]; ok {
		s.mu.Unlock()
		s.respond(f.ID, nil, nil) // already subscribed — idempotent
		return
	}
	subCtx, cancel := context.WithCancel(ctx)
	ch, err := s.svc.Subscribe(subCtx, sess)
	if err != nil {
		cancel()
		s.mu.Unlock()
		s.fail(f.ID, err)
		return
	}
	s.subs[sess] = cancel
	s.mu.Unlock()
	s.respond(f.ID, nil, nil)

	// The hub broadcasts the full wire form (image blocks with raw Data —
	// free in-process). This is the serialization boundary: strip payloads
	// unless this client negotiated them, so a non-negotiating client sees
	// exactly the lean wire shape.
	strip := !s.contract.HasFeature(FeatureImageData)
	// A window is cut for a client that asked for one, and only for that client. The
	// hub broadcasts one full snapshot; this is the per-connection edge, where image
	// data is already dropped per contract. Window BEFORE stripping so the strip walk
	// only touches what will actually be sent.
	window := 0
	if s.contract.HasFeature(FeatureHistoryWindow) {
		window = HistoryWindow
	}

	go func() {
		defer func() {
			s.mu.Lock()
			if c, ok := s.subs[sess]; ok {
				c()
				delete(s.subs, sess)
			}
			s.mu.Unlock()
		}()
		for {
			select {
			case <-subCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				ev = windowSnapshot(ev, window)
				if strip {
					ev = stripImageData(ev)
				}
				if err := s.write(EventFrame(sess, ev)); err != nil {
					return
				}
			}
		}
	}()
}

func (s *serveState) unsubscribe(sess string) {
	s.mu.Lock()
	if c, ok := s.subs[sess]; ok {
		c()
		delete(s.subs, sess)
	}
	s.mu.Unlock()
}

func (s *serveState) closeSubs() {
	s.mu.Lock()
	for sess, c := range s.subs {
		c()
		delete(s.subs, sess)
	}
	s.mu.Unlock()
}
