package tenant

import (
	"context"
	"sort"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// Panel is the supervisor's operator surface: the ONE producer of the tenant
// picture, and the implementation of [ctrlproto.TenantsController].
//
// One producer rather than two readers, for the reason SecretsStatus is one: an
// HTML page and a wire client rendering the same struct cannot disagree about
// who is suspended, and a second reader of the registry would be a second answer
// waiting to drift from the first.
//
// It composes the two halves the supervisor keeps apart — the durable registry
// (Store) and the live children (Supervisor) — and reports the containment,
// because "is this host actually separating these people" is the first question
// a list of environments raises and the answer has until now existed only in one
// line of startup logging.
type Panel struct {
	store *Store
	sup   *Supervisor

	mu sync.Mutex
	// refusals is keyed by SUBJECT so a browser retrying every few seconds
	// collapses into one row with a count, rather than flooding out the other
	// people an operator needs to see.
	refusals map[string]*ctrlproto.TenantRefusal
}

// NewPanel builds the operator surface over a registry and a supervisor.
func NewPanel(store *Store, sup *Supervisor) *Panel {
	return &Panel{store: store, sup: sup, refusals: map[string]*ctrlproto.TenantRefusal{}}
}

// maxRefusals bounds the in-memory refusal log.
//
// Bounded because the input is untrusted in the only way that matters here: the
// subjects come from an identity provider, and a misconfigured one — or a
// hostile one — can mint as many distinct `sub` values as it likes. An operator
// reading this pane wants the handful of people who cannot get in, not a
// transcript; anything past the newest few dozen is noise that costs memory.
const maxRefusals = 50

// NoteRefusal records that an authenticated caller was refused an environment.
//
// 🔑 This is the case the enrolment record cannot cover, and it is D8's
// "an authenticated user with no environment is invisible otherwise". A person
// whose identity-provider groups were never mapped has no registry record to
// hang an observation on — and creating one would be enrolling them by the back
// door, which is precisely what Store.NoteUnentitled refuses to do. So the
// commonest enrolment problem, a brand-new user, leaves no trace anywhere unless
// it leaves one here.
//
// In-memory and diagnostic. It does not survive a restart, and nothing reads it
// to act: a durable file listing identities that were deliberately NOT enrolled
// is a worse thing to hold than the problem it solves.
func (p *Panel) NoteRefusal(principal authz.Principal, reason string, when time.Time) {
	if p == nil || principal.Subject == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if r, ok := p.refusals[principal.Subject]; ok {
		r.Count++
		r.LastAt = when.UTC()
		r.Reason = reason
		if principal.Display != "" {
			r.Display = principal.Display
		}
		return
	}
	if len(p.refusals) >= maxRefusals {
		p.evictOldestLocked()
	}
	p.refusals[principal.Subject] = &ctrlproto.TenantRefusal{
		Subject: principal.Subject,
		Display: principal.Display,
		Source:  string(principal.Source),
		Reason:  reason,
		Count:   1,
		LastAt:  when.UTC(),
	}
}

// evictOldestLocked drops the least recently refused subject. Oldest rather
// than an arbitrary map entry: the row an operator is least likely to still be
// investigating is the one nobody has retried in longest.
func (p *Panel) evictOldestLocked() {
	var (
		oldestKey string
		oldestAt  time.Time
	)
	for k, r := range p.refusals {
		if oldestKey == "" || r.LastAt.Before(oldestAt) {
			oldestKey, oldestAt = k, r.LastAt
		}
	}
	if oldestKey != "" {
		delete(p.refusals, oldestKey)
	}
}

// ForgetRefusal drops a subject's refusal row, because they have since been let
// in. Keeping it would leave an operator reading a live-looking problem that is
// already fixed.
func (p *Panel) ForgetRefusal(subject string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.refusals, subject)
	p.mu.Unlock()
}

