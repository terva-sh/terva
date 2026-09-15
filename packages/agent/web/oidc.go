//go:build terva_web

package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/oidc"
	"terva.sh/terva/packages/i18n"
)

// OIDCCallbackPath is the provider's redirect target. Exported because an
// operator must register this exact path with their identity provider, and a
// startup error that names it is worth more than one that does not.
const OIDCCallbackPath = "/auth/oidc/callback"

// Where the browser goes to start and finish a single sign-on.
const (
	oidcStartPath    = "/auth/oidc/start"
	oidcCallbackPath = OIDCCallbackPath
	oidcLogoutPath   = "/auth/oidc/logout"

	// oidcAttemptCookie carries the id of an in-flight login.
	//
	// 🔑 SameSite=Lax is REQUIRED here, not a preference. The callback is a
	// top-level navigation from the identity provider — a cross-site request —
	// and a Strict cookie would simply not be sent, so every login would fail
	// its state check with nothing to point at.
	oidcAttemptCookie = "terva_oidc_attempt"

	// oidcSessionCookie carries the id of an established login. Strict, matching
	// the bearer-token cookie: the post-callback redirect is terva→terva, so a
	// Strict cookie rides it, and nothing needs it on a cross-site request.
	oidcSessionCookie = "terva_session"

	oidcAttemptTTL = 10 * time.Minute
	oidcSessionTTL = 12 * time.Hour
)

// oidcState is the daemon's login state: sessions that exist, and logins in
// flight.
//
// 🔑 Both are SERVER-SIDE, with only an opaque random id in the cookie. The
// alternative — a signed cookie carrying the principal — needs a signing key,
// and $TERVA_HOME is model-writable, so a key kept there would be a poor
// boundary and a real design question (proposal open question 3). Holding the
// state here means there is nothing to forge and nothing to protect.
//
// ⚠️ Sessions do not survive a daemon restart. For one daemon that is a cheap
// re-login rather than a design problem, and it is stated so nobody reads the
// eviction as a bug.
type OIDCState struct {
	mu       sync.Mutex
	sessions map[string]oidcSession
	attempts map[string]oidcAttempt
}

type oidcSession struct {
	principal authz.Principal
	expires   time.Time
}

// oidcAttempt is one login in flight: everything needed to prove the callback
// belongs to the browser that started it.
type oidcAttempt struct {
	state    string
	nonce    string
	verifier string // PKCE
	next     string // where to land afterwards
	expires  time.Time
}

// NewOIDCState builds the daemon's login state. Exported for the composition
// root; everything inside this package uses the type directly.
func NewOIDCState() *OIDCState {
	return &OIDCState{
		sessions: map[string]oidcSession{},
		attempts: map[string]oidcAttempt{},
	}
}

// opaqueID mints a cookie value with no structure to guess.
func opaqueID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sweep drops expired entries. Called on every write so a long-running daemon
// does not accumulate dead logins; there is no separate reaper to forget.
func (s *OIDCState) sweep(now time.Time) {
	for k, v := range s.attempts {
		if now.After(v.expires) {
			delete(s.attempts, k)
		}
	}
	for k, v := range s.sessions {
		if now.After(v.expires) {
			delete(s.sessions, k)
		}
	}
}

func (s *OIDCState) putAttempt(id string, a oidcAttempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(time.Now())
	s.attempts[id] = a
}

// takeAttempt returns an attempt and removes it: a login attempt is
// single-use, so a replayed callback finds nothing.
func (s *OIDCState) takeAttempt(id string) (oidcAttempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[id]
	if !ok {
		return oidcAttempt{}, false
	}
	delete(s.attempts, id)
	if time.Now().After(a.expires) {
		return oidcAttempt{}, false
	}
	return a, true
}

func (s *OIDCState) putSession(id string, p authz.Principal, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(time.Now())
	s.sessions[id] = oidcSession{principal: p, expires: time.Now().Add(ttl)}
}

func (s *OIDCState) session(id string) (authz.Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || time.Now().After(sess.expires) {
		return authz.Principal{}, false
	}
	return sess.principal, true
}

func (s *OIDCState) dropSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// principalFromOIDCSession returns the principal a request's session cookie
// names, if any.
func principalFromOIDCSession(opts Options, r *http.Request) (authz.Principal, bool) {
	if opts.OIDCState == nil {
		return authz.Principal{}, false
	}
	c, err := r.Cookie(oidcSessionCookie)
	if err != nil || c.Value == "" {
		return authz.Principal{}, false
	}
	return opts.OIDCState.session(c.Value)
}

