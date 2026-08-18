package workspace

// A paid A/B over the two next-step asks — the measurement `scripts/eval`
// cannot make.
//
// WHY IT IS HERE AND NOT THERE. That harness drives `terva --json <prompt>`, an
// agent loop in print mode, and scores tool calls and final answers. But
// `suggest.next_step` has exactly two callers, the TUI's animation tick and its
// /nextstep handler, so no scenario prompt can reach the ask at all. There is
// no arm for that harness to score. Rather than run six unrelated scenarios and
// present a green table as evidence about a prompt none of them touch, the
// probe goes where the code is.
//
// WHAT MAKES IT AN HONEST A/B. Everything except the one sentence under test is
// shared by construction: the same session, the same transcript, the same
// system prompt, the same model, the same cap, the same reasoning-off request —
// because both arms are the SAME CALL with `OnDemand` flipped. The param is the
// arm switch, so there is no overlay to stage, no second binary, and no way for
// the arms to differ anywhere else. `--verify-only`'s job in the shell harness
// (proving the arms differ where you meant and nowhere else) is done here by
// the type system plus the free mode below.
//
// It calls the REAL SuggestNextStep. A probe that reassembled the request from
// the same constants would measure a copy of the code and pass while shipping
// prose the model never sees — the same class of error as scraping a
// description out of source instead of capturing it from the running binary.
//
// Run it:
//
//	scripts/eval/nextstep-ab.sh --verify     # free: prints both asks, no model call
//	scripts/eval/nextstep-ab.sh              # paid: ~42 short completions
//
// Skipped unless TERVA_EVAL_NEXTSTEP_AB is set, so `just test` never spends.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The two arms. Named rather than boolean at the call site, because a table of
// results headed "true / false" tells a reader nothing a month later.
var nextStepArms = []struct {
	name   string
	params ctrlproto.NextStepParams
}{
	{"volunteered", ctrlproto.NextStepParams{}},
	{"asked", ctrlproto.NextStepParams{OnDemand: true}},
}

// nextStepProbe is one seeded conversation. Each ends where a next step is
// plainly available but not taken, because a scenario the model cannot answer
// measures the scenario rather than the prompt — and an empty reply is a
// legitimate answer to "if no next step is obvious", so a transcript with no
// obvious next step would score silence as failure.
type nextStepProbe struct {
	id string
	// turns alternate user, assistant, user, assistant…
	turns []string
	why   string
}

var nextStepProbes = []nextStepProbe{
	{
		id: "diagnosed-not-fixed",
		turns: []string{
			"the build is broken, can you look?",
			"`go build ./...` fails in packages/agent/tools/edit.go:212 — `undefined: normalizePath`. The function was renamed to `resolveWorkspacePath` in the last commit, and this one call site was missed.",
		},
		why: "the fix is named and untaken; the obvious next line is to apply it",
	},
	{
		id: "written-not-run",
		turns: []string{
			"add a test for the CSV serializer's quoting",
			"Added `TestSerializerQuotesEmbeddedCommas` in packages/export/csv_test.go, covering an embedded comma, an embedded quote, and a trailing newline. I have not run it yet.",
		},
		why: "the untaken step is running what was just written",
	},
	{
		id: "half-migrated",
		turns: []string{
			"move the session store off the global mutex",
			"Converted `sessions.go` to per-session locks. `store_index.go` and `store_gc.go` still take the old global lock, so the two schemes coexist right now — I stopped to check the plan before touching the GC path.",
		},
		why: "the model asked to check in; the user's next line is a decision",
	},
}

// A completion is scored on what the MODEL produced and on what the USER would
// get, kept apart on purpose. firstLine salvages a model that ignores "one
// line" by taking its opening line, so scoring only the salvaged result would
// certify a prompt that has stopped working: the user still sees one tidy line
// while the ask is being disobeyed underneath. The eval harness learned this
// same lesson as "the call and the answer are scored apart".
var (
	// Assistant voice where the user's own voice belongs. The ask says "write it
	// as the user would type it to you", and this is what ignoring that looks
	// like — a reply ABOUT the next step rather than the next step.
	reAssistantVoice = regexp.MustCompile(`(?i)^(i'?ll |i will |i'?d |i can |i have |sure[,.! ]|certainly|here'?s |here is |let me |you (should|could|might|can) |the (user|next step) )`)
	// Markup of any kind: a list marker, a heading, a fence, a wrapping quote.
	// The composer takes a plain line; anything else arrives as literal junk.
	reMarkup = regexp.MustCompile("^([-*+#>]|\\d+[.)])\\s|```|^\"|^'|^\\*\\*")
)

