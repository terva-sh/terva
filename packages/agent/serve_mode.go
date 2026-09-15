//go:build terva_web

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tenant"
	"terva.sh/terva/packages/agent/web"
	"terva.sh/terva/packages/i18n"
)

// runServeMode runs the multi-tenant supervisor.
//
// It is the sibling of runWebMode and deliberately much smaller, because the
// thing it supervises is runWebMode: a tenant's environment is today's daemon
// with a different TERVA_HOME, so nothing here builds a workspace, resolves a
// credential, or starts an agent. Every isolation property comes from the
// supervisor and the OS.
//
// SIGINT/SIGTERM cancel the context, which drains the listener and then brings
// every child down — a child holds live sessions, so it is signalled rather
// than killed (see Supervisor.StopAll).
func runServeMode(ctx context.Context, args build.Args, version string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := args.TenantRoot
	if root == "" {
		root = filepath.Join(config.TervaHome(), "tenants")
	}
	runDir := args.TenantRunDir
	if runDir == "" {
		runDir = defaultTenantRunDir()
	}

	containment, err := resolveContainment(args)
	if err != nil {
		return err
	}
	sup, err := tenant.NewSupervisor(tenant.SupervisorOptions{
		Root:        root,
		RunDir:      runDir,
		Containment: containment,
	})
	if err != nil {
		return err
	}
	defer sup.StopAll()

	store := tenant.NewStoreIn(config.TervaHome())
	panel := tenant.NewPanel(store, sup)

	trustedProxies, err := web.ParseTrustedProxies(args.WebTrustedProxies)
	if err != nil {
		return err
	}
	insecureCIDRs, err := web.ParseTrustedProxies(args.WebInsecureCIDRs)
	if err != nil {
		return fmt.Errorf("--web-insecure-cidr: %w", err)
	}

	// Discovery at startup, for runWebMode's reason: an unreachable provider
	// should stop the supervisor with a clear message rather than wait to
	// surprise the first person who tries to sign in. It matters more here —
	// without an identity provider a supervisor has nothing to tell callers
	// apart by, and ServeTenants refuses to start.
	oidcProvider, oidcState, err := resolveWebOIDC(ctx)
	if err != nil {
		return err
	}

	// Stage is the operator's decision about the HOST, and it is only half the
	// question — the other half belongs to each tenant and is deliberately not
	// asked here.
	//
	// This flag mounts /stage/ (the static app; see newSupervisorMux) and lets a
	// child's `stage` feature reach a browser at all. Whether a given tenant is
	// OFFERED it is their own web_stage, read by their own daemon from their own
	// home — which is the only place it could be read from, since the containment
	// that separates tenants is exactly what stops the supervisor looking inside
	// one. So the two answers compose: the operator says whether the surface
	// exists here, the tenant says whether they want it.
	//
	// Flag OR config knob, matching runWebMode: a deployment enables Stage
	// without a launch flag. Read once at start, like the child's.
	cfg, _ := config.LoadConfig()
	allowStage := args.WebStage || cfg.WebStage

	opts := web.Options{
		Addr:           args.WebAddr,
		OIDCProvider:   oidcProvider,
		OIDCState:      oidcState,
		AuthHeader:     args.WebAuthHeader,
		TrustedProxies: trustedProxies,
		Token:          args.WebToken,
		AllowInsecure:  args.WebInsecure,
		InsecureCIDRs:  insecureCIDRs,
		AllowStage:     allowStage,
		Version:        version,
		Locale:         i18n.ActiveLang(),
	}
	// Asked before anything is announced. A daemon that prints where its
	// tenants live and only then refuses to start reads like one that came up
	// and crashed; ServeTenants asks again, so this is an ordering courtesy
	// rather than the enforcement.
	if err := web.CheckSupervisorAuth(opts); err != nil {
		return err
	}

	// Reaping an idle child is cheap and reversible — the next request starts a
	// fresh daemon over the same home — so it is on by default. Deleting a home
	// is neither, and is not built at all.
	idle := args.TenantIdleTimeout
	switch {
	case idle < 0:
		idle = 0 // --tenant-idle-timeout off
	case idle == 0:
		idle = defaultTenantIdle
	}
	if idle > 0 {
		go sup.RunReaper(ctx, idle, 0)
	}

	fmt.Fprintf(os.Stderr, "terva serve: tenant homes under %s, sockets under %s\n", root, runDir)
	if idle > 0 {
		fmt.Fprintf(os.Stderr, "terva serve: an environment with no connection for %s is stopped and restarts on the next request (its data is untouched)\n", idle)
	} else {
		fmt.Fprintln(os.Stderr, "terva serve: idle environments are never stopped (--tenant-idle-timeout off)")
	}
	fmt.Fprintf(os.Stderr, "terva serve: containment — %s\n", containment.Describe())
	if !containment.Isolates() {
		fmt.Fprintln(os.Stderr, "terva serve: this host carries ONE tenant; a second is refused. Pass --containment systemd for per-tenant uids (see examples/deploy/systemd/)")
	}
	fmt.Fprintln(os.Stderr, "terva serve: the operator panel is at /supervisor (owner role required)")
	if allowStage {
		fmt.Fprintln(os.Stderr, "terva serve: Stage is mounted at /stage/ — each tenant is offered it only if their own web_stage is on")
	} else {
		fmt.Fprintln(os.Stderr, "terva serve: Stage is not served here (--web-stage to mount it); a tenant enabling web_stage is not offered a link to it")
	}

	return web.ServeTenants(ctx, resolveTenant(sup, store, panel), panel, opts)
}

