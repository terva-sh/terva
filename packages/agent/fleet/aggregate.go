package fleet

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// LocalOrigin is the origin the hub's own workspace answers to by default.
const LocalOrigin = "local"

var (
	// ErrUnknownOrigin means no member answers to that origin.
	ErrUnknownOrigin = errors.New("fleet: no member with that origin")
	// ErrRemoteNotRouted means the session lives on a member and the hub does
	// not carry commands to a member yet. That is fleet-control's job.
	ErrRemoteNotRouted = errors.New("fleet: the hub does not route commands to a member yet")
)

// Source is one member's workspace and the origin it answers to.
type Source struct {
	Origin string
	Svc    ctrlproto.WorkspaceService
}

// Aggregate presents the hub's members as one workspace.
//
// It is deliberately NOT a ctrlproto.WorkspaceService yet. Most of that
// interface takes a session id, and every one of those methods would have to
// route that id or refuse it. Embedding a local workspace to inherit them is
// the one thing this milestone must not do: an unrouted call would land a
// remote-addressed command on a local session, silently, which is exactly the
// failure the acceptance criteria name. The routed adapter belongs with
// fleet-control, which is the milestone that actually carries commands. Until
// then the read surface here is the whole of it, and [Aggregate.RouteCommand]
// is the seam that adapter will be built on.
//
// TestAggregateIsNotAWorkspaceService pins this, so the decision does not rest
// on a count in a comment. An earlier version of this comment said 47 of 49,
// which was wrong: 38 of the 49 take a session id. Recount with awk over the
// interface in packages/agent/ctrlproto/service.go rather than trusting a
// number written here.
type Aggregate struct {
	hub *Hub

	// locals are the in-process workspaces, the default first. The default is
	// the directory the daemon started in: the source every session-less
	// method goes to, and the one a bare id addresses. Further locals are
	// other directories opened in this process (docs/proposals/
	// workspaces-as-sources.md). None of them is a member: nothing crosses a
	// socket to reach one, which is why RouteCommand routes to any of them.
	mu     sync.RWMutex
	locals []Source

	// OnSourceError observes a member that failed to answer a fan-in, so a
	// caller can log or badge it. Optional.
	OnSourceError func(origin string, err error)
}

// ErrOriginTaken means a source already answers to that origin.
var ErrOriginTaken = errors.New("fleet: a source already answers to that origin")

// NewAggregate builds the hub-side view over hub's members plus an optional
// default local workspace. A nil hub is a fleet of one. A zero def, one with
// no Svc, is a hub that carries only members, which is what a dedicated hub
// host looks like. An empty def.Origin means [LocalOrigin].
func NewAggregate(hub *Hub, def Source) (*Aggregate, error) {
	a := &Aggregate{hub: hub}
	if def.Svc == nil {
		return a, nil
	}
	if def.Origin == "" {
		def.Origin = LocalOrigin
	}
	if !ctrlproto.ValidOrigin(def.Origin) {
		return nil, fmt.Errorf("fleet: %q is not a usable origin for the local workspace", def.Origin)
	}
	a.locals = []Source{def}
	return a, nil
}

// AddLocal registers another in-process workspace under its origin. The
// origin must be usable and unclaimed by any local or any member, so a
// directory can never shadow a machine. The default is never added this way:
// it is fixed at construction, so there is always exactly one.
func (a *Aggregate) AddLocal(src Source) error {
	if src.Svc == nil {
		return errors.New("fleet: AddLocal needs a workspace")
	}
	if !ctrlproto.ValidOrigin(src.Origin) {
		return fmt.Errorf("fleet: %q is not a usable origin for a local workspace", src.Origin)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range a.locals {
		if l.Origin == src.Origin {
			return fmt.Errorf("%w: %q is a local workspace", ErrOriginTaken, src.Origin)
		}
	}
	if a.hub != nil {
		if _, ok := a.hub.Client(src.Origin); ok {
			return fmt.Errorf("%w: %q is a member", ErrOriginTaken, src.Origin)
		}
	}
	a.locals = append(a.locals, src)
	return nil
}

// RemoveLocal forgets a local workspace. It reports whether one was removed.
// The default cannot be removed: it is the source a bare id addresses, and a
// hub that lost it would refuse every client that predates federation.
func (a *Aggregate) RemoveLocal(origin string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := 1; i < len(a.locals); i++ {
		if a.locals[i].Origin == origin {
			a.locals = append(a.locals[:i], a.locals[i+1:]...)
			return true
		}
	}
	return false
}

// Default is the in-process workspace that session-less methods go to and
// that a bare id addresses. ok is false on a hub with no local workspace.
func (a *Aggregate) Default() (src Source, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.locals) == 0 {
		return Source{}, false
	}
	return a.locals[0], true
}

// Locals lists the in-process workspaces, the default first.
func (a *Aggregate) Locals() []Source {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]Source(nil), a.locals...)
}

// isLocal reports whether origin names an in-process workspace, which is the
// property RouteCommand cares about: a command to one of these cannot land on
// another daemon, because there is no other daemon on the path.
func (a *Aggregate) isLocal(origin string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, l := range a.locals {
		if l.Origin == origin {
			return true
		}
	}
	return false
}

