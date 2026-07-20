//go:build terva_web

package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStageOffByDefault — with web_stage off, /stage/ is not mounted, so it falls
// through to the panel SPA rather than serving the Stage app.
func TestStageOffByDefault(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	defer srv.Close()

	resp := authedGet(t, srv.URL+"/stage/", "secret")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "terva Stage") {
		t.Error("the Stage app must not be served when web_stage is off")
	}
}

// TestStageServedWhenEnabled — with web_stage on, /stage/ serves the gated Stage
// shell, and a client route under it SPA-falls-back to that shell.
func TestStageServedWhenEnabled(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret", AllowStage: true}))
	defer srv.Close()

	// Gated like the panel: no token → 401, not a leak of the shell.
	if resp := authedGet(t, srv.URL+"/stage/", ""); resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("unauthenticated /stage/: status %d, want 401", resp.StatusCode)
	}

	resp := authedGet(t, srv.URL+"/stage/", "secret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/stage/: status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "terva Stage") {
		t.Errorf("/stage/ did not serve the Stage shell:\n%.200s", body)
	}

	// A client route → SPA fallback to the Stage shell.
	r2 := authedGet(t, srv.URL+"/stage/library", "secret")
	defer r2.Body.Close()
	b2, _ := io.ReadAll(r2.Body)
	if !strings.Contains(string(b2), "terva Stage") {
		t.Error("/stage/<route> should fall back to the Stage shell")
	}
}

// TestStageHasOwnManifest — the installability spike: the Stage app carries its
// own PWA identity (start_url/scope /stage/), not the panel's. The manifest is
// served UNGATED under /stage/ (it is part of the Stage PWA shell), so a
// logged-out (re)install can read Stage's identity, mirroring the root manifest.
func TestStageHasOwnManifest(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret", AllowStage: true}))
	defer srv.Close()

	// No credential: the manifest is ungated, like /manifest.webmanifest at root.
	resp := authedGet(t, srv.URL+"/stage/stage.webmanifest", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unauthenticated /stage/stage.webmanifest: status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"terva Stage"`, `"start_url": "/stage/"`, `"scope": "/stage/"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("stage manifest missing %s:\n%s", want, body)
		}
	}
	// The Stage shell links its own manifest before the plugin's root one.
	shell := authedGet(t, srv.URL+"/stage/", "secret")
	defer shell.Body.Close()
	sb, _ := io.ReadAll(shell.Body)
	if si, ri := strings.Index(string(sb), "stage.webmanifest"), strings.Index(string(sb), `"./manifest.webmanifest"`); si < 0 || (ri >= 0 && si > ri) {
		t.Error("stage.html must link stage.webmanifest, and before the root manifest (first wins)")
	}
}
