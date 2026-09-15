package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// newTestPanel builds a panel over a real registry and a real supervisor, so
// every assertion below is about the thing that ships rather than a double.
func newTestPanel(t *testing.T, c Containment) (*Panel, *Store, *Supervisor) {
	t.Helper()
	store := newTestStore(t)
	sup := newTestSupervisor(t, c)
	return NewPanel(store, sup), store, sup
}

func enrolIn(t *testing.T, s *Store, subject, display string) Record {
	t.Helper()
	rec, _, err := s.Enrol(subject, display)
	if err != nil {
		t.Fatalf("enrol %s: %v", subject, err)
	}
	return rec
}

func listOne(t *testing.T, p *Panel, id string) ctrlproto.TenantInfo {
	t.Helper()
	res, err := p.TenantsList(context.Background())
	if err != nil {
		t.Fatalf("TenantsList: %v", err)
	}
	for _, info := range res.Tenants {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("%s is not in the listing (%d rows)", id, len(res.Tenants))
	return ctrlproto.TenantInfo{}
}

// The listing joins the two halves the supervisor deliberately keeps apart: the
// durable registry and the processes that happen to be up.
func TestTheListingReportsWhatIsEnrolledAndWhatIsRunning(t *testing.T) {
	skipOnWindows(t)
	p, store, sup := newTestPanel(t, isolating{})

	ada := enrolIn(t, store, "sub-ada", "Ada")
	bob := enrolIn(t, store, "sub-bob", "Bob")
	if _, err := sup.Start(context.Background(), ada); err != nil {
		t.Fatalf("start ada: %v", err)
	}

	if got := listOne(t, p, ada.ID); !got.Running || got.Display != "Ada" || got.Subject != "sub-ada" {
		t.Errorf("ada's row = %+v", got)
	}
	// 🔑 The row that matters most: stopped is the NORMAL state for an idle
	// environment, and it must still be listed. A panel that showed only live
	// children would hide everyone who had gone home for the day.
	if got := listOne(t, p, bob.ID); got.Running {
		t.Errorf("bob has no child but reports running: %+v", got)
	}
}

// The home is what the containment ACTUALLY used, so it is reported only where
// there is something to have asked. A path the supervisor merely proposed is one
// an operator could `ls` and find empty.
func TestOnlyARunningEnvironmentReportsAHome(t *testing.T) {
	skipOnWindows(t)
	p, store, sup := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")

	if got := listOne(t, p, rec.ID); got.Home != "" {
		t.Errorf("a stopped environment reported home %q", got.Home)
	}

	child, err := sup.Start(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	got := listOne(t, p, rec.ID)
	if got.Home != child.Home {
		t.Errorf("running home = %q, want the child's own %q", got.Home, child.Home)
	}
	if got.Home == "" {
		t.Error("a running environment reported no home at all")
	}
}

// 🚨 An environment that is idle and one that dies on every start both read
// "not running", and they need opposite responses from an operator. Found by
// running the binary against a child that exited on a missing credential: the
// panel called it "stopped", which is what a healthy idle environment is called.
func TestAnEnvironmentThatWillNotStartIsNotJustStopped(t *testing.T) {
	skipOnWindows(t)
	p, store, sup := newTestPanel(t, refusing{})
	rec := enrolIn(t, store, "sub-ada", "Ada")

	// The healthy resting state first: nobody has asked, so there is nothing to
	// report. This is the control — without it, a panel that reported a failure
	// for every row would pass the assertion below.
	if got := listOne(t, p, rec.ID); got.LastStartError != "" {
		t.Fatalf("CONTROL: an environment nobody has asked for reports a failure: %q", got.LastStartError)
	}

	if _, err := sup.Start(context.Background(), rec); err == nil {
		t.Fatal("the refusing containment started a child")
	}
	got := listOne(t, p, rec.ID)
	if got.Running {
		t.Fatal("a child that would not start is reported as running")
	}
	if !strings.Contains(got.LastStartError, "this host has no room") {
		t.Errorf("the row does not carry why it would not start: %q", got.LastStartError)
	}
	if got.LastStartErrorAt == nil {
		t.Error("the failure has no timestamp, so an operator cannot tell a live problem from an old one")
	}
}

// ...and a failure is history once the environment comes up, or an operator
// would chase a problem that fixed itself.
func TestAStartFailureIsForgottenOnceItStarts(t *testing.T) {
	skipOnWindows(t)
	p, store, sup := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")

	// A real failure first: the containment refuses a second tenant on a
	// non-isolating host. Here we fake one directly, because what is under test
	// is the clearing, not the recording.
	sup.noteFailure(rec.ID, errors.New("something was wrong once"))
	if got := listOne(t, p, rec.ID); got.LastStartError == "" {
		t.Fatal("CONTROL: the failure was not recorded, so clearing it proves nothing")
	}

	if _, err := sup.Start(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := listOne(t, p, rec.ID); got.LastStartError != "" {
		t.Errorf("a running environment still reports %q", got.LastStartError)
	}
}

// refusing is a Containment that claims to isolate and refuses to start
// anything — the shape of a host whose unit template is missing, or whose
// children die immediately.
type refusing struct{ SameUser }

func (refusing) Describe() string { return "test double (refuses to start)" }
func (refusing) Isolates() bool   { return true }
func (refusing) Start(context.Context, Spec) (*Child, error) {
	return nil, errors.New("tenant: this host has no room for another environment")
}

// "Is this host actually separating these people" is the first question a list
// of environments raises, and until this shipped the only answer was one line of
// startup logging that had long since scrolled away.
func TestTheListingSaysWhatTheHostSeparates(t *testing.T) {
	skipOnWindows(t)

	t.Run("a containment that does not isolate says so", func(t *testing.T) {
		p, _, _ := newTestPanel(t, SameUser{})
		res, err := p.TenantsList(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Containment.Isolates {
			t.Error("SameUser reported that it isolates")
		}
		if !strings.Contains(res.Containment.Describe, "share") {
			t.Errorf("the description does not say what is shared: %q", res.Containment.Describe)
		}
	})

	t.Run("CONTROL: one that does is reported as such", func(t *testing.T) {
		p, _, _ := newTestPanel(t, isolating{})
		res, err := p.TenantsList(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Containment.Isolates {
			t.Error("an isolating containment was reported as not isolating")
		}
	})
}

// 🚨 A suspension that waits for the next idle reap is not a suspension. Both
// halves have to land: the durable flag and the running process.
func TestSuspendingStopsTheRunningEnvironment(t *testing.T) {
	skipOnWindows(t)
	p, store, sup := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")

	child, err := sup.Start(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.TenantsSuspend(context.Background(), ctrlproto.TenantRef{ID: rec.ID}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if child.Alive() {
		t.Error("the child was still running after its environment was suspended")
	}
	if got := listOne(t, p, rec.ID); !got.Suspended || got.Running {
		t.Errorf("after suspend the row is %+v", got)
	}

	// And it stays down. The supervisor refuses from its OWN state here, not
	// from the record — a caller holding a record read a moment earlier must
	// not be able to start it again.
	stale := rec // suspended=false, exactly as an in-flight request would hold it
	if _, err := sup.Start(context.Background(), stale); err == nil {
		t.Fatal("a stale record restarted a suspended environment")
	}
}

// Resuming is the exact inverse, and the environment comes back to the data it
// always had. Suspension destroys nothing — that is the whole posture.
func TestResumingBringsTheEnvironmentBack(t *testing.T) {
	skipOnWindows(t)
	p, store, sup := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")

	if _, err := sup.Start(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := p.TenantsSuspend(context.Background(), ctrlproto.TenantRef{ID: rec.ID}); err != nil {
		t.Fatal(err)
	}
	if err := p.TenantsResume(context.Background(), ctrlproto.TenantRef{ID: rec.ID}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := listOne(t, p, rec.ID); got.Suspended {
		t.Error("the registry still calls it suspended after a resume")
	}
	// The record the supervisor is handed comes from the registry, which now
	// says it is not suspended — the same read the request path does.
	fresh, err := store.Lookup("sub-ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Start(context.Background(), fresh); err != nil {
		t.Fatalf("a resumed environment would not start: %v", err)
	}
}

// The id names a directory, a socket and a unit instance, so it is refused at
// every boundary rather than trusted because some earlier one checked it.
func TestAMalformedIDIsRefusedOnTheWire(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})

	for _, id := range []string{"", "../../etc", "t-nothex0000000z", "t-abc"} {
		err := p.TenantsSuspend(context.Background(), ctrlproto.TenantRef{ID: id})
		if err == nil {
			t.Fatalf("suspend accepted the id %q", id)
		}
		var ce *ctrlproto.Error
		if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
			t.Errorf("%q was refused with %v, want a bad-request wire error", id, err)
		}
	}
}

// An id that is well-formed but names nobody must not read as success — an
// operator suspending a typo'd id would otherwise believe they had.
func TestSuspendingAnUnknownEnvironmentIsAnError(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})

	err := p.TenantsSuspend(context.Background(), ctrlproto.TenantRef{ID: "t-" + strings.Repeat("a", 16)})
	if err == nil {
		t.Fatal("suspending an environment that does not exist reported success")
	}
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeNotFound {
		t.Errorf("refused with %v, want not-found", err)
	}
}

// D8: "an authenticated user with no environment is invisible otherwise." The
// registry cannot cover this case — a person nobody mapped has no record to
// annotate, and creating one would enrol them by the back door.
func TestARefusedNewcomerIsVisibleToTheOperator(t *testing.T) {
	skipOnWindows(t)
	p, store, _ := newTestPanel(t, isolating{})

	who := authz.Principal{Subject: "sub-carol", Display: "Carol", Source: authz.SourceOIDC}
	p.NoteRefusal(who, "carries none of this daemon's roles", time.Now())

	res, err := p.TenantsList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != 1 {
		t.Fatalf("%d refusals recorded, want 1", len(res.Refusals))
	}
	got := res.Refusals[0]
	if got.Subject != "sub-carol" || got.Display != "Carol" || got.Source != "oidc" || got.Count != 1 {
		t.Errorf("refusal row = %+v", got)
	}
	if !strings.Contains(got.Reason, "roles") {
		t.Errorf("the row does not say why: %q", got.Reason)
	}

	// The control that makes the test meaningful: Carol was NOT enrolled by
	// being noticed. Recording the problem must not create the environment.
	if _, err := store.Lookup("sub-carol"); err == nil {
		t.Fatal("noting a refusal enrolled the subject")
	}
}

// A browser retrying every few seconds must collapse into one row, or the one
// person an operator is looking for is pushed off the list by the one who is
// already reconnecting.
func TestRepeatedRefusalsCollapseIntoACount(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})

	who := authz.Principal{Subject: "sub-carol", Source: authz.SourceOIDC}
	base := time.Now()
	for i := range 5 {
		p.NoteRefusal(who, "no role", base.Add(time.Duration(i)*time.Second))
	}
	res, _ := p.TenantsList(context.Background())
	if len(res.Refusals) != 1 {
		t.Fatalf("%d rows for one subject", len(res.Refusals))
	}
	if res.Refusals[0].Count != 5 {
		t.Errorf("count = %d, want 5", res.Refusals[0].Count)
	}
}

// The subjects come from an identity provider, so the log is bounded: a
// misconfigured one can mint as many distinct `sub` values as it likes.
func TestTheRefusalLogIsBoundedAndKeepsTheNewest(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})

	base := time.Now()
	for i := range maxRefusals + 20 {
		p.NoteRefusal(
			authz.Principal{Subject: fmt.Sprintf("sub-%03d", i), Source: authz.SourceOIDC},
			"no role", base.Add(time.Duration(i)*time.Second))
	}
	res, _ := p.TenantsList(context.Background())
	if len(res.Refusals) > maxRefusals {
		t.Fatalf("%d rows retained, cap is %d", len(res.Refusals), maxRefusals)
	}
	// Newest first, and the newest subject survived the eviction.
	if res.Refusals[0].Subject != fmt.Sprintf("sub-%03d", maxRefusals+19) {
		t.Errorf("the newest refusal is not first: %q", res.Refusals[0].Subject)
	}
	for i := 1; i < len(res.Refusals); i++ {
		if res.Refusals[i].LastAt.After(res.Refusals[i-1].LastAt) {
			t.Fatalf("row %d is newer than the one above it", i)
		}
	}
}

// Someone who was let in since must stop showing as a live problem.
func TestARefusalIsForgottenWhenTheyGetIn(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})

	p.NoteRefusal(authz.Principal{Subject: "sub-carol"}, "no role", time.Now())
	p.ForgetRefusal("sub-carol")
	res, _ := p.TenantsList(context.Background())
	if len(res.Refusals) != 0 {
		t.Errorf("%d refusals after the subject was admitted", len(res.Refusals))
	}
}

// The panel names the roles a claim would have to map to, read from the
// authority table rather than retyped — so a role that exists is a role the
// operator is told about.
func TestTheListingNamesEveryRoleFromTheAuthorityTable(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})
	res, _ := p.TenantsList(context.Background())
	if len(res.Roles) != len(authz.RoleNames()) {
		t.Fatalf("roles = %v, want %v", res.Roles, authz.RoleNames())
	}
}
