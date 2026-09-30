package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// Talkoot proposals (decision 0025): a member or a person proposes a roster
// change, it waits in the inbox, and only a person's approval writes
// talkoot.md.
//
// 🔑 A member reaches talkootPropose through its seat, and nothing else. It
// cannot reach talkootDecide at all: only the talkoot.decide verb of a
// ctrlproto client calls it. That is the whole of "a member proposes and a
// person applies".

// maxWhyBytes bounds the reason a proposer gives, which the card shows.
const maxWhyBytes = 4 * 1024

// errProposalChangesNothing refuses a batch that leaves every member as it
// was, such as an edit to the value a field already has.
var errProposalChangesNothing = errors.New("talkoot: the proposal changes no member")

// talkootPropose records a proposal from a member, from a person when from
// starts with human:, or from a recruiter session when it starts with
// recruiter:. draft, when set, is a recruiter's new persona file. still, when
// set, runs inside the router call and refuses for a seat that an update
// retired.
//
// It checks the whole batch against the roster rules before anything is
// written, so a batch that fails makes no envelope and no card.
//
// 🔑 It takes no lock on the roster. The proposal records the revision it was
// checked against, and an approval refuses when the roster has another one.
// A seat call must not wait on run.update in any case: an update holds it
// while it revokes seats, and a revoke waits for the seat call. run.propose
// is safe to wait on, because its holder waits only for mu, and an update
// releases mu before it revokes.
func (w *Workspace) talkootPropose(id, from string, ops []talkoot.Op, undo, why, draft string, still func() error) (talkoot.Proposal, error) {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkoot.Proposal{}, err
	}
	if len(why) > maxWhyBytes {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the reason is %d bytes, above the %d limit", len(why), maxWhyBytes)
	}
	recruiter := strings.HasPrefix(from, talkoot.RecruiterPrefix)
	var pd *talkoot.PersonaDraft
	if recruiter {
		if undo != "" {
			return talkoot.Proposal{}, errors.New("talkoot: a recruiter proposes members, and a person undoes a change")
		}
		if err := checkRecruitOps(ops); err != nil {
			return talkoot.Proposal{}, err
		}
	}
	if draft != "" {
		// 🔑 Only a recruiter writes a persona through a proposal. A member's
		// talkoot_propose has no persona field, and a person writes one
		// through the persona library.
		if !recruiter {
			return talkoot.Proposal{}, errors.New("talkoot: only a recruiter proposes a new persona")
		}
		if pd, err = checkPersonaDraft(draft, ops); err != nil {
			return talkoot.Proposal{}, err
		}
	}
	if undo != "" {
		if len(ops) > 0 {
			return talkoot.Proposal{}, errors.New("talkoot: give the operations or the proposal to undo, not both")
		}
		applied, err := talkoot.LoadProposal(run.dir, undo)
		if err != nil {
			return talkoot.Proposal{}, err
		}
		if applied.Status != talkoot.ProposalApproved {
			return talkoot.Proposal{}, fmt.Errorf("talkoot: proposal %s is %s, and only an approved proposal can be undone", undo, applied.Status)
		}
		if ops, err = talkoot.Inverse(applied.Changes); err != nil {
			return talkoot.Proposal{}, err
		}
	}
	text, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: read the roster: %w", err)
	}
	changes, _, _, err := w.proposalPreview(id, text, ops, pd)
	if err != nil {
		return talkoot.Proposal{}, err
	}
	run.propose.Lock()
	defer run.propose.Unlock()
	pending, err := w.pendingProposals(id, run.dir)
	if err != nil {
		return talkoot.Proposal{}, err
	}
	if len(pending) >= talkoot.MaxPendingProposals {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: %d proposals already wait for a person, the limit", len(pending))
	}
	summary := talkoot.Summarize(ops)
	if undo != "" {
		summary = "undo " + undo + ": " + summary
	}

	var env talkoot.Envelope
	err = run.do(func(rt *talkoot.Router) (err error) {
		if still != nil {
			if err := still(); err != nil {
				return err
			}
		}
		if person, ok := strings.CutPrefix(from, talkoot.HumanPrefix); ok {
			env, err = rt.ProposeAs(person, summary)
		} else if session, ok := strings.CutPrefix(from, talkoot.RecruiterPrefix); ok {
			env, err = rt.ProposeRecruit(session, summary)
		} else {
			env, err = rt.Propose(from, summary)
		}
		return err
	})
	if err != nil {
		return talkoot.Proposal{}, err
	}
	now := time.Now()
	p := talkoot.Proposal{
		ID: talkoot.NewProposalID(now), Proposer: from, At: now, Base: talkoot.RosterRevision(text),
		Ops: ops, Summary: summary, Why: why, Undoes: undo, Envelope: env.ID,
		Changes: changes, Status: talkoot.ProposalPending, Persona: pd,
	}
	if err := talkoot.SaveProposal(run.dir, p); err != nil {
		// ⚠️ The room holds the envelope, and no card waits for it. The
		// proposer is told, and proposes again.
		return talkoot.Proposal{}, err
	}
	w.talkootEmit(talkootEvent{Talkoot: id, Kind: "inbox", Wire: new(ctrlproto.TalkootInboxEvent(id, proposalCard(id, p)))})
	return p, nil
}

