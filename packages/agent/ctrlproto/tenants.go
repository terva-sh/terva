package ctrlproto

import (
	"context"
	"time"
)

// The tenants group: the environments on a multi-tenant host, and the two acts
// an operator may perform on one.
//
// # Why it is here but not in the dispatch table
//
// The vocabulary belongs to the protocol — `terva attach` will speak these verbs
// against a supervisor, and a second definition of them elsewhere would be a
// second protocol. The HANDLERS do not: dispatch_table.go is what a
// [WorkspaceService] carrier serves, and no such carrier may ever serve these.
// The supervisor answers them on its own connection with its own loop
// (packages/agent/tenant/admin.go), so the workspace daemon a tenant is proxied
// to has no entry for `tenants.list` at all — the honest answer to a tenant
// naming it is "unknown method", from a daemon that genuinely has none.
//
// TestTenantsVerbsAreNotOnAWorkspaceCarrier pins that, and
// methods_complete_test.go's servedElsewhere map records the exemption in the
// census that would otherwise demand a handler.
//
// # What is deliberately absent
//
// DELETE, in any form. D7 ships suspend before deletion and D8's own
// deprovisioning story (step 9) is two-phase — suspend, grace, delete — with a
// human inside it and the uid reclaimed only after the home is verifiably gone.
// A wire verb that removed an environment would be the fastest possible way to
// lose someone's work to a mis-click, and it is trivial to add later and
// impossible to un-ship. The same argument secrets.go makes about rotation.
//
// QUOTA. There is no quota system to report on, and a field that always reads
// "unlimited" teaches an operator to ignore it.
//
// PER-TENANT USAGE, which D8 asked for first and which turns out to be blocked
// by the very thing that makes the supervisor safe — see [TenantsListResult].

// TenantsController is the interface behind [GroupTenants]. Only the `terva
// serve` supervisor implements it.
type TenantsController interface {
	// TenantsList reports every enrolment, the live state of each, and what
	// this host's containment actually separates.
	TenantsList(ctx context.Context) (TenantsListResult, error)
	// TenantsSuspend stops the environment and refuses to start it again. It
	// destroys nothing: the home, its sessions, and the enrolment all stay.
	TenantsSuspend(ctx context.Context, p TenantRef) error
	// TenantsResume lets it start on the next request.
	TenantsResume(ctx context.Context, p TenantRef) error
}

// TenantRef names one environment by its opaque local id.
//
// The ID, never the subject. An operator looking at the panel has both, but a
// verb keyed on the identity-provider subject would make the supervisor's
// mutation surface addressable by a value that arrives in a JWT — and the id is
// what already names the home, the socket and the unit.
type TenantRef struct {
	ID string `json:"id"`
}

// TenantInfo is one enrolled environment: what the registry durably knows, plus
// whether it happens to be running right now.
type TenantInfo struct {
	// ID is the opaque local id — the name of the home, the socket and (under
	// systemd containment) the unit instance.
	ID string `json:"id"`
	// Subject is the identity-provider `sub` this environment is keyed on. It
	// is shown to the OPERATOR and to nobody else; the id is opaque precisely
	// so tenants cannot read each other's subjects off the filesystem.
	Subject string `json:"subject"`
	Display string `json:"display,omitempty"`

	EnrolledAt time.Time `json:"enrolled_at"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`

	// Suspended reports the flag that stops this environment being started.
	Suspended bool `json:"suspended,omitempty"`

	// UnentitledSince is when this subject last signed in successfully carrying
	// no role, or nil if the last thing we saw them do was sign in entitled.
	// EVIDENCE for a human, never a trigger — see tenant.Store.NoteUnentitled.
	//
	// A POINTER for the reason [SecretsComponent.LastSeen] is one: `omitempty`
	// does nothing to a time.Time, so a zero value ships
	// "0001-01-01T00:00:00Z" and a client renders a real-looking date for
	// something that never happened.
	UnentitledSince *time.Time `json:"unentitled_since,omitempty"`

	// Running reports a live daemon for this environment. False is the NORMAL
	// state, not a fault: an idle environment is stopped after
	// --tenant-idle-timeout and restarts on the owner's next request with its
	// data untouched.
	Running bool `json:"running,omitempty"`
	// LastStartError is why this environment last refused to come up, empty
	// when the last thing it did was start.
	//
	// 🔑 Without it, Running:false means two opposite things — an idle
	// environment that will start on the owner's next request, and one that
	// dies every time it is asked. The second is what an operator opened the
	// panel to diagnose, and the supervisor is the only component that ever
	// sees the reason: the caller gets a deliberately generic 403, and the log
	// line has scrolled. Found by running the binary, where a tenant whose
	// child exited on a missing credential was reported as plain "stopped".
	LastStartError   string     `json:"last_start_error,omitempty"`
	LastStartErrorAt *time.Time `json:"last_start_error_at,omitempty"`

	// Home is where the environment's data actually lives, and it is set ONLY
	// while it is running.
	//
	// 🔑 Empty rather than computed for a stopped one. The supervisor asks for
	// a home; the containment decides where it goes, and a systemd unit places
	// its own StateDirectory somewhere the supervisor never named. Printing the
	// path the supervisor WOULD have asked for would be a path an operator
	// could `ls` and find empty, which is worse than no path.
	Home string `json:"home,omitempty"`
}