type nextStepCriterion struct {
	name string
	// ok scores one completion: raw is everything the model produced, line is
	// what SuggestNextStep would hand the composer, and stop is why the provider
	// stopped generating.
	ok func(raw, line string, stop provider.StopReason) bool
}

var nextStepCriteria = []nextStepCriterion{
	{"offered", func(_, line string, _ provider.StopReason) bool { return line != "" }},
	// 🚨 The criterion this probe was missing, and the reason its first run was
	// worthless. Every one of gemini-flash-latest's severed fragments — "Fix it
	// and run the", "Mig", "Update store_" — passed all four criteria below: a
	// truncated fragment IS one line, IS short, IS in the user's voice and
	// carries no markup. 42/42 green, and what reached the composer was the
	// opening of a sentence. Scoring the shape of an answer certifies a prompt
	// while the user gets nothing, which is the same failure scripts/eval records
	// as scoring the call and never the final answer.
	{"not cut off", func(_, _ string, stop provider.StopReason) bool {
		return stop != provider.StopLength
	}},
	{"one line", func(raw, _ string, _ provider.StopReason) bool {
		// The RAW text, which is the only place a violation is visible.
		return len(strings.Fields(strings.TrimSpace(raw))) > 0 &&
			!strings.Contains(strings.TrimSpace(raw), "\n")
	}},
	{"user voice", func(_, line string, _ provider.StopReason) bool {
		return line != "" && !reAssistantVoice.MatchString(strings.TrimSpace(line))
	}},
	{"no markup", func(_, line string, _ provider.StopReason) bool {
		return line != "" && !reMarkup.MatchString(strings.TrimSpace(line))
	}},
	{"short", func(_, line string, _ provider.StopReason) bool {
		// "One short line." The daemon's hard cap is 240 runes; a line over 120
		// is not what the ask asked for even though it fits.
		n := utf8.RuneCountInString(strings.TrimSpace(line))
		return n > 0 && n <= 120
	}},
}

// recordingClient tees a real provider's stream so the probe sees the raw
// completion while SuggestNextStep consumes it normally. It records the
// REQUEST too, which is what lets the free mode prove what went out.
type recordingClient struct {
	provider.Client
	mu     sync.Mutex
	raws   []string
	reqs   []provider.Request
	usages []provider.Usage
	stops  []provider.StopReason
}

func (c *recordingClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	in, err := c.Client.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		var sb strings.Builder
		var usage provider.Usage
		var stop provider.StopReason
		for ev := range in {
			switch e := ev.(type) {
			case provider.EventTextDelta:
				sb.WriteString(e.Delta)
			case provider.EventUsage:
				usage = e.Usage
			case provider.EventDone:
				stop = e.Stop
			}
			out <- ev
		}
		c.mu.Lock()
		c.raws = append(c.raws, sb.String())
		c.usages = append(c.usages, usage)
		c.stops = append(c.stops, stop)
		c.mu.Unlock()
	}()
	return out, nil
}

func (c *recordingClient) lastRaw() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.raws) == 0 {
		return ""
	}
	return c.raws[len(c.raws)-1]
}

// lastUsage is how a truncated answer is told apart from a short one. A model
// whose OUTPUT tokens sit at the cap while its visible text is five words spent
// the budget somewhere the probe cannot see — reasoning — and what reached the
// composer is the beginning of a sentence, not a suggestion.
func (c *recordingClient) lastUsage() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.usages) == 0 {
		return provider.Usage{}
	}
	return c.usages[len(c.usages)-1]
}

// lastStop is the provider's own account of why it stopped, which is the only
// sound way to tell a finished answer from a severed one.
//
// A token-count heuristic was tried first and had to be thrown away. It read
// "output tokens near the cap" as truncation, which was right on
// gemini-flash-latest (197 tokens, empty text) and WRONG on deepseek-v4-pro,
// where completions ran to 212, 323 and 500 output tokens — past the cap, and
// carrying whole sentences. The heuristic invented a 10-point difference between
// the arms out of its own false positives.
func (c *recordingClient) lastStop() provider.StopReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.stops) == 0 {
		return ""
	}
	return c.stops[len(c.stops)-1]
}

// liveNextStepSession seeds one probe's conversation against a given client.
//
// The credential must already be resolved before this runs: chatTestWorkspace
// redirects TERVA_HOME to a scratch directory, and a Resolve after that finds
// no credentials at all.
func liveNextStepSession(t *testing.T, p nextStepProbe, cl provider.Client, prov, model, system string) (*Workspace, *wsSession) {
	t.Helper()
	w, s, _ := chatTestWorkspace(t, "s-"+p.id)
	ag := core.NewAgent(cl, model, system, core.Registry{})
	var msgs []provider.Message
	for i, text := range p.turns {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		msgs = append(msgs, provider.Message{
			Role:    role,
			Content: []provider.Content{provider.TextBlock{Text: text}},
			Time:    time.Now(),
		})
	}
	ag.SetMessages(msgs)
	s.agent = ag
	// The request's model comes from the SESSION, not from the agent.
	s.mu.Lock()
	s.provider, s.model = prov, model
	s.mu.Unlock()
	return w, s
}

