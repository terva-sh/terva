package ctrlclient

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// service.go holds 93 forwarders sending 91 distinct Method constants, each one
// a copy-paste of the one above it. Nothing checked which verb any of them
// actually sends: before this test, `grep -o 'ctrlproto\.Method[A-Za-z]*'
// *_test.go` in this package returned NOTHING. TestHandshakeAndCalls drives
// three forwarders and asserts on their results, never on the verb.
//
// So a forwarder that sends the wrong constant compiled, satisfied its
// `var _ ctrlproto.XController` assertion, passed forwarder_complete_test.go
// (which checks that a controller is forwarded, not that it is forwarded
// CORRECTLY), and failed only in front of a user.
//
// That is precisely the bug class the server-side dispatch table was built to
// eliminate. dispatch.go's header records it shipping SEVEN times — the worst
// being personas.delete dispatched as PersonasEdit, which bound {name} as a
// write and overwrote the persona with an empty document. The server was cured
// structurally, by making a map key something two verbs cannot share. The
// client was never examined for the same disease, and it is still hand-written
// in exactly the shape that produced it.
//
// WHERE THE EXPECTATION COMES FROM. Not from a hand-written table of 93
// verbs — that would be the same copy-paste, restated, and a table transcribed
// from the current code blesses whatever is wrong in it today. It comes from
// the SERVER: dispatch_table.go already says, for each verb, which controller
// method answers it. Service implements those same controller interfaces. So
// the contract is a closed loop that neither side states alone:
//
//	Service.CardsRestore must send the verb whose handler calls CardsRestore.
//
// A forwarder sending the wrong constant lands on a handler that invokes a
// DIFFERENT controller method than the forwarder's own name, and this fails.
// Nothing needs updating when a verb is added: both sides are read from source.
//
// The verb is read off the WIRE (a recording FrameConn), not off the source, so
// what is checked is the frame the daemon would actually receive.

// notDispatched are Service methods whose verb is deliberately absent from the
// dispatch table, with the reason. Keep this list tiny and argued.
var notDispatched = map[string]string{
	// dispatch_table.go's header: "subscribe/unsubscribe are absent by design:
	// serveState answers them, because they manage the connection's event pumps
	// rather than calling the service."
	"Subscribe": "subscribe is answered by serveState itself, not the dispatch table — it manages this connection's event pumps rather than calling the service",
}

// --- the recording carrier ---

// recordConn is a FrameConn that completes the handshake and answers every
// command with an empty OK, keeping the command frames it saw. It exists so the
// test observes the verb that reached the wire rather than the constant named
// in the source.
type recordConn struct {
	mu     sync.Mutex
	calls  []ctrlproto.Frame
	out    chan ctrlproto.Frame
	closed chan struct{}
	once   sync.Once
}

func newRecordConn() *recordConn {
	return &recordConn{
		out:    make(chan ctrlproto.Frame, 64),
		closed: make(chan struct{}),
	}
}

func (r *recordConn) push(f ctrlproto.Frame) {
	select {
	case r.out <- f:
	case <-r.closed:
	}
}

func (r *recordConn) ReadFrame(ctx context.Context) (ctrlproto.Frame, error) {
	select {
	case f := <-r.out:
		return f, nil
	case <-r.closed:
		return ctrlproto.Frame{}, io.EOF
	case <-ctx.Done():
		return ctrlproto.Frame{}, ctx.Err()
	}
}

func (r *recordConn) WriteFrame(_ context.Context, f ctrlproto.Frame) error {
	switch f.Kind {
	case ctrlproto.KindHello:
		r.push(ctrlproto.HelloFrame(ctrlproto.ServerHello("roundtrip-fake", "0.0.0")))
	case ctrlproto.KindCmd:
		r.mu.Lock()
		r.calls = append(r.calls, f)
		r.mu.Unlock()
		// An empty object binds into any result struct, so every forwarder
		// returns cleanly and none of them blocks.
		ok, err := ctrlproto.OKFrame(f.ID, map[string]any{})
		if err != nil {
			return err
		}
		r.push(ok)
	}
	return nil
}

func (r *recordConn) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

// take returns the commands recorded since the last take, and clears them.
func (r *recordConn) take() []ctrlproto.Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

// --- reading the server's half of the contract ---

// methodValues maps each Method constant NAME to its wire string, from
// ../methods.go.
func methodValues(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parse methods.go: %v", err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Method" {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(bl.Value); err == nil {
					out[n.Name] = v
				}
			}
		}
	}
	// A parse that quietly finds nothing would make this test vacuous.
	if len(out) < 90 {
		t.Fatalf("found only %d Method constants in methods.go; the parse is not seeing them", len(out))
	}
	return out
}

