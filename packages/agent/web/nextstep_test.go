//go:build terva_web

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// suggest.next_step is served by an OPTIONAL controller (NextStepController),
// so whether the browser can reach it is a property of the transport and of the
// service the web server happens to be handed -- not something the method's own
// definition settles.
//
// The static half of the answer is in the call chain: runWebMode builds a
// *workspace.Workspace and passes that exact value to web.Serve, ServeConn
// dispatches through the shared table with no web-side allowlist, and
// workspace_nextstep.go carries a compile-time assertion that *Workspace
// implements the interface. These two tests are the other half, over a real
// WebSocket, because the TUI reached this method through a different carrier
// and "the web should work the same way" was an inference until something
// exercised it.

// nextStepWS is fakeWS plus the optional controller.
type nextStepWS struct {
	*fakeWS
	line     string
	gotSess  string
	onDemand bool
	called   bool
}

func (f *nextStepWS) SuggestNextStep(_ context.Context, sess string, p ctrlproto.NextStepParams) (ctrlproto.NextStepResult, error) {
	f.called = true
	f.gotSess = sess
	f.onDemand = p.OnDemand
	return ctrlproto.NextStepResult{Line: f.line}, nil
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	mustWrite(t, c, ctrlproto.HelloFrame(ctrlproto.Hello{
		Role: ctrlproto.RoleClient, Protocol: ctrlproto.Protocol,
		Groups: []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupSession},
	}))
	if f := mustRead(t, c); f.Kind != ctrlproto.KindHello {
		t.Fatalf("want server hello, got %+v", f)
	}
	return c
}

// readResp pulls frames until the response correlated to id arrives, skipping
// the events a subscribed session may interleave.
func readResp(t *testing.T, c *websocket.Conn, id uint64) ctrlproto.Frame {
	t.Helper()
	for i := 0; i < 20; i++ {
		f := mustRead(t, c)
		if f.Kind == ctrlproto.KindResp && f.ID == id {
			return f
		}
	}
	t.Fatalf("no resp for id %d", id)
	return ctrlproto.Frame{}
}

// TestSuggestNextStepOverTheWebSocket is the reachability proof: a browser
// client can call the same method the TUI's /nextstep uses, and the on_demand
// flag survives the trip.
func TestSuggestNextStepOverTheWebSocket(t *testing.T) {
	svc := &nextStepWS{fakeWS: newFakeWS(), line: "run the tests"}
	srv := httptest.NewServer(newMux(context.Background(), svc, Options{Version: "test"}))
	defer srv.Close()

	c := dialWS(t, srv.URL)
	defer c.Close()

	mustWrite(t, c, mustCmd(t, 7, "s1", ctrlproto.MethodSuggestNextStep,
		ctrlproto.NextStepParams{OnDemand: true}))

	f := readResp(t, c, 7)
	if f.Error != nil {
		t.Fatalf("suggest.next_step failed: %+v", f.Error)
	}
	var out ctrlproto.NextStepResult
	if err := json.Unmarshal(f.Result, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Line != "run the tests" {
		t.Errorf("line = %q, want %q", out.Line, "run the tests")
	}
	if !svc.called {
		t.Error("controller was never called")
	}
	if svc.gotSess != "s1" {
		t.Errorf("sess = %q, want s1 -- the session rides the frame", svc.gotSess)
	}
	// The whole reason the param exists: an asked-for suggestion is framed to
	// the model differently from one terva volunteered. If the flag is dropped
	// in transit the web would silently get the idle prompt, which tells the
	// model as a fact that the user "has not asked you for anything".
	if !svc.onDemand {
		t.Error("on_demand did not survive the wire")
	}
}

// TestSuggestNextStepWithoutTheControllerRefusesCleanly pins the other side of
// an optional controller. A service that does not implement it must produce a
// structured error, not a hang and not a panic, so a client can decide whether
// to offer the command at all.
func TestSuggestNextStepWithoutTheControllerRefusesCleanly(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Version: "test"}))
	defer srv.Close()

	c := dialWS(t, srv.URL)
	defer c.Close()

	mustWrite(t, c, mustCmd(t, 9, "s1", ctrlproto.MethodSuggestNextStep, ctrlproto.NextStepParams{}))

	f := readResp(t, c, 9)
	if f.Error == nil {
		t.Fatalf("want a structured error when the carrier lacks the controller, got result %s", f.Result)
	}
	if f.Result != nil {
		t.Errorf("both result and error set: %s", f.Result)
	}
}
