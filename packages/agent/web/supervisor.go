//go:build terva_web

package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tenant"
)

// ServeTenants runs the multi-tenant supervisor: the only listener on the host,
// authenticating every caller and proxying each to their own environment.
//
// It shares Serve's listener, so it answers to the same fail-closed bind check.
// What differs is the mux — see newSupervisorMux for the split between what the
// supervisor serves itself and what belongs to a tenant.
func ServeTenants(ctx context.Context, resolve TenantResolver, panel ctrlproto.TenantsController, opts Options) error {
	// Re-checked here even though the composition root asks first, so the
	// refusal cannot be skipped by a future second caller that forgets to.
	if err := CheckSupervisorAuth(opts); err != nil {
		return err
	}
	if panel == nil {
		return errors.New("terva serve: no operator panel was built — a supervisor with no way to see or suspend its environments is not one anybody should run")
	}
	return serveMux(ctx, opts, func(o Options) http.Handler {
		return newSupervisorMux(ctx, resolve, panel, o)
	}, "terva serve")
}

// CheckSupervisorAuth refuses a supervisor that cannot tell people apart.
//
// Exported so the composition root can ask BEFORE it announces anything: a
// daemon that prints its tenant root and containment and only then refuses to
// start reads like one that came up and crashed.
//
// 🚨 This is the supervisor's version of checkBindSafety, and it is a hard
// refusal for the same reason: the failure it prevents is silent. A bearer
// token is ONE shared secret — everyone who holds it authenticates as the same
// principal, so every caller would resolve to the same environment and read
// each other's conversations. Nothing in the running system would look wrong;
// it would simply be one account with several people in it.
//
// So `terva serve` requires an identity provider that names a person: OIDC, or
// a forward-auth header asserted by a proxy that has already done it. A token
// may still be set ALONGSIDE one — as a second factor in front of the whole
// surface — but never as the only thing establishing who is asking.
func CheckSupervisorAuth(opts Options) error {
	if opts.OIDCProvider != nil || opts.AuthHeader != "" {
		return nil
	}
	if opts.Token != "" {
		return errors.New("terva serve: a bearer token authenticates the operator, not a person — every caller would share one environment. Configure web_oidc in config.json, or front this with a proxy and pass --web-auth-header")
	}
	return errors.New("terva serve: no way to tell callers apart — configure web_oidc in config.json, or front this with a proxy and pass --web-auth-header")
}

// newSupervisorMux is the path allowlist D4's correction turned on.
//
// The split is by what the bytes belong to, and it is DEFAULT-DENY: routes are
// named here or they do not exist. A supervisor that proxied by default and
// listed exceptions would serve its own $TERVA_HOME the day someone added a
// route — which is exactly the leak the per-tenant process exists to prevent,
// reintroduced by the component doing the isolating.
func newSupervisorMux(ctx context.Context, resolve TenantResolver, panel ctrlproto.TenantsController, opts Options) *http.ServeMux {
	mux := http.NewServeMux()

	// ---- the supervisor's own, and none of it is tenant data ----
	//
	// All of it has to work BEFORE a tenant exists, which is what makes an
	// unauthenticated shell correct here rather than an oversight: this is how
	// an anonymous browser becomes someone.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc(loginPath, handleLogin(opts))
	mux.HandleFunc(oidcStartPath, handleOIDCStart(opts))
	mux.HandleFunc(oidcCallbackPath, handleOIDCCallback(opts))
	mux.HandleFunc(oidcLogoutPath, handleOIDCLogout(opts))
	mux.Handle(authStatusPath, authMiddleware(opts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})))
	for _, p := range pwaShellPaths() {
		mux.Handle(p, securityHeaders(shellHandler()))
	}
	mux.Handle(assetsPrefix, securityHeaders(shellHandler()))

	// ---- the OPERATOR's, and served by nothing a tenant can reach ----
	//
	// The admin surface lives on its own routes, not on a role check inside the
	// proxied ones. If /ws served the tenants group to a caller who passed a
	// test, tenant→admin escalation would be one bug away in the busiest code
	// here; instead the group is absent from the proxy's allowlist and absent
	// from the workspace dispatch table, and these two handlers are the only
	// code in the process that can answer it (D8).
	mux.Handle(supervisorPath, authMiddleware(opts, requireTenantsGrant(handleSupervisorPage(panel))))
	mux.Handle(supervisorWSPath, authMiddleware(opts, requireTenantsGrant(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSupervisorWS(ctx, panel, opts, w, r)
	}))))

	// ---- Stage, when this host serves it at all ----
	//
	// Off by default and mounted only on --web-stage, because Stage is not core:
	// a supervisor that was never asked for it does not grow a second app.
	//
	// Served LOCALLY rather than proxied, which is the opposite of the rule two
	// blocks down and for the opposite reason. stageHandler and stageShellHandler
	// read embedded build output and never touch config.TervaHome(), so there is
	// no tenant's bytes to fetch from a child — these are the same bytes for
	// everyone, exactly like "/" and /assets/ above. Proxying them would cost a
	// child start per navigation to serve a file the supervisor is holding.
	//
	// 🔑 The ungated half is not an oversight, it is a requirement: the /stage/-
	// scoped service worker PRECACHES these, and a precache entry the client
	// cannot fetch is a worker that cannot install (see stagePwaShellPaths).
	// Proxying them was never an option either — tenantProxy sits behind
	// authMiddleware, and an unauthenticated precache fetch has no tenant to
	// resolve.
	//
	// 🚨 Whether a given TENANT is offered Stage is not decided here. It cannot
	// be: it lives in that tenant's own config, inside the home the containment
	// exists to keep the supervisor out of. What this mount decides is whether
	// the surface exists on the host; tenant.Carrier carries the same answer to
	// the hello, so a child that advertises Stage on a host without these routes
	// has the claim stripped rather than the link broken.
	if opts.AllowStage {
		for _, p := range stagePwaShellPaths() {
			mux.Handle(p, securityHeaders(stageShellHandler()))
		}
		mux.Handle("/stage/assets/", securityHeaders(stageShellHandler()))
		mux.Handle("/stage/", authMiddleware(opts, securityHeaders(stageHandler())))
	}

	// ---- the tenant's, every byte of it, proxied to their child ----
	//
	// /ws is the control plane. The other three resolve config.TervaHome() at
	// request time IN THE SERVING PROCESS, so a supervisor that handled them
	// would serve its own home to everyone: /media/ reaches the card and
	// background stores, /upload the composer's staging area, /shared/ the
	// files an agent handed back.
	carrier := tenant.Carrier{Stage: opts.AllowStage}
	mux.Handle("/ws", authMiddleware(opts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyWS(ctx, resolve, carrier, w, r)
	})))
	tenantHTTP := authMiddleware(opts, securityHeaders(tenantProxy(resolve)))
	mux.Handle("/media/", tenantHTTP)
	mux.Handle(uploadPath, tenantHTTP)
	mux.Handle(sharedPath, tenantHTTP)

	// The panel shell itself is static and identical for everyone, so it is
	// served here rather than fetched from a child.
	mux.Handle("/", authMiddleware(opts, securityHeaders(staticHandler())))
	return mux
}

