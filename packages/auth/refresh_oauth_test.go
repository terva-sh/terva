package auth

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func staleToken() OAuthToken {
	now := time.Now()
	return OAuthToken{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		IssuedAt:     now.Add(-8 * time.Hour),
		Expiry:       now.Add(5 * time.Minute), // inside the window, not yet dead
	}
}

// N callers racing to refresh one token must produce exactly ONE refresh call.
//
// This is not politeness to the auth server. A provider that rotates refresh
// tokens — OAuth 2.1 and RFC 9700 both recommend it — treats a REPLAYED refresh
// token as an attack and revokes the whole grant. So a lost race is not a
// wasted request, it is a logged-out user. The re-read after acquiring the lock
// is what stops a loser presenting a token the winner already spent.
func TestRefreshOAuthCollapsesConcurrentCallers(t *testing.T) {
	s := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := s.SetOAuth("kimi", staleToken()); err != nil {
		t.Fatal(err)
	}

	var mints int32
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait() // all eight arrive together
			_, err := s.RefreshOAuth("kimi", func(cur *OAuthToken) (*OAuthToken, error) {
				atomic.AddInt32(&mints, 1)
				now := time.Now()
				return &OAuthToken{
					AccessToken:  "new-access",
					RefreshToken: "new-refresh", // rotated, as a strict server would
					IssuedAt:     now,
					Expiry:       now.Add(8 * time.Hour),
				}, nil
			})
			if err != nil {
				t.Errorf("RefreshOAuth: %v", err)
			}
		}()
	}
	start.Done()
	wg.Wait()

	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Errorf("%d refresh calls for one stale token; a rotating provider revokes the grant on the replays", got)
	}
	c, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Kimi.OAuth.AccessToken != "new-access" || c.Kimi.OAuth.RefreshToken != "new-refresh" {
		t.Errorf("stored token = %+v, want the refreshed pair", c.Kimi.OAuth)
	}
}

// A mint failure must leave the stored token alone. It may still have hours of
// life — throwing it away on a network blip turns a blip into a re-login.
func TestRefreshOAuthFailureKeepsStoredToken(t *testing.T) {
	s := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	original := staleToken()
	if err := s.SetOAuth("kimi", original); err != nil {
		t.Fatal(err)
	}

	got, err := s.RefreshOAuth("kimi", func(*OAuthToken) (*OAuthToken, error) {
		return nil, &TokenError{Status: 503, Description: "upstream unavailable"}
	})
	if err == nil {
		t.Fatal("RefreshOAuth reported success on a failed mint")
	}
	if got == nil || got.AccessToken != original.AccessToken {
		t.Errorf("returned %+v, want the still-valid stored token back", got)
	}
	c, _ := s.Load()
	if c.Kimi.OAuth.AccessToken != original.AccessToken {
		t.Errorf("stored token was replaced on failure: %+v", c.Kimi.OAuth)
	}
}

// mint sees the token on DISK, not whatever the caller was holding — a caller
// that loaded before queueing on the lock has a stale copy by definition, and
// refreshing from it would present a refresh token the winner already spent.
func TestRefreshOAuthMintsFromStoredNotCallerCopy(t *testing.T) {
	s := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := s.SetOAuth("kimi", staleToken()); err != nil {
		t.Fatal(err)
	}
	// Someone else wrote a different token in the meantime — still due, so the
	// double check does not short-circuit and mint really is reached.
	now := time.Now()
	if err := s.SetOAuth("kimi", OAuthToken{
		AccessToken:  "someone-elses-access",
		RefreshToken: "someone-elses-refresh",
		IssuedAt:     now.Add(-8 * time.Hour),
		Expiry:       now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	var seen string
	if _, err := s.RefreshOAuth("kimi", func(cur *OAuthToken) (*OAuthToken, error) {
		seen = cur.RefreshToken
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != "someone-elses-refresh" {
		t.Errorf("mint saw %q, want the token currently on disk", seen)
	}
}

// A token that no longer needs refreshing by the time the lock is acquired must
// not be refreshed at all — that is the whole point of the double check.
func TestRefreshOAuthSkipsWhenNoLongerDue(t *testing.T) {
	s := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	now := time.Now()
	fresh := OAuthToken{
		AccessToken:  "fresh",
		RefreshToken: "r",
		IssuedAt:     now,
		Expiry:       now.Add(8 * time.Hour),
	}
	if err := s.SetOAuth("kimi", fresh); err != nil {
		t.Fatal(err)
	}
	called := false
	got, err := s.RefreshOAuth("kimi", func(*OAuthToken) (*OAuthToken, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("refreshed a token that was not due")
	}
	if got.AccessToken != "fresh" {
		t.Errorf("returned %+v, want the stored token", got)
	}
}
