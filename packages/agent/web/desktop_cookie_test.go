//go:build terva_web

package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDesktopCookieIsSessionOnly(t *testing.T) {
	for _, sessionOnly := range []bool{false, true} {
		w := httptest.NewRecorder()
		opts := Options{Token: "temporary", SessionCookie: sessionOnly}
		form := url.Values{"token": {opts.Token}, "next": {"/"}}
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/auth", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		handleLogin(opts)(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("login returned %d", w.Code)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing authentication cookie")
		}
		c := cookies[0]
		if !c.HttpOnly || c.Value != "temporary" {
			t.Fatal("invalid authentication cookie")
		}
		if sessionOnly && (c.MaxAge != 0 || !c.Expires.IsZero()) {
			t.Fatal("desktop cookie persists across browser sessions")
		}
		if !sessionOnly && c.MaxAge != tokenCookieMaxAge {
			t.Fatal("web cookie lifetime changed")
		}
	}
}

func TestDesktopQueryAuthenticationUsesSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/auth/status?token=temporary", nil)
	authMiddleware(Options{Token: "temporary", SessionCookie: true}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("authentication returned %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != 0 || !cookies[0].Expires.IsZero() {
		t.Fatal("desktop middleware produced a persistent cookie")
	}
}
