package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// The supervisor spawns a real process, so these tests need something to spawn.
// TestMain re-execs THIS test binary with the same argv the supervisor builds
// (`web --web-addr unix:<path>`), and fakeDaemon stands in for terva web: it
// records what environment it was handed, binds the socket, and waits to be
// signalled. Nothing about the supervisor's spawn path is stubbed — it locates
// a binary, sets an environment, applies containment, starts a process and
// waits for a socket to answer, exactly as it does in production.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "web" {
		fakeDaemon()
		return
	}
	os.Exit(m.Run())
}

// observedName is where the fake daemon records what it inherited, inside the
// home it was pointed at — which is itself part of what is being asserted.
const observedName = "observed.json"

type observed struct {
	Home    string `json:"home"`
	Cwd     string `json:"cwd"`
	Token   string `json:"token"`
	ListenF string `json:"listen_fds"`
	Path    string `json:"path"`
}

func fakeDaemon() {
	var sock string
	for i, a := range os.Args {
		if a == "--web-addr" && i+1 < len(os.Args) {
			sock = strings.TrimPrefix(os.Args[i+1], "unix:")
		}
	}
	if os.Getenv("TENANT_FAKE_DIE") != "" {
		os.Exit(3)
	}
	cwd, _ := os.Getwd()
	home := os.Getenv(homeEnv)
	b, _ := json.Marshal(observed{
		Home:    home,
		Cwd:     cwd,
		Token:   os.Getenv("TERVA_WEB_TOKEN"),
		ListenF: os.Getenv("LISTEN_FDS"),
		Path:    os.Getenv("PATH"),
	})
	if home != "" {
		_ = os.WriteFile(filepath.Join(home, observedName), b, 0o600)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(4)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	_ = ln.Close()
	os.Exit(0)
}

// isolating is a Containment that claims a real boundary without applying one.
// It exists to be the must-succeed half of the refusal tests: without it, a
// refusal that fired for any other reason would read as the containment rule
// working.
type isolating struct{ SameUser }

func (isolating) Describe() string { return "test double (claims to isolate, applies nothing)" }
func (isolating) Isolates() bool   { return true }

func newTestSupervisor(t *testing.T, c Containment) *Supervisor {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := NewSupervisor(SupervisorOptions{
		Root:         filepath.Join(dir, "tenants"),
		RunDir:       testsupport.SocketDir(t),
		Containment:  c,
		Exe:          os.Args[0],
		StartTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	t.Cleanup(s.StopAll)
	return s
}

func enrol(t *testing.T, subject string) Record {
	t.Helper()
	rec, _, err := NewStoreIn(testsupport.TempDir(t)).Enrol(subject, subject)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	return rec
}

func readObserved(t *testing.T, c *Child) observed {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.Home, observedName))
	if err != nil {
		t.Fatalf("the child recorded nothing in its home: %v", err)
	}
	var o observed
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the supervisor's child carrier is a unix socket")
	}
}

// The whole mechanism in one test: the child runs somewhere else, pointed at
// its own home by the one environment variable that makes all 171 call sites of
// config.TervaHome() correct.
func TestAChildRunsInItsOwnHome(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, SameUser{})
	rec := enrol(t, "sub-a")

	c, err := s.Start(context.Background(), rec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !c.Alive() {
		t.Fatal("the child is not running")
	}
	if _, err := os.Stat(c.Socket); err != nil {
		t.Fatalf("no socket at %s: %v", c.Socket, err)
	}
	got := readObserved(t, c)
	if got.Home != c.Home {
		t.Errorf("child saw TERVA_HOME=%q, want %q", got.Home, c.Home)
	}
	if !strings.Contains(c.Home, rec.ID) {
		t.Errorf("home %q is not named for the tenant", c.Home)
	}
}

// A reconnect, or a second tab, must reach the process already serving. Two
// daemons over one home is not something anyone should reach by accident.
func TestStartIsIdempotent(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, SameUser{})
	rec := enrol(t, "sub-a")

	first, err := s.Start(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Start(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the second Start returned a different child")
	}
	if n := len(s.Running()); n != 1 {
		t.Errorf("%d children running, want 1", n)
	}
}

