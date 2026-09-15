package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIdP is a minimal OpenID provider that REALLY SIGNS. The signing is the
// point: an unsigned fixture would let every test below pass against a relying
// party that had skipped verification entirely.
type fakeIdP struct {
	srv       *httptest.Server
	issuer    string
	issuerOut string // what the discovery document claims (mix-up tests)
	key       *rsa.PrivateKey
	signWith  *rsa.PrivateKey // defaults to key; set to forge
	claims    map[string]any
	audience  any
	omitIDTok bool
	tokenErr  string
	lastForm  url.Values
	rawIDTok  string   // when set, /token returns this verbatim (forgery tests)
	keyID     string   // JWKS kid; defaults to k1
	algsOut   []string // what discovery advertises; defaults to RS256
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	f := &fakeIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := f.issuerOut
		if iss == "" {
			iss = f.issuer
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss,
			"authorization_endpoint":                f.issuer + "/authorize",
			"token_endpoint":                        f.issuer + "/token",
			"jwks_uri":                              f.issuer + "/jwks",
			"id_token_signing_alg_values_supported": f.algs(),
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &f.key.PublicKey, KeyID: f.kid(), Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if f.tokenErr != "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": f.tokenErr, "error_description": "nope"})
			return
		}
		body := map[string]any{"access_token": "at", "token_type": "Bearer"}
		switch {
		case f.rawIDTok != "":
			body["id_token"] = f.rawIDTok
		case !f.omitIDTok:
			body["id_token"] = f.mintIDToken(t)
		}
		json.NewEncoder(w).Encode(body)
	})
	f.srv = httptest.NewTLSServer(mux)
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) kid() string {
	if f.keyID == "" {
		return "k1"
	}
	return f.keyID
}

func (f *fakeIdP) algs() []string {
	if len(f.algsOut) == 0 {
		return []string{"RS256"}
	}
	return f.algsOut
}

// payload is the claim set mintIDToken signs, exposed so a forgery test can
// wrap the same honest claims in a dishonest envelope.
func (f *fakeIdP) payload() []byte {
	aud := any(f.audience)
	if aud == nil {
		aud = "terva"
	}
	payload := map[string]any{
		"iss": f.issuer, "sub": "user-123", "aud": aud,
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	for k, v := range f.claims {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	return b
}

func (f *fakeIdP) mintIDToken(t *testing.T) string {
	t.Helper()
	b := f.payload()
	signKey := f.signWith
	if signKey == nil {
		signKey = f.key
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: signKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", f.kid()),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	obj, err := signer.Sign(b)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

func (f *fakeIdP) provider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	cfg.Issuer = f.issuer
	if cfg.ClientID == "" {
		cfg.ClientID = "terva"
	}
	p, err := Discover(context.Background(), cfg, "https://terva.example/cb", f.srv.Client())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return p
}

func TestExchangeYieldsTheIdentity(t *testing.T) {
	f := newFakeIdP(t)
	f.claims = map[string]any{
		"nonce": "n1", "email": "drew@example.com", "name": "Drew",
		"groups": []string{"terva-admins", "everyone"},
	}
	p := f.provider(t, Config{})

	pkce, _ := NewLoginSecrets()
	id, err := p.Exchange(context.Background(), "code", "n1", pkce, f.srv.Client())
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if id.Subject != "user-123" || id.Email != "drew@example.com" || id.Name != "Drew" {
		t.Errorf("identity = %+v", id)
	}
	if len(id.Groups) != 2 {
		t.Errorf("Groups = %v", id.Groups)
	}
	if f.lastForm.Get("code_verifier") != pkce {
		t.Error("the PKCE verifier did not reach the token endpoint")
	}
}

// 🚨 THE test that the dependency exists to make possible. A token signed by a
// key the IdP never published must be refused. Under the previous hand-rolled
// scheme — which skipped signature verification on the strength of TLS — this
// forgery was accepted, and every other test in this file passed anyway.
func TestTokenSignedByTheWrongKeyIsRejected(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{})

	attacker, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.signWith = attacker // JWKS still publishes only the real key

	pkce, _ := NewLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err == nil {
		t.Fatal("a token signed by an unpublished key was ACCEPTED")
	}
}

// The control for the test above: with the real key, the same path succeeds. A
// rejection test alone cannot tell "verification works" from "nothing works".
func TestTokenSignedByThePublishedKeyIsAccepted(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{})
	pkce, _ := NewLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err != nil {
		t.Fatalf("a correctly signed token was refused: %v", err)
	}
}

// 🔑 The three failure modes decision 0013 names, pinned. Every JWT validator
// that has shipped one of these as a CVE shipped it silently, so each test
// forges a token whose CLAIMS are honest and whose ENVELOPE is not: if any of
// them passes, the library or our configuration of it has regressed, and the
// spending verbs behind the capability mask are open.

// `none`: an unsigned token whose header announces there is no signature to
// check. RFC 7518 makes the algorithm legal; a relying party must refuse it.
func TestUnsignedNoneAlgorithmIsRejected(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{})

	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT","kid":"k1"}`))
	f.rawIDTok = header + "." + enc.EncodeToString(f.payload()) + "."

	pkce, _ := NewLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err == nil {
		t.Fatal("an unsigned alg=none token was ACCEPTED")
	}
}

