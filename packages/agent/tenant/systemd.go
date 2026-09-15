package tenant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// SystemdUnit runs each tenant as an instance of a systemd template, so systemd
// holds the privilege to become another user and the supervisor never does.
//
// 🔑 This is what lets D5's hard rule stand: `terva serve` parses JWTs off the
// network and proxies websockets, so it must not hold CAP_SETUID. Here it holds
// nothing — it asks systemd, over polkit, to start one instance of a template
// the operator installed. The authority is the operator's, granted once, scoped
// to that template.
//
// The operator installs terva-tenant@.socket and terva-tenant@.service plus a
// polkit rule; see examples/deploy/systemd/. The supervisor deliberately cannot
// write those files — a component that could install its own unit could grant
// itself anything the unit may do.
type SystemdUnit struct {
	// Template is the unit template's prefix: "terva-tenant" addresses
	// terva-tenant@<id>.service and terva-tenant@<id>.socket.
	Template string

	// run executes systemctl. Injectable so the decision logic can be tested
	// without a systemd host; the real thing is verified on Linux.
	run func(ctx context.Context, args ...string) (string, error)
}

// NewSystemdUnit checks the prerequisites an operator is expected to have met.
func NewSystemdUnit(template string) (*SystemdUnit, error) {
	if strings.TrimSpace(template) == "" {
		return nil, errors.New("tenant: no systemd unit template named")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, fmt.Errorf("tenant: systemd containment needs systemctl on PATH: %w", err)
	}
	return &SystemdUnit{Template: template}, nil
}

func (s *SystemdUnit) systemctl(ctx context.Context, args ...string) (string, error) {
	if s.run != nil {
		return s.run(ctx, args...)
	}
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	return string(out), err
}

func (s *SystemdUnit) Describe() string {
	return "systemd unit template " + s.Template + "@ (one transient uid and state directory per tenant, allocated by systemd)"
}

// Isolates is true, and Start proves it rather than trusting it — see
// verifyPerInstanceUser. An unverified true here would be the worst possible
// lie: the supervisor admits a second tenant on the strength of it.
func (s *SystemdUnit) Isolates() bool { return true }

// Start brings up one tenant's units and returns the child once it answers.
func (s *SystemdUnit) Start(ctx context.Context, spec Spec) (*Child, error) {
	service := s.unit(spec.ID, "service")
	socket := s.unit(spec.ID, "socket")

	// 🚨 The claim Isolates() makes, checked BEFORE anything is started.
	// systemctl show resolves a template instance without running it, so a unit
	// that would share a uid is refused with nothing left behind to clean up.
	if err := s.verifyPerInstanceUser(ctx, spec.ID, service); err != nil {
		return nil, err
	}

	// The unit is the source of truth for its own socket path — asking removes
	// a convention the supervisor and the unit file would otherwise have to
	// agree on silently.
	path, err := s.socketPath(ctx, socket)
	if err != nil {
		return nil, err
	}

	// Starting the SOCKET, not the service: the service is socket-activated, so
	// the first dial brings it up. That also means the supervisor never has to
	// know whether it is already running.
	if out, err := s.systemctl(ctx, "start", socket); err != nil {
		return nil, fmt.Errorf("tenant: systemctl start %s: %w — is the unit template installed, and does polkit let this user manage %s@*? (%s)",
			socket, err, s.Template, strings.TrimSpace(out))
	}

	c := &Child{ID: spec.ID, Socket: path, done: make(chan struct{})}
	c.Home = s.stateDir(ctx, service)
	c.stop = func() {
		_ = s.stopUnits(context.WithoutCancel(ctx), spec.ID)
		close(c.done)
	}

	if err := waitForHealthz(ctx, path, spec.Timeout); err != nil {
		c.shutdown()
		return nil, fmt.Errorf("tenant: %s: %w", spec.ID, err)
	}
	return c, nil
}

func (s *SystemdUnit) unit(id, kind string) string {
	return s.Template + "@" + id + "." + kind
}

func (s *SystemdUnit) stopUnits(ctx context.Context, id string) error {
	_, err := s.systemctl(ctx, "stop", s.unit(id, "socket"), s.unit(id, "service"))
	return err
}

