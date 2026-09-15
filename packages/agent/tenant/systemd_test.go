package tenant

import (
	"context"
	"strings"
	"testing"
)

// fakeSystemctl answers `systemctl show -p <prop>` from a table and records
// every invocation. It stands in for the manager so the DECISIONS can be tested
// anywhere; the mechanics are verified against real systemd on Linux.
//
// 🪤 Its answers are TRANSCRIBED from a real systemd 255, not invented. The
// first version of this file made up the Listen format ("Stream /path") and the
// parser was written to match — so the test passed while the real thing would
// have returned "(Stream)" as the socket path. A fake that encodes the same
// assumption as the code under test asserts nothing.
type fakeSystemctl struct {
	props map[string]string
	calls []string
	fail  map[string]bool
}

func (f *fakeSystemctl) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.fail[args[0]] {
		return "Failed to start unit.", errRun
	}
	if len(args) >= 4 && args[0] == "show" && args[1] == "-p" {
		return f.props[args[2]], nil
	}
	return "", nil
}

var errRun = &runError{}

type runError struct{}

func (*runError) Error() string { return "exit status 1" }

func newFakeUnit(props map[string]string) (*SystemdUnit, *fakeSystemctl) {
	f := &fakeSystemctl{props: props, fail: map[string]bool{}}
	return &SystemdUnit{Template: "terva-tenant", run: f.run}, f
}

const testID = "t-0123456789abcdef"

// The step-4 finding, enforced. systemd names a DynamicUser after %p — the
// TEMPLATE — so the obvious unit runs every tenant as one uid. A backend that
// claims Isolates() must not accept that template.
func TestATemplateThatSharesOneUIDIsRefused(t *testing.T) {
	// Each case names its own remedy, because they differ: three of these are
	// fixed by making the user name per-instance, and the fourth by making it
	// shorter. A blanket "mentions %i" check would have passed the wrong advice
	// on the last one.
	cases := []struct {
		name, user, wantIn, remedy string
	}{
		{"no User= at all", "", "sets no User=", "%i"},
		{"the %p trap", "terva-tenant", "the template prefix, not the instance", "%i"},
		{"a fixed name", "terva-tenants", "does not name this instance", "%i"},
		// A per-instance name the kernel will not accept. systemd's own failure
		// for this is "Failed to spawn 'start' task: Invalid argument", which
		// names neither the user nor the limit.
		{"too long for a login name", "a-very-long-prefix-indeed-" + testID, "login name accepts", "Shorten"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newFakeUnit(map[string]string{"User": tc.user})
			err := u.verifyPerInstanceUser(context.Background(), testID, "terva-tenant@"+testID+".service")
			if err == nil {
				t.Fatalf("a template resolving User=%q was accepted — every tenant would share its uid", tc.user)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("refused for the wrong reason: %v", err)
			}
			if !strings.Contains(err.Error(), tc.remedy) {
				t.Errorf("the refusal does not name the remedy (%q): %v", tc.remedy, err)
			}
		})
	}
}

// The control. Without it, a check that refused everything would look identical
// to one that works.
func TestAPerInstanceUserIsAccepted(t *testing.T) {
	u, _ := newFakeUnit(map[string]string{"User": "terva-t-" + testID})
	if err := u.verifyPerInstanceUser(context.Background(), testID, "svc"); err != nil {
		t.Fatalf("CONTROL: a per-instance User= must be accepted: %v", err)
	}
}

// The unit is the source of truth for its own socket, so the supervisor and the
// unit file cannot silently disagree about a path.
func TestTheSocketPathComesFromTheUnit(t *testing.T) {
	u, _ := newFakeUnit(map[string]string{"Listen": "/run/terva-t-abc.sock (Stream)"})
	got, err := u.socketPath(context.Background(), "sock")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/run/terva-t-abc.sock" {
		t.Errorf("got %q", got)
	}
}

// A TCP socket unit would leave the supervisor with nothing to dial over, and
// the tenant boundary is the socket file's permissions.
func TestATCPSocketUnitIsRefused(t *testing.T) {
	u, _ := newFakeUnit(map[string]string{"Listen": "127.0.0.1:8730 (Stream)"})
	_, err := u.socketPath(context.Background(), "terva-tenant@x.socket")
	if err == nil {
		t.Fatal("a TCP socket unit was accepted")
	}
	if !strings.Contains(err.Error(), "ListenStream=/run") {
		t.Errorf("the refusal does not name the remedy: %v", err)
	}
}

// A failed start has to name what an operator can act on: the missing unit, or
// the missing polkit grant.
func TestAFailedStartNamesTheLikelyCause(t *testing.T) {
	// A template that passes verification, so the failure under test is the
	// start itself and not something earlier.
	u, f := newFakeUnit(map[string]string{"User": "terva-" + testID, "Listen": "/run/x.sock (Stream)"})
	f.fail["start"] = true
	_, err := u.Start(context.Background(), Spec{ID: testID})
	if err == nil {
		t.Fatal("a failed systemctl start was reported as success")
	}
	for _, want := range []string{"unit template", "polkit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// A template that would share a uid must not START anything.
//
// The check runs before the unit does, because systemctl show resolves a
// template instance without running it — so the refusal leaves nothing behind
// rather than something to clean up, and there is no window in which a tenant
// is running under a uid the supervisor is about to reject.
func TestASharedUIDTemplateStartsNothing(t *testing.T) {
	u, f := newFakeUnit(map[string]string{"User": "terva-tenant"}) // the %p trap
	if _, err := u.Start(context.Background(), Spec{ID: testID}); err == nil {
		t.Fatal("the shared-uid template was accepted")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "start ") {
			t.Errorf("a tenant was started under a template that shares one uid: %v", f.calls)
		}
	}
}

// It starts the SOCKET, not the service: the service is socket-activated, so
// the supervisor never has to know whether it is already up.
func TestStartBringsUpTheSocketUnit(t *testing.T) {
	u, f := newFakeUnit(map[string]string{"User": "terva-" + testID, "Listen": "/nonexistent.sock (Stream)"})
	// Start fails at the health check (nothing is listening), which is fine —
	// what is asserted is WHICH unit it asked systemd to bring up.
	_, _ = u.Start(context.Background(), Spec{ID: testID, Timeout: 1})
	var started []string
	for _, c := range f.calls {
		if after, ok := strings.CutPrefix(c, "start "); ok {
			started = append(started, after)
		}
	}
	if len(started) != 1 || started[0] != "terva-tenant@"+testID+".socket" {
		t.Errorf("started %v, want exactly the socket unit", started)
	}
}

func TestNewSystemdUnitRefusesAnEmptyTemplate(t *testing.T) {
	if _, err := NewSystemdUnit("  "); err == nil {
		t.Fatal("an empty template was accepted")
	}
}
