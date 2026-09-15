package tenant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/envcompat"
)

// homeEnv is the variable that makes every one of config.TervaHome()'s 171 call
// sites correct in the child. Setting it is the entire mechanism: the child is
// today's daemon, unmodified, pointed at a different directory.
const homeEnv = envcompat.HomeEnv

// Child is one tenant's running daemon, and the socket the supervisor reaches
// it on.
//
// Home and Socket are what the Containment ACTUALLY used, which need not be
// what the Spec asked for — a systemd unit places its own state directory and
// its own socket, and the supervisor has to dial where the child is rather than
// where it hoped.
type Child struct {
	ID     string
	Home   string
	Socket string

	done chan struct{}
	err  error

	// stop is the Containment's teardown. It must be idempotent-safe under
	// once; the supervisor may stop a child that has already exited.
	stop     func()
	stopOnce sync.Once

	// mu guards the idleness bookkeeping the reaper reads (see reap.go).
	mu       sync.Mutex
	holds    int
	lastUsed time.Time
}

// Wait blocks until the child exits and reports why.
func (c *Child) Wait() error {
	<-c.done
	return c.err
}

// Alive reports whether the child is still running.
func (c *Child) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// shutdown brings the child down through whatever started it.
func (c *Child) shutdown() {
	c.stopOnce.Do(func() {
		if c.stop != nil {
			c.stop()
		}
	})
}

// SupervisorOptions configures how children are started.
type SupervisorOptions struct {
	// Root is the directory tenant homes live under. Each child gets
	// Root/<tenant-id>.
	Root string

	// RunDir is where the per-tenant sockets are created. It is separate from
	// Root because a socket is runtime state, not tenant data — and because on
	// a real deployment the two want different filesystems (%t vs %S).
	RunDir string

	// Containment starts and confines each child. Required: there is no
	// default, because the safe default and the convenient one differ and
	// picking either silently would be wrong.
	Containment Containment

	// Exe is the terva binary to run. Empty means this process's own.
	Exe string

	// StartTimeout bounds how long a child may take to answer on its socket.
	StartTimeout time.Duration
}

// Supervisor owns the running children: one per tenant, started on demand and
// reused across connections.
type Supervisor struct {
	opts SupervisorOptions

	mu       sync.Mutex
	children map[string]*Child
	// denied is the LIVE half of suspension, and it exists because the durable
	// half cannot win a race on its own.
	//
	// 🚨 Start is handed a Record that a caller read from the registry moments
	// earlier, so an operator who suspends someone mid-request would set the
	// flag, stop the child — and then watch the in-flight request start it
	// again from a record that predates the suspension. This set is consulted
	// under the same mutex that registers a child, so Deny and Start cannot
	// interleave that way. The registry stays the source of truth across a
	// restart; this is the source of truth for the next millisecond.
	denied map[string]bool
	// failures is why a tenant's environment last refused to come up.
	//
	// 🔑 Found by running the binary: WITHOUT it, the panel cannot tell an idle
	// environment from a broken one. Both read "not running" — and one is the
	// normal resting state while the other is the thing an operator opened the
	// panel to diagnose. The supervisor is the only component that ever sees
	// the reason (the caller gets a deliberately generic 403, and the log line
	// has since scrolled), so if it is not kept here it is nowhere.
	failures map[string]startFailure
}

// startFailure is one environment's last refusal to come up.
type startFailure struct {
	reason string
	at     time.Time
}

// NewSupervisor validates the configuration and returns a supervisor.
//
// 🚨 The containment check is here rather than at start time on purpose. A
// supervisor whose containment does not isolate is a SINGLE-tenant supervisor:
// its children share a uid, so tenant A's bash tool reads tenant B's home and
// dials tenant B's socket, whatever TERVA_HOME says. Discovering that at the
// moment a second person signs in would mean refusing a real user mid-login, or
// — far worse — not refusing them. It is a startup question, and it is asked
// the way checkBindSafety asks its own: fail closed, and name the remedy.
func NewSupervisor(opts SupervisorOptions) (*Supervisor, error) {
	if opts.Containment == nil {
		return nil, errors.New("tenant: no containment chosen — pass SameUser{} to say explicitly that children are not separated")
	}
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("tenant: no tenant root configured")
	}
	if strings.TrimSpace(opts.RunDir) == "" {
		return nil, errors.New("tenant: no runtime directory configured")
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 30 * time.Second
	}
	return &Supervisor{
		opts:     opts,
		children: map[string]*Child{},
		denied:   map[string]bool{},
		failures: map[string]startFailure{},
	}, nil
}

