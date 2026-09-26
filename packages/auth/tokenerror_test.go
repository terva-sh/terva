package auth

import "testing"

// The whole point of the split: a revoked grant must not be retried on a timer
// (it cannot succeed, and it hammers the auth server the user's next /login has
// to reach), while a transient failure must not be mistaken for one (that turns
// a bad ten minutes into a re-login).
func TestTokenErrorTerminalClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantCode string
		terminal bool
	}{
		// The one that matters: RFC 6749 defines invalid_grant as expired,
		// revoked, or mismatched — and a rotating server returns it for a
		// REPLAYED refresh token, which is what concurrent refreshes produce.
		{"revoked grant", 400, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`, "invalid_grant", true},
		{"bad client", 401, `{"error":"invalid_client"}`, "invalid_client", true},
		{"rate limited", 429, `{"error":"slow_down"}`, "slow_down", false},
		{"server error", 500, `{"error":"server_error"}`, "server_error", false},
		{"gateway down, no body", 502, `<html>502 Bad Gateway</html>`, "", false},
		// The endpoint answered, understood, and refused. No schedule turns
		// that into a working token.
		{"refused, unparseable body", 400, `nope`, "", true},
		{"unauthorized, unparseable body", 401, ``, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTokenError(tc.status, []byte(tc.body))
			if e.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", e.Code, tc.wantCode)
			}
			if got := e.Terminal(); got != tc.terminal {
				t.Errorf("Terminal() = %v, want %v (%s)", got, tc.terminal, e)
			}
		})
	}
}

// The description is what a human reads. A documented body yields the
// provider's own words; an undocumented one must still carry the raw response
// rather than swallowing it.
func TestTokenErrorKeepsSomethingReadable(t *testing.T) {
	e := newTokenError(400, []byte(`{"error":"invalid_grant","error_description":"grant is invalid"}`))
	if e.Description != "grant is invalid" {
		t.Errorf("Description = %q, want the provider's error_description", e.Description)
	}
	raw := newTokenError(502, []byte("  upstream exploded  "))
	if raw.Description != "upstream exploded" {
		t.Errorf("Description = %q, want the trimmed raw body", raw.Description)
	}
}
