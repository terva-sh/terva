package tenant

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Start forks a child as the supervisor's own user.
//
// This is what the supervisor did inline before Containment grew a Start
// method, moved behind the seam unchanged: the supervisor no longer knows that
// a child is a process it forked, which is what lets the systemd backend exist
// without touching Supervisor at all.
func (SameUser) Start(ctx context.Context, spec Spec) (*Child, error) {
	if err := os.MkdirAll(spec.Home, 0o700); err != nil {
		return nil, fmt.Errorf("tenant: create home: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.Socket), 0o700); err != nil {
		return nil, fmt.Errorf("tenant: create run dir: %w", err)
	}
	if err := checkSocketPath(spec.Socket); err != nil {
		return nil, err
	}
	// A socket left by a child that died without cleaning up would make the new
	// child refuse to bind. terva web removes a stale socket itself, but only
	// after proving nothing answers on it, which is the check that matters.
	_ = os.Remove(spec.Socket)

	exe := spec.Exe
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("tenant: locate terva: %w", err)
		}
		exe = self
	}

	cmd := exec.Command(exe, "web", "--web-addr", "unix:"+spec.Socket)
	cmd.Dir = spec.Home
	cmd.Env = spec.Env
	cmd.Stdout = os.Stderr // the child's ready line belongs in the supervisor's log
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tenant: spawn %s: %w", spec.ID, err)
	}

	c := &Child{ID: spec.ID, Home: spec.Home, Socket: spec.Socket, done: make(chan struct{})}
	c.stop = func() {
		if cmd.Process == nil {
			return
		}
		// The polite signal first because the child holds live sessions: terva
		// web's SIGTERM path cancels its context so the server drains and every
		// session's agent and extension subprocesses come down with it. Killing
		// outright would orphan those grandchildren.
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-c.done
		}
		_ = os.Remove(c.Socket)
	}
	go func() {
		c.err = cmd.Wait()
		close(c.done)
	}()

	if err := waitForSocket(ctx, c, spec.Socket, spec.Timeout); err != nil {
		c.shutdown()
		return nil, err
	}
	return c, nil
}

// maxSocketPath is the portable ceiling on a unix socket path: the kernel's
// sockaddr_un holds 104 bytes on darwin and 108 on linux, minus the NUL. The
// lower of the two, with a byte to spare.
const maxSocketPath = 100

// checkSocketPath refuses a path the kernel would truncate.
//
// Without this the failure lands in the CHILD, as a bind error on a path it was
// handed — so the supervisor reports "exited before it served: exit status 1"
// and the operator goes looking for a crash. The limit is small enough to hit
// by accident: a tenant id is 34 characters before ".sock", so a run directory
// a few levels deep is all it takes.
func checkSocketPath(p string) error {
	if len(p) <= maxSocketPath {
		return nil
	}
	return fmt.Errorf("tenant: the child's socket path is %d bytes and the kernel accepts %d — choose a shorter run directory than %q (a per-user runtime dir like /run/user/$UID is the intended home for it)",
		len(p), maxSocketPath, filepath.Dir(p))
}