// TestLiveNextStepAskAB is the probe. Two modes, one of which is free.
func TestLiveNextStepAskAB(t *testing.T) {
	mode := os.Getenv("TERVA_EVAL_NEXTSTEP_AB")
	if mode == "" {
		t.Skip("live A/B: set TERVA_EVAL_NEXTSTEP_AB=1 (spends real money) or =verify (free)")
	}

	if mode == "verify" {
		verifyNextStepArms(t)
		return
	}

	// Resolve FIRST — chatTestWorkspace moves TERVA_HOME out from under this.
	prov, model := os.Getenv("TERVA_EVAL_PROVIDER"), os.Getenv("TERVA_EVAL_MODEL")
	r, err := build.Resolve(build.Args{Provider: prov, Model: model, CWD: testsupport.TempDir(t)}, true)
	if err != nil {
		t.Fatalf("resolve a credential: %v\n(log in first, or set TERVA_EVAL_PROVIDER / TERVA_EVAL_MODEL)", err)
	}
	reps := 7
	if v := os.Getenv("TERVA_EVAL_NEXTSTEP_REPS"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			reps = n
		}
	}
	t.Logf("provider=%s model=%s probes=%d reps=%d arms=2 => %d completions",
		r.Provider, r.Model, len(nextStepProbes), reps, len(nextStepProbes)*reps*2)

	// tally[probe][arm][criterion] = passes, and scored[probe][arm] = completions
	// that came back at all. The second is what keeps a failed run from being
	// reported as a measured one.
	type key struct{ probe, arm, crit string }
	tally := map[key]int{}
	scored := map[key]int{}
	var log []map[string]any
	var okRuns, failed int

	for _, p := range nextStepProbes {
		for _, arm := range nextStepArms {
			for rep := range reps {
				rec := &recordingClient{Client: r.NewClient()}
				w, _ := liveNextStepSession(t, p, rec, r.Provider, r.Model, r.SystemPrompt)
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				got, askErr := w.SuggestNextStep(ctx, "s-"+p.id, arm.params)
				cancel()
				if askErr != nil {
					failed++
					t.Errorf("[%s/%s rep %d] ask failed: %v", p.id, arm.name, rep, askErr)
					// Stop early when nothing has EVER worked. A misconfigured
					// provider otherwise fails identically 42 times and then prints a
					// full table of zeroes, which reads like a measurement rather
					// than an authentication problem. Two strikes rather than one, so
					// a transient blip on the very first call does not abort a run.
					if okRuns == 0 && failed >= 2 {
						t.Fatalf("aborting after %d failed calls with no successful one: "+
							"nothing was measured. check the credential for provider %q "+
							"(or pass --provider/--model)", failed, r.Provider)
					}
					continue
				}
				okRuns++
				scored[key{p.id, arm.name, ""}]++
				raw := rec.lastRaw()
				u := rec.lastUsage()
				stop := rec.lastStop()
				for _, c := range nextStepCriteria {
					if c.ok(raw, got.Line, stop) {
						tally[key{p.id, arm.name, c.name}]++
					}
				}
				// Counts stay numbers: a scorer reading this file should be able to
				// average out_tokens without coercing a string first.
				log = append(log, map[string]any{
					"probe": p.id, "arm": arm.name, "rep": rep,
					"line": got.Line, "raw": raw,
					"out_tokens": u.OutputTokens,
					"in_tokens":  u.InputTokens,
					"stop":       string(stop),
				})
			}
		}
	}

	// The report. Every criterion for every probe, both arms side by side, so a
	// regression cannot hide inside an average.
	if okRuns == 0 {
		t.Fatal("no completion came back at all: there is nothing to score, and a table of zeroes is not a result")
	}
	t.Log("")
	t.Logf("%-22s %-12s %11s %11s   %s", "probe", "criterion", "volunteered", "asked", "verdict")
	t.Log(strings.Repeat("-", 78))
	for _, p := range nextStepProbes {
		na := scored[key{p.id, "volunteered", ""}]
		nb := scored[key{p.id, "asked", ""}]
		if na == 0 || nb == 0 {
			// Void, never averaged in as a zero — the same rule scripts/eval applies
			// to a scenario whose tool was never called.
			t.Logf("%-22s %-12s %11s %11s   no scorable run (%d/%d completions)",
				p.id, "—", "—", "—", na+nb, reps*2)
			continue
		}
		for _, c := range nextStepCriteria {
			a := tally[key{p.id, "volunteered", c.name}]
			b := tally[key{p.id, "asked", c.name}]
			t.Logf("%-22s %-12s %8d/%-2d %8d/%-2d   %s",
				p.id, c.name, a, na, b, nb, nextStepVerdictN(a, na, b, nb))
		}
	}
	t.Log("")
	for _, c := range nextStepCriteria {
		var a, b, na, nb int
		for _, p := range nextStepProbes {
			a += tally[key{p.id, "volunteered", c.name}]
			b += tally[key{p.id, "asked", c.name}]
			na += scored[key{p.id, "volunteered", ""}]
			nb += scored[key{p.id, "asked", ""}]
		}
		t.Logf("TOTAL %-12s  volunteered %d/%d   asked %d/%d   %s",
			c.name, a, na, b, nb, nextStepVerdictN(a, na, b, nb))
	}
	if failed > 0 {
		t.Logf("")
		t.Logf("%d of %d calls failed and are excluded from every denominator above",
			failed, len(nextStepProbes)*reps*2)
	}

	// Every completion goes to disk. A table with no transcripts behind it
	// cannot be re-read when someone doubts it later.
	outDir := filepath.Join("..", "..", "..", ".eval", "nextstep-ab")
	if err := os.MkdirAll(outDir, 0o755); err == nil {
		path := filepath.Join(outDir, fmt.Sprintf("run-%d.json", time.Now().Unix()))
		body, _ := json.MarshalIndent(map[string]any{
			"provider": r.Provider, "model": r.Model, "reps": reps, "runs": log,
		}, "", "  ")
		if err := os.WriteFile(path, body, 0o644); err == nil {
			t.Logf("completions written to %s", path)
		}
	}
}