// TenantsList reports every enrolment, what is running, and what this host
// actually separates.
func (p *Panel) TenantsList(ctx context.Context) (ctrlproto.TenantsListResult, error) {
	records, err := p.store.List()
	if err != nil {
		return ctrlproto.TenantsListResult{}, err
	}

	// The live half, indexed once rather than searched per record.
	live := map[string]*Child{}
	for _, c := range p.sup.Running() {
		live[c.ID] = c
	}

	out := ctrlproto.TenantsListResult{
		Tenants: make([]ctrlproto.TenantInfo, 0, len(records)),
		Containment: ctrlproto.TenantsContainment{
			Describe: p.sup.Containment().Describe(),
			Isolates: p.sup.Containment().Isolates(),
		},
		Refusals: p.refusalRows(),
		Roles:    authz.RoleNames(),
	}
	for _, r := range records {
		info := ctrlproto.TenantInfo{
			ID:              r.ID,
			Subject:         r.Subject,
			Display:         r.Display,
			EnrolledAt:      r.EnrolledAt,
			LastSeenAt:      r.LastSeenAt,
			Suspended:       r.Suspended,
			UnentitledSince: r.UnentitledSince,
		}
		if c, ok := live[r.ID]; ok {
			info.Running = true
			// The home the containment ACTUALLY used, which is why this is set
			// only here: a systemd unit places its own state directory, so the
			// path the supervisor asked for is not necessarily the path the
			// data is in, and a stopped tenant has nobody to ask.
			info.Home = c.Home
		} else if reason, at, failed := p.sup.StartFailure(r.ID); failed {
			// Not running for a REASON, which is a different row from not
			// running because nobody has asked. Only meaningful when it is
			// down: a live child that once failed to start has since started.
			info.LastStartError = reason
			stamp := at
			info.LastStartErrorAt = &stamp
		}
		out.Tenants = append(out.Tenants, info)
	}
	return out, nil
}

// refusalRows snapshots the refusal log, most recent first.
func (p *Panel) refusalRows() []ctrlproto.TenantRefusal {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.refusals) == 0 {
		return nil
	}
	out := make([]ctrlproto.TenantRefusal, 0, len(p.refusals))
	for _, r := range p.refusals {
		out = append(out, *r) // a copy: the caller must not hold a live pointer
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// TenantsSuspend stops an environment and refuses to start it again.
//
// Both halves, in this order: the durable flag first, so a supervisor that died
// between the two comes back still suspended; then the live denial, which stops
// whatever is running and closes the window an in-flight request would otherwise
// use to start it again from the record it read a moment ago.
//
// It destroys nothing. That is not a limitation of this verb, it is the whole
// posture — see reap.go and D7.
func (p *Panel) TenantsSuspend(ctx context.Context, ref ctrlproto.TenantRef) error {
	if err := p.check(ref); err != nil {
		return err
	}
	if err := p.store.SetSuspended(ref.ID, true); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", err)
	}
	p.sup.Deny(ref.ID)
	return nil
}

// TenantsResume lets an environment start again.
//
// The mirror order: lift the live denial LAST, so there is no instant where the
// supervisor would start a child the registry still calls suspended.
func (p *Panel) TenantsResume(ctx context.Context, ref ctrlproto.TenantRef) error {
	if err := p.check(ref); err != nil {
		return err
	}
	if err := p.store.SetSuspended(ref.ID, false); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", err)
	}
	p.sup.Allow(ref.ID)
	return nil
}

// check refuses a malformed id before it reaches a registry or a process.
//
// The id names a directory, a socket and a unit instance, so it is validated at
// every boundary it crosses rather than trusted because it was validated at one
// of them. Store.read refuses a malformed id at load and Supervisor.Start
// refuses one at spawn; this is the wire's turn.
func (p *Panel) check(ref ctrlproto.TenantRef) error {
	if !ValidID(ref.ID) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "not a tenant id: %q", ref.ID)
	}
	return nil
}