// Containment reports what this supervisor separates children with, for the
// operator surface that has to say so.
func (s *Supervisor) Containment() Containment { return s.opts.Containment }

// ErrWouldShareAUID is returned when a second tenant would be started under a
// containment that does not separate them.
var ErrWouldShareAUID = errors.New("tenant: this containment does not separate tenants")

// Start returns the tenant's running child, starting it if it is not up.
//
// It is idempotent: a second browser tab, or a reconnect, gets the process that
// is already serving rather than a second one over the same home. Two terva
// daemons over one TERVA_HOME is not a configuration anyone should reach by
// accident.
func (s *Supervisor) Start(ctx context.Context, rec Record) (*Child, error) {
	if !ValidID(rec.ID) {
		return nil, fmt.Errorf("tenant: refusing to start a malformed id %q", rec.ID)
	}
	if rec.Suspended {
		return nil, fmt.Errorf("tenant: environment %s is suspended", rec.ID)
	}

	s.mu.Lock()
	if s.denied[rec.ID] {
		s.mu.Unlock()
		return nil, fmt.Errorf("tenant: environment %s is suspended", rec.ID)
	}
	if c, ok := s.children[rec.ID]; ok && c.Alive() {
		s.mu.Unlock()
		return c, nil
	}
	// The containment refusal is re-checked here, not only at construction: a
	// supervisor that started with one tenant and no containment is correct
	// until the SECOND tenant arrives, and that arrival is a runtime event.
	if !s.opts.Containment.Isolates() {
		for id, c := range s.children {
			if id != rec.ID && c.Alive() {
				s.mu.Unlock()
				return nil, s.noteFailure(rec.ID, fmt.Errorf("%w: %s is already running and containment is %s — a second tenant would share its uid, and could read its home and dial its socket",
					ErrWouldShareAUID, id, s.opts.Containment.Describe()))
			}
		}
	}
	s.mu.Unlock()

	c, err := s.opts.Containment.Start(ctx, s.specFor(rec))
	if err != nil {
		return nil, s.noteFailure(rec.ID, err)
	}
	// A child nobody ever dials must still age, or an environment started by a
	// request that then failed would sit there forever.
	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()

	s.mu.Lock()
	// Suspension may have landed WHILE this child was starting — containment
	// start is the slowest thing here, and the lock is not held across it. The
	// check above cannot cover that window, so it is asked again on the way in:
	// an operator who suspended someone must not find a child that appeared
	// afterwards.
	if s.denied[rec.ID] {
		s.mu.Unlock()
		c.shutdown()
		return nil, fmt.Errorf("tenant: environment %s was suspended while it was starting", rec.ID)
	}
	// Another caller may have won the race while we were starting. Theirs is
	// the live child; ours is stopped rather than left orphaned holding a
	// socket nobody will dial. (Same shape as the auth store's refresh: re-read
	// under the lock, and defer to whoever got there first.)
	if live, ok := s.children[rec.ID]; ok && live.Alive() {
		s.mu.Unlock()
		c.shutdown()
		return live, nil
	}
	s.children[rec.ID] = c
	delete(s.failures, rec.ID) // it came up; whatever stopped it last time is history
	s.mu.Unlock()
	return c, nil
}

// noteFailure records why an environment would not start and returns the error
// unchanged, so a caller reads as `return nil, s.noteFailure(id, err)` and
// cannot record one thing while returning another.
func (s *Supervisor) noteFailure(id string, err error) error {
	s.mu.Lock()
	s.failures[id] = startFailure{reason: err.Error(), at: time.Now()}
	s.mu.Unlock()
	return err
}

// StartFailure reports why id last refused to start, and when. ok is false when
// the last thing it did was start, or when it has never been asked to.
func (s *Supervisor) StartFailure(id string) (reason string, at time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.failures[id]
	return f.reason, f.at, ok
}

