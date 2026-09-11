package fleet

import (
	"context"
	"errors"
	"fmt"

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
	hub         *Hub
	local       ctrlproto.WorkspaceService
	localOrigin string

	// OnSourceError observes a member that failed to answer a fan-in, so a
	// caller can log or badge it. Optional.
	OnSourceError func(origin string, err error)
}

// NewAggregate builds the hub-side view over hub's members plus an optional
// local workspace. A nil hub is a fleet of one, and a nil local workspace is a
// hub that carries only members, which is what a dedicated hub host looks like.
func NewAggregate(hub *Hub, local ctrlproto.WorkspaceService, localOrigin string) (*Aggregate, error) {
	if localOrigin == "" {
		localOrigin = LocalOrigin
	}
	if local != nil && !ctrlproto.ValidOrigin(localOrigin) {
		return nil, fmt.Errorf("fleet: %q is not a usable origin for the local workspace", localOrigin)
	}
	return &Aggregate{hub: hub, local: local, localOrigin: localOrigin}, nil
}

// Sources lists every member, the hub's own workspace first.
//
// The local workspace is an entry in this list and nothing more. That is what
// "an ordinary member" means here: ordinary at THIS layer, where the merge
// iterates and never asks which kind of member it is holding. It is not a claim
// that the hub talks to itself over a socket. Decision 0014's "a local member
// checks in too" is about a separate local daemon, and reading it the other way
// would force two processes onto a single machine for no gain.
func (a *Aggregate) Sources() []Source {
	var out []Source
	if a.local != nil {
		out = append(out, Source{Origin: a.localOrigin, Svc: a.local})
	}
	if a.hub != nil {
		for _, origin := range a.hub.Members() {
			if c, ok := a.hub.Client(origin); ok {
				out = append(out, Source{Origin: origin, Svc: c.Service()})
			}
		}
	}
	return out
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
		if a.local == nil {
			return Source{}, "", fmt.Errorf("%w: %q carries no origin and this hub has no local workspace", ErrUnknownOrigin, federated)
		}
		return Source{Origin: a.localOrigin, Svc: a.local}, id, nil
	}
	for _, src := range a.Sources() {
		if src.Origin == origin {
			return src, id, nil
		}
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
	if src.Origin != a.localOrigin {
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
		if a.local == nil {
			return nil, fmt.Errorf("%w: this hub has no local workspace to subscribe to", ErrUnknownOrigin)
		}
		return a.local.Subscribe(ctx, sess)
	}
	src, id, err := a.Route(sess)
	if err != nil {
		return nil, err
	}
	return src.Svc.Subscribe(ctx, id)
}
