package tenant

import (
	"fmt"
	"slices"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// forwardable is the allowlist of method groups a tenant's connection may carry
// to its child. It is an ALLOWLIST, not a denylist, and that is the whole
// design: a denylist with exceptions fails open the moment a group is added,
// while an unlisted group here is simply not proxied — so a new
// supervisor-scoped group shows up as a refusal in development rather than as a
// tenant reaching something it should never have seen.
//
// 🚨 What is missing matters more than what is here. A future `tenants` group —
// enrol, suspend, list every environment on the box — is served on the
// SUPERVISOR's own connection and is deliberately absent. A tenant should not be
// able to name it, never mind be refused it: refusal is a check, and a check is
// one bug away from passing. Non-existence is a boundary.
var forwardable = map[ctrlproto.Group]bool{
	ctrlproto.GroupConversation: true,
	ctrlproto.GroupSession:      true,
	ctrlproto.GroupControl:      true,
	ctrlproto.GroupReplay:       true,
	// GroupAuth is NOT forwardable, and this is the coupling D4 calls
	// load-bearing. Tenants share one provider credential, so a tenant holding
	// auth.* could replace or delete the credential every other tenant is
	// using, or substitute their own so the operator's users bill to someone
	// else's account. The operator manages credentials; a tenant spends them.
	//
	// GroupSecrets is absent for the same reason, one rung higher: it reports
	// on the key that opens everything, including material auth never touches.
}

// Forwardable reports whether a group may cross the proxy at all, before any
// question of who is asking.
func Forwardable(g ctrlproto.Group) bool { return forwardable[g] }

// ForwardableGroups returns the groups a tenant connection may carry,
// intersected with what the child actually offered. Two filters, in order: what
// the proxy will carry for anyone, then what this principal was granted.
//
// The order matters for the reason authz.Restrict intersects rather than
// replaces — each filter can only ever narrow, so no combination of them can
// hand back something the child did not offer or the operator disabled.
func ForwardableGroups(offered []ctrlproto.Group, p authz.Principal) []ctrlproto.Group {
	out := make([]ctrlproto.Group, 0, len(offered))
	for _, g := range offered {
		if forwardable[g] {
			out = append(out, g)
		}
	}
	return authz.Restrict(out, p)
}

// Carrier is what the SUPERVISOR serves over HTTP, as opposed to what the child
// would serve if you dialled it directly.
//
// 🔑 It exists because a hello feature is a claim about the CARRIER, and under
// `terva serve` the carrier is the supervisor — the child is behind a proxy that
// forwards four paths and serves the rest itself. A child describing its own
// routes is describing a server the browser is not talking to.
type Carrier struct {
	// Stage reports that this supervisor mounts /stage/. False is the default
	// and means the routes do not exist here, whatever a child believes about
	// its own mux.
	Stage bool
}

// routeBackedFeatures maps each hello feature that names an HTTP ROUTE to
// whether this carrier serves it. A feature absent from this map crosses the
// proxy untouched, which is correct only for features backed by a ctrlproto
// verb rather than a route — those are already gated by the group allowlist.
//
// 🚨 The failure this prevents is not a leak, it is a LIE, and it was live: a
// tenant can set web_stage in their own config, at which point their child
// advertises `stage` and the panel shows an "open in Stage" link. Under `terva
// serve` that link reached /stage/, which no supervisor route matched, so it
// fell through to "/" and answered with the MAIN app's shell. The surface the
// hello promised did not exist on the host the browser was talking to.
//
// TestEveryRootAppendedFeatureIsCarrierClassified is what keeps this honest as
// features are added: a feature the web composition root appends must be named
// here or explicitly excused, because "not in the map" and "nobody looked" are
// the same value and very different facts.
func routeBackedFeatures(c Carrier) map[string]bool {
	return map[string]bool{
		// /stage/ and the /stage/-scoped PWA shell.
		ctrlproto.FeatureStage: c.Stage,
		// FeatureAttachments (POST /upload) and FeatureSharedFiles (GET
		// /shared/) are route-backed too and are ALWAYS carried: both paths are
		// on the proxy's allowlist, so the supervisor genuinely serves them for
		// every tenant. Named here rather than omitted so the census can tell a
		// deliberate always-true from an oversight.
		ctrlproto.FeatureAttachments: true,
		ctrlproto.FeatureSharedFiles: true,
	}
}

// ForwardableFeatures drops the features this carrier cannot honour.
func ForwardableFeatures(offered []string, c Carrier) []string {
	backed := routeBackedFeatures(c)
	out := make([]string, 0, len(offered))
	for _, f := range offered {
		if served, gated := backed[f]; gated && !served {
			continue
		}
		out = append(out, f)
	}
	return out
}

// NarrowHello rewrites the child's server hello for the tenant's browser.
//
// The child is a full daemon and says so: it offers every group it serves,
// because on its own socket the only caller is the supervisor. What reaches the
// browser must be what THIS principal may use, or the panel renders affordances
// for verbs the gate will refuse — and a UI that offers what it cannot do is
// worse than one that offers less.
//
// The same sentence is why FEATURES are narrowed here too, against the carrier
// rather than the principal: a feature naming a route this supervisor does not
// mount is an affordance nothing can serve. Two filters, two questions — may
// this caller use it, and does this host have it at all.
func NarrowHello(h ctrlproto.Hello, p authz.Principal, c Carrier) ctrlproto.Hello {
	h.Groups = ForwardableGroups(h.Groups, p)
	h.Features = ForwardableFeatures(h.Features, c)
	return h
}

// CheckCommand decides whether one command frame may be forwarded, and returns
// the error frame to answer with when it may not.
//
// Both gates are asked, because they answer different questions and step 2
// established that neither implies the other: the GROUP says whether this kind
// of authority crosses the proxy at all, and the CAPABILITY says whether this
// principal may read, write, or spend. A viewer holds the same groups as a
// member and differs only in the mask.
func CheckCommand(f ctrlproto.Frame, p authz.Principal) *ctrlproto.Frame {
	if f.Kind != ctrlproto.KindCmd {
		return nil
	}
	g := f.Method.Group()
	if !forwardable[g] {
		// Deliberately the same shape of answer a tenant gets for a verb that
		// does not exist on this connection. Saying "that group is
		// supervisor-only" would confirm the surface is there, which is the one
		// thing a boundary built on non-existence must not do.
		return errFrame(f.ID, ctrlproto.CodeUnsupported,
			fmt.Sprintf("not served here: %s", f.Method))
	}
	if !slices.Contains(authz.Grant(p), g) {
		return errFrame(f.ID, ctrlproto.CodeForbidden,
			fmt.Sprintf("not permitted for this caller: %s", f.Method))
	}
	if !f.Method.Permits(authz.Authority(p)) {
		return errFrame(f.ID, ctrlproto.CodeForbidden,
			fmt.Sprintf("not permitted for this caller: %s", f.Method))
	}
	return nil
}

func errFrame(id uint64, code, msg string) *ctrlproto.Frame {
	f := ctrlproto.ErrFrame(id, code, msg)
	return &f
}