// verifyPerInstanceUser refuses a template that gives every tenant one uid.
//
// 🚨 This is step 4's finding turned into an enforcement, and it is the reason
// this backend may claim Isolates(). systemd derives a DynamicUser's name from
// `%p` — the TEMPLATE prefix — not `%i`, so the obvious
// `DynamicUser=yes` template runs every instance as ONE user. Measured on
// systemd 255: terva-tenant@9001 and terva-tenant@9002 both ran as uid 62162.
// See docs/reviews/2026-08-12-systemd-socket-activation-on-linux.md.
//
// The unit must therefore say `User=<something>-%i`. What is checked is that
// the RESOLVED user name carries the instance id, because the name is exactly
// what systemd hashes into a uid: two instances whose names differ get
// different uids, and two whose names match cannot.
func (s *SystemdUnit) verifyPerInstanceUser(ctx context.Context, id, service string) error {
	out, err := s.systemctl(ctx, "show", "-p", "User", "--value", service)
	if err != nil {
		return fmt.Errorf("tenant: could not ask systemd what user %s runs as: %w", service, err)
	}
	user := strings.TrimSpace(out)
	switch {
	case user == "":
		return fmt.Errorf("tenant: %s sets no User=, so DynamicUser names it after the template and EVERY tenant shares one uid — add `User=%s-%%i` to the unit (see docs/reviews/2026-08-12-systemd-socket-activation-on-linux.md)",
			service, s.Template)
	case user == s.Template:
		return fmt.Errorf("tenant: %s runs as %q — the template prefix, not the instance, so every tenant shares this uid. The unit needs `User=%s-%%i`; `DynamicUser=yes` alone is NOT per-instance",
			service, user, s.Template)
	case !strings.Contains(user, id):
		return fmt.Errorf("tenant: %s runs as %q, which does not name this instance — two tenants under the same user name get the same uid. The unit needs an instance-scoped `User=…-%%i`",
			service, user)
	case len(user) > MaxUserNameLen:
		// Caught here because systemd's own failure is unreadable: the service
		// dies with "Failed to spawn 'start' task: Invalid argument" and a
		// result of 'resources', which names neither the user nor the length.
		return fmt.Errorf("tenant: %s would run as %q, which is %d characters — a Linux login name accepts %d, so the unit cannot start. Shorten the unit template's User= prefix",
			service, user, len(user), MaxUserNameLen)
	}
	return nil
}

// socketPath asks the socket unit where it listens.
func (s *SystemdUnit) socketPath(ctx context.Context, socket string) (string, error) {
	out, err := s.systemctl(ctx, "show", "-p", "Listen", "--value", socket)
	if err != nil {
		return "", fmt.Errorf("tenant: could not ask systemd where %s listens: %w", socket, err)
	}
	// systemd 255 prints "<address> (<kind>)" — e.g.
	// "/run/terva-t-abc.sock (Stream)". The first draft of this parser assumed
	// the opposite order and passed its test anyway, because the fake it was
	// tested against encoded the same assumption. Take the first field that
	// looks like a path rather than trusting a position.
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	for _, f := range strings.Fields(line) {
		if strings.HasPrefix(f, "/") {
			return f, nil
		}
	}
	return "", fmt.Errorf("tenant: %s does not listen on a filesystem socket (systemd reports %q) — the supervisor dials tenants over a unix socket, so declare `ListenStream=/run/…` in the unit", socket, line)
}

// stateDir asks systemd where the unit's state directory landed, for reporting.
// Best-effort: the supervisor does not read the tenant's home, and under
// DynamicUser it could not — /var/lib/private is root-only.
func (s *SystemdUnit) stateDir(ctx context.Context, service string) string {
	out, err := s.systemctl(ctx, "show", "-p", "StateDirectory", "--value", service)
	if err != nil {
		return ""
	}
	d := strings.TrimSpace(out)
	if d == "" {
		return ""
	}
	return "/var/lib/" + strings.Fields(d)[0]
}

// waitForHealthz waits for the SERVICE to answer, not merely for the socket to
// exist.
//
// 🪤 Under socket activation the socket is systemd's and is listening from the
// moment the .socket unit starts — a bare connect succeeds instantly, before
// terva has been started at all, so the readiness check SameUser uses would
// report a child that is not there yet. The connection would then sit in the
// backlog and the first real request would block on a cold start. Asking for a
// response is the only signal that distinguishes them.
func waitForHealthz(ctx context.Context, socket string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://tenant/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("healthz answered %d", resp.StatusCode)
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("did not answer on %s within %s: %w", socket, timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