// TenantsContainment is what this host actually separates, in the panel's
// words.
//
// It rides the tenant list because it is the first question an operator has
// when two people share a box, and until now the only place it was ever stated
// was one line on stderr at startup. Isolates false means the host carries
// exactly ONE tenant — a second is refused at start — so a list showing five
// enrolments needs this next to it or it reads as five live environments.
type TenantsContainment struct {
	// Describe is the operator-facing sentence (tenant.Containment.Describe).
	Describe string `json:"describe"`
	Isolates bool   `json:"isolates"`
}

// TenantRefusal is one authenticated sign-in that was refused an environment,
// for D8's "an authenticated user with no environment is invisible otherwise".
//
// 🔑 This is the case the enrolment record cannot cover. A person whose groups
// were never mapped has no record to hang an observation on, and creating one
// would be enrolling them by the back door — so the most common enrolment
// problem, a brand-new user, leaves no trace at all without this.
//
// It is DIAGNOSTIC and in-memory: it does not survive a supervisor restart, and
// nothing reads it to act. Persisting it would mean writing a durable file of
// identities that were deliberately not enrolled, which is a worse thing to
// hold than the problem it solves.
type TenantRefusal struct {
	Subject string `json:"subject"`
	Display string `json:"display,omitempty"`
	// Source names the authn mode that produced the principal (oidc,
	// forward-auth), so an operator can tell "my IdP claim mapping is wrong"
	// from "my proxy is asserting the wrong header".
	Source string `json:"source,omitempty"`
	// Reason is the refusal in the words the caller was given.
	Reason string `json:"reason"`
	// Count is how many times this subject has been refused since the
	// supervisor started; LastAt is the most recent.
	Count  int       `json:"count"`
	LastAt time.Time `json:"last_at"`
}

// TenantsListResult is the supervisor panel's whole picture.
//
// # The gap it names rather than hides
//
// D8 asks first for per-tenant usage against the shared subscription, and it is
// NOT here. The reason is structural rather than unfinished work: a tenant's
// usage lives inside that tenant's home, and under the containment that makes
// the supervisor safe — a private uid per environment, `StateDirectory=` — the
// supervisor cannot read it. It could ask a RUNNING child over its socket, but
// then a person's spend would drop to zero when their laptop closed and their
// daemon was reaped, which reads as "spent nothing" and is worse than absent.
//
// Reporting spend durably needs the child to push it outward to a ledger the
// supervisor owns. That is a mechanism, not a field, and it belongs with the
// deprovisioning work rather than bolted onto a listing.
type TenantsListResult struct {
	Tenants     []TenantInfo       `json:"tenants,omitempty"`
	Containment TenantsContainment `json:"containment"`
	// Refusals are the authenticated callers this supervisor turned away since
	// it started, most recent first. Empty is the healthy state.
	Refusals []TenantRefusal `json:"refusals,omitempty"`
	// Roles names every role this build understands, so a panel can tell an
	// operator what a claim would have to map to. Derived from the authority
	// table by the caller, never retyped.
	Roles []string `json:"roles,omitempty"`
}