// verbHandlers maps each verb's wire string to the SET of controller methods
// its dispatch entry invokes, from ../dispatch_table.go.
//
// A set, not one name, because a handler may legitimately reach more than one
// controller method: turn.swipe routes on the presence of Index, calling
// SwipeMessage or SwipeTurn. Both client forwarders are then correct, and no
// exemption is needed to say so.
func verbHandlers(t *testing.T) map[string]map[string]bool {
	t.Helper()
	values := methodValues(t)

	f, err := parser.ParseFile(token.NewFileSet(), "../dispatch_table.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatch_table.go: %v", err)
	}

	out := map[string]map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return true
		}
		verb, ok := values[key.Name]
		if !ok {
			return true
		}
		// The value is one of do/get/act/ask(msg, func(recv T, ...) ...).
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok {
			return true
		}
		var lit *ast.FuncLit
		for _, a := range call.Args {
			if fl, ok := a.(*ast.FuncLit); ok {
				lit = fl
				break
			}
		}
		if lit == nil || lit.Type.Params == nil || len(lit.Type.Params.List) == 0 {
			return true
		}
		names := lit.Type.Params.List[0].Names
		if len(names) == 0 {
			return true
		}
		recv := names[0].Name // "c" for a controller, "svc" for WorkspaceService

		// Collect every method invoked on that receiver anywhere in the body —
		// not just in a return, because several handlers assign first
		// (`list, err := svc.Sessions(ctx)`) and shape the result afterwards.
		got := map[string]bool{}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == recv {
				got[sel.Sel.Name] = true
			}
			return true
		})
		if len(got) > 0 {
			out[verb] = got
		}
		return true
	})

	if len(out) < 100 {
		t.Fatalf("resolved only %d verb->handler entries from dispatch_table.go; the parse is not seeing them", len(out))
	}
	return out
}

// --- the test ---

// connectedRecorder wires a live Client to a recordConn and returns both.
func connectedRecorder(t *testing.T) (*Service, *recordConn) {
	t.Helper()
	rc := newRecordConn()
	c, err := New(Options{
		Backoff: 10 * time.Millisecond,
		Dial: func(context.Context) (ctrlproto.FrameConn, error) {
			return rc, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = c.Close() })
	t.Cleanup(func() { _ = rc.Close() })
	go func() { _ = c.Run(ctx) }()
	waitFor(t, "recorder connect", c.Connected)
	// The handshake itself is not a command; drop anything queued before we
	// start attributing frames to methods.
	rc.take()
	return c.Service(), rc
}

// callZero invokes m on svc with a real context and zero values everywhere
// else. The params never reach a daemon — only the verb is under test.
func callZero(t *testing.T, svc *Service, m reflect.Method) {
	t.Helper()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	args := make([]reflect.Value, m.Type.NumIn())
	args[0] = reflect.ValueOf(svc)
	for i := 1; i < m.Type.NumIn(); i++ {
		at := m.Type.In(i)
		if at == ctxType {
			args[i] = reflect.ValueOf(context.Background())
			continue
		}
		args[i] = reflect.Zero(at)
	}
	m.Func.Call(args)
}

// TestEveryForwarderSendsTheVerbItsHandlerAnswers drives all 93 forwarders and
// checks each one against the server's own dispatch table. See the header for
// why the expectation is derived rather than written down.
func TestEveryForwarderSendsTheVerbItsHandlerAnswers(t *testing.T) {
	handlers := verbHandlers(t)
	svc, rc := connectedRecorder(t)

	st := reflect.TypeOf(svc)
	driven := 0
	for i := 0; i < st.NumMethod(); i++ {
		m := st.Method(i)
		t.Run(m.Name, func(t *testing.T) {
			callZero(t, svc, m)
			frames := rc.take()

			if len(frames) == 0 {
				t.Fatalf("%s sent no command frame. A forwarder that sends nothing "+
					"cannot be caught by the compiler or by forwarder_complete_test.go — "+
					"if this method is deliberately local, say so in notDispatched.", m.Name)
			}
			if len(frames) > 1 {
				var verbs []string
				for _, f := range frames {
					verbs = append(verbs, string(f.Method))
				}
				t.Fatalf("%s sent %d command frames (%v); a forwarder should send exactly one",
					m.Name, len(frames), verbs)
			}
			verb := string(frames[0].Method)
			if verb == "" {
				t.Fatalf("%s sent a command frame with an empty method", m.Name)
			}

			if reason, exempt := notDispatched[m.Name]; exempt {
				if _, dispatched := handlers[verb]; dispatched {
					t.Fatalf("%s sends %q, which the dispatch table DOES answer — "+
						"remove it from notDispatched (listed reason: %s)", m.Name, verb, reason)
				}
				return
			}

			answered, ok := handlers[verb]
			if !ok {
				t.Fatalf("%s sends %q, which no entry in dispatch_table.go answers. "+
					"The daemon will reject this call as an unknown method.", m.Name, verb)
			}
			if !answered[m.Name] {
				var names []string
				for n := range answered {
					names = append(names, n)
				}
				sort.Strings(names)
				t.Errorf("%s sends %q, but that verb's handler calls %v — not %s.\n"+
					"Either the forwarder names the wrong Method constant, or it is "+
					"forwarding to a handler that does something else.",
					m.Name, verb, names, m.Name)
			}
			driven++
		})
	}

	// A reflection walk that found nothing would pass silently.
	if driven < 80 {
		t.Errorf("only %d forwarders were driven; service.go holds ~93 and this "+
			"walk is not seeing them", driven)
	}
}

// TestNotDispatchedNamesRealMethods keeps the exemption list honest: an entry
// for a method that no longer exists is a stale excuse, and the next person to
// read it would trust it.
func TestNotDispatchedNamesRealMethods(t *testing.T) {
	st := reflect.TypeOf((*Service)(nil))
	have := map[string]bool{}
	for i := 0; i < st.NumMethod(); i++ {
		have[st.Method(i).Name] = true
	}
	for name := range notDispatched {
		if !have[name] {
			t.Errorf("notDispatched lists %q, which is not a method on *Service (renamed or removed?)", name)
		}
	}
}