// The rule that makes SameUser honest. A containment that does not separate
// tenants may carry one; the second would share its uid, and could read the
// first's home and dial its socket whatever TERVA_HOME says.
func TestASecondTenantIsRefusedWhenContainmentDoesNotIsolate(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, SameUser{})

	if _, err := s.Start(context.Background(), enrol(t, "sub-a")); err != nil {
		t.Fatalf("CONTROL: the FIRST tenant must start under SameUser: %v", err)
	}
	_, err := s.Start(context.Background(), enrol(t, "sub-b"))
	if !errors.Is(err, ErrWouldShareAUID) {
		t.Fatalf("the second tenant was not refused: err=%v", err)
	}
	if !strings.Contains(err.Error(), "share its uid") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	// The other must-succeed half: the refusal has to be about containment, not
	// about there being two of anything. Under a containment that claims to
	// isolate, the same second tenant starts.
	iso := newTestSupervisor(t, isolating{})
	if _, err := iso.Start(context.Background(), enrol(t, "sub-a")); err != nil {
		t.Fatalf("CONTROL: first tenant under an isolating containment: %v", err)
	}
	if _, err := iso.Start(context.Background(), enrol(t, "sub-b")); err != nil {
		t.Fatalf("CONTROL: an isolating containment must carry a second tenant: %v", err)
	}
}

// terva hands the agent's shell tool the process environment, so the
// supervisor's own bearer token reaching a child is one `env` call away from a
// tenant's model — and that token authenticates as the OPERATOR.
func TestTheChildDoesNotInheritTheSupervisorsCredentials(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("TERVA_WEB_TOKEN", "operator-secret-do-not-leak")
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_PID", "1")

	s := newTestSupervisor(t, SameUser{})
	c, err := s.Start(context.Background(), enrol(t, "sub-a"))
	if err != nil {
		t.Fatal(err)
	}
	got := readObserved(t, c)
	if got.Token != "" {
		t.Errorf("the child inherited the supervisor's bearer token: %q", got.Token)
	}
	if got.ListenF != "" {
		t.Errorf("the child inherited LISTEN_FDS=%q and would try to adopt the supervisor's listening socket", got.ListenF)
	}
	// The control: the scrub must be selective. An empty environment would pass
	// both checks above while breaking every child that needs to find a binary.
	if got.Path == "" {
		t.Error("CONTROL: PATH did not survive — the child got no environment at all, so the assertions above prove nothing")
	}
}

// "It crashed" and "it is slow" send an operator to different places, so the
// two must not arrive as the same message.
func TestAChildThatDiesIsReportedAsExitedNotTimedOut(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("TENANT_FAKE_DIE", "1")
	s := newTestSupervisor(t, SameUser{})

	start := time.Now()
	_, err := s.Start(context.Background(), enrol(t, "sub-a"))
	if err == nil {
		t.Fatal("a child that exited immediately was reported as started")
	}
	if !strings.Contains(err.Error(), "exited before it served") {
		t.Errorf("wrong diagnosis: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("waited %s for a process that was already gone — it timed out instead of noticing", elapsed)
	}
	if n := len(s.Running()); n != 0 {
		t.Errorf("%d children left registered after a failed start", n)
	}
}

func TestStopBringsTheChildDown(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, SameUser{})
	rec := enrol(t, "sub-a")
	c, err := s.Start(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	s.Stop(rec.ID)
	if c.Alive() {
		t.Error("the child survived Stop")
	}
	if _, err := os.Stat(c.Socket); err == nil {
		t.Error("the socket was left behind")
	}
	if n := len(s.Running()); n != 0 {
		t.Errorf("%d children still registered", n)
	}
}

func TestASuspendedTenantIsNotSpawned(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, SameUser{})
	rec := enrol(t, "sub-a")
	rec.Suspended = true
	if _, err := s.Start(context.Background(), rec); err == nil {
		t.Fatal("a suspended environment was spawned")
	}
	if n := len(s.Running()); n != 0 {
		t.Error("a suspended environment left a child running")
	}
}

// The id names a directory and a socket path, so a record that reached the
// supervisor without going through the store must still not escape the root.
func TestAMalformedIDIsNotSpawned(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, SameUser{})
	_, err := s.Start(context.Background(), Record{ID: "../../etc", Subject: "s"})
	if err == nil {
		t.Fatal("a traversing id was spawned")
	}
	if !strings.Contains(err.Error(), "malformed id") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

func TestNewSupervisorRefusesAnUnstatedContainment(t *testing.T) {
	dir := testsupport.TempDir(t)
	_, err := NewSupervisor(SupervisorOptions{Root: dir, RunDir: dir})
	if err == nil {
		t.Fatal("a supervisor was built with no containment decision")
	}
	if !strings.Contains(err.Error(), "SameUser") {
		t.Errorf("the error does not name the remedy: %v", err)
	}
	// The control: with the decision stated, the same options build.
	if _, err := NewSupervisor(SupervisorOptions{Root: dir, RunDir: dir, Containment: SameUser{}}); err != nil {
		t.Fatalf("CONTROL: stating SameUser must be enough: %v", err)
	}
}