// proposalPreview applies a batch to the roster text and checks the result as
// an update would: the roster rules and this workspace's own. d, when set, is
// a drafted persona that resolves as if it were in the library.
func (w *Workspace) proposalPreview(id string, text []byte, ops []talkoot.Op, d *talkoot.PersonaDraft) ([]talkoot.MemberChange, []byte, talkoot.Roster, error) {
	cur, err := talkoot.Parse(text, id+"/"+talkoot.FileName)
	if err != nil {
		return nil, nil, talkoot.Roster{}, err
	}
	next, err := talkoot.ApplyOps(text, ops)
	if err != nil {
		return nil, nil, talkoot.Roster{}, err
	}
	nr, err := w.parseRosterEnv(id, next, envWithDraft(d))
	if err != nil {
		return nil, nil, talkoot.Roster{}, err
	}
	changes := talkoot.Diff(cur, nr)
	if len(changes) == 0 {
		return nil, nil, talkoot.Roster{}, errProposalChangesNothing
	}
	return changes, next, nr, nil
}

// pendingProposals returns the talkoot's pending proposals. A record that
// does not read is reported, and it stays pending with its problem until a
// person declines it, so the cap still holds and the inbox shows it.
func (w *Workspace) pendingProposals(id, dir string) ([]talkoot.Proposal, error) {
	all, damaged, err := w.listProposals(id, dir)
	if err != nil {
		return nil, err
	}
	out := make([]talkoot.Proposal, 0, len(all)+len(damaged))
	for _, p := range all {
		if p.Status == talkoot.ProposalPending {
			out = append(out, p)
		}
	}
	for _, pid := range damaged {
		out = append(out, damagedProposal(pid))
	}
	byAge(out)
	return out, nil
}

// byAge sorts proposals oldest first. An id is a ULID, so its order is the
// order of submission, and a damaged record with no time still sorts.
func byAge(ps []talkoot.Proposal) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
}

// damagedProposal stands in for a record that does not read. It waits, and
// its problem says how to clear it.
func damagedProposal(pid string) talkoot.Proposal {
	return talkoot.Proposal{ID: pid, Status: talkoot.ProposalPending, Problem: talkoot.ErrProposalDamaged.Error() + "; decline it to clear it"}
}

func (w *Workspace) listProposals(id, dir string) ([]talkoot.Proposal, []string, error) {
	all, damaged, err := talkoot.ListProposals(dir)
	for _, pid := range damaged {
		w.diagf("talkoot %s: the record of proposal %s does not read", id, pid)
	}
	return all, damaged, err
}

// talkootProposals lists a talkoot's proposals, oldest first: the pending
// ones, or every one with all.
func (w *Workspace) talkootProposals(id string, all bool) ([]talkoot.Proposal, error) {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return nil, err
	}
	if all {
		list, damaged, err := w.listProposals(id, run.dir)
		for _, pid := range damaged {
			list = append(list, damagedProposal(pid))
		}
		byAge(list)
		return list, err
	}
	return w.pendingProposals(id, run.dir)
}

