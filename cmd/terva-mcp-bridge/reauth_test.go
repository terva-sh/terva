package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// countingAuth reports how many times the bridge re-authenticated.
type countingAuth struct {
	reauths atomic.Int32
	fail    error
}

func (c *countingAuth) header(context.Context) (string, error) { return "Bearer t", nil }
func (c *countingAuth) reauth(context.Context) error {
	c.reauths.Add(1)
	return c.fail
}

// The 401 → re-auth → replay-once rule was written twice, fifteen lines apart,
// and the second copy reported SUCCESS on a still-unauthorized send.
//
// `do` returns a nil error for a 401 on purpose — that status is the re-auth
// signal, not a failure. notify re-sent the frame and returned the second
// send's error, which for another 401 is nil. Its one caller is the MCP
// handshake, so a bridge holding a dead credential announced a healthy
// connection and then failed every call made through it.
func TestANotifyThatStaysUnauthorizedIsNotReportedAsSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
		// Valid JSON-RPC, which is what makes the sibling hole in call() quiet.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1}`))
	}))
	defer srv.Close()

	auth := &countingAuth{}
	u := newUpstream(srv.URL, srv.Client(), auth, nil)

	err := u.notify(context.Background(), "notifications/initialized", nil)
	if err == nil {
		t.Fatal("notify reported success while the upstream was still returning 401 — " +
			"the MCP handshake announces a healthy connection on a dead credential")
	}
	if !strings.Contains(err.Error(), "still unauthorized") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if got := auth.reauths.Load(); got != 1 {
		t.Errorf("re-authenticated %d times, want exactly 1", got)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("sent %d times, want the original plus one replay", got)
	}
}

// call had a quieter version of the same hole: on the SECOND 401 it fell
// through to json.Unmarshal, so a 401 body that parses as JSON-RPC returned a
// nil result and a nil error. The "still unauthorized" line at the bottom of
// its loop was unreachable.
func TestACallThatStaysUnauthorizedDoesNotReturnANilResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1}`))
	}))
	defer srv.Close()

	u := newUpstream(srv.URL, srv.Client(), &countingAuth{}, nil)

	res, err := u.call(context.Background(), json.RawMessage(`1`), "tools/list", nil)
	if err == nil {
		t.Fatalf("call returned result %q and no error on a persistent 401", string(res))
	}
	if !strings.Contains(err.Error(), "still unauthorized") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

// The complement, and the reason the retry exists: one 401 followed by a good
// response must succeed. Without this, "always fail on 401" would pass both
// tests above and break every expired-token recovery.
func TestASingle401IsRecoveredByOneReplay(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer srv.Close()

	auth := &countingAuth{}
	u := newUpstream(srv.URL, srv.Client(), auth, nil)

	res, err := u.call(context.Background(), json.RawMessage(`1`), "tools/list", nil)
	if err != nil {
		t.Fatalf("a recoverable 401 was not recovered: %v", err)
	}
	if !strings.Contains(string(res), "tools") {
		t.Errorf("result = %s, want the replayed response", res)
	}
	if got := auth.reauths.Load(); got != 1 {
		t.Errorf("re-authenticated %d times, want exactly 1", got)
	}

	if err := u.notify(context.Background(), "notifications/initialized", nil); err != nil {
		t.Errorf("notify on an authorized upstream failed: %v", err)
	}
}

// A re-auth that itself fails must surface, not fall through to a replay that
// will 401 again with a less useful message.
func TestAFailedReauthSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	auth := &countingAuth{fail: errRefreshRefused}
	u := newUpstream(srv.URL, srv.Client(), auth, nil)

	if err := u.notify(context.Background(), "notifications/initialized", nil); err == nil {
		t.Fatal("a failed re-auth was reported as a successful notify")
	} else if !strings.Contains(err.Error(), errRefreshRefused.Error()) {
		t.Errorf("the re-auth failure was swallowed: %v", err)
	}
}

var errRefreshRefused = errors.New("refresh token rejected")
