package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// codexIdentityCaptureServer answers anything with an empty SSE stream and an
// empty JSON body, and hands back the headers of the LAST request it saw.
//
// It does not try to satisfy each endpoint's response shape. The identity
// headers are written before a response exists, so a caller that fails to
// decode the reply has still proved what it sent. Every test below asserts the
// captured headers are non-nil, which is what separates "the request went out
// and said X" from "no request was ever made".
func codexIdentityCaptureServer(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if strings.Contains(r.Header.Get("accept"), "event-stream") {
			w.Header().Set("content-type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header { return got }
}

// nativeUserAgentRE is the shape the native identity must present. Asserting
// the shape rather than the literal keeps the probed version out of the
// expectation, because the version legitimately differs per machine.
var nativeUserAgentRE = regexp.MustCompile(`^codex_cli_rs/\d+\.\d+\.\d+$`)

// assertIdentity checks one captured request against one expected identity.
func assertIdentity(t *testing.T, label string, h http.Header, native bool) {
	t.Helper()
	if h == nil {
		t.Fatalf("%s: no request reached the server, so this test proved nothing", label)
	}
	originator, userAgent := h.Get("originator"), h.Get("user-agent")
	if native {
		if originator != "codex_cli_rs" {
			t.Errorf("%s: originator = %q, want \"codex_cli_rs\"", label, originator)
		}
		if !nativeUserAgentRE.MatchString(userAgent) {
			t.Errorf("%s: user-agent = %q, want codex_cli_rs/<version triple>", label, userAgent)
		}
		return
	}
	if originator != "terva" {
		t.Errorf("%s: originator = %q, want \"terva\" — the DEFAULT must name terva, and a "+
			"default that impersonates another client is the thing this setting exists to keep opt-in",
			label, originator)
	}
	if !strings.HasPrefix(userAgent, "terva ") {
		t.Errorf("%s: user-agent = %q, want terva's own", label, userAgent)
	}
}

// All three request paths must agree. A conversation that named terva on its
// stream and the Codex CLI on its /compact call would present two identities
// to one backend, which is worse than either choice made consistently.
//
// The paths are driven through their real entry points rather than through
// identityHeaders directly, because the bug this guards against is a site that
// forgets to ask: identityHeaders can be perfect while one of three callers
// still writes a literal.
func TestCodexIdentityIsConsistentAcrossEveryRequestPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity string
		native   bool
	}{
		{"default names terva", CodexIdentityTerva, false},
		{"native names the codex cli", CodexIdentityNative, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("stream", func(t *testing.T) {
				srv, headers := codexIdentityCaptureServer(t)
				c := NewOpenAICodexSource(StaticCredential("token"), "acct", srv.URL,
					WithCodexClientIdentity(tc.identity))
				if _, err := c.Stream(context.Background(), Request{
					Model:          "gpt-5.6-sol",
					PromptCacheKey: "cache-key",
					Messages:       []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
				}); err != nil {
					t.Fatalf("Stream: %v", err)
				}
				assertIdentity(t, "stream", headers(), tc.native)
			})

			t.Run("compact", func(t *testing.T) {
				srv, headers := codexIdentityCaptureServer(t)
				c := NewOpenAICodexSource(StaticCredential("token"), "acct", srv.URL+"/responses",
					WithCodexClientIdentity(tc.identity)).(*codexClient)
				// The reply is an empty object, so this returns an error. The
				// headers are what is under test and they are already sent.
				_, _, _ = c.CompactServerSide(context.Background(), Request{
					Model:          "gpt-5.6-sol",
					PromptCacheKey: "cache-key",
					Messages:       []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
				})
				assertIdentity(t, "compact", headers(), tc.native)
			})

			t.Run("wham", func(t *testing.T) {
				srv, headers := codexIdentityCaptureServer(t)
				c := NewOpenAICodexSource(StaticCredential("token"), "acct",
					srv.URL+"/backend-api/codex/responses",
					WithCodexClientIdentity(tc.identity)).(*codexClient)
				_, _ = c.ListResets(context.Background())
				assertIdentity(t, "wham", headers(), tc.native)
			})
		})
	}
}

// The session-id header is what actually earns the cache (25/30 against 3/18),
// so it must survive the identity switch. Turning the identity on and losing
// session-id would trade the measured lever for the unmeasured one.
func TestCodexNativeIdentityStillSendsSessionID(t *testing.T) {
	const key = "0e3c4d40-b787-8bcc-9f12-20260804154657"
	srv, headers := codexIdentityCaptureServer(t)
	c := NewOpenAICodexSource(StaticCredential("token"), "acct", srv.URL,
		WithCodexClientIdentity(CodexIdentityNative))

	if _, err := c.Stream(context.Background(), Request{
		Model:          "gpt-5.6-sol",
		PromptCacheKey: key,
		Messages:       []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	h := headers()
	if h == nil {
		t.Fatal("no request reached the server")
	}
	if got, want := h.Get("session-id"), codexSessionID(key); got != want {
		t.Errorf("session-id = %q, want %q — the identity switch must not disturb the one "+
			"header that was measured to matter", got, want)
	}
}

// A typo must not silently become an identity. It keeps the default, because a
// request that names terva is always the safe reading of a configuration
// mistake, and ValidCodexIdentity is what lets the config layer say so out loud.
func TestCodexUnknownIdentityKeywordKeepsTheDefault(t *testing.T) {
	for _, bad := range []string{"codex_cli_rs", "native ", "Native", "true", "yes", "spoof"} {
		if ValidCodexIdentity(bad) {
			t.Errorf("ValidCodexIdentity(%q) = true, want false", bad)
		}
		srv, headers := codexIdentityCaptureServer(t)
		c := NewOpenAICodexSource(StaticCredential("token"), "acct", srv.URL,
			WithCodexClientIdentity(bad))
		if _, err := c.Stream(context.Background(), Request{
			Model:    "gpt-5.6-sol",
			Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		}); err != nil {
			t.Fatalf("Stream: %v", err)
		}
		assertIdentity(t, "identity "+bad, headers(), false)
	}
}

// The two keywords the client implements, and nothing else. This is the list
// the configuration layer validates against, so it is worth pinning that
// "" and "native" both pass.
func TestValidCodexIdentityAcceptsOnlyTheImplementedKeywords(t *testing.T) {
	for _, ok := range []string{CodexIdentityTerva, CodexIdentityNative} {
		if !ValidCodexIdentity(ok) {
			t.Errorf("ValidCodexIdentity(%q) = false, want true", ok)
		}
	}
}

// Constructing with no option at all must behave exactly as constructing with
// the empty keyword. Without this, a caller that simply never passes the option
// (every existing call site, and the live probes) could drift onto a different
// path from one that passes "".
func TestCodexIdentityDefaultsWithNoOptionAtAll(t *testing.T) {
	srv, headers := codexIdentityCaptureServer(t)
	c := NewOpenAICodex("token", "acct", srv.URL)
	if _, err := c.Stream(context.Background(), Request{
		Model:    "gpt-5.6-sol",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	assertIdentity(t, "no option", headers(), false)
}
