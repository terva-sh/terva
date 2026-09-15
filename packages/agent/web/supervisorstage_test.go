//go:build terva_web

package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tenant"
)

// The whole point, over a real proxied connection: a child that believes it
// serves Stage must not be able to tell the browser so through a supervisor
// that does not. This is the defect the route gap actually caused — not a
// missing page, but an "open in Stage" link the panel was told to render.
func TestAChildCannotAdvertiseStageThroughASupervisorThatDoesNotServeIt(t *testing.T) {
	skipIfNoUnix(t)

	owner := authz.Principal{Subject: "s", Source: authz.SourceOIDC, Roles: []authz.Role{authz.RoleOwner}}

	t.Run("stripped when the host does not serve it", func(t *testing.T) {
		fc := startFakeChild(t)
		c := dialProxy(t, proxyServer(t, fc, owner, tenant.Carrier{Stage: false}))
		hello := readFrame(t, c)
		if hello.Hello == nil {
			t.Fatalf("no hello: %+v", hello)
		}
		if slices.Contains(hello.Hello.Features, ctrlproto.FeatureStage) {
			t.Errorf("the browser was told this host serves Stage: %v", hello.Hello.Features)
		}
		// The control — narrowing dropped the claim, not the whole list.
		if !slices.Contains(hello.Hello.Features, ctrlproto.FeatureImages) {
			t.Errorf("an unrelated feature was dropped too: %v", hello.Hello.Features)
		}
	})

	t.Run("kept when it does", func(t *testing.T) {
		fc := startFakeChild(t)
		c := dialProxy(t, proxyServer(t, fc, owner, tenant.Carrier{Stage: true}))
		hello := readFrame(t, c)
		if hello.Hello == nil {
			t.Fatalf("no hello: %+v", hello)
		}
		if !slices.Contains(hello.Hello.Features, ctrlproto.FeatureStage) {
			t.Errorf("a host that mounts /stage/ withheld the child's Stage claim: %v", hello.Hello.Features)
		}
	})
}

// stageTitle tells the two embedded shells apart. index.html is <title>terva</title>
// and stage.html is <title>terva Stage</title> — the only stable difference
// between them, since the asset names are content-hashed. The single-tenant
// Stage tests discriminate the same way.
const stageTitle = "terva Stage"

// stageMux builds a supervisor whose Stage decision is the thing under test.
func stageMux(t *testing.T, allowStage bool, got *[]string) *httptest.Server {
	t.Helper()
	child := startRecordingChild(t, got)
	opts := Options{AuthHeader: "X-Forwarded-User", AllowStage: allowStage}
	srv := httptest.NewServer(newSupervisorMux(context.Background(), fixedResolver(child), newTestPanel(t), opts))
	t.Cleanup(srv.Close)
	return srv
}

func getAs(t *testing.T, srv *httptest.Server, path, user string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if user != "" {
		req.Header.Set("X-Forwarded-User", user)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Off by default: a supervisor nobody asked for Stage does not grow a second
// app. Asserted on the BYTES, because /stage/ does not 404 when it is absent —
// it falls through to "/" and answers with the main shell, which is exactly the
// confusion that made this gap invisible.
func TestStageIsNotServedUnlessTheOperatorAsks(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	srv := stageMux(t, false, &got)

	_, body := getAs(t, srv, "/stage/", "ada")
	if strings.Contains(body, stageTitle) {
		t.Error("/stage/ served the Stage shell on a supervisor that was never asked for it")
	}
	// And say what it DID answer with, because "not the Stage shell" is the weak
	// half of this claim: /stage/ falls through to "/" and returns the main app,
	// byte for byte. That is why the gap was invisible — nothing 404s.
	if _, root := getAs(t, srv, "/", "ada"); body != root {
		t.Errorf("/stage/ answered with something that is neither the Stage shell nor the main one (%d bytes)", len(body))
	}
	// The precache files must not be there either — an installed worker for an
	// app the host does not serve is worse than no worker.
	if status, _ := getAs(t, srv, "/stage/assets/nonexistent.js", ""); status == http.StatusOK {
		t.Error("/stage/assets/ answered on a supervisor with Stage off")
	}
	if len(got) != 0 {
		t.Errorf("a Stage path was proxied to the child: %v", got)
	}
}

// With the flag, the shell is served — and served LOCALLY. The child must not
// see a byte of it: these are embedded build output, identical for everyone,
// and fetching them from a tenant's daemon would cost a child start per
// navigation to deliver a file the supervisor is already holding.
func TestStageIsServedBySupervisorAndNeverProxied(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	srv := stageMux(t, true, &got)

	status, body := getAs(t, srv, "/stage/", "ada")
	if status != http.StatusOK {
		t.Fatalf("/stage/ answered %d with Stage enabled", status)
	}
	if !strings.Contains(body, stageTitle) {
		t.Errorf("/stage/ did not answer with the Stage shell: %.160q", body)
	}
	if len(got) != 0 {
		t.Errorf("Stage was proxied to the child instead of served locally: %v", got)
	}
}

// The gate stays on the shell: an unauthenticated navigation to /stage/ answers
// with the login form, exactly as "/" does. Stage being static does not make it
// public.
func TestTheStageShellStaysBehindTheAuthGate(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	srv := stageMux(t, true, &got)

	status, _ := getAs(t, srv, "/stage/", "")
	if status == http.StatusOK {
		t.Errorf("/stage/ served the shell to an unauthenticated caller (%d)", status)
	}
}

// ...and the precache files stay OUTSIDE it, which is not a relaxation but a
// requirement: the /stage/-scoped service worker precaches them, and a precache
// entry the client cannot fetch is a worker that cannot install. The single-
// tenant mux has the same exception; this is that guarantee under the
// supervisor, where the gate is stricter and the mistake easier to make.
func TestStagePrecacheIsFetchableWithoutACredentialUnderTheSupervisor(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	srv := stageMux(t, true, &got)

	paths := stagePwaShellPaths()
	if len(paths) == 0 {
		t.Skip("no built Stage shell files in this tree")
	}
	for _, p := range paths {
		if status, _ := getAs(t, srv, p, ""); status != http.StatusOK {
			t.Errorf("%s answered %d without a credential — the Stage service worker cannot install", p, status)
		}
	}
	if len(got) != 0 {
		t.Errorf("an ungated Stage shell file was proxied, which cannot work — the proxy needs a resolved tenant: %v", got)
	}
}
