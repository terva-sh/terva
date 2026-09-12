// Package groom is tier 5's surfacing pass. It reads a pool of draft tickets
// and reports which ones look promotable, with reasons, and what is not ready
// with why.
//
// It never changes a ticket, and that is structural rather than disciplined.
// This package takes a projection of each draft and never a store handle, so
// no code path here can write one. TKT-01M29G8ZSF criterion 2 asks for that
// guarantee, and a package that cannot reach a store is a stronger answer than
// a package that chooses not to.
//
// It mirrors packages/agent/classifier: a ready client comes in, so the pass
// stays testable with no provider and no credential.
package groom

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/modelreply"
	"terva.sh/terva/packages/provider"
)

// DefaultTimeout bounds one pass. It is longer than the classifier's eight
// seconds because this reads a whole pool rather than one call, and nothing
// waits on it: a groom run is not in anybody's turn.
const DefaultTimeout = 90 * time.Second

// maxTokens caps the reply. The report is a list of ids with one sentence
// each, so this is sized for a pool in the low hundreds.
const maxTokens = 4096

// ExcerptBytes caps the description text taken from each draft.
//
// Measured against this repository's store on 2026-09-11, at 79 drafts: the
// draft files total 364 KB, of which descriptions are 270 KB. The median
// description is 1797 bytes, the p90 is 4956, and a single outlier is 35876.
// Sending the corpus whole is roughly 68k tokens at four bytes per token, and
// that one outlier would contribute half a percent of the whole call by itself.
//
// At 600 bytes the projection lands near 47 KB, about 12k tokens, a fifth of
// the corpus, and the outlier stops dominating. Six hundred bytes is roughly a
// paragraph, which is where a ticket says what it is before it says how.
//
// The token figures are a budget and not a tokenizer reading. Report.SentBytes
// carries what the pass actually sent, so a caller can hold the estimate
// against a real usage number rather than trusting the arithmetic here.
const ExcerptBytes = 600

// Draft is one ticket as the pass sees it. It is deliberately a projection and
// not a ticket: it carries no status, no claim, and no way back to the store.
type Draft struct {
	ID       string
	Title    string
	Labels   []string
	Priority string
	// Criteria is how many acceptance criteria the draft carries. A draft
	// with none has not been thought through yet.
	Criteria int
	// HasPlan reports an implementation plan. Measured on 2026-09-11, seven
	// of 79 drafts had one, which makes it the sharpest readiness signal in
	// this store today.
	HasPlan bool
	// Excerpt is the first ExcerptBytes of the description.
	Excerpt string
}

// Excerpt truncates s to ExcerptBytes on a rune boundary, so a multi-byte
// character is never cut in half and handed to a model as mojibake.
func Excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= ExcerptBytes {
		return s
	}
	cut := s[:ExcerptBytes]
	// Drop only a trailing PARTIAL rune. Walking back to the last rune-start
	// byte instead is the obvious move and it is wrong: a cut that lands
	// exactly on a rune boundary ends in a continuation byte, so that walk
	// strips a complete rune down to its leading byte and produces the
	// mojibake it was written to prevent. DecodeLastRuneInString reports
	// (RuneError, 1) only for an actually invalid tail.
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + "..."
}

// Finding is one entry in a report. Title comes from the pool and never from
// the model, so a report cannot attach a real id to an invented title.
type Finding struct {
	ID     string
	Title  string
	Reason string
}

// Report is what one pass produced.
type Report struct {
	// Candidates are drafts the pass thinks are worth a person's attention.
	Candidates []Finding
	// NotReady are drafts it looked at and set aside, with why.
	NotReady []Finding
	// Scanned is how many drafts went into the call.
	Scanned int
	// SentBytes is the size of the projection actually sent, which is the
	// measurement ExcerptBytes estimates.
	SentBytes int
	// Unknown counts ids the model named that were not in the pool. A model
	// that invents ids is a model whose reasons are worth less, so this is
	// reported rather than swallowed.
	Unknown int
}

// Options configures a Pass.
type Options struct {
	// Client and Model are the resolved model. A caller is expected to have
	// resolved something cheap, the weak swarm_tiers rung, the same way the
	// classifier does.
	Client provider.Client
	Model  string
	// Reasoning is the effort. Empty means off.
	Reasoning string
	// Timeout bounds one pass; 0 means DefaultTimeout.
	Timeout time.Duration
	// Logf, when set, receives one line per failure.
	Logf func(format string, args ...any)
}

// Pass is one surfacing pass backed by a single model call.
type Pass struct{ opts Options }