// handleOIDCStart begins a login: mint the per-attempt secrets, stash them
// against this browser, and send it to the identity provider.
func handleOIDCStart(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.OIDCProvider == nil || opts.OIDCState == nil {
			http.Error(w, i18n.T("single sign-on is not configured on this daemon"), http.StatusNotFound)
			return
		}
		verifier, nonce := oidc.NewLoginSecrets()
		state, err := opaqueID()
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		id, err := opaqueID()
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		opts.OIDCState.putAttempt(id, oidcAttempt{
			state:    state,
			nonce:    nonce,
			verifier: verifier,
			next:     safeNext(r.URL.Query().Get("next")),
			expires:  time.Now().Add(oidcAttemptTTL),
		})
		http.SetCookie(w, &http.Cookie{
			Name:     oidcAttemptCookie,
			Value:    id,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode, // see the constant's comment
			Secure:   requestIsHTTPS(r),
			MaxAge:   int(oidcAttemptTTL.Seconds()),
		})
		http.Redirect(w, r, opts.OIDCProvider.AuthorizeURL(state, nonce, verifier), http.StatusSeeOther)
	}
}

// handleOIDCCallback finishes a login.
func handleOIDCCallback(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.OIDCProvider == nil || opts.OIDCState == nil {
			http.Error(w, i18n.T("single sign-on is not configured on this daemon"), http.StatusNotFound)
			return
		}
		fail := func(logLine string) {
			// The operator gets the reason; the browser gets a form. A precise
			// message here would narrate the state of someone else's login
			// attempt to whoever managed to reach this URL.
			fmt.Fprintf(os.Stderr, "terva web: single sign-on failed for %s — %s\n", clientDesc(opts, r), logLine)
			serveLogin(w, r, opts, http.StatusUnauthorized, i18n.T("Single sign-on did not complete. Please try again."))
		}

		// The IdP reports its own refusals here (access_denied, and so on).
		if e := r.URL.Query().Get("error"); e != "" {
			fail("the provider refused: " + e)
			return
		}
		c, err := r.Cookie(oidcAttemptCookie)
		if err != nil || c.Value == "" {
			fail("no in-flight login for this browser (cookie missing or expired)")
			return
		}
		// Single-use: a replayed callback finds nothing.
		attempt, ok := opts.OIDCState.takeAttempt(c.Value)
		clearCookie(w, r, oidcAttemptCookie)
		if !ok {
			fail("the login attempt had expired or was already used")
			return
		}
		// 🚨 The CSRF check. Without it, an attacker who can make this browser
		// issue a GET to the callback with their own authorization code logs the
		// victim into the ATTACKER's account, and everything the victim then
		// types goes to a session the attacker controls. Constant-time because
		// the value is a secret being compared, and cheap.
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(attempt.state)) != 1 {
			fail("state mismatch — the callback did not belong to this login attempt")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			fail("the callback carried no authorization code")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		id, err := opts.OIDCProvider.Exchange(ctx, code, attempt.nonce, attempt.verifier, opts.OIDCHTTPClient)
		if err != nil {
			fail(err.Error())
			return
		}
		roles := opts.OIDCProvider.Roles(id)
		if len(roles) == 0 {
			// 🔑 Authenticated, and deliberately not admitted. This is the
			// common misconfiguration AND the intended answer for a stranger,
			// so it is logged with enough detail for an operator to tell which.
			fmt.Fprintf(os.Stderr, "terva web: %s signed in but holds no mapped group — groups=%v; add one to the role map to grant access\n", id.Subject, id.Groups)
			serveLogin(w, r, opts, http.StatusForbidden, i18n.T("You signed in successfully, but this terva has not granted your account access. Ask the operator to map one of your groups to a role."))
			return
		}
		p := authz.Principal{
			Subject: id.Subject,
			Display: firstNonEmptyStr(id.Name, id.Email, id.Subject),
			Roles:   authzRoles(roles),
			Source:  authz.SourceOIDC,
		}
		sid, err := opaqueID()
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		opts.OIDCState.putSession(sid, p, oidcSessionTTL)
		http.SetCookie(w, &http.Cookie{
			Name:     oidcSessionCookie,
			Value:    sid,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   requestIsHTTPS(r),
			MaxAge:   int(oidcSessionTTL.Seconds()),
		})
		fmt.Fprintf(os.Stderr, "terva web: %s signed in as %v\n", p.Display, p.Roles)
		http.Redirect(w, r, attempt.next, http.StatusSeeOther)
	}
}

// handleOIDCLogout drops the session both sides: the cookie and the record.
//
// Dropping the RECORD is the half that matters — clearing only the cookie would
// leave a live session id that anyone holding a copy could keep using.
func handleOIDCLogout(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.OIDCState != nil {
			if c, err := r.Cookie(oidcSessionCookie); err == nil && c.Value != "" {
				opts.OIDCState.dropSession(c.Value)
			}
		}
		clearCookie(w, r, oidcSessionCookie)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), MaxAge: -1,
	})
}

// authzRoles converts configured role names to typed roles, dropping any the
// build does not know.
//
// 🔑 An unknown name is DROPPED, never passed through. A role map naming a role
// terva has no table entry for must grant nothing — authz.Grant would ignore it
// anyway, and letting the string travel would make a typo look like a working
// grant right up until someone checked what it allowed.
func authzRoles(names []string) []authz.Role {
	var out []authz.Role
	for _, n := range names {
		r := authz.Role(n)
		if !authz.KnownRole(r) {
			fmt.Fprintf(os.Stderr, "terva web: role map names %q, which is not a terva role — ignoring\n", n)
			continue
		}
		out = append(out, r)
	}
	return out
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
