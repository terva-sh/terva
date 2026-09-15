//go:build terva_web

package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tenant"
)

// The supervisor's own surfaces: the operator page and the operator socket.
//
// Both are mounted by newSupervisorMux and NEITHER is proxied. That is the D8
// boundary at the HTTP layer, matching the two below it: the ctrlproto dispatch
// table has no tenants.* entry, and the proxy's forwardable allowlist has no
// tenants group. A tenant's bytes reach a workspace daemon; an operator's reach
// this file.
const (
	// supervisorPath is the operator page. A page rather than a view in the PWA
	// because it must work when NOTHING else does — before any tenant exists,
	// with every child stopped, on a host whose containment refuses to start a
	// second environment. A surface for diagnosing that cannot be built out of
	// the parts being diagnosed.
	supervisorPath = "/supervisor"
	// supervisorWSPath is the same picture on the wire, for `terva attach` and
	// any other operator client.
	supervisorWSPath = "/supervisor/ws"
)

// requireTenantsGrant gates a handler on the authority to manage environments.
//
// 🔑 It asks the AUTHORITY TABLE, not for a named role. authz.Grant is the one
// place a role's reach is written down, so "who may manage tenants" stays a
// row in that table rather than a second permission vocabulary here — and the
// day an explicit admin role is added, this needs no edit.
//
// A refusal is 403 and NAMES what is missing, rather than a 404 pretending the
// route is not there. The non-existence posture that governs the ctrlproto group
// is about what a TENANT'S CONNECTION can reach, and it is enforced structurally
// three layers down; hiding this route from an authenticated operator would buy
// none of that and would leave someone whose claim mapping is wrong unable to
// tell a misconfigured role from a build without a panel. Same argument D7 makes
// for naming the missing role instead of denying generically.
func requireTenantsGrant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r)
		if !ok {
			// Unreachable behind authMiddleware, and refused rather than
			// assumed: "we do not know who this is" never reads as the owner.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !slices.Contains(authz.Grant(p), ctrlproto.GroupTenants) {
			fmt.Fprintf(os.Stderr, "terva serve: %s asked for the supervisor panel and holds no role that manages environments\n", p.Subject)
			http.Error(w, "managing environments needs the owner role on this daemon — map one of this account's identity-provider groups to `owner` in web_oidc.role_map", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveSupervisorWS runs one operator ctrlproto connection.
//
// It is deliberately NOT serveWS with a different service. serveWS builds a
// workspace hello, advertises carrier features (uploads, shared files, stage)
// and hands ServeConn a WorkspaceService; none of that exists here, and reusing
// it would mean a supervisor connection whose surface is "everything, minus the
// bits someone remembered to remove".
func serveSupervisorWS(ctx context.Context, ctl ctrlproto.TenantsController, opts Options, w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	c.SetReadLimit(maxFrameBytes)
	who := clientDesc(opts, r)
	fmt.Fprintf(os.Stderr, "terva serve: operator connected to the supervisor panel — %s\n", who)
	connCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		fmt.Fprintf(os.Stderr, "terva serve: operator disconnected from the supervisor panel — %s\n", who)
	}()
	go func() {
		<-connCtx.Done()
		_ = c.Close()
	}()
	conn := &wsConn{c: c}
	conn.armReadDeadline()
	go conn.keepalive(connCtx)

	hello := tenant.AdminHello("terva serve", opts.Version)
	hello.Locale = opts.Locale
	// The capability mask is this principal's, not the owner's by assumption:
	// a role that ever holds the group read-only gets the listing and not the
	// lever, without another branch here.
	_ = tenant.ServeAdmin(connCtx, conn, ctl, hello, authz.Authority(principal))
}

// supervisorTmpl is the operator page: one self-contained document with no
// script, for login.go's reason run one step further.
//
// A page that depended on the bundled PWA could not be shown on a supervisor
// whose asset pipeline was the thing that broke — and unlike the tenant panel,
// this surface's whole job is to be readable when the rest is not. The style is
// inlined under a per-response nonce so a strict style-src survives.
//
// Prose is English rather than i18n.T, matching the rest of `terva serve`: its
// operator output is stderr in English, and a panel that spoke a translated
// dialect of those same messages would be harder to search for, not easier.
var supervisorTmpl = template.Must(template.New("supervisor").Funcs(template.FuncMap{
	"ago": humanAgo,
	"at":  func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>terva serve — environments</title>
<style nonce="{{.Nonce}}">
  :root { color-scheme: light dark; }
  body { margin: 0; padding: 2rem 1.25rem 4rem;
         font: 15px/1.55 ui-sans-serif, system-ui, sans-serif;
         background: #fbfbfa; color: #1c1b1a; }
  main { max-width: 62rem; margin: 0 auto; }
  h1 { font-size: 1.15rem; font-weight: 600; margin: 0 0 .25rem; }
  h2 { font-size: .9rem; font-weight: 600; margin: 2rem 0 .5rem; }
  .sub { color: #6b6a67; font-size: .8125rem; margin: 0 0 1.5rem; }
  .banner { border-radius: .375rem; padding: .625rem .75rem; font-size: .8125rem;
            margin: 0 0 1.25rem; border: 1px solid #d6d3d1; background: #fff; }
  .banner.warn { border-color: #b45309; background: #fffbeb; color: #78350f; }
  .wrap { overflow-x: auto; }
  table { border-collapse: collapse; width: 100%; font-size: .8125rem; }
  th, td { text-align: left; padding: .5rem .625rem; border-bottom: 1px solid #e7e5e4;
           vertical-align: top; white-space: nowrap; }
  th { font-weight: 600; color: #6b6a67; font-size: .75rem; text-transform: uppercase;
       letter-spacing: .04em; }
  td.wrap-any { white-space: normal; }
  code { font: 12px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; color: #6b6a67; }
  .tag { display: inline-block; border-radius: .25rem; padding: .0625rem .375rem;
         font-size: .6875rem; font-weight: 600; border: 1px solid #d6d3d1; }
  .tag.run { border-color: #15803d; color: #15803d; }
  .tag.off { border-color: #a8a29e; color: #78716c; }
  .tag.susp { border-color: #b91c1c; color: #b91c1c; }
  .note { color: #b45309; white-space: normal; display: inline-block; max-width: 30rem; }
  button { font: inherit; font-size: .75rem; border: 1px solid #d6d3d1; border-radius: .25rem;
           padding: .1875rem .5rem; background: #fff; color: inherit; cursor: pointer; }
  button:hover { border-color: #a8a29e; }
  .empty { color: #6b6a67; font-size: .8125rem; margin: .25rem 0 0; }
  footer { margin-top: 2.5rem; color: #6b6a67; font-size: .75rem; }
  @media (prefers-color-scheme: dark) {
    body { background: #1c1b1a; color: #e7e5e4; }
    .sub, th, code, .empty, footer { color: #a8a29e; }
    .banner { border-color: #44403c; background: #292725; }
    .banner.warn { border-color: #b45309; background: #2a2010; color: #fcd34d; }
    th, td { border-bottom-color: #383431; }
    button { background: #292725; border-color: #44403c; }
    .tag.run { color: #4ade80; border-color: #166534; }
    .tag.off { color: #a8a29e; border-color: #57534e; }
    .tag.susp { color: #f87171; border-color: #7f1d1d; }
  }
</style>
</head>
<body>
<main>
  <h1>Environments</h1>
  <p class="sub">One terva per person on this host. {{.Signed}}</p>

  <p class="banner{{if not .Data.Containment.Isolates}} warn{{end}}">
    Containment — {{.Data.Containment.Describe}}
    {{if not .Data.Containment.Isolates}}<br><strong>This host carries one tenant.</strong>
    A second is refused at start, because these children would share a uid and could
    read each other's homes. Pass <code>--containment systemd</code> for per-tenant uids.{{end}}
  </p>

  {{if .Err}}<p class="banner warn">{{.Err}}</p>{{end}}

  <h2>Enrolled</h2>
  {{if .Data.Tenants}}
  <div class="wrap">
  <table>
    <thead><tr>
      <th>Person</th><th>Environment</th><th>State</th><th>Enrolled</th><th>Last seen</th><th></th>
    </tr></thead>
    <tbody>
    {{range .Data.Tenants}}
      <tr>
        <td>{{if .Display}}{{.Display}}<br>{{end}}<code>{{.Subject}}</code></td>
        <td><code>{{.ID}}</code>{{if .Home}}<br><code>{{.Home}}</code>{{end}}</td>
        <td>
          {{if .Suspended}}<span class="tag susp">suspended</span>
          {{else if .Running}}<span class="tag run">running</span>
          {{else if .LastStartError}}<span class="tag susp">will not start</span>
          {{else}}<span class="tag off">stopped</span>{{end}}
          {{if .UnentitledSince}}<br><span class="note">no role since {{at .UnentitledSince}}</span>{{end}}
          {{if .LastStartError}}<br><span class="note">{{ago .LastStartErrorAt}}: {{.LastStartError}}</span>{{end}}
        </td>
        <td>{{at .EnrolledAt}}</td>
        <td>{{ago .LastSeenAt}}</td>
        <td>
          <form method="post" action="{{$.Action}}">
            <input type="hidden" name="id" value="{{.ID}}">
            <button type="submit" name="do" value="{{if .Suspended}}resume{{else}}suspend{{end}}">
              {{if .Suspended}}Resume{{else}}Suspend{{end}}
            </button>
          </form>
        </td>
      </tr>
    {{end}}
    </tbody>
  </table>
  </div>
  <p class="empty">Suspending stops the environment and refuses to start it again. It deletes
    nothing — the home, its sessions and the enrolment all stay. <strong>Stopped</strong> is
    normal: an idle environment is stopped and restarts on its owner's next request.
    <strong>Will not start</strong> is not — it means the last attempt failed, and the reason
    beside it is the one nothing else records.</p>
  {{else}}
  <p class="empty">Nobody is enrolled yet. The first person to sign in carrying a mapped role
    gets an environment.</p>
  {{end}}

  <h2>Refused</h2>
  {{if .Data.Refusals}}
  <div class="wrap">
  <table>
    <thead><tr><th>Person</th><th>Via</th><th>Why</th><th>Last</th><th>Times</th></tr></thead>
    <tbody>
    {{range .Data.Refusals}}
      <tr>
        <td>{{if .Display}}{{.Display}}<br>{{end}}<code>{{.Subject}}</code></td>
        <td>{{.Source}}</td>
        <td class="wrap-any">{{.Reason}}</td>
        <td>{{ago .LastAt}}</td>
        <td>{{.Count}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  </div>
  <p class="empty">These people authenticated and were turned away. Recorded in memory only —
    this list starts empty on every restart, and nothing acts on it.</p>
  {{else}}
  <p class="empty">Nobody has been turned away since this supervisor started.</p>
  {{end}}

  <footer>
    Roles this daemon understands: {{.Roles}}. An environment is granted on a matching role,
    not on a successful sign-in.
  </footer>
</main>
</body>
</html>
`))

// humanAgo renders a timestamp as elapsed time, because "last seen" is a
// question about recency and an operator should not have to subtract dates in
// their head to answer it. A zero time means never.
func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// handleSupervisorPage serves the operator page and its two actions.
//
// POST-redirect-GET so a browser reload does not re-fire a suspension, and
// same-origin checked so a page on another site cannot post one. The session
// cookie is already SameSite=Strict, which is the primary defence; the origin
// check is the belt to that suspenders, because the forward-auth mode has no
// terva cookie at all and its credential IS ambient.
func handleSupervisorPage(ctl ctrlproto.TenantsController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var errMsg string
		if r.Method == http.MethodPost {
			if !sameOrigin(r, "supervisor action") {
				http.Error(w, "cross-origin request", http.StatusForbidden)
				return
			}
			errMsg = applySupervisorAction(r, ctl)
			if errMsg == "" {
				http.Redirect(w, r, supervisorPath, http.StatusSeeOther)
				return
			}
		} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		data, err := ctl.TenantsList(r.Context())
		if err != nil {
			// The registry is the one thing this page cannot render without,
			// and a malformed one is exactly the state an operator opened the
			// page to find out about. So the error is the page's content, not a
			// blank 500.
			fmt.Fprintf(os.Stderr, "terva serve: the supervisor panel could not read the registry: %v\n", err)
			http.Error(w, "the tenant registry could not be read: "+err.Error(), http.StatusInternalServerError)
			return
		}

		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		n := base64.RawStdEncoding.EncodeToString(nonce)

		h := w.Header()
		h.Set("Content-Security-Policy", loginCSP(n))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// no-store because the page lists who is signed in and where their data
		// is. Nothing between here and the operator should keep a copy.
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Type", "text/html; charset=utf-8")

		signed := "Nobody is signed in." // replaced below when we know who
		if p, ok := PrincipalFrom(r); ok {
			signed = "Signed in as " + firstNonEmpty(p.Display, p.Subject) + "."
		}
		_ = supervisorTmpl.Execute(w, struct {
			Nonce, Err, Action, Roles, Signed string
			Data                              ctrlproto.TenantsListResult
		}{
			Nonce:  n,
			Err:    errMsg,
			Action: supervisorPath,
			Roles:  strings.Join(data.Roles, ", "),
			Signed: signed,
			Data:   data,
		})
	}
}

// applySupervisorAction performs one posted action and returns a message to
// render, or "" when it worked.
//
// Two named actions rather than a checkbox whose absence means resume: an HTML
// form omits an unchecked box entirely, so "suspended=false" and "the field did
// not arrive" are the same request — and the failure would silently un-suspend
// someone. The wire verbs are split for the same reason.
func applySupervisorAction(r *http.Request, ctl ctrlproto.TenantsController) string {
	if err := r.ParseForm(); err != nil {
		return "Malformed submission."
	}
	id := strings.TrimSpace(r.PostFormValue("id"))
	who := "unknown"
	if p, ok := PrincipalFrom(r); ok {
		who = p.Subject
	}
	var (
		err        error
		did, doing string
	)
	switch action := r.PostFormValue("do"); action {
	case "suspend":
		err, did, doing = ctl.TenantsSuspend(r.Context(), ctrlproto.TenantRef{ID: id}), "suspended", action
	case "resume":
		err, did, doing = ctl.TenantsResume(r.Context(), ctrlproto.TenantRef{ID: id}), "resumed", action
	default:
		return "Unknown action."
	}
	// Logged AFTER, and only on success. The first cut of this announced the
	// act in the past tense BEFORE performing it, so a failure wrote a pair of
	// contradictory lines and an operator scanning the log would meet the
	// reassuring one first.
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva serve: %s could not %s environment %s: %v\n", who, doing, id, err)
		return err.Error()
	}
	fmt.Fprintf(os.Stderr, "terva serve: %s %s environment %s\n", who, did, id)
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
