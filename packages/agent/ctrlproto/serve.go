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
//
// The verb table lives in dispatch_table.go; this is only the routing around it.
// Group negotiation happens BEFORE the lookup so a verb whose group the client
// did not negotiate answers "not negotiated" rather than running — a client that
// never asked for a group must not reach its handlers by naming a verb.
func (s *serveState) handle(ctx context.Context, f Frame) {
	if g := f.Method.Group(); g != "" && !s.contract.Has(g) {
		s.write(ErrFrame(f.ID, CodeUnsupported, "method group not negotiated: "+string(g)))
		return
	}

	// subscribe/unsubscribe are not service calls: they start and stop this
	// connection's event pump and touch s.subs, which no handler may reach.
	switch f.Method {
	case MethodSubscribe:
		s.subscribe(ctx, f)
		return
	case MethodUnsubscribe:
		s.unsubscribe(f.Sess)
		s.respond(f.ID, nil, nil)
		return
	}

	h, ok := dispatch[f.Method]
	if !ok {
		s.write(ErrFrame(f.ID, CodeBadRequest, "unknown method: "+string(f.Method)))
		return
	}
	h(s, ctx, f)
}

// respond writes a success frame carrying result, or maps err to a coded
// failure frame. A nil result yields a bare ok response.
func (s *serveState) respond(id uint64, result any, err error) {
	if err != nil {
		s.fail(id, err)
		return
	}
	// The image-data contract applies to everything crossing this boundary,
	// not just to what the event pumps push. This is the only OKFrame writer,
	// so it is where a pulled result gets the same treatment.
	if !s.contract.HasFeature(FeatureImageData) {
		result = stripResultImageData(result)
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
