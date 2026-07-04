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
	case MethodSessionDelete:
		s.respond(f.ID, nil, s.svc.DeleteSession(ctx, f.Sess))
	case MethodUsageGet:
		u, err := s.svc.Usage(ctx, f.Sess)
		s.respond(f.ID, UsageResult{Usage: u}, err)
	case MethodContextGet:
		b, err := s.svc.Context(ctx, f.Sess)
		s.respond(f.ID, ContextResult{Breakdown: b}, err)
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
		s.respond(f.ID, nil, s.svc.SwitchModel(ctx, f.Sess, p.Model))
	case MethodModelFavorite:
		var p FavoriteParams
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, s.svc.SetFavoriteModel(ctx, p.Provider, p.Model, p.On))
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

// subscribe starts (idempotently) an event pump for the frame's session,
// forwarding every event as a [KindEvent] frame until the connection or the
// subscription ends.
func (s *serveState) subscribe(ctx context.Context, f Frame) {
	sess := f.Sess
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
