package ctrlproto

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
		m, err := s.svc.Models(ctx)
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
			s.write(ErrFrame(f.ID, CodeUnsupported, "login not supported"))
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
			s.write(ErrFrame(f.ID, CodeUnsupported, "login not supported"))
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
			s.write(ErrFrame(f.ID, CodeUnsupported, "login not supported"))
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
			s.write(ErrFrame(f.ID, CodeUnsupported, "login not supported"))
			return
		}
		var p AuthLogoutParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, ac.AuthLogout(ctx, p))

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
func (s *serveState) fail(id uint64, err error) {
	var ce *Error
	if errors.As(err, &ce) {
		s.write(Frame{Kind: KindResp, ID: id, Error: ce})
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