// talkootDecide applies a person's decision on a proposal. A decline writes
// the decision and nothing else. An approval checks the roster revision, and
// then commits through the same path as a person's own update, with a roster
// line that names the proposal and its proposer.
//
// edited, when it holds an operation on an approval, is applied in place of
// the proposal's operations, which the record keeps. The edit passes every
// check the originals did. An empty list is no edit, so a client that always
// sends the field can approve as made.
func (w *Workspace) talkootDecide(ctx context.Context, id, by, pid, decision string, edited []talkoot.Op, reason string) (talkoot.Proposal, error) {
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: %q must name a person in 1 to 64 letters, digits, and . _ @ -", by)
	}
	if decision != ctrlproto.TalkootDecisionApprove && decision != ctrlproto.TalkootDecisionDecline {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the decision must be approve or decline, not %q", decision)
	}
	if len(reason) > maxWhyBytes {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the reason is %d bytes, above the %d limit", len(reason), maxWhyBytes)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkoot.Proposal{}, err
	}
	run.update.Lock()
	defer run.update.Unlock()
	p, err := talkoot.LoadProposal(run.dir, pid)
	if errors.Is(err, talkoot.ErrProposalDamaged) && decision == ctrlproto.TalkootDecisionDecline {
		// A decline clears a damaged record, so it does not hold a place
		// under the cap for good. Nothing of it can be applied.
		p, err = talkoot.Proposal{ID: pid, Status: talkoot.ProposalPending, Problem: err.Error()}, nil
	}
	if err != nil {
		return talkoot.Proposal{}, err
	}
	if p.Status != talkoot.ProposalPending {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: proposal %s is already %s", pid, p.Status)
	}
	edit := len(edited) > 0
	waiting := p
	resolved := func() {
		w.talkootEmit(talkootEvent{Talkoot: id, Kind: "inbox", Wire: new(ctrlproto.TalkootInboxResolvedEvent(id, ctrlproto.TalkootCard{
			Member: p.Proposer, Kind: ctrlproto.TalkootCardProposal, ID: p.ID,
		}))})
	}
	p.DecidedBy, p.DecidedAt, p.Reason = humanBy(by), time.Now(), reason
	if decision == ctrlproto.TalkootDecisionDecline {
		p.Status, p.Problem = talkoot.ProposalDeclined, ""
		if err := talkoot.SaveProposal(run.dir, p); err != nil {
			return talkoot.Proposal{}, fmt.Errorf("talkoot: the decline of proposal %s could not be saved, so its card still waits: %w", p.ID, err)
		}
		resolved()
		return p, nil
	}

	// refused keeps the proposal waiting, with the reason on its card.
	refused := func(err error) (talkoot.Proposal, error) {
		p = waiting
		p.Problem = err.Error()
		if serr := talkoot.SaveProposal(run.dir, p); serr != nil {
			w.diagf("talkoot %s: could not record why proposal %s was refused: %v", id, p.ID, serr)
		}
		w.talkootEmit(talkootEvent{Talkoot: id, Kind: "inbox", Wire: new(ctrlproto.TalkootInboxEvent(id, proposalCard(id, p)))})
		return p, err
	}
	text, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: read the roster: %w", err)
	}
	// The base check guards the proposal as made. A person's edit is written
	// against the roster as it is now, and proposalPreview checks it there.
	ops := p.Ops
	if edit {
		ops = edited
	} else if talkoot.RosterRevision(text) != p.Base {
		return refused(fmt.Errorf("%w; decline it, or approve it with its operations edited against the roster as it is now", talkoot.ErrProposalStale))
	}
	changes, next, nr, err := w.proposalPreview(id, text, ops, p.Persona)
	if err != nil {
		if edit {
			// The person's own edit failed. The proposal as made has no new
			// problem.
			return talkoot.Proposal{}, err
		}
		return refused(err)
	}
	// A drafted persona is written only when the operations that apply still
	// name it. A person's edit can drop it, and then no file is written.
	writeDraft := opsUseDraft(ops, p.Persona)
	if writeDraft {
		if !w.libraryTrusted() {
			return refused(errors.New("talkoot: the approval writes a persona, and writing a persona requires a trusted workspace"))
		}
		if taken := personaTaken(p.Persona); taken != "" {
			return refused(fmt.Errorf("talkoot: the persona name %q is now taken by %s; decline this proposal, and ask the recruiter for another name", p.Persona.Name, taken))
		}
		// 🚨 The record is a file, so it can change after propose checked it.
		// The write checks the draft again, as propose did.
		if !strings.HasPrefix(p.Proposer, talkoot.RecruiterPrefix) {
			return refused(fmt.Errorf("talkoot: only a recruiter proposes a new persona, and this proposal came from %s", p.Proposer))
		}
		if d, err := checkPersonaDraft(p.Persona.Text, ops); err != nil {
			return refused(err)
		} else if d.Name != p.Persona.Name {
			return refused(fmt.Errorf("talkoot: the persona text names %q, and the proposal names %q", d.Name, p.Persona.Name))
		}
	}
	// 🔑 The record says approved before the roster changes, so every roster
	// change from a proposal has an approved record behind it, and its undo
	// works. A record that does not save changes nothing. A roster that then
	// fails puts the record back to pending.
	p.Status, p.Changes, p.Edited, p.Problem = talkoot.ProposalApproved, changes, edit, ""
	if edit {
		p.Applied = ops
	}
	if err := talkoot.SaveProposal(run.dir, p); err != nil {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the approval of proposal %s could not be recorded, so the roster is unchanged: %w", p.ID, err)
	}
	unapprove := func() {
		if rerr := talkoot.SaveProposal(run.dir, waiting); rerr != nil {
			// ⚠️ The record says approved over a roster that did not change.
			// Its undo changes no member, so the daemon refuses it.
			w.diagf("talkoot %s: proposal %s was not applied, and its record could not go back to pending: %v", id, p.ID, rerr)
		}
	}
	// The persona goes in before the roster that names it, so a roster never
	// names a persona the library does not hold.
	var written string
	if writeDraft {
		if written, err = writePersonaDraft(p.Persona); err != nil {
			// A file the library cannot read still holds the path, and the
			// name check above does not see it. refused puts the record back.
			return refused(fmt.Errorf("%w; the roster is unchanged", err))
		}
	}
	if _, err := w.applyRosterLocked(ctx, run, by, next, nr, rosterSource{proposal: p.ID, proposer: p.Proposer, edited: edit}); err != nil {
		if written != "" {
			if rerr := os.Remove(written); rerr != nil {
				// ⚠️ The file now holds the name, so the next approval would
				// refuse it as taken. The card names the file to remove.
				w.diagf("talkoot %s: proposal %s was not applied, and the persona it wrote could not be removed: %v", id, p.ID, rerr)
				return refused(fmt.Errorf("talkoot: the roster change failed (%v), and the persona file %s it wrote could not be removed; remove that file before you approve again", err, written))
			}
		}
		unapprove()
		return talkoot.Proposal{}, err
	}
	if written != "" {
		w.broadcastLibraryChanged()
	}
	resolved()
	return p, nil
}

