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

const talkootTitle = "terva Talkoot"

// talkootServer is a single-daemon mux with a token, and with Talkoot on or off.
func talkootServer(t *testing.T, on bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "s3cret", Talkoot: on}))
	t.Cleanup(srv.Close)
	return srv
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// A daemon that runs no talkoots serves no Talkoot app. /talkoot/ then falls
// through to "/" and answers with the panel, so the test reads the bytes.
func TestTalkootAppOffWhenTalkootIsOff(t *testing.T) {
	srv := talkootServer(t, false)
	if body := bodyOf(t, authedGet(t, srv.URL+"/talkoot/", "s3cret")); strings.Contains(body, talkootTitle) {
		t.Error("/talkoot/ served the Talkoot app on a daemon with talkoot_enabled off")
	}
	resp := authedGet(t, srv.URL+"/talkoot/talkoot.webmanifest", "")
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("the Talkoot manifest answered on a daemon with Talkoot off")
	}
}

// With Talkoot on, /talkoot/ serves the shell behind the gate, and a client
// route falls back to the same shell.
func TestTalkootAppShellIsGatedAndFallsBack(t *testing.T) {
	srv := talkootServer(t, true)
	for _, p := range []string{"/talkoot/", "/talkoot/team/x"} {
		resp := authedGet(t, srv.URL+p, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with no credential = %d, want 401, which carries the login form", p, resp.StatusCode)
		}
		resp = authedGet(t, srv.URL+p, "s3cret")
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("GET %s = %d, want 200", p, resp.StatusCode)
		}
		if body := bodyOf(t, resp); !strings.Contains(body, talkootTitle) {
			t.Errorf("GET %s did not answer with the Talkoot shell: %.160q", p, body)
		}
	}
}

// The app's worker lives at /talkoot/sw.js, so every precache entry resolves
// under /talkoot/, and workbox drops the whole install on one non-OK answer.
// Each entry must therefore answer without a credential.
func TestTalkootAppPrecacheFetchableWithoutACredential(t *testing.T) {
	srv := talkootServer(t, true)
	matches := precacheEntry.FindAllStringSubmatch(swSource(t), -1)
	if len(matches) == 0 {
		t.Fatal("no precache manifest found in sw.js; the regexp is stale, not the worker")
	}
	for _, m := range matches {
		resp, err := srv.Client().Get(srv.URL + "/talkoot/" + m[1])
		if err != nil {
			t.Fatalf("GET /talkoot/%s: %v", m[1], err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("the Talkoot app's worker precaches /talkoot/%s, but a client with no credential gets %d", m[1], resp.StatusCode)
		}
	}
}

// The app is installable on its own: its manifest is ungated and names the
// /talkoot/ scope, and the shell links it ahead of the root manifest.
func TestTalkootAppHasOwnManifest(t *testing.T) {
	srv := talkootServer(t, true)
	resp := authedGet(t, srv.URL+"/talkoot/talkoot.webmanifest", "")
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("unauthenticated /talkoot/talkoot.webmanifest = %d, want 200", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	for _, want := range []string{`"terva Talkoot"`, `"start_url": "/talkoot/"`, `"scope": "/talkoot/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the Talkoot manifest lacks %s:\n%s", want, body)
		}
	}
	shell := bodyOf(t, authedGet(t, srv.URL+"/talkoot/", "s3cret"))
	if ti, ri := strings.Index(shell, "talkoot.webmanifest"), strings.Index(shell, `"./manifest.webmanifest"`); ti < 0 || (ri >= 0 && ti > ri) {
		t.Error("talkoot.html must link talkoot.webmanifest, and before the root manifest")
	}
}

// talkoot.html must never enter the precache manifest, for index.html's
// reason: a precached shell answers the navigation and buries the login form.
func TestTalkootAppShellNotPrecached(t *testing.T) {
	if strings.Contains(swSource(t), `"talkoot.html"`) {
		t.Fatal("sw.js precaches talkoot.html. Keep html out of workbox.globPatterns.")
	}
}

// Under the supervisor the app is served for every tenant, whatever the
// supervisor's own config says, because each tenant's daemon decides whether
// it runs talkoots. It is served locally, and the gate stays on the shell.
func TestTalkootAppIsServedBySupervisorAndNeverProxied(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	srv := stageMux(t, false, &got)

	status, body := getAs(t, srv, "/talkoot/", "ada")
	if status != http.StatusOK || !strings.Contains(body, talkootTitle) {
		t.Fatalf("/talkoot/ = %d without the Talkoot shell: %.160q", status, body)
	}
	if status, _ := getAs(t, srv, "/talkoot/", ""); status == http.StatusOK {
		t.Error("/talkoot/ served the shell to an unauthenticated caller")
	}
	for _, p := range appShellPaths(talkootPrefix, talkootManifest) {
		if status, _ := getAs(t, srv, p, ""); status != http.StatusOK {
			t.Errorf("%s answered %d without a credential, so the app's worker cannot install", p, status)
		}
	}
	if len(got) != 0 {
		t.Errorf("a Talkoot app path was proxied to the child: %v", got)
	}
}
