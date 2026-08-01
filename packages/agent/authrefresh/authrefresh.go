// Package authrefresh keeps stored OAuth subscriptions alive while terva runs.
//
// Before this, refresh was entirely demand-driven: the credential resolver ran
// at boot and on a /model switch, and the per-request CredentialSource ran once
// per Stream. Both only ever looked at the provider being USED. So a
// subscription you were not talking to aged untouched, and an instance sitting
// idle refreshed nothing at all — the token went stale, then the refresh grant
// behind it eventually did too, and the first sign of any of it was a 401 on
// the next turn.
//
// What this cannot do is cover a machine where terva never runs. A refresh
// grant has its own lifetime and nothing in a program that is not executing
// will extend it. The honest scope is: while terva is up, no credential lapses
// for want of asking.
package authrefresh

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider/auth"
)

const (
	// tickInterval is how often the sweep runs. The window decides when a token
	// is DUE; the tick decides how late we can be in noticing, and those compose
	// badly if the tick is not comfortably shorter than the shortest window.
	//
	// Kimi's access token lives fifteen minutes and comes due with five to go.
	// At the five-minute tick this started with, jitter could put the next sweep
	// at 16 minutes — past expiry, having never looked. Two minutes leaves the
	// worst case at 2.4, so a due token is always seen with margin left to retry
	// in. A sweep with nothing due is one read of a small file; the extra
	// wakeups cost nothing worth counting.
	tickInterval = 2 * time.Minute

	// retryFloor / retryCeil bound the backoff after a TRANSIENT failure. The
	// ceiling stays well inside a typical refresh window so a provider having a
	// bad ten minutes still gets several attempts before its token actually
	// expires.
	retryFloor = 1 * time.Minute
	retryCeil  = 30 * time.Minute

	// initialDelay holds the first sweep back a moment.
	//
	// Not for load — for the report. A host installs its diagnostic sink just
	// AFTER building the workspace that starts this (the TUI's SetDiag), and the
	// default sink is a line on stderr. A lapse reported in that window prints
	// into a terminal the TUI is about to take over full-screen. Five seconds is
	// nothing against a refresh window measured in hours, and it doubles as
	// startup spread for a herd launched by one script.
	initialDelay = 5 * time.Second
)

// Reporter is told when a credential has lapsed for good, so a host can say so
// somewhere the user will see it. Called at most once per dead grant, not once
// per attempt: the point is to inform, not to nag.
type Reporter func(provider string, err error)

// Refresher sweeps stored OAuth credentials and refreshes the ones that have
// entered their refresh window.
type Refresher struct {
	report Reporter

	mu sync.Mutex
	// backoff is the next time a provider may be retried after a transient
	// failure, keyed by provider.
	backoff map[string]time.Time
	// failures counts consecutive transient failures, for the backoff curve.
	failures map[string]int
	// lapsed remembers which grant was declared dead, keyed by provider and
	// valued by the expiry of the token that failed. Keying on the TOKEN and
	// not just the provider is what lets a fresh /login be noticed: a new grant
	// has a different expiry, so it is tried again rather than written off
	// forever by a verdict about the credential it replaced.
	lapsed map[string]time.Time
}

// New returns a Refresher. report may be nil.
func New(report Reporter) *Refresher {
	return &Refresher{
		report:   report,
		backoff:  map[string]time.Time{},
		failures: map[string]int{},
		lapsed:   map[string]time.Time{},
	}
}

// Start runs an immediate sweep, then keeps sweeping until ctx is done.
//
// The immediate sweep is the cheapest part of the whole design and does most of
// the work: opening terva once inside a token's refresh window is enough to
// keep that grant alive, which turns "I haven't used kimi in a week" from a
// re-login into nothing at all.
func Start(ctx context.Context, report Reporter) (stop func()) {
	r := New(report)
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTimer(jitter(initialDelay))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.Sweep(ctx)
				// Re-jittered every tick, not once. A fixed offset chosen at
				// startup keeps N instances the same distance apart forever;
				// re-rolling lets a herd that started together drift apart.
				t.Reset(jitter(tickInterval))
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// jitter spreads a nominal interval over ±20%.
//
// Instances started by one shell script, or by a `just` recipe that launches
// several, otherwise tick in lockstep for the life of the process. The file
// lock and its re-read already make a simultaneous sweep cost one refresh
// rather than N, so this is defence in depth — but it is the cheap half, and
// it also keeps N instances from all waking to read the same file at once.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // not security-sensitive
}

// Sweep refreshes every stored OAuth credential that has entered its refresh
// window. Providers with no refresh flow, providers already in backoff, and
// grants already declared dead are skipped.
func (r *Refresher) Sweep(ctx context.Context) {
	creds, err := config.AuthStoreFor().Load()
	if err != nil {
		return
	}
	now := time.Now()
	for _, name := range creds.OAuthProviders() {
		if ctx.Err() != nil {
			return
		}
		if _, ok := config.OAuthProviderFor(name); !ok {
			continue // stored an OAuth token, but terva has no refresh flow for it
		}
		tok := creds.OAuthFor(name)
		if tok == nil {
			continue
		}
		if _, due := tok.StaleFor(now); !due {
			continue
		}
		if !r.mayAttempt(name, tok, now) {
			continue
		}
		_, err := config.RefreshIfExpired(name, tok)
		r.record(name, tok, err)
	}
}

// mayAttempt reports whether provider is eligible for an attempt right now.
func (r *Refresher) mayAttempt(provider string, tok *auth.OAuthToken, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if dead, ok := r.lapsed[provider]; ok && dead.Equal(tok.Expiry) {
		return false // same dead grant; a /login replaces it with a different one
	}
	if until, ok := r.backoff[provider]; ok && now.Before(until) {
		return false
	}
	return true
}

// record folds an attempt's outcome into the retry state.
func (r *Refresher) record(provider string, tok *auth.OAuthToken, err error) {
	r.mu.Lock()
	if err == nil {
		delete(r.backoff, provider)
		delete(r.failures, provider)
		delete(r.lapsed, provider)
		r.mu.Unlock()
		return
	}

	var te *auth.TokenError
	terminal := errors.As(err, &te) && te.Terminal()
	// No refresh_token at all is terminal for the same reason: there is
	// nothing to present, and no wait changes that.
	if !terminal && tok.RefreshToken == "" {
		terminal = true
	}
	if terminal {
		_, already := r.lapsed[provider]
		r.lapsed[provider] = tok.Expiry
		delete(r.backoff, provider)
		delete(r.failures, provider)
		report := r.report
		r.mu.Unlock()
		// Retrying a revoked grant on a timer cannot succeed, and it hammers
		// the same auth server the user's next /login has to reach.
		if !already && report != nil {
			report(provider, err)
		}
		return
	}

	r.failures[provider]++
	n := r.failures[provider]
	r.mu.Unlock()
	r.setBackoff(provider, n)
}

func (r *Refresher) setBackoff(provider string, failures int) {
	d := retryFloor << min(failures-1, 8)
	if d > retryCeil || d <= 0 {
		d = retryCeil
	}
	r.mu.Lock()
	r.backoff[provider] = time.Now().Add(jitter(d))
	r.mu.Unlock()
}
