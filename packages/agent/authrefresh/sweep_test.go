package authrefresh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// tokenServer stands in for auth.kimi.com. It records how many refreshes it was
// asked for, so a test can assert the sweep did not hammer it.
func tokenServer(t *testing.T, hits *int32, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// pointKimiAt redirects the kimi OAuth flow at a test server for the duration
// of one test. KimiOAuth is a package var, so this must be restored.
func pointKimiAt(t *testing.T, url string) {
	t.Helper()
	orig := auth.KimiOAuth
	auth.KimiOAuth.TokenURL = url
	t.Cleanup(func() { auth.KimiOAuth = orig })
}

func storeStaleKimi(t *testing.T) *auth.Store {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := config.AuthStoreFor()
	now := time.Now()
	if err := s.SetOAuth("kimi", auth.OAuthToken{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		IssuedAt:     now.Add(-8 * time.Hour),
		Expiry:       now.Add(5 * time.Minute), // in the window, still usable
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// The whole path: a sweep finds a credential nothing is currently using, sees
// it has entered its refresh window, refreshes it, and persists the result with
// both ends of its life stamped.
//
// This is the case demand-driven refresh could never reach — the provider is
// not the selected one, so nothing resolves it and nothing makes a request with
// it. It aged out in silence until a turn finally used it and 401'd.
func TestSweepRefreshesAnUnusedCredential(t *testing.T) {
	var hits int32
	pointKimiAt(t, tokenServer(t, &hits, 200,
		`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":28800}`))
	s := storeStaleKimi(t)

	New(nil).Sweep(context.Background())

	if hits != 1 {
		t.Errorf("token endpoint hit %d times, want 1", hits)
	}
	c, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	tok := c.Kimi.OAuth
	if tok.AccessToken != "new-access" {
		t.Fatalf("access token = %q, want the refreshed one", tok.AccessToken)
	}
	if tok.RefreshToken != "new-refresh" {
		t.Errorf("refresh token = %q, want the rotated one", tok.RefreshToken)
	}
	if tok.IssuedAt.IsZero() {
		t.Error("IssuedAt not stamped — the next refresh window has no lifetime to size itself against")
	}
	if lifetime := tok.Expiry.Sub(tok.IssuedAt); lifetime < 7*time.Hour || lifetime > 9*time.Hour {
		t.Errorf("recorded lifetime %v, want ~8h from expires_in", lifetime)
	}
	if tok.NeedsRefresh() {
		t.Error("the freshly refreshed token is already due again")
	}
}

// A credential still well inside its life is left alone. Refreshing on every
// tick would spend grants for nothing and, on a rotating provider, multiply the
// chances of a race.
func TestSweepLeavesFreshCredentialsAlone(t *testing.T) {
	var hits int32
	pointKimiAt(t, tokenServer(t, &hits, 200, `{"access_token":"x","expires_in":28800}`))
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	now := time.Now()
	if err := config.AuthStoreFor().SetOAuth("kimi", auth.OAuthToken{
		AccessToken:  "still-good",
		RefreshToken: "r",
		IssuedAt:     now,
		Expiry:       now.Add(8 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	New(nil).Sweep(context.Background())

	if hits != 0 {
		t.Errorf("refreshed a token with hours of life left (%d hits)", hits)
	}
}

// A revoked grant is reported once and then left alone, however many sweeps
// run. The user has to re-login; a timer cannot fix it.
func TestSweepReportsRevokedGrantOnceThenStops(t *testing.T) {
	var hits int32
	pointKimiAt(t, tokenServer(t, &hits, 400,
		`{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`))
	storeStaleKimi(t)

	var reports int
	var lastErr error
	r := New(func(_ string, err error) { reports++; lastErr = err })
	for range 5 {
		r.Sweep(context.Background())
	}

	if hits != 1 {
		t.Errorf("token endpoint hit %d times for a revoked grant, want 1", hits)
	}
	if reports != 1 {
		t.Errorf("reported %d times, want 1", reports)
	}
	if lastErr == nil {
		t.Fatal("no error reported")
	}
}

// Start's stop must actually join its goroutine — a sweep left running past
// Close can hold the auth lock while the next instance queues on it.
func TestStartStopJoins(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	done := make(chan struct{})
	go func() {
		stop := Start(context.Background(), nil)
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() did not return; the sweep goroutine outlives the workspace")
	}
}