// Sources lists every source, the in-process workspaces first with the
// default at the head, then the members.
//
// A local workspace is an entry in this list and nothing more. That is what
// "an ordinary member" means here: ordinary at THIS layer, where the merge
// iterates and never asks which kind of member it is holding. It is not a claim
// that the hub talks to itself over a socket. Decision 0014's "a local member
// checks in too" is about a separate local daemon, and reading it the other way
// would force two processes onto a single machine for no gain.
func (a *Aggregate) Sources() []Source {
	out := a.Locals()
	if a.hub != nil {
		for _, origin := range a.hub.Members() {
			c, ok := a.hub.Client(origin)
			// A member the hub is still expecting back is not a source. Hub
			// members stay listed once they have checked in, because their
			// client is parked in dial waiting for them, so without this a
			// machine that lost power keeps appearing live for as long as the
			// hub runs. Asking it anything returns ErrNotConnected, which the
			// fan-in would then report through OnSourceError on every single
			// page load: noise that says nothing a reader did not know.
			if !ok || !c.Connected() {
				continue
			}
			out = append(out, Source{Origin: origin, Svc: c.Service()})
		}
	}
	return out
}

// sourceFor resolves one origin WITHOUT asking whether it is connected now.
//
// Route wants this and the fan-in does not, which is why they stopped sharing
// one list. A member that is briefly parked is still a known origin, and
// answering "unknown origin" for it would send a reader hunting a typo when the
// truth is a reconnect in progress. The call that follows fails with
// ErrNotConnected instead, which says the true thing.
func (a *Aggregate) sourceFor(origin string) (Source, bool) {
	a.mu.RLock()
	for _, l := range a.locals {
		if l.Origin == origin {
			a.mu.RUnlock()
			return l, true
		}
	}
	a.mu.RUnlock()
	if a.hub == nil {
		return Source{}, false
	}
	c, ok := a.hub.Client(origin)
	if !ok {
		return Source{}, false
	}
	return Source{Origin: origin, Svc: c.Service()}, true
}

// Sessions is the fan-in: every member's list, concatenated, each entry
// stamped with its origin and its id rewritten to the federated form.
//
// A member that fails to answer is skipped rather than failing the call. One
// unreachable daemon must not blank the board for every other one, and a hub
// whose whole purpose is watching a fleet is least useful at the moment a
// member breaks. OnSourceError carries the reason out.
func (a *Aggregate) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	var out []ctrlproto.SessionInfo
	for _, src := range a.Sources() {
		list, err := src.Svc.Sessions(ctx)
		if err != nil {
			if a.OnSourceError != nil {
				a.OnSourceError(src.Origin, err)
			}
			continue
		}
		for _, s := range list {
			s.Origin = src.Origin
			s.ID = ctrlproto.JoinFederatedID(src.Origin, s.ID)
			out = append(out, s)
		}
	}
	return out, nil
}

// Route resolves a federated id to the member that owns it and the id that
// member knows it by. It is for READS. Use [Aggregate.RouteCommand] for
// anything that changes a session.
//
// A bare id, with no origin, addresses the hub's own workspace. That keeps a
// client that predates federation working against the hub's local sessions.
func (a *Aggregate) Route(federated string) (Source, string, error) {
	origin, id := ctrlproto.SplitFederatedID(federated)
	if origin == "" {
		def, ok := a.Default()
		if !ok {
			return Source{}, "", fmt.Errorf("%w: %q carries no origin and this hub has no local workspace", ErrUnknownOrigin, federated)
		}
		return def, id, nil
	}
	if src, ok := a.sourceFor(origin); ok {
		return src, id, nil
	}
	return Source{}, "", fmt.Errorf("%w: %q", ErrUnknownOrigin, origin)
}

// RouteCommand resolves a federated id for an operation that changes a
// session, and refuses when that session lives on a member.
//
// It returns a zero Source and an empty id on refusal, deliberately. A caller
// that ignores the error still cannot reach a workspace, so the failure mode
// this guards against, a remote-addressed command quietly applying to a local
// session, cannot happen by omission. That is worth more than a tidier
// signature.
func (a *Aggregate) RouteCommand(federated string) (Source, string, error) {
	src, id, err := a.Route(federated)
	if err != nil {
		return Source{}, "", err
	}
	if !a.isLocal(src.Origin) {
		return Source{}, "", fmt.Errorf("%w: session %q lives on member %q", ErrRemoteNotRouted, federated, src.Origin)
	}
	return src, id, nil
}

// Subscribe is the fan-up: a subscription to a federated id delivers that
// member's events.
//
// There is no re-stamping code here, and there should not be. An event body
// carries no session id; Frame.Sess is the sole routing truth, and ServeConn
// stamps outgoing frames with the sess the client subscribed WITH. So a browser
// that subscribes to "neot/abc123" receives frames addressed to "neot/abc123"
// by construction, while this end reads the member's channel for "abc123".
// Routing the subscription IS the re-stamp.
//
// The workspace address is not federated. A subscription to #workspace gets the
// hub's own workspace events and nothing else. A member has a #workspace of its
// own, and merging the two is not this milestone's job: a member's
// sessions_changed must never reach a browser as though the hub's own session
// set had changed, because the browser would refetch a list that did not move.
func (a *Aggregate) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	if sess == ctrlproto.AddrWorkspace {
		def, ok := a.Default()
		if !ok {
			return nil, fmt.Errorf("%w: this hub has no local workspace to subscribe to", ErrUnknownOrigin)
		}
		return def.Svc.Subscribe(ctx, sess)
	}
	src, id, err := a.Route(sess)
	if err != nil {
		return nil, err
	}
	return src.Svc.Subscribe(ctx, id)
}