// specFor describes the child the supervisor wants, naming no mechanism.
func (s *Supervisor) specFor(rec Record) Spec {
	home := rec.Home(s.opts.Root)
	return Spec{
		ID:      rec.ID,
		Home:    home,
		Socket:  filepath.Join(s.opts.RunDir, rec.ID+".sock"),
		Exe:     s.opts.Exe,
		Env:     childEnv(home),
		Timeout: s.opts.StartTimeout,
	}
}

// childEnv builds the child's environment: the supervisor's own, sanitised,
// with TERVA_HOME repointed and the supervisor's own credentials removed.
//
// 🚨 The scrub is the security-relevant half. terva hands the agent's shell tool
// the process environment, so anything left here is one `env` call away from a
// tenant's model — and the supervisor's bearer token authenticates as the
// OPERATOR, not as the tenant. A child that inherited it could dial the
// supervisor back as an owner.
func childEnv(home string) []string {
	out := make([]string, 0, 16)
	for _, kv := range procenv.Inherited() {
		k, _, _ := strings.Cut(kv, "=")
		if _, drop := notForChildren[strings.ToUpper(k)]; drop {
			continue
		}
		out = append(out, kv)
	}
	return append(out, homeEnv+"="+home)
}

// notForChildren are the supervisor's own secrets and routing, stripped before
// a tenant's child inherits them. Keyed uppercase; the lookup upper-cases too,
// because Windows environment keys are case-insensitive.
var notForChildren = map[string]struct{}{
	// The supervisor's bearer token — the operator's credential.
	"TERVA_WEB_TOKEN": {},
	// The supervisor's own home. Repointed explicitly below rather than
	// inherited, so a child can never fall back to the supervisor's registry.
	strings.ToUpper(homeEnv): {},
	// Socket activation belongs to the process systemd started. A child that
	// inherited these would try to adopt the SUPERVISOR's listening socket.
	"LISTEN_FDS":     {},
	"LISTEN_PID":     {},
	"LISTEN_FDNAMES": {},
}

// waitForSocket blocks until the child answers on its socket, it dies, or the
// deadline passes. A child that exited is reported as such rather than as a
// timeout: "it crashed" and "it is slow" send an operator to different places.
func waitForSocket(ctx context.Context, c *Child, path string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if !c.Alive() {
			return fmt.Errorf("tenant: %s exited before it served: %w", c.ID, c.err)
		}
		conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tenant: %s did not answer on %s within %s", c.ID, path, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return fmt.Errorf("tenant: %s exited before it served: %w", c.ID, c.err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Deny stops a tenant's child and refuses to start it again until [Allow].
//
// It is the LIVE half of suspension; the durable half is Record.Suspended in
// the registry, and the panel sets both. Neither is sufficient alone: the
// registry survives a restart and this survives the millisecond between an
// operator's click and an in-flight request that already read the old record.
//
// It destroys nothing — see reap.go on why stopping a process and deleting a
// home are never allowed to share a path.
func (s *Supervisor) Deny(id string) {
	s.mu.Lock()
	s.denied[id] = true
	c := s.children[id]
	delete(s.children, id)
	s.mu.Unlock()
	if c != nil {
		c.shutdown()
	}
}

// Allow lifts a Deny. The tenant's next request starts a fresh daemon over the
// home it always had.
func (s *Supervisor) Allow(id string) {
	s.mu.Lock()
	delete(s.denied, id)
	s.mu.Unlock()
}

// Stop shuts one tenant's child down and forgets it.
func (s *Supervisor) Stop(id string) {
	s.mu.Lock()
	c := s.children[id]
	delete(s.children, id)
	s.mu.Unlock()
	if c != nil {
		c.shutdown()
	}
}

// StopAll shuts every child down. The supervisor's own shutdown path.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	all := make([]*Child, 0, len(s.children))
	for _, c := range s.children {
		all = append(all, c)
	}
	s.children = map[string]*Child{}
	s.mu.Unlock()
	for _, c := range all {
		c.shutdown()
	}
}

// Running lists the live children, for the supervisor panel and for tests.
func (s *Supervisor) Running() []*Child {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Child, 0, len(s.children))
	for _, c := range s.children {
		if c.Alive() {
			out = append(out, c)
		}
	}
	return out
}