// proposalCard is a pending proposal as the inbox shows it.
func proposalCard(id string, p talkoot.Proposal) ctrlproto.TalkootCard {
	wp := wireProposal(p)
	return ctrlproto.TalkootCard{Member: p.Proposer, Kind: ctrlproto.TalkootCardProposal, ID: p.ID, At: p.At, Proposal: &wp}
}

// proposalCards returns the pending proposals of the talkoot in dir as cards.
// A store that does not read shows no cards, and the inbox still lists the
// rest.
func (w *Workspace) proposalCards(id, dir string) []ctrlproto.TalkootCard {
	pending, err := w.pendingProposals(id, dir)
	if err != nil {
		w.diagf("talkoot %s: the proposals do not read: %v", id, err)
		return nil
	}
	out := make([]ctrlproto.TalkootCard, 0, len(pending))
	for _, p := range pending {
		out = append(out, proposalCard(id, p))
	}
	return out
}

func wireProposal(p talkoot.Proposal) ctrlproto.TalkootProposal {
	out := ctrlproto.TalkootProposal{
		ID: p.ID, Proposer: p.Proposer, At: p.At, Title: proposalTitle(p), Summary: p.Summary, Why: p.Why,
		Class: string(talkoot.ClassOfOps(p.Ops)), Undoes: p.Undoes, Envelope: p.Envelope,
		Ops: make([]ctrlproto.TalkootOp, 0, len(p.Ops)), Changes: wireChanges(p.Changes),
		Status: p.Status, DecidedBy: p.DecidedBy, DecidedAt: p.DecidedAt, Reason: p.Reason,
		Edited: p.Edited, Problem: p.Problem,
	}
	if isMemberProposer(p.Proposer) {
		out.SelfAuthority = talkoot.SelfAuthority(p.Proposer, p.Ops)
	}
	if p.Persona != nil {
		out.Persona = &ctrlproto.TalkootPersonaDraft{Name: p.Persona.Name, Text: p.Persona.Text}
	}
	for _, op := range p.Ops {
		out.Ops = append(out.Ops, ctrlproto.TalkootOp{Op: op.Op, Member: op.Member, Set: op.Set})
	}
	for _, op := range p.Applied {
		out.Applied = append(out.Applied, ctrlproto.TalkootOp{Op: op.Op, Member: op.Member, Set: op.Set})
	}
	return out
}

