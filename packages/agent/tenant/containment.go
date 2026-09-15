// Package tenant models the per-tenant child a supervisor runs on behalf of an
// authenticated principal: who it is, where its home is, and how it is confined.
//
// The shape here is deliberate and is the part D6 of docs/proposals/
// daemon-access-auth.md says to get right first: a call site names WHAT it wants
// started — an identity, a home, a socket — and never WHERE or HOW it runs.
// Containment is a parameter. Get that wrong and the uid pool, the systemd unit
// and the eventual container backend each grow their own call site.
package tenant

import (
	"context"
	"time"
)

// Spec is what the supervisor asks for: one tenant's daemon, in its own home,
// answering on its own socket. It names no mechanism, which is the point — a
// Containment decides whether that means fork/exec, a systemd unit, or one day
// a container.
type Spec struct {
	// ID is the tenant's opaque local id. It names the home, the socket, and
	// (for the systemd backend) the unit instance.
	ID string

	// Home becomes the child's TERVA_HOME — the value that makes all 171 call
	// sites of config.TervaHome() correct by construction.
	//
	// A Containment that manages its own state directory may IGNORE this and
	// report where it actually put the home; see Child.Home.
	Home string

	// Socket is where the supervisor expects to dial the child. A Containment
	// that owns socket creation may likewise place it elsewhere and say so.
	Socket string

	// Exe is the terva binary to run. Empty means the supervisor's own.
	Exe string

	// Env is the child's environment, already scrubbed of the supervisor's
	// credentials. A Containment that does not pass an environment through
	// (systemd takes it from the unit) may ignore it.
	Env []string

	// Timeout bounds how long the child may take to answer on its socket.
	Timeout time.Duration
}

// Containment is how a tenant's child is run and confined from its siblings.
//
// 🚨 A per-tenant TERVA_HOME is a ROUTING decision, not a boundary. The tenant's
// model can run bash as the child's uid, so if two children share a uid, tenant
// A's shell tool simply reads tenant B's home — the env var did not stop it, and
// nothing reports the crossing. Isolates() is how a Containment answers that
// honestly, and the supervisor refuses configurations where the answer is no and
// the tenant count is more than one.
//
// 🔑 Start rather than "decorate the command we were going to run": the first
// version of this interface was Apply(*exec.Cmd), which quietly assumed the
// supervisor forks the child. Under systemd it does not — systemd starts the
// process and the supervisor asks it to — so that shape could not have carried
// the second implementation, in an interface whose whole purpose was carrying
// it. An interface that presumes the mechanism is not a seam.
type Containment interface {
	// Start brings the tenant's daemon up and returns it ready to dial. It
	// must not return until the socket answers or the attempt has failed.
	Start(ctx context.Context, spec Spec) (*Child, error)

	// Describe names the boundary in one phrase, for the ready line, the logs
	// and (later) the supervisor panel. An operator should be able to read it
	// and know what is and is not separated.
	Describe() string

	// Isolates reports whether this is a real boundary between tenants. A
	// Containment that returns false is usable — a single-tenant supervisor is
	// a legitimate configuration — but it may not carry two tenants.
	Isolates() bool
}

// SameUser runs every child as the supervisor's own uid, separated only by
// TERVA_HOME. It is the honest name for "no containment": a development
// configuration, and the correct one for a supervisor serving exactly one
// tenant, where there is no sibling to be separated from.
//
// It exists as a named type rather than as a nil Containment because "no
// containment" is a decision an operator should have to make, and because
// Isolates() gives the supervisor something to refuse on. A nil default would
// make the unsafe multi-tenant configuration the one you get by saying nothing.
type SameUser struct{}

func (SameUser) Describe() string {
	return "none (children share the supervisor's uid; TERVA_HOME separates their data, nothing separates their processes)"
}

func (SameUser) Isolates() bool { return false }
