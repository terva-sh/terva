package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// RaatiBoardHook lets the host mirror an agent-convened deliberation
// onto its live board (the web raati pane). Begin claims the board —
// false means another deliberation is showing and this run proceeds
// unwatched; Event folds coordinator events; End releases the claim
// with the run's operational error, if any. seatOrder is the resolved
// pool→seat policy, so the board can say how the seats were dealt.
type RaatiBoardHook interface {
	Begin(question, class, binding, seatOrder string) bool
	Event(ev raati.Event)
	End(err error)
}

// RaatiConveneTool lets the main agent convene a deliberation panel
// (docs/proposals/raati-deliberation.md) on one decisive question and
// block for the verdict + minority report. Opt-in via the user
// config's raati.convene_tool — a convening spends real sub-agent
// turns — and deliberately left unclassified in permissions, so it
// always hits the approval gate before spending.
type RaatiConveneTool struct {
	// Engine runs the panel. Nil means deliberation isn't available in
	// this mode and the tool always errors.
	Engine raati.Engine

	// Enabled reads the live opt-in at call time (like SwarmSpawnTool),
	// so flipping the config off mid-session refuses the next call
	// without rebuilding the agent. Nil = disabled.
	Enabled func() bool

	// HostProvider / HostModel seat rigor level 0 and anchor the level-1
	// ladder.
	HostProvider string
	HostModel    string

	// Tiers is the user's per-provider tier ladder (level 1).
	Tiers SwarmTierMap

	// Level2 is the user's cross-provider seat list (level 2).
	Level2 []raati.Binding

	// SeatOrder and SeatMap are the user's pool→seat policy
	// (Config.Raati); empty SeatOrder means the shuffled-per-convene
	// default. Config-governed, not model-facing: the agent asks for a
	// deliberation, the operator decides how the seats are dealt.
	SeatOrder raati.SeatOrder
	SeatMap   []int

	// Profiles are the user's named convening bundles (raati.profiles).
	// The agent selects one BY NAME — the description enumerates them —
	// and the profile fills every argument the call left unset. Seat
	// composition only ever comes from here or the config: a profile
	// may pin the panel, the calling agent may not.
	Profiles map[string]raati.Profile

	// Board, when set, mirrors the deliberation onto the host's board.
	Board RaatiBoardHook

	// Persist, when set, writes the finished record and returns its path.
	Persist func(*raati.Result) (string, error)

	// Answer, when set, is the inquiry clerk (rung 1: answers strictly
	// from the question + evidence the tool call itself supplied). Nil
	// hosts leave inquire unsupported.
	Answer func(ctx context.Context, question, evidence string, qs []raati.Inquiry) []raati.Inquiry

	// RoundTimeout bounds each round; zero uses the coordinator default.
	RoundTimeout time.Duration
}