// New returns a Pass, or nil when it has nothing to run on.
func New(opts Options) *Pass {
	if opts.Client == nil || strings.TrimSpace(opts.Model) == "" {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Pass{opts: opts}
}

// systemPrompt is the pass's whole instruction, overridable from the prompts
// catalog as groom.system.
//
// The JSON field names below are PARSED, not read: candidates, not_ready, id
// and reason. A translation that renames them makes every reply unparseable,
// and this pass would then report nothing while looking like it ran.
const systemPrompt = `You read a pool of DRAFT tickets and say which ones a person should look at now.

You are surfacing, not deciding. You never promote anything. Your report goes
to a person who then decides, so your reasons matter more than your verdicts.

Return exactly one JSON object, no prose around it:
{"candidates":[{"id":"...","reason":"..."}],"not_ready":[{"id":"...","reason":"..."}]}

candidates: the draft is ready for a person to weigh now. Good signs are a
clear statement of the work, acceptance criteria, an implementation plan, and a
trigger that has already fired. A draft that duplicates another is a candidate
when merging them is the action to take, and say so in the reason.

not_ready: you looked and set it aside. Say what is missing, specifically. "Too
vague" is not a reason. "Names no acceptance criteria and its description does
not say what would change" is.

Use only ids from the pool you were given. Do not invent one. Do not repeat an
id in both lists. You may leave a draft out of both lists entirely when you
have nothing useful to say about it.

Be sparing with candidates. A list naming most of the pool has told the reader
nothing, and the point of this pass is to be worth reading.`

// Run makes one pass over drafts.
//
// A nil Pass, an empty pool, or a failed call all return a Report rather than
// an error where they can, because an empty report that says why is useful and
// an error that loses the scan count is not.
func (p *Pass) Run(ctx context.Context, drafts []Draft) (Report, error) {
	rep := Report{Scanned: len(drafts)}
	if p == nil {
		return rep, fmt.Errorf("no model is configured for the surfacing pass")
	}
	if len(drafts) == 0 {
		// Not an error. A store with no drafts is a healthy store, and the
		// caller renders "nothing to groom" from Scanned == 0.
		return rep, nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()

	pool := renderPool(drafts)
	rep.SentBytes = len(pool)

	var zero float32
	stream, err := p.opts.Client.Stream(ctx, provider.Request{
		Model:        p.opts.Model,
		System:       i18n.P("groom.system", systemPrompt),
		MaxTokens:    maxTokens,
		Reasoning:    p.opts.Reasoning,
		ReasoningSet: true,
		// The same pool should groom the same way twice. A reader comparing
		// two runs is trying to see what changed in the store, not what
		// changed in the sampler.
		Temperature: &zero,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: pool}},
			Time:    time.Now(),
		}},
	})
	if err != nil {
		return rep, p.fail("stream: %v", err)
	}

	var sb strings.Builder
	for e := range stream {
		switch t := e.(type) {
		case provider.EventTextDelta:
			sb.WriteString(t.Delta)
		case provider.EventDone:
			if t.Err != nil {
				return rep, p.fail("stream: %v", t.Err)
			}
		}
	}

	obj, ok := modelreply.LastJSONObject(sb.String())
	if !ok {
		return rep, p.fail("no JSON object in reply %q", truncate(sb.String(), 200))
	}
	var parsed struct {
		Candidates []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"candidates"`
		NotReady []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"not_ready"`
	}
	if err := json.Unmarshal([]byte(obj), &parsed); err != nil {
		return rep, p.fail("unparseable report %q: %v", truncate(obj, 200), err)
	}

	byID := make(map[string]Draft, len(drafts))
	for _, d := range drafts {
		byID[d.ID] = d
	}
	seen := make(map[string]bool, len(drafts))

	for _, c := range parsed.Candidates {
		if f, ok := resolve(byID, seen, c.ID, c.Reason); ok {
			rep.Candidates = append(rep.Candidates, f)
		} else {
			rep.Unknown++
		}
	}
	for _, n := range parsed.NotReady {
		if f, ok := resolve(byID, seen, n.ID, n.Reason); ok {
			rep.NotReady = append(rep.NotReady, f)
		} else {
			rep.Unknown++
		}
	}
	return rep, nil
}

// resolve turns a model-named id into a Finding, refusing one that was not in
// the pool and one that has already been placed. A duplicate id in both lists
// is the model contradicting itself, and the first placement wins.
func resolve(byID map[string]Draft, seen map[string]bool, id, reason string) (Finding, bool) {
	id = strings.TrimSpace(id)
	d, ok := byID[id]
	if !ok || seen[id] {
		return Finding{}, false
	}
	seen[id] = true
	return Finding{ID: d.ID, Title: d.Title, Reason: strings.TrimSpace(reason)}, true
}

// renderPool is what the model reads. Ids are sorted so two runs over an
// unchanged store send byte-identical input, which is what makes the zero
// temperature above worth setting.
func renderPool(drafts []Draft) string {
	sorted := make([]Draft, len(drafts))
	copy(sorted, drafts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	var sb strings.Builder
	for _, d := range sorted {
		fmt.Fprintf(&sb, "id: %s\ntitle: %s\n", d.ID, d.Title)
		if len(d.Labels) > 0 {
			fmt.Fprintf(&sb, "labels: %s\n", strings.Join(d.Labels, ", "))
		}
		fmt.Fprintf(&sb, "priority: %s\ncriteria: %d\nplan: %t\n", d.Priority, d.Criteria, d.HasPlan)
		if d.Excerpt != "" {
			fmt.Fprintf(&sb, "excerpt: %s\n", d.Excerpt)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// fail logs and returns. A groom run reports its failure rather than
// abstaining, because unlike a classifier nothing downstream is waiting to
// make a decision without it.
func (p *Pass) fail(format string, args ...any) error {
	if p.opts.Logf != nil {
		p.opts.Logf("groom: "+format, args...)
	}
	return fmt.Errorf(format, args...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