// nextStepVerdictN borrows scripts/eval's honesty about small samples: at these
// counts a one- or two-run gap is noise, and "both perfect" means the probe had
// no power to detect a regression either.
//
// The two arms carry their own denominators because a failed call is excluded
// rather than counted as a miss, so the arms can end up with different numbers
// of scorable runs. Comparing raw counts across unequal denominators would
// invent a difference, so the comparison is on rates.
func nextStepVerdictN(a, na, b, nb int) string {
	if na == 0 || nb == 0 {
		return "no scorable run"
	}
	ra, rb := float64(a)/float64(na), float64(b)/float64(nb)
	switch {
	case a == na && b == nb:
		return "no signal (both 100%)"
	case ra == rb:
		return "no change"
	case rb > ra:
		return fmt.Sprintf("improvement +%.0fpp", 100*(rb-ra))
	default:
		return fmt.Sprintf("REGRESSION -%.0fpp", 100*(ra-rb))
	}
}

// verifyNextStepArms is the free mode: it proves what each arm actually sends,
// with a fake client, before anyone pays to find out.
func verifyNextStepArms(t *testing.T) {
	t.Helper()
	asks := map[string]string{}
	for _, arm := range nextStepArms {
		w, _, cl := nextStepSession(t, "s-verify-"+arm.name, "run the tests")
		if _, err := w.SuggestNextStep(context.Background(), "s-verify-"+arm.name, arm.params); err != nil {
			t.Fatalf("[%s] ask: %v", arm.name, err)
		}
		req := cl.lastReq(t)
		asks[arm.name] = messageText(req.Messages[len(req.Messages)-1])
	}

	a, b := asks["volunteered"], asks["asked"]
	pa, pb := strings.SplitN(a, "\n\n", 2), strings.SplitN(b, "\n\n", 2)
	if len(pa) != 2 || len(pb) != 2 {
		t.Fatalf("expected two paragraphs per ask; got %d and %d", len(pa), len(pb))
	}
	t.Log("")
	t.Log("arm volunteered, paragraph 1:")
	t.Logf("  %s", pa[0])
	t.Log("arm asked, paragraph 1:")
	t.Logf("  %s", pb[0])
	t.Log("")
	if pa[1] != pb[1] {
		t.Fatalf("the arms differ in the REQUEST paragraph too, so a result could not be attributed to the provenance sentence:\n  %q\n  %q", pa[1], pb[1])
	}
	t.Logf("paragraph 2 is byte-identical across the arms (%d bytes) — the arms differ in the provenance sentence and nowhere else", len(pa[1]))
	if pa[0] == pb[0] {
		t.Fatal("the arms open identically: there is nothing to measure")
	}
	t.Log("0 model calls. verified.")
}