// proposalTitle says who proposes what. A member that changes its own
// authority is named first, so a person sees it before the details
// (decision 0025).
func proposalTitle(p talkoot.Proposal) string {
	switch {
	case p.Proposer == "" && p.Status == talkoot.ProposalPending:
		return "proposal " + p.ID + " does not read"
	case p.Proposer == "":
		return "proposal " + p.ID + ", whose record did not read, was " + p.Status
	case isMemberProposer(p.Proposer) && talkoot.SelfAuthority(p.Proposer, p.Ops):
		return p.Proposer + " proposes a change to its own authority: " + p.Summary
	case strings.HasPrefix(p.Proposer, talkoot.RecruiterPrefix) && p.Persona != nil:
		return "recruiter session " + strings.TrimPrefix(p.Proposer, talkoot.RecruiterPrefix) + " proposes, with the new persona " + p.Persona.Name + ": " + p.Summary
	case strings.HasPrefix(p.Proposer, talkoot.RecruiterPrefix):
		return "recruiter session " + strings.TrimPrefix(p.Proposer, talkoot.RecruiterPrefix) + " proposes: " + p.Summary
	case p.Undoes != "":
		return p.Proposer + " proposes an undo: " + p.Summary
	}
	return p.Proposer + " proposes: " + p.Summary
}

// isMemberProposer reports whether a proposer is a member, and not a person or
// a recruiter.
func isMemberProposer(proposer string) bool {
	return !strings.HasPrefix(proposer, talkoot.HumanPrefix) && !strings.HasPrefix(proposer, talkoot.RecruiterPrefix)
}

func wireChanges(cs []talkoot.MemberChange) []ctrlproto.TalkootMemberChange {
	out := make([]ctrlproto.TalkootMemberChange, 0, len(cs))
	for _, c := range cs {
		out = append(out, ctrlproto.TalkootMemberChange{
			Member: c.Member, Before: wireEntry(c.Before), After: wireEntry(c.After),
			Widens: talkoot.Widens(c), Authority: talkoot.AuthorityChanges(c),
		})
	}
	return out
}

func wireEntry(m *talkoot.Member) *ctrlproto.TalkootMemberEntry {
	if m == nil {
		return nil
	}
	return &ctrlproto.TalkootMemberEntry{
		ID: m.ID, Role: m.Role, Title: m.Title, Mark: wireMark(m.Mark), Persona: m.Persona, Driver: m.Driver, Model: m.Model,
		Tier: m.Tier, Posture: m.Posture, Workspace: m.Workspace, Reviewer: m.Reviewer,
		BudgetUSDPerDay: m.BudgetUSDPerDay, TurnsPerDay: m.TurnsPerDay, Tools: m.Tools,
	}
}

func wireOps(in []ctrlproto.TalkootOp) []talkoot.Op {
	if in == nil {
		return nil
	}
	out := make([]talkoot.Op, 0, len(in))
	for _, op := range in {
		out = append(out, talkoot.Op{Op: op.Op, Member: op.Member, Set: op.Set})
	}
	return out
}

// Propose submits a proposal from the seat's member. The seat names the
// proposer, so a member never proposes as another.
func (s talkootSeat) Propose(ops []talkoot.Op, undo, why string) (talkoot.Proposal, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return talkoot.Proposal{}, errSeatRevoked
	}
	return s.w.talkootPropose(s.b.run.id, s.b.member, ops, undo, why, "", func() error {
		if s.b.retired.Load() {
			return errSeatRevoked
		}
		return nil
	})
}
