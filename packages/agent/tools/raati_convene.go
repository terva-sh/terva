package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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

	// SpareHost reads the live raati.spare_host knob at call time: seat
	// auto-resolved panels off the session's provider account so panel
	// traffic does not evict the session's provider-side prompt cache
	// (SpareHostLadder). Nil = off.
	SpareHost func() bool

	// HostProvider / HostModel seat rigor level 0 and anchor the level-1
	// ladder.
	HostProvider string
	HostModel    string
	// hostMu guards SetHost's live mutation of HostProvider/HostModel on a
	// mid-session model swap; Execute reads them through host(). Construction
	// sets the fields directly, before the tool is registered.
	hostMu sync.RWMutex

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

// SetHost updates the host provider/model that seats rigor level 0 and
// anchors the level-1 ladder. Safe to call while a turn runs: a mid-session
// model swap mutates this live, and Execute reads through host() under the
// read lock. Implements HostRouted.
func (t *RaatiConveneTool) SetHost(provider, model string) {
	t.hostMu.Lock()
	defer t.hostMu.Unlock()
	t.HostProvider = provider
	t.HostModel = model
}

func (t *RaatiConveneTool) host() (provider, model string) {
	t.hostMu.RLock()
	defer t.hostMu.RUnlock()
	return t.HostProvider, t.HostModel
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
      "description": "The one decisive question for the panel. Write the question so that approval or rejection has a meaning. The panelists have no tools and no context from this conversation. Therefore the question and the evidence must be complete without other information."
    },
    "profile": {
      "type": "string",
      "description": "A named convening profile from the configuration of the user, in raati.profiles. The description of this tool lists the configured profiles and the purpose of each one. A profile gives defaults for the level, the class, and the form of the deliberation, and it can also set the seats of the panel. A value that you give in this call replaces the value from the profile, but the configuration always controls the seats. Give a profile with the question and the evidence only. A value that the profile already sets gives no advantage. An explicit level also stops the profile, and the profile then cannot seat the strongest panel that the configuration permits."
    },
    "class": {
      "type": "string",
      "enum": ["advisory", "gate", "veto"],
      "description": "The decision class. With advisory, which is the default, the majority decides and the tool attaches the dissent. With gate, all the panelists must agree, and the result is a failure if a unit dissents, abstains, or is absent. Use gate when the user asks for a strict check. With veto, the majority decides, but the benevolence seat can stop the decision."
    },
    "level": {
      "type": "integer",
      "enum": [0, 1, 2],
      "description": "The rigor level. Omit this field when you give a profile, because the profile then selects the level. The profile seats the highest rigor that the configuration of the user permits. You cannot see that configuration, so an explicit level can only make the panel weaker, or ask for a level that does not exist and cause an error. Give this field only when you must force one level. Level 0 is the default without a profile, and all the seats use the model of this session. Level 0 is the cheapest, but the judgments correlate, because the seats have the same weights and different priors. Level 0 is sufficient for triage. Level 1 uses the weak, medium, and strong models of the provider. Level 2 uses three different providers and gives true independence, and it needs raati.level2 in the configuration of the user."
    },
    "evidence": {
      "type": "string",
      "description": "The material for the decision, written in full in this field: the diffs, the logs, the constraints, and the earlier decisions. The panel sees only the material that you put here."
    },
    "single_round": {
      "type": "boolean",
      "description": "Do not run the cross-examination round, and make the first ballots final. This decreases the cost and the time by approximately one half, but no panelist can answer another panelist. Use this field for quick triage only."
    },
    "inquire": {
      "type": "boolean",
      "description": "Permit each panelist to ask a maximum of two questions. A clerk answers them between the rounds, and uses only the evidence that you supplied. This needs one more model pass. The tool records as open each question that the evidence cannot answer. Give better evidence instead of this field when the evidence has a gap."
    },
    "converge": {
      "type": "boolean",
      "description": "Permit one more reveal round. The tool runs this round only if the cross-examination changed a verdict, and the round makes mutual changes stable. This round never corrects a split that the panel escalated. The round costs a maximum of three more sub-agent turns when it runs."
    }
  },
  "required": ["question"]
}`

func (t *RaatiConveneTool) Name() string { return "raati_convene" }

// raatiConveneDesc is the English default for tool.raati_convene.description:
// the part that is true whatever the user has configured.
const raatiConveneDesc = "Convene a deliberation panel of three units, which is a raati, on one decisive question, and wait for its verdict. The three panelists have different priors: truth, consequence, and human impact. They deliberate without knowledge of each other, then cross-examine, then cast ballots. The tool counts the ballots under the decision class.\n\n" +
	"This tool is expensive and slow. It uses approximately six sub-agent model turns and some minutes of time.\n\n" +
	"Convene a panel only when an independent judgment changes your next action. Examples are a decision with a large effect, a decision that is difficult to reverse, a decision that is truly ambiguous, and a check that the user asks for. Do not convene a panel for a routine choice. Do not convene a panel for a question that your evidence answers. Do not convene a panel more than one time on the same question.\n\n" +
	"A split verdict is information and not a failure. Always read the minority report before you act. If the panel asks questions, an open question shows that the evidence is not sufficient. Convene the panel again with the answers, and do not send the same evidence again.\n\n" +
	"An escalated decision means that the panel could not decide. Give such a decision to the user, and do not convene the panel again.\n\n" +
	"If the tool refuses to convene a panel, or if the convening fails, no panel ran. Say this each time that you report the decision. Never say that a panel reviewed the work if no panel ran."

// raatiProfilesDesc is the addendum shown only when the user has configured
// convening profiles. Its single %s is the rendered profile list.
//
// It is a SECOND key rather than English glue appended to the first, because
// glue is what makes a keyed entry untranslatable and unoverridable: an
// operator who rewrote the base description would find this paragraph still
// in the shipped English, welded on after their text.
const raatiProfilesDesc = "\n\nTo convene a panel by profile, give a profile with the question and the evidence, and omit 'level'. A profile selects the level itself, and it seats the highest rigor that the configuration of the user permits. You cannot see that configuration. Therefore an explicit level can only make the panel weaker, or ask for a level that does not exist. These are the configured convening profiles. Select one by its purpose: %s."

func (t *RaatiConveneTool) Description() string {
	d := i18n.D("tool.raati_convene.description", raatiConveneDesc)
	if len(t.Profiles) == 0 {
		return d
	}
	// Enumerate the user's convening profiles so the agent can choose
	// by purpose. The description is the whole selection surface: the
	// agent names a profile, the config decides what that means.
	var sb strings.Builder
	for _, name := range raati.ProfileNames(t.Profiles) {
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString("[")
		sb.WriteString(raati.ProfileLine(name, t.Profiles[name]))
		sb.WriteString("]")
	}
	return d + i18n.D("tool.raati_convene.profiles", raatiProfilesDesc, sb.String())
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
	hostProvider, hostModel := t.host()
	// The ladder provider level 1 rides: the host's own unless the user
	// asked panels to spare the session's account (raati.spare_host).
	// Auto-level resolves against the same choice, so sparing can seat a
	// real off-account level 1 where the host alone would degrade to a
	// correlated level 0.
	ladderProv := SpareHostLadder(hostProvider, t.SpareHost != nil && t.SpareHost(), t.Tiers, t.Level2)
	level, viaAuto := 0, false
	if a.Level != nil {
		level = *a.Level
	} else if v, ok, auto := prof.PickLevel(HighestRaatiLevel(ladderProv, t.Tiers, t.Level2, len(raati.DefaultPanel()))); ok {
		level, viaAuto = v, auto
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

	pool, err := ResolveRaatiBindings(level, hostProvider, hostModel, ladderProv, t.Tiers, level2, len(raati.DefaultPanel()))
	if err != nil {
		return toolErr("raati_convene: " + err.Error()), nil
	}
	// The gate honesty check reads the resolved SEATS, not the level: a
	// level whose ladder is one model at three efforts is a real advisory
	// panel and not three independent judges.
	if err := RefuseCorrelatedGate(a.Profile, class, pool, viaAuto); err != nil {
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
	resolvedOrder, err := raati.SeatOrderFor(string(seatOrder), pool)
	if err != nil {
		return toolErr("raati_convene: " + err.Error()), nil
	}
	watched := t.Board != nil && t.Board.Begin(question, string(class), RaatiLevelName(level), string(resolvedOrder))
	// The live view for every surface whose window on a tool call is its
	// progress string. It folds the SAME event feed the web board folds, so
	// the two cannot drift into disagreeing about what the panel is doing.
	board := newRaatiLiveBoard(RaatiLevelName(level), string(class), string(resolvedOrder), raati.DefaultPanel(), progress)
	cfg := raati.Config{
		Engine:       t.Engine,
		Class:        class,
		Bindings:     pool,
		SeatOrder:    seatOrder,
		SeatMap:      seatMap,
		RoundTimeout: t.RoundTimeout,
		SingleRound:  singleRound,
		// Narration goes THROUGH the board rather than around it: both feeds
		// describe the same deliberation, and only one of them can be what a
		// surface shows.
		Progress: board.Narrate,
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
	cfg.OnEvent = board.Event
	if watched {
		hook := t.Board
		cfg.OnEvent = func(ev raati.Event) {
			board.Event(ev)
			hook.Event(ev)
		}
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
		Content: []provider.Content{provider.TextBlock{Text: raatiToolReport(res, pool, recordPath)}},
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
// re-rolling the same packet and reconvening with a better one. The
// resolved pool rides along because the record's units don't carry
// thinking effort: the correlation caveat has to come from the seats
// as they were actually dealt, in the transcript itself — a warning
// only the convening agent's judgment carries is a warning the next
// compaction loses.
func raatiToolReport(res *raati.Result, pool []raati.Binding, recordPath string) string {
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
	// Correlation cuts one way: a correlated panel finding a gap found a
	// real gap, but a correlated panel agreeing is one model agreeing
	// with itself, and an approval is exactly when the agent wants to
	// stop looking.
	if len(pool) > 1 && raati.SameWeights(pool) {
		if raati.SameEffort(pool) {
			sb.WriteString("correlated panel: every seat is the same model, so the priors differ but the judgment does not — weigh an approval as one opinion, not three\n")
		} else {
			sb.WriteString("one-model panel: the seats differ only in thinking effort — a real advisory spread, but not independent judges; weigh an approval accordingly\n")
		}
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
		// The reconvene coaching is for verdicts the open questions might
		// have changed. On an approval it once talked an agent into buying
		// a whole second panel to turn 3-approve into 3-approve — there the
		// cheap honest move is answering the question in the decision
		// record, not re-convening.
		if open > 0 {
			if o.Decision == "approved" {
				sb.WriteString("the panel approved despite these — answer them in your decision record; reconvene only if an answer could plausibly flip a ballot\n")
			} else {
				sb.WriteString("open questions are unmet evidence — reconvening with answers beats re-rolling the same packet\n")
			}
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