// raatiConveneArgs is the wire shape. Level and the bool knobs are
// pointers so "the call said nothing" is distinguishable from "the
// call said 0/false" — a selected profile fills only the former.
type raatiConveneArgs struct {
	Question    string `json:"question"`
	Profile     string `json:"profile,omitempty"`
	Class       string `json:"class,omitempty"`
	Level       *int   `json:"level,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	SingleRound *bool  `json:"single_round,omitempty"`
	Inquire     *bool  `json:"inquire,omitempty"`
	Converge    *bool  `json:"converge,omitempty"`
}

const raatiConveneSchema = `{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "The ONE decisive question before the panel, phrased so approve/reject is meaningful. Panelists have no tools and no context from this conversation — the question plus evidence must stand alone."
    },
    "profile": {
      "type": "string",
      "description": "Named convening profile from the user's config (raati.profiles); the tool description lists what is configured and what each is for. A profile supplies defaults for level, class, and deliberation shape, and may pin the panel's seats; anything you set explicitly in this call overrides the profile — except seat composition, which is config-owned."
    },
    "class": {
      "type": "string",
      "enum": ["advisory", "gate", "veto"],
      "description": "Decision class. advisory (default): majority decides, dissent attached. gate: unanimity to pass, fails closed on any dissent, abstention, or missing unit — use when the user asked for a hard check. veto: majority, but the benevolence seat can block."
    },
    "level": {
      "type": "integer",
      "enum": [0, 1, 2],
      "description": "Rigor level. 0 (default): all seats on this session's model — cheapest, but the judgments are CORRELATED (same weights, different priors); fine for triage. 1: the provider's weak/medium/strong ladder. 2: three different providers — real independence; needs raati.level2 in the user config."
    },
    "evidence": {
      "type": "string",
      "description": "The decision-relevant material, inlined (diffs, logs, constraints, prior decisions). Do not assume the panel can see anything you haven't put here."
    },
    "single_round": {
      "type": "boolean",
      "description": "Skip the cross-examination round: blind ballots are final. Roughly halves cost and time, at the price of no rebuttal — for quick triage only."
    },
    "inquire": {
      "type": "boolean",
      "description": "Let panelists pose up to two questions each; a clerk answers between rounds STRICTLY from the evidence you supplied (one extra model pass). Questions the evidence cannot answer are recorded as open — supply better evidence instead of enabling this to paper over gaps."
    },
    "converge": {
      "type": "boolean",
      "description": "Permit ONE extra reveal round, run only if cross-examination flipped a verdict — stabilizes mutual revisions. Never resolves an escalated split; costs up to three more sub-agent turns when triggered."
    }
  },
  "required": ["question"]
}`

func (t *RaatiConveneTool) Name() string { return "raati_convene" }

func (t *RaatiConveneTool) Description() string {
	d := "Convene a three-unit deliberation panel (a raati) on ONE decisive question and wait for its verdict. Three panelists with deliberately different priors — truth, consequence, human impact — deliberate blind, cross-examine, and cast ballots tallied under the decision class. EXPENSIVE AND SLOW: roughly six sub-agent model turns and minutes of wall clock. Convene only when independent judgment changes what you do next: a high-stakes or hard-to-reverse decision, a genuinely ambiguous call, or a gate the user asked to be checked. Never for routine choices, questions the evidence at hand already answers, or repeatedly on the same question. A split verdict is information, not failure — ALWAYS read the minority report before acting, and if the panel posed questions, treat open ones as unmet evidence: reconvene with answers, don't re-roll the same packet. An 'escalated' decision means the panel could not decide: take it to the user, don't re-roll it."
	if len(t.Profiles) == 0 {
		return d
	}
	// Enumerate the user's convening profiles so the agent can choose
	// by purpose. The description is the whole selection surface: the
	// agent names a profile, the config decides what that means.
	var sb strings.Builder
	sb.WriteString(d)
	sb.WriteString(" Configured convening profiles (pass one as 'profile'; pick by what it is for):")
	for _, name := range raati.ProfileNames(t.Profiles) {
		sb.WriteString(" [")
		sb.WriteString(raati.ProfileLine(name, t.Profiles[name]))
		sb.WriteString("]")
	}
	sb.WriteString(".")
	return sb.String()
}

func (t *RaatiConveneTool) Schema() json.RawMessage { return json.RawMessage(raatiConveneSchema) }

func (t *RaatiConveneTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	if t.Enabled == nil || !t.Enabled() {
		return toolErr("raati_convene: deliberation is not enabled (user config raati.convene_tool)"), nil
	}
	if t.Engine == nil {
		return toolErr("raati_convene: no deliberation engine in this mode"), nil
	}
	var a raatiConveneArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	question := strings.TrimSpace(a.Question)
	if question == "" {
		return toolErr("raati_convene: question is required"), nil
	}

	// A profile fills what the call left unset; explicit args win.
	// Seat composition is the exception — it only ever comes from the
	// profile or the config, never from the calling agent.
	var prof raati.Profile
	if name := strings.TrimSpace(a.Profile); name != "" {
		var err error
		if prof, err = raati.ProfileFor(t.Profiles, name); err != nil {
			return toolErr("raati_convene: " + err.Error()), nil
		}
	}
	classArg := strings.TrimSpace(a.Class)
	if classArg == "" {
		classArg = prof.Class
	}
	class, err := raati.ParseClass(classArg)
	if err != nil {
		return toolErr("raati_convene: " + err.Error()), nil
	}
	level := 0
	if a.Level != nil {
		level = *a.Level
	} else if v, ok, viaAuto := prof.PickLevel(HighestRaatiLevel(t.HostProvider, t.Tiers, t.Level2, len(raati.DefaultPanel()))); ok {
		if err := RefuseCorrelatedGate(a.Profile, class, v, viaAuto); err != nil {
			return toolErr("raati_convene: " + err.Error()), nil
		}
		level = v
	}
	singleRound := prof.SingleRound != nil && *prof.SingleRound
	if a.SingleRound != nil {
		singleRound = *a.SingleRound
	}
	converge := prof.Converge != nil && *prof.Converge
	if a.Converge != nil {
		converge = *a.Converge
	}
	// The tool's inquiry clerk is rung 1 only; a profile asking for
	// "convener" degrades to the record clerk here, the same degrade
	// the board applies when there is no session to ask.
	inquire := false
	switch prof.Inquire {
	case "", "record", "convener":
		inquire = prof.Inquire != ""
	default:
		return toolErr(fmt.Sprintf("raati_convene: profile %q: inquire must be record or convener, got %q", a.Profile, prof.Inquire)), nil
	}
	if a.Inquire != nil {
		inquire = *a.Inquire
	}
	level2 := t.Level2
	if len(prof.Seats) > 0 {
		if err := prof.ValidSeats(len(raati.DefaultPanel())); err != nil {
			return toolErr(fmt.Sprintf("raati_convene: profile %q: %s", a.Profile, err.Error())), nil
		}
		level2 = prof.Seats
	}

	pool, err := ResolveRaatiBindings(level, t.HostProvider, t.HostModel, t.Tiers, level2, len(raati.DefaultPanel()))
	if err != nil {
		return toolErr("raati_convene: " + err.Error()), nil
	}

	// The panel judges what it is shown; a silently clipped exhibit
	// would be a lie by omission, so truncation is disclosed to it.
	const evidenceCap = 32 * 1024
	evidence := a.Evidence
	if len(evidence) > evidenceCap {
		evidence = evidence[:evidenceCap] + "\n[evidence truncated at 32KiB — the panel judges what it was shown]"
	}

	seatOrder, seatMap := t.SeatOrder, t.SeatMap
	if prof.SeatOrder != "" {
		seatOrder = raati.SeatOrder(prof.SeatOrder)
	}
	if len(prof.SeatMap) > 0 {
		seatMap = prof.SeatMap
	}
	resolvedOrder, err := raati.ParseSeatOrder(string(seatOrder))
	if err != nil {
		return toolErr("raati_convene: " + err.Error()), nil
	}
	watched := t.Board != nil && t.Board.Begin(question, string(class), RaatiLevelName(level), string(resolvedOrder))
	cfg := raati.Config{
		Engine:       t.Engine,
		Class:        class,
		Bindings:     pool,
		SeatOrder:    seatOrder,
		SeatMap:      seatMap,
		RoundTimeout: t.RoundTimeout,
		SingleRound:  singleRound,
		Progress:     progress,
	}
	if converge {
		cfg.MaxRounds = 3
	}
	if inquire && t.Answer != nil {
		question, evidence := question, evidence
		cfg.AnswerInquiries = func(ctx context.Context, qs []raati.Inquiry) []raati.Inquiry {
			return t.Answer(ctx, question, evidence, qs)
		}
	}
	if watched {
		cfg.OnEvent = t.Board.Event
	}
	res, err := raati.Convene(ctx, cfg, question, evidence)
	if watched {
		t.Board.End(err)
	}
	if err != nil {
		return toolErr("raati_convene: " + err.Error()), nil
	}

	recordPath := ""
	if t.Persist != nil {
		recordPath, _ = t.Persist(res) // best-effort; the verdict stands without it
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: raatiToolReport(res, recordPath)}},
		Details: map[string]any{
			"decision": string(res.Outcome.Decision),
			"rule":     res.Outcome.Rule,
			"degraded": res.Outcome.Degraded,
			"minority": len(res.Outcome.Minority),
		},
	}, nil
}

// raatiToolReport renders the verdict for the convening agent: the
// decision, the tally, every seat's final ballot, the minority report,
// and the inquiry docket. The last two are the parts the description
// orders the agent to read: dissent says why the panel disagreed, and
// open inquiries say what evidence was missing — the difference between
// re-rolling the same packet and reconvening with a better one.
func raatiToolReport(res *raati.Result, recordPath string) string {
	var sb strings.Builder
	o := res.Outcome
	fmt.Fprintf(&sb, "verdict: %s (rule %s; %d approve / %d reject / %d abstain / %d absent)\n",
		strings.ToUpper(string(o.Decision)), o.Rule, o.Tally.Approve, o.Tally.Reject, o.Tally.Abstain, o.Tally.Absent)
	if o.Degraded {
		sb.WriteString("degraded: not every unit voted\n")
	}
	var seats []string
	for _, u := range res.Units {
		if u.Provider != "" || u.Model != "" {
			seats = append(seats, fmt.Sprintf("%s=%s/%s", u.Name, u.Provider, u.Model))
		}
	}
	if len(seats) > 0 {
		fmt.Fprintf(&sb, "seats: %s\n", strings.Join(seats, "  "))
	}
	for _, b := range res.Final {
		if b.Absent {
			fmt.Fprintf(&sb, "- %s: absent (%s)\n", b.Unit, b.Rationale)
			continue
		}
		fmt.Fprintf(&sb, "- %s: %s (%.2f) — %s\n", b.Unit, b.Verdict, b.Confidence, b.Rationale)
	}
	if len(o.Minority) > 0 {
		sb.WriteString("minority report (weigh this before acting):\n")
		for _, m := range o.Minority {
			fmt.Fprintf(&sb, "  %s: %s\n", m.Unit, m.Rationale)
		}
	}
	if len(res.Inquiries) > 0 {
		sb.WriteString("the panel asked:\n")
		open := 0
		for _, q := range res.Inquiries {
			fmt.Fprintf(&sb, "  %s: %s\n", q.Unit, q.Question)
			if q.Source == raati.SourceUnanswered {
				open++
				sb.WriteString("    open — the record does not answer this; the panel decided with this gap\n")
				continue
			}
			fmt.Fprintf(&sb, "    answer (%s): %s\n", q.Source, q.Answer)
		}
		if open > 0 {
			sb.WriteString("open questions are unmet evidence — reconvening with answers beats re-rolling the same packet\n")
		}
	}
	if o.Decision == "escalated" {
		sb.WriteString("the panel could not decide — surface this to the user rather than re-rolling\n")
	}
	if recordPath != "" {
		fmt.Fprintf(&sb, "record: %s\n", recordPath)
	}
	return sb.String()
}
