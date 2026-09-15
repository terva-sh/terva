package tenant

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The supervisor's own connection.
//
// 🚨 This is a SECOND ctrlproto server, and its existence is the D8 boundary
// rather than a convenience. The supervisor proxies every tenant's connection to
// that tenant's workspace daemon (web/tenantproxy.go). If the admin surface were
// served on those same connections and merely CHECKED for a role, tenant→admin
// escalation would be one bug away, in the busiest code on the box — so instead:
//
//   - the proxy's forwardable allowlist does not carry GroupTenants (gate.go),
//   - the ctrlproto dispatch table has no entry for tenants.* at all
//     (ctrlproto/tenants.go), so the workspace daemon a tenant reaches genuinely
//     cannot serve them,
//   - and this loop, which CAN, serves nothing else. It has no WorkspaceService,
//     so there is no session, no transcript and no model call reachable from an
//     admin connection either. The isolation runs in both directions.
//
// It is a second SERVICE, not a second protocol: same frames, same handshake,
// same negotiation, same error codes. What differs is the surface behind it.

// AdminHello is the supervisor's server hello: one group, and nothing else.
//
// Deliberately not built from [ctrlproto.ServerHello] and trimmed. Starting from
// the full workspace hello and removing things is the shape where a group added
// upstream silently appears here; starting from nothing and adding one means a
// new group reaches this connection only when someone writes it down.
func AdminHello(agent, version string) ctrlproto.Hello {
	return ctrlproto.Hello{
		Role:     ctrlproto.RoleServer,
		Protocol: ctrlproto.Protocol,
		Agent:    agent,
		Version:  version,
		Groups:   []ctrlproto.Group{ctrlproto.GroupTenants},
	}
}

// ServeAdmin runs the supervisor's ctrlproto connection over conn until the
// peer goes away or ctx ends, answering the tenants group from ctl.
//
// authority is the caller's capability mask, and it is a real gate rather than
// decoration: `tenants.list` reads and `tenants.suspend` writes, so a role that
// is ever given the group read-only gets the listing and not the lever. Passing
// a zero mask denies everything, which is the safe direction for a carrier that
// forgot to establish who is asking.
func ServeAdmin(ctx context.Context, conn ctrlproto.FrameConn, ctl ctrlproto.TenantsController, hello ctrlproto.Hello, authority ctrlproto.Capability) error {
	first, err := conn.ReadFrame(ctx)
	if err != nil {
		return err
	}
	if first.Kind != ctrlproto.KindHello || first.Hello == nil {
		_ = conn.WriteFrame(ctx, ctrlproto.ErrFrame(first.ID, ctrlproto.CodeBadRequest, "expected hello frame"))
		return fmt.Errorf("tenant: expected hello, got %q", first.Kind)
	}
	contract := ctrlproto.Negotiate(hello, *first.Hello)

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Serialized like ServeConn's: nothing here pumps events today, but a
	// writer that is safe only by accident stops being safe the day one does.
	var wmu sync.Mutex
	write := func(f ctrlproto.Frame) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteFrame(loopCtx, f)
	}
	if err := write(ctrlproto.HelloFrame(hello)); err != nil {
		return err
	}

	for {
		f, err := conn.ReadFrame(loopCtx)
		if err != nil {
			return err
		}
		if f.Kind != ctrlproto.KindCmd {
			// A client sending hello/resp/event frames is confused, not hostile;
			// ignore them rather than tearing the connection down. ServeConn's
			// posture, for the same reason.
			continue
		}
		handleAdmin(loopCtx, write, ctl, contract, authority, f)
	}
}

// handleAdmin answers one command frame.
func handleAdmin(ctx context.Context, write func(ctrlproto.Frame) error, ctl ctrlproto.TenantsController, contract ctrlproto.Contract, authority ctrlproto.Capability, f ctrlproto.Frame) {
	// Anything outside the tenants group gets the answer it deserves on a
	// connection that has no workspace: it is not served here. Note that this
	// covers subscribe/unsubscribe too — there is no event pump, and a client
	// that subscribed would otherwise wait forever for a snapshot.
	if f.Method.Group() != ctrlproto.GroupTenants {
		_ = write(ctrlproto.ErrFrame(f.ID, ctrlproto.CodeUnsupported,
			fmt.Sprintf("not served here: %s", f.Method)))
		return
	}
	if !contract.Has(ctrlproto.GroupTenants) {
		_ = write(ctrlproto.ErrFrame(f.ID, ctrlproto.CodeUnsupported,
			"method group not negotiated: "+string(ctrlproto.GroupTenants)))
		return
	}
	// The second gate, and a different question from the first: the group says
	// which surface this caller asked for, the mask says what they may cause.
	if !f.Method.Permits(authority) {
		_ = write(ctrlproto.ErrFrame(f.ID, ctrlproto.CodeForbidden,
			"not permitted for this caller: "+string(f.Method)))
		return
	}

	switch f.Method {
	case ctrlproto.MethodTenantsList:
		res, err := ctl.TenantsList(ctx)
		respondAdmin(write, f.ID, res, err)
	case ctrlproto.MethodTenantsSuspend:
		var p ctrlproto.TenantRef
		if err := f.Bind(&p); err != nil {
			_ = write(ctrlproto.ErrFrame(f.ID, ctrlproto.CodeBadRequest, err.Error()))
			return
		}
		respondAdmin(write, f.ID, nil, ctl.TenantsSuspend(ctx, p))
	case ctrlproto.MethodTenantsResume:
		var p ctrlproto.TenantRef
		if err := f.Bind(&p); err != nil {
			_ = write(ctrlproto.ErrFrame(f.ID, ctrlproto.CodeBadRequest, err.Error()))
			return
		}
		respondAdmin(write, f.ID, nil, ctl.TenantsResume(ctx, p))
	default:
		// Reachable only for a tenants.* verb this switch has not learned yet —
		// TestEveryTenantsVerbIsServed makes that state fail in the suite rather
		// than on an operator's connection.
		_ = write(ctrlproto.ErrFrame(f.ID, ctrlproto.CodeBadRequest, "unknown method: "+string(f.Method)))
	}
}

// respondAdmin writes a success frame carrying result, or maps err to a coded
// failure. A *ctrlproto.Error keeps its code; anything else is internal.
func respondAdmin(write func(ctrlproto.Frame) error, id uint64, result any, err error) {
	if err != nil {
		var ce *ctrlproto.Error
		if errors.As(err, &ce) {
			_ = write(ctrlproto.ErrFrame(id, ce.Code, ce.Message))
			return
		}
		_ = write(ctrlproto.ErrFrame(id, ctrlproto.CodeInternal, err.Error()))
		return
	}
	fr, merr := ctrlproto.OKFrame(id, result)
	if merr != nil {
		_ = write(ctrlproto.ErrFrame(id, ctrlproto.CodeInternal, merr.Error()))
		return
	}
	_ = write(fr)
}