// resolveTenant maps an authenticated principal to its running environment.
//
// 🚨 Enrolment is gated on a ROLE, not on authentication. D7 is explicit that a
// successful sign-in carrying no matching role is authenticated-but-
// unprovisioned, and the refusal names what is missing rather than denying
// generically — an operator debugging "why can't Ada get in" should not have to
// guess. A caller that handed every successful login to Enrol would have
// answered "is this person entitled to an environment" with "did they log in".
func resolveTenant(sup *tenant.Supervisor, store *tenant.Store, panel *tenant.Panel) web.TenantResolver {
	return func(ctx context.Context, p authz.Principal) (*tenant.Child, error) {
		if p.Subject == "" {
			return nil, errors.New("the identity provider returned no subject, so there is nothing stable to key an environment on")
		}
		if len(p.Roles) == 0 {
			// The refusal names what would fix it. D7: "the refusal should name
			// the role that is missing rather than denying generically, or
			// every enrolment problem becomes a support conversation."
			//
			// The observation is recorded as evidence and acts on nothing —
			// see Store.NoteUnentitled. A revoked role and a mistyped group
			// mapping are identical from here.
			if err := store.NoteUnentitled(p.Subject, time.Now()); err != nil {
				fmt.Fprintf(os.Stderr, "terva serve: could not record the unentitled sign-in for %s: %v\n", p.Subject, err)
			}
			err := fmt.Errorf("%s authenticated but carries none of this daemon's roles (%s) — map one of their identity-provider groups to a role in web_oidc.role_map",
				p.Subject, strings.Join(authz.RoleNames(), ", "))
			// 🔑 And in the panel, which is the ONLY place this becomes visible
			// for someone who was never enrolled. NoteUnentitled above can only
			// annotate an existing record, and deliberately creates none — so
			// without this the commonest enrolment problem, a brand-new user
			// whose groups nobody mapped, leaves no trace anywhere at all.
			panel.NoteRefusal(p, err.Error(), time.Now())
			return nil, err
		}

		// They carry a role, so any refusal recorded for them is answering a
		// question that has since changed. Leaving it would show an operator a
		// live-looking enrolment problem that is already fixed — the same
		// reason ClearUnentitled exists for the durable half.
		panel.ForgetRefusal(p.Subject)

		// Read first, and write only when there is something to write. Enrol
		// takes the registry's file lock and rewrites the whole file, so
		// calling it per request made a bookkeeping field cost a serialised
		// write on every /media/ fetch.
		rec, err := store.Lookup(p.Subject)
		switch {
		case errors.Is(err, tenant.ErrNotEnrolled):
			var created bool
			rec, created, err = store.Enrol(p.Subject, p.Display)
			if err != nil {
				return nil, err
			}
			if created {
				fmt.Fprintf(os.Stderr, "terva serve: enrolled a new environment %s for %s\n", rec.ID, p.Subject)
			}
		case err != nil:
			return nil, err
		default:
			if rec.Stale(time.Now()) {
				if err := store.Touch(rec.ID); err != nil {
					// Bookkeeping must not fail a live session.
					fmt.Fprintf(os.Stderr, "terva serve: could not stamp last-seen for %s: %v\n", rec.ID, err)
				}
			}
			if rec.UnentitledSince != nil {
				// They are back with a role, so the evidence is stale and
				// keeping it would misinform whoever reads the panel later.
				_ = store.ClearUnentitled(rec.ID)
			}
		}
		return sup.Start(ctx, rec)
	}
}

// defaultTenantRunDir picks somewhere short for the per-tenant sockets.
//
// Short is not a preference: a unix socket path is capped at ~104 bytes by the
// kernel, and a tenant id spends 34 of them. XDG_RUNTIME_DIR (/run/user/$UID on
// a systemd host) is both the correct home for runtime state and short enough;
// the fallback under TERVA_HOME is neither, but it is somewhere the supervisor
// can definitely write, and tenant.Start reports the budget clearly if it does
// not fit.
func defaultTenantRunDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "terva-tenants")
	}
	return filepath.Join(config.TervaHome(), "run")
}

// resolveContainment turns --containment into the thing that starts children.
//
// The default is the honest one, not the capable one: SameUser reports that it
// does not isolate, so a supervisor that was never configured carries exactly
// one tenant and refuses the second by name. Defaulting to systemd would be
// worse in both directions — it would fail on a host without it, and on a host
// with it, it would silently depend on unit files nobody had been asked to
// install.
func resolveContainment(args build.Args) (tenant.Containment, error) {
	switch strings.TrimSpace(args.Containment) {
	case "", "none":
		return tenant.SameUser{}, nil
	case "systemd":
		template := strings.TrimSpace(args.ContainmentTemplate)
		if template == "" {
			template = "terva-tenant"
		}
		return tenant.NewSystemdUnit(template)
	default:
		return nil, fmt.Errorf("--containment %q: want \"none\" or \"systemd\"", args.Containment)
	}
}

// defaultTenantIdle is how long a tenant's daemon stays up with nobody attached.
//
// Long enough that stepping away from the browser does not cost a cold start on
// return, short enough that a person who signed in once last week is not still
// holding a process. The cost of getting it wrong is a slow first response, not
// lost work — which is the whole reason reaping a child is allowed to be a
// default and deleting a home is not.
const defaultTenantIdle = 30 * time.Minute
