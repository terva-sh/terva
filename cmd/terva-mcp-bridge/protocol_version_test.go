package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// remoteAnswer is what versionRemote answers at initialize. It differs
// from the bridge's offer, so a bridge that sent its offer upstream fails.
const remoteAnswer = "2025-03-26"

// tervaOffer is what the local terva client offers the bridge over stdio. It
// differs from the bridge's offer and the remote's answer, so a bridge that
// relayed it upstream fails.
const tervaOffer = "2099-01-01"

// versionRemote is a fake remote MCP server that records the protocol version
// header of every request it receives, whatever its HTTP method.
type versionRemote struct {
	// unauthorizedOnce names the methods that get one 401 before their real
	// answer, which drives the bridge's re-auth replay.
	unauthorizedOnce map[string]bool

	mu   sync.Mutex
	seen []seenRequest
}

// seenRequest is what the fake recorded of one request. method is the
// JSON-RPC method of a POST, or the HTTP method of anything else. versions
// holds every MCP-Protocol-Version value, so an absent header and an empty one
// differ.
type seenRequest struct {
	method   string
	refused  bool
	versions []string
}

func (f *versionRemote) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	var req rpcRequest
	_ = json.Unmarshal(body, &req)
	method := req.Method
	if r.Method != http.MethodPost {
		method = r.Method
	}

	f.mu.Lock()
	refuse := f.unauthorizedOnce[method]
	delete(f.unauthorizedOnce, method)
	f.seen = append(f.seen, seenRequest{method: method, refused: refuse, versions: r.Header.Values("MCP-Protocol-Version")})
	f.mu.Unlock()

	if refuse {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-v")
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": remoteAnswer,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "version-remote", "version": "1"},
		})
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{
			"tools": []map[string]any{{"name": "remote_echo", "inputSchema": map[string]any{"type": "object"}}},
		})
	case "tools/call":
		writeRPC(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "called remote_echo"}},
		})
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// runBridgeSession runs the bridge against f as relay mode does. The upstream
// handshake comes first. Then the stdio server takes terva's initialize, which
// offers tervaOffer, and relays one tools/list and one tools/call. It returns
// what the fake recorded and the stdio responses by id.
func runBridgeSession(t *testing.T, f *versionRemote, headers map[string]string) ([]seenRequest, map[string]rpcResponse) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	up := newUpstream(srv.URL, srv.Client(), &countingAuth{}, headers)
	initRes, err := up.initialize(context.Background())
	if err != nil {
		t.Fatalf("upstream initialize: %v", err)
	}
	responses := driveStdio(t, up, initRes, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + tervaOffer + `"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remote_echo","arguments":{}}}`,
	})
	for _, id := range []string{"2", "3"} {
		if resp := responses[id]; resp.Error != nil || resp.Result == nil {
			t.Fatalf("relayed request %s failed: %+v", id, resp)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.seen...), responses
}

// assertProtocolVersions checks the rule on every recorded request. An
// initialize carries no version, because the remote has not answered yet. Every
// other request carries exactly the remote's answer.
func assertProtocolVersions(t *testing.T, seen []seenRequest) {
	t.Helper()
	for _, s := range seen {
		if s.method == "initialize" {
			if len(s.versions) != 0 {
				t.Errorf("initialize carried MCP-Protocol-Version %q, want no header before the remote answers", s.versions)
			}
			continue
		}
		if len(s.versions) != 1 || s.versions[0] != remoteAnswer {
			t.Errorf("%s carried MCP-Protocol-Version %q, want exactly [%s], the remote's answer (the bridge offered %s, terva offered %s)",
				s.method, s.versions, remoteAnswer, protocolVersion, tervaOffer)
		}
	}
}

// requireCounts fails when the fake recorded a method a different number of
// times than want. A record that missed a request would pass
// assertProtocolVersions unseen.
func requireCounts(t *testing.T, seen []seenRequest, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, s := range seen {
		got[s.method]++
	}
	for m, n := range want {
		if got[m] != n {
			t.Errorf("the fake recorded %d %s requests, want %d; recorded %+v", got[m], m, n, seen)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the fake recorded the methods %v, want exactly %v", got, want)
	}
}

// Every request that the bridge sends to the remote after initialize carries
// MCP-Protocol-Version with the remote's answer. The initialize request carries
// none, because the spec makes a server answer 400 to a version it does not
// support, and the bridge's offer would then refuse a handshake that negotiates
// down. terva's own offer over stdio never reaches the remote.
func TestTheBridgeSendsTheRemotesNegotiatedVersionUpstream(t *testing.T) {
	// The premise. A remote that answered the offer could not tell the
	// negotiated version from the offered one.
	if remoteAnswer == protocolVersion || tervaOffer == protocolVersion || tervaOffer == remoteAnswer {
		t.Fatalf("the three versions must differ: offer %s, answer %s, terva %s", protocolVersion, remoteAnswer, tervaOffer)
	}
	seen, responses := runBridgeSession(t, &versionRemote{}, nil)

	assertProtocolVersions(t, seen)
	requireCounts(t, seen, map[string]int{"initialize": 1, "notifications/initialized": 1, "tools/list": 1, "tools/call": 1})

	// The bridge answers terva's initialize itself, with the remote's result.
	// So both legs of the relay agree on the remote's version.
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(responses["1"].Result, &init); err != nil || init.ProtocolVersion != remoteAnswer {
		t.Errorf("terva's initialize was answered with protocolVersion %q (err %v), want the remote's %s", init.ProtocolVersion, err, remoteAnswer)
	}
}

// A 401 makes the bridge re-authenticate and send the same frame again. The
// replay of initialize still carries no version, and the replay of a later
// request still carries the remote's answer.
func TestTheBridgesReauthReplaysKeepTheRule(t *testing.T) {
	f := &versionRemote{unauthorizedOnce: map[string]bool{"initialize": true, "tools/call": true}}
	seen, _ := runBridgeSession(t, f, nil)

	assertProtocolVersions(t, seen)
	requireCounts(t, seen, map[string]int{"initialize": 2, "notifications/initialized": 1, "tools/list": 1, "tools/call": 2})
	refused := 0
	for _, s := range seen {
		if s.refused {
			refused++
		}
	}
	if refused != 2 {
		t.Errorf("the fake refused %d requests, want 2, so the replays did not run; recorded %+v", refused, seen)
	}
}

// The protocol owns the version, so an operator's --header of that name does
// not replace the remote's answer on a later request. On initialize the bridge
// sends no version of its own, so the operator's value goes out there.
func TestAConfiguredVersionHeaderDoesNotReplaceTheNegotiatedOne(t *testing.T) {
	const configured = "1999-01-01"
	seen, _ := runBridgeSession(t, &versionRemote{}, map[string]string{"MCP-Protocol-Version": configured})

	checked := 0
	for _, s := range seen {
		want := remoteAnswer
		if s.method == "initialize" {
			want = configured
		}
		if len(s.versions) != 1 || s.versions[0] != want {
			t.Errorf("%s carried MCP-Protocol-Version %q, want exactly [%s]", s.method, s.versions, want)
		}
		checked++
	}
	if checked != 4 {
		t.Errorf("checked %d requests, want 4; recorded %+v", checked, seen)
	}
}
