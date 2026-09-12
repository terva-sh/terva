package agent

// `terva ticket groom`, tier 5's surfacing pass. TKT-01M29G8ZSF.
//
// The verb is terva's and not git-ticket's, so runTicketIn intercepts it before
// argv reaches the embedded CLI, which would refuse a word it does not know.
//
// Output here is not run through i18n, which matches the rest of the
// `terva ticket` surface: ticketcmd.go and ticketinitactor.go both print raw.
// The one translatable string in this feature is the system prompt, and it
// lives in the groom package behind i18n.P like the classifier's.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/groom"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/agent/tools"
)

// groomVerb is the word runTicketIn intercepts.
const groomVerb = "groom"

// newGroomPassFn is the seam a test replaces to drive the whole command with
// no provider and no credential. Production always uses newGroomPass.
var newGroomPassFn = newGroomPass

// groomPass is what the command needs from the surfacing pass. It is an
// interface so a test drives the whole command with no provider and no
// credential.
type groomPass interface {
	Run(ctx context.Context, drafts []groom.Draft) (groom.Report, error)
}

// runTicketGroom handles `terva ticket groom`. It returns the process exit
// status.
func runTicketGroom(dir string, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("terva ticket groom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	storePath := fs.String("store", "", "path to the .tickets store")
	write := fs.Bool("note", false, "record the report as a note on each candidate")
	fs.Usage = func() {
		fmt.Fprint(stderr, `read the draft pool and report which drafts are worth a person's attention

usage: terva ticket groom [--note] [--store PATH]

It never changes a ticket's status. With no --note it writes nothing at all,
which is what makes it safe to run from a timer.

flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	s, err := openGroomStore(dir, *storePath)
	if err != nil {
		fmt.Fprintln(stderr, "terva ticket groom:", err)
		return 1
	}

	ctx := context.Background()
	drafts, err := s.List(ctx, ticket.Filter{Status: []string{ticket.StatusDraft}})
	if err != nil {
		fmt.Fprintln(stderr, "terva ticket groom:", err)
		return 1
	}

	// An empty pool is answered before any model is resolved. A store with no
	// drafts must not need a credential, and it must not cost a call.
	if len(drafts) == 0 {
		renderGroomReport(stdout, groom.Report{})
		return 0
	}

	pass, err := newGroomPassFn(dir, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "terva ticket groom:", err)
		return 1
	}

	rep, err := pass.Run(ctx, projectDrafts(drafts))
	if err != nil {
		fmt.Fprintln(stderr, "terva ticket groom:", err)
		return 1
	}
	renderGroomReport(stdout, rep)

	if *write {
		if n, err := recordGroomNotes(ctx, s, rep); err != nil {
			// The report is already printed and correct. A failed note is
			// worth saying so, and it is not worth discarding the run.
			fmt.Fprintf(stderr, "terva ticket groom: recorded %d of %d notes: %v\n", n, len(rep.Candidates), err)
			return 1
		} else if n > 0 {
			fmt.Fprintf(stdout, "\nRecorded a note on %d candidate(s).\n", n)
		}
	}
	return 0
}

// openGroomStore opens the named store, or discovers the one governing dir.
func openGroomStore(dir, storePath string) (*ticket.Store, error) {
	if strings.TrimSpace(storePath) != "" {
		return ticket.Open(storePath)
	}
	return ticket.Discover(dir)
}

// projectDrafts turns store tickets into the bounded projection the pass
// reads. Nothing here carries a status, a claim, or a way back to the store:
// the pass is handed facts and cannot reach the ticket they came from.
func projectDrafts(ts []*ticket.Ticket) []groom.Draft {
	out := make([]groom.Draft, 0, len(ts))
	for _, t := range ts {
		if t == nil {
			continue
		}
		out = append(out, groom.Draft{
			ID:       t.ID,
			Title:    t.Title,
			Labels:   t.Labels,
			Priority: t.Priority,
			Criteria: countChecklist(t.Body.AcceptanceCriteria),
			HasPlan:  strings.TrimSpace(t.Body.ImplementationPlan) != "",
			Excerpt:  groom.Excerpt(t.Body.Description),
		})
	}
	return out
}

// countChecklist counts the checkbox lines in a rendered section. The store
// writes "- [ ]" and "- [x]", so both shapes count: how many criteria a draft
// has is the question, not how many are ticked.
func countChecklist(section string) int {
	n := 0
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- [") {
			n++
		}
	}
	return n
}

// newGroomPass resolves the model and builds the pass.
//
// The weak swarm_tiers rung is the default, the same ladder the classifier and
// swarm_spawn use. Where nothing resolves this falls back to the host model
// and SAYS SO, which is a deliberate departure from the classifier: that one
// refuses, because it runs on every gated call for a whole session and a
// silent fallback rebuilds an invisible per-call bill. A groom run is one call
// a person asked for, so the useful answer is the report plus a warning about
// what it cost, not a refusal.
func newGroomPass(dir string, stderr io.Writer) (groomPass, error) {
	r, err := build.Resolve(build.Args{Mode: mode.Print, CWD: dir}, true)
	if err != nil {
		return nil, fmt.Errorf("resolving a model: %w", err)
	}
	if !r.HasCredential() {
		return nil, fmt.Errorf("no credential for provider %q, so the pass cannot run", r.Provider)
	}

	// 🪤 The tier pick carries a reasoning effort, and groom takes the model
	// from it but not the effort. Either reason alone settles this.
	//
	// A pass that reads titles and 600-byte excerpts and answers with a list of
	// ids has nothing to reason about, so thinking tokens on the weak rung are
	// money spent for no better report. And the pass asks for temperature 0 so
	// that two runs over one pool stay comparable, which Anthropic refuses beside
	// enabled thinking. That pair is an http 400, and it is why this command
	// first shipped unable to complete a single call.
	//
	// The effort stays empty, and groom.Run sends ReasoningSet alongside it, so
	// this is an explicit off rather than an absent choice. That distinction
	// matters: a model with its own DefaultReasoning would otherwise turn every
	// groom run into a thinking turn.
	model, reasoning := r.Model, ""
	cfg, cerr := config.LoadConfig()
	if cerr == nil {
		if pick := tools.ResolveSwarmTier(r.Provider, r.Model, "weak", build.SwarmTierMap(cfg.SwarmTiers)); pick.Model != "" {
			model = pick.Model
		}
	}
	if model == r.Model {
		fmt.Fprintf(stderr, "terva ticket groom: no weak tier resolves for provider %q, so this runs on the host model %q at host price; set swarm_tiers.%s.weak (see `terva models tiers`)\n", r.Provider, r.Model, r.Provider)
	}

	p := groom.New(groom.Options{
		Client:    r.NewClient(),
		Model:     model,
		Reasoning: reasoning,
	})
	if p == nil {
		return nil, fmt.Errorf("could not build a pass for %q/%q", r.Provider, model)
	}
	return p, nil
}

// recordGroomNotes appends the report to each candidate, and returns how many
// landed. It writes a note and nothing else: no status moves here, and there
// is no code path in this file that moves one.
func recordGroomNotes(ctx context.Context, s *ticket.Store, rep groom.Report) (int, error) {
	n := 0
	for _, c := range rep.Candidates {
		text := fmt.Sprintf("**Surfaced by `terva ticket groom`.** %s\n\nThis is an advisory note from a weak-tier pass. It moves nothing, and a person still decides whether this draft is promoted.", c.Reason)
		if _, err := s.Apply(ctx, c.ID, ticket.AppendNote{Text: text}, ticket.ApplyOptions{}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// renderGroomReport writes the report. Every shape says something: an empty
// store, a scan that found nothing, and a scan with findings are three
// different answers, and empty output would be indistinguishable from a crash.
func renderGroomReport(w io.Writer, rep groom.Report) {
	if rep.Scanned == 0 {
		fmt.Fprintln(w, "No drafts in this store, so there is nothing to groom.")
		return
	}

	fmt.Fprintf(w, "Scanned %d draft(s), %d bytes sent.\n", rep.Scanned, rep.SentBytes)

	if len(rep.Candidates) == 0 && len(rep.NotReady) == 0 {
		fmt.Fprintf(w, "\nThe pass named no candidates and set nothing aside. That is an answer, not\nan error: it looked at all %d and had nothing useful to say.\n", rep.Scanned)
	}

	if len(rep.Candidates) > 0 {
		fmt.Fprintf(w, "\nWorth a look now (%d):\n", len(rep.Candidates))
		for _, c := range rep.Candidates {
			fmt.Fprintf(w, "  %s  %s\n      %s\n", c.ID, c.Title, c.Reason)
		}
	}
	if len(rep.NotReady) > 0 {
		fmt.Fprintf(w, "\nNot ready, and why (%d):\n", len(rep.NotReady))
		for _, c := range rep.NotReady {
			fmt.Fprintf(w, "  %s  %s\n      %s\n", c.ID, c.Title, c.Reason)
		}
	}
	if rep.Unknown > 0 {
		fmt.Fprintf(w, "\n%d entry(ies) named an id that is not in the pool, or repeated one, and were\ndropped. Treat the reasons above with more caution than usual.\n", rep.Unknown)
	}

	// The bound that groom.ExcerptBytes aims at, measured against the provider's
	// own count rather than against a bytes-per-token estimate. Printed on every
	// run, so the figure can never drift from the real one without a reader
	// seeing it. Zero when the provider reported no usage, and then it is better
	// to print nothing than to print a confident 0.
	if n := rep.Usage.PromptTokens(); n > 0 {
		fmt.Fprintf(w, "\nSent %d bytes of projection inside a %d-token prompt (%d new, %d from cache,\n%d written to cache). Reply %d token(s), $%.4f.\n",
			rep.SentBytes, n, rep.Usage.InputTokens, rep.Usage.CacheReadTokens,
			rep.Usage.CacheWriteTokens, rep.Usage.OutputTokens, rep.Usage.CostUSD)
	}

	fmt.Fprintf(w, "\nThis is advisory. A weak model read %d draft(s) and two runs may differ.\nSaying nothing about a draft is not a judgement on it, and nothing here\nchanged a ticket.\n", rep.Scanned)
}
