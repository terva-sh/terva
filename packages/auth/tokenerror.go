package auth

// Why a token request failed, in the only two categories a retry policy cares
// about: the grant is gone and no amount of waiting brings it back, or
// something transient happened and the token we still hold is probably fine.
//
// Retrying a revoked grant on a timer is the worst of both — it cannot succeed,
// and it hammers an auth server that may be the same one rate-limiting the
// login the user is about to attempt by hand.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TokenError is a non-2xx response from an OAuth token endpoint.
type TokenError struct {
	Status int
	// Code is RFC 6749's `error` field ("invalid_grant", "invalid_client",
	// "slow_down", ...). Empty when the body was not the documented shape —
	// which happens, so nothing may depend on it being present.
	Code string
	// Description is `error_description`, or the raw body when the response
	// did not parse. It is what a human needs to see.
	Description string
}

func (e *TokenError) Error() string {
	switch {
	case e.Code != "" && e.Description != "":
		return fmt.Sprintf("token http %d: %s: %s", e.Status, e.Code, e.Description)
	case e.Code != "":
		return fmt.Sprintf("token http %d: %s", e.Status, e.Code)
	default:
		return fmt.Sprintf("token http %d: %s", e.Status, e.Description)
	}
}

// Terminal reports whether the grant is gone for good, so the caller should
// stop retrying and tell the user to sign in again.
//
// invalid_grant is the authoritative one: RFC 6749 defines it as a refresh
// token that is expired, revoked, or does not match — and a server that
// rotates refresh tokens returns it for a REPLAYED one, which is what two
// instances refreshing concurrently produces. invalid_client and
// unauthorized_client mean the registration itself is no longer accepted.
//
// A 400/401 with no recognizable code is treated as terminal too: the token
// endpoint answered, understood the request, and refused it. Retrying that on
// a schedule has never once turned into a working token.
func (e *TokenError) Terminal() bool {
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client", "invalid_request", "unsupported_grant_type":
		return true
	case "":
		return e.Status == 400 || e.Status == 401
	}
	return false
}

func newTokenError(status int, body []byte) *TokenError {
	e := &TokenError{Status: status, Description: strings.TrimSpace(string(body))}
	var raw struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &raw) == nil && raw.Error != "" {
		e.Code = raw.Error
		if raw.Description != "" {
			e.Description = raw.Description
		}
	}
	return e
}