// Algorithm confusion: the token names HS256 and is MACed with the IdP's own
// PUBLIC key as the shared secret. A validator that picks the verification
// routine from the token's header, and hands it whatever key the kid names,
// accepts this — and the public key is public, so anyone can mint one. The
// discovery document advertises HS256 here on purpose. Two layers refuse it:
// the JOSE library will not MAC-verify against a public key, and terva's own
// algorithm list never contains HS256. Measured 2026-09-13, the second alone
// is not load-bearing — the test passes with the pin removed — so what this
// pins is that the refusal survives either layer changing.
func TestHMACWithThePublicKeyIsRejected(t *testing.T) {
	f := newFakeIdP(t)
	f.algsOut = []string{"RS256", "HS256"}
	p := f.provider(t, Config{})

	secret, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: secret},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := signer.Sign(f.payload())
	if err != nil {
		t.Fatal(err)
	}
	f.rawIDTok, err = obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	pkce, _ := NewLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err == nil {
		t.Fatal("an HS256 token MACed with the public key was ACCEPTED")
	}
}

// kid handling, both halves. A token naming a kid the keyset does not publish
// is refused even when the IdP is re-asked — the library refetches on an
// unknown kid, and the refetch must not turn "unknown" into "first key wins".
// Then the control: a real rotation, where the IdP publishes the new kid, is
// accepted on the same refetch path. Together they show the refetch exists
// and that it is a lookup, not a fallback.
func TestUnknownKidIsRefusedAndRotationIsHonoured(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{})
	pkce, _ := NewLoginSecrets()

	// Sign with the real key, but under a kid the JWKS does not carry.
	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.signWith, f.keyID = rotated, "k2"
	f.rawIDTok = f.mintIDToken(t) // header kid=k2
	f.keyID = "k1"                // ...but the keyset still publishes only k1
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err == nil {
		t.Fatal("a token under an unpublished kid was ACCEPTED")
	}

	// Now the IdP really rotates: k2 is published, and the same token verifies.
	f.key, f.keyID = rotated, "k2"
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err != nil {
		t.Fatalf("a token under a freshly published kid was refused: %v", err)
	}
}

// 🚨 IdP mix-up: a metadata document naming a different issuer would redirect
// the token exchange, handing an authorization code to whoever it named.
func TestDiscoveryRejectsAnIssuerMismatch(t *testing.T) {
	f := newFakeIdP(t)
	f.issuerOut = "https://evil.example.com"
	_, err := Discover(context.Background(), Config{Issuer: f.issuer, ClientID: "terva"},
		"https://terva.example/cb", f.srv.Client())
	if err == nil {
		t.Fatal("discovery accepted a document whose issuer did not match")
	}
}

// Plaintext discovery is unauthenticated, so an attacker who can rewrite it
// points the JWKS fetch at their own keys and the signature check proves nothing.
func TestPlaintextIssuerIsRefused(t *testing.T) {
	_, err := Discover(context.Background(), Config{Issuer: "http://idp.example.com", ClientID: "terva"}, "https://terva.example/cb", nil)
	if err == nil {
		t.Fatal("a plaintext issuer was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("the refusal did not explain the TLS requirement: %v", err)
	}
}

// Replay: a token minted for a different login attempt must not land here. The
// library cannot check this — it does not know what we sent — so it stays ours.
func TestNonceMismatchIsRejected(t *testing.T) {
	f := newFakeIdP(t)
	f.claims = map[string]any{"nonce": "someone-elses"}
	p := f.provider(t, Config{})
	pkce, _ := NewLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", "mine", pkce, f.srv.Client()); err == nil {
		t.Fatal("a token with the wrong nonce was accepted")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	f := newFakeIdP(t)
	f.claims = map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}
	p := f.provider(t, Config{})
	pkce, _ := NewLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", "", pkce, f.srv.Client()); err == nil {
		t.Fatal("an expired id_token was accepted")
	}
}