// resolveForRequest is the ONE place a resolver failure becomes a status.
//
// It exists because the two entry points diverged the first time they were
// written separately: proxyWS logged the reason and tenantProxy swallowed it,
// so an operator watching a tenant get a 403 saw nothing at all — and the
// message thrown away was the one that explains WHY, including the containment
// refusal that names the tenant already running.
//
// The caller gets a generic body on purpose. "This host cannot separate two
// tenants" is an operator's problem and an operator's log line; a person trying
// to open their notes can do nothing with it.
func resolveForRequest(w http.ResponseWriter, r *http.Request, resolve TenantResolver) (*tenant.Child, bool) {
	principal, ok := PrincipalFrom(r)
	if !ok {
		// Unreachable behind authMiddleware, and refused rather than assumed:
		// a missing principal is the shape a mis-wired route has, and the safe
		// reading of "we do not know who this is" is not "the owner".
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	child, err := resolve(r.Context(), principal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva serve: no environment for %s: %v\n", principal.Subject, err)
		http.Error(w, "no environment is available for this account", http.StatusForbidden)
		return nil, false
	}
	return child, true
}

// tenantProxy forwards a plain HTTP request to the caller's own child over its
// private socket.
//
// A fresh ReverseProxy per request rather than one shared: the destination is
// per-caller, and a shared proxy would need the socket path threaded through a
// Director anyway. The cost is an object; the alternative is a shared object
// whose target is mutable, which is a data race waiting for two tenants.
func tenantProxy(resolve TenantResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		child, ok := resolveForRequest(w, r, resolve)
		if !ok {
			return
		}
		// A short HTTP request holds the child too — brief, but it closes the
		// window where a reaper stops a daemon between resolving it and dialling.
		defer child.Hold()()
		socket := child.Socket
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = "http"
				// The child never resolves this host — the transport dials the
				// socket — but net/http requires one.
				pr.Out.URL.Host = "tenant"
				pr.Out.Host = "tenant"
				// Deliberately NOT SetXForwarded: the child trusts its socket,
				// and an X-Forwarded-For it did not ask for is a header a tenant
				// could learn to spoof through an upstream that reflects it.
			},
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				fmt.Fprintf(os.Stderr, "terva serve: proxy to %s: %v\n", child.ID, err)
				http.Error(w, "the environment is not answering", http.StatusBadGateway)
			},
		}
		rp.ServeHTTP(w, r)
	})
}