func TestAudienceMustIncludeThisClient(t *testing.T) {
	for _, tc := range []struct {
		name string
		aud  any
		ok   bool
	}{
		{"string match", "terva", true},
		{"string mismatch", "someone-else", false},
		{"array containing", []string{"other", "terva"}, true},
		{"array without", []string{"other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIdP(t)
			f.audience = tc.aud
			p := f.provider(t, Config{})
			pkce, _ := NewLoginSecrets()
			_, err := p.Exchange(context.Background(), "c", "", pkce, f.srv.Client())
			if tc.ok && err != nil {
				t.Fatalf("audience %v was refused: %v", tc.aud, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("audience %v was accepted", tc.aud)
			}
		})
	}
}

// 🔑 An authenticated user with no MAPPED group gets no roles. Authenticating
// proves who someone is; it does not mean the operator meant to admit them.
func TestUnmappedGroupsYieldNoRoles(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{RoleMap: map[string]string{"terva-admins": "owner"}})
	if got := p.Roles(&Identity{Groups: []string{"some-other-team"}}); len(got) != 0 {
		t.Fatalf("unmapped groups produced roles %v", got)
	}
	if got := p.Roles(&Identity{Groups: nil}); len(got) != 0 {
		t.Fatalf("no groups produced roles %v", got)
	}
	if got := p.Roles(nil); len(got) != 0 {
		t.Fatalf("a nil identity produced roles %v", got)
	}
}

func TestRoleMappingAndDedupe(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{RoleMap: map[string]string{
		"admins": "owner", "staff": "operator", "also-admins": "owner",
	}})
	got := p.Roles(&Identity{Groups: []string{"admins", "staff", "also-admins", "unknown"}})
	if len(got) != 2 {
		t.Fatalf("roles = %v, want owner+operator deduped", got)
	}
}

// IdPs disagree about whether a single group is a string or a one-element list.
func TestGroupsClaimAcceptsBothShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  any
		want int
	}{
		{"list", []string{"a", "b"}, 2},
		{"single string", "a", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIdP(t)
			f.claims = map[string]any{"groups": tc.val}
			p := f.provider(t, Config{})
			pkce, _ := NewLoginSecrets()
			id, err := p.Exchange(context.Background(), "c", "", pkce, f.srv.Client())
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if len(id.Groups) != tc.want {
				t.Errorf("groups = %v, want %d", id.Groups, tc.want)
			}
		})
	}
}

func TestCustomGroupsClaim(t *testing.T) {
	f := newFakeIdP(t)
	f.claims = map[string]any{"roles": []string{"x"}, "groups": []string{"ignored"}}
	p := f.provider(t, Config{GroupsClaim: "roles"})
	pkce, _ := NewLoginSecrets()
	id, err := p.Exchange(context.Background(), "c", "", pkce, f.srv.Client())
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "x" {
		t.Fatalf("groups = %v, want the configured claim", id.Groups)
	}
}

func TestTokenEndpointErrorIsSurfaced(t *testing.T) {
	f := newFakeIdP(t)
	f.tokenErr = "invalid_grant"
	p := f.provider(t, Config{})
	pkce, _ := NewLoginSecrets()
	_, err := p.Exchange(context.Background(), "c", "", pkce, f.srv.Client())
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error did not name the IdP's reason: %v", err)
	}
}

// A response with no id_token is a misconfigured client (missing openid scope),
// and the error should say so rather than nil-panicking downstream.
func TestMissingIDTokenIsAClearError(t *testing.T) {
	f := newFakeIdP(t)
	f.omitIDTok = true
	p := f.provider(t, Config{})
	pkce, _ := NewLoginSecrets()
	_, err := p.Exchange(context.Background(), "c", "", pkce, f.srv.Client())
	if err == nil || !strings.Contains(err.Error(), "openid") {
		t.Fatalf("error did not point at the openid scope: %v", err)
	}
}

func TestAuthorizeURLCarriesPKCEAndNonce(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(t, Config{})
	pkce, nonce := NewLoginSecrets()
	raw := p.AuthorizeURL("st8", nonce, pkce)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": "terva", "state": "st8",
		"nonce": nonce, "code_challenge_method": "S256",
		"redirect_uri": "https://terva.example/cb",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	// The challenge must be the HASH, never the verifier itself — sending the
	// verifier downgrades PKCE to decoration.
	if ch := q.Get("code_challenge"); ch == "" || ch == pkce {
		t.Errorf("code_challenge = %q, want an S256 hash distinct from the verifier", ch)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope %q must always include openid", q.Get("scope"))
	}
}

func TestScopesAlwaysIncludeOpenIDExactlyOnce(t *testing.T) {
	got := scopes([]string{"openid", "email"})
	n := 0
	for _, s := range got {
		if s == "openid" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("scopes = %v, want exactly one openid", got)
	}
	if len(got) != 2 {
		t.Errorf("scopes = %v lost a configured scope", got)
	}
}

func TestLoginSecretsAreFreshEachTime(t *testing.T) {
	a, an := NewLoginSecrets()
	b, bn := NewLoginSecrets()
	if a == b || an == bn {
		t.Fatal("two login attempts shared a secret")
	}
	if a == an {
		t.Fatal("the PKCE verifier and the nonce are the same value")
	}
}
