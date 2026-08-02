package workspace

// The raati surface: the deliberation board (kind="raati",
// docs/proposals/raati-deliberation.md). Unlike the tasks pane, which
// polls the swarm because it has no change feed, the board is pushed
// directly: the raati coordinator's structured event feed
// (raati.Event) folds into a RaatiView under a mutex and every change
// broadcasts a surface_updated to all sessions — the deliberation is
// workspace-global, like the swarm that runs it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/agent/run"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// raatiBoard is the workspace's deliberation state: at most one run at
// a time (a raati is a panel, not a queue), and the last run's result
// stays on the board until the next convening or a reset.
type raatiBoard struct {
	mu      sync.Mutex
	view    ctrlproto.RaatiView
	running bool
	accents map[string]string // unit name → persona accent hex
	// summarize, when set, replaces the real conversation summarizer —
	// a test seam ONLY: the default path resolves live credentials and
	// runs a real model turn, which a unit test must never do.
	summarize func(question, conversation string) (string, error)
	// answer, when set, replaces the real inquiry clerk — the same
	// test-seam rule as summarize.
	answer func(ctx context.Context, question, evidence string, qs []raati.Inquiry) []raati.Inquiry
	// ask, when set, replaces the convener ask (rung 2) — a test seam;
	// the real path surfaces a webAsker dialog on the convening session.
	// Takes the whole batch, one answer back per question.
	ask func(ctx context.Context, s *wsSession, set []core.UserQuestion) ([]core.UserAnswer, error)
}

// raatiView snapshots the board for surface.get, attaching the archive
// list (read fresh — records are small and few, and the pane fetches
// only on pushes).
func (w *Workspace) raatiView() *ctrlproto.RaatiView {
	w.raati.mu.Lock()
	v := w.raati.view
	v.Units = append([]ctrlproto.RaatiUnit(nil), w.raati.view.Units...)
	v.Minority = append([]ctrlproto.RaatiVoice(nil), w.raati.view.Minority...)
	w.raati.mu.Unlock()
	// 50 keeps the client's filter/search useful without paging; records
	// are ~3.5KB and read only on pushes.
	v.History = raatiHistory(50)
	// Profiles read fresh, like the archive: a hand-edited config shows
	// up on the next push without a session rebuild. Names only — the
	// contents apply server-side when a convene names one.
	if uc, err := config.LoadConfig(); err == nil {
		profs := build.RaatiProfiles(uc)
		for _, name := range raati.ProfileNames(profs) {
			v.Profiles = append(v.Profiles, ctrlproto.RaatiProfileInfo{Name: name, Description: profs[name].Description})
		}
	}
	return &v
}

// raatiAction dispatches a board action from a client. s is the
// session the action arrived on — the conversation the "attach this
// conversation" convene option reads from.
func (w *Workspace) raatiAction(s *wsSession, action string, args map[string]string) error {
	switch action {
	case "convene":
		return w.raatiConvene(s, args)
	case "reset":
		w.raati.mu.Lock()
		if w.raati.running {
			w.raati.mu.Unlock()
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a deliberation is in progress"))
		}
		w.raati.view = ctrlproto.RaatiView{}
		w.raati.mu.Unlock()
		w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("raati"))
		return nil
	case "show":
		return w.raatiShow(args["id"])
	}
	return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown raati action %q", action))
}

// raatiSeatOverrides parses the form's per-seat level-2 bindings
// (provider_0/model_0 …). Nil when no seat was chosen (fall back to
// raati.level2); an incomplete panel is an error, never a guess —
// which seat runs which model is the whole point of level 2.
func raatiSeatOverrides(args map[string]string, seats int) ([]raati.Binding, error) {
	out := make([]raati.Binding, seats)
	chosen := 0
	for i := 0; i < seats; i++ {
		p := strings.TrimSpace(args[fmt.Sprintf("provider_%d", i)])
		m := strings.TrimSpace(args[fmt.Sprintf("model_%d", i)])
		if p == "" && m == "" {
			continue
		}
		if p == "" || m == "" {
			return nil, i18n.Errorf("seat %d needs both provider and model", i+1)
		}
		out[i] = raati.Binding{Provider: p, Model: m}
		chosen++
	}
	if chosen == 0 {
		return nil, nil
	}
	if chosen < seats {
		return nil, i18n.Errorf("seat the whole panel or none: %d of %d seats chosen", chosen, seats)
	}
	return out, nil
}

// raatiShow loads a persisted deliberation record onto the board as an
// archived view. Never while a live run holds the board.
func (w *Workspace) raatiShow(id string) error {
	nanos, ok := raatiRecordNanos(id)
	if !ok {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("bad record id %q", id))
	}
	raw, err := os.ReadFile(filepath.Join(config.TervaHome(), "raati", id))
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no record %q", id))
	}
	var res raati.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "record %q: %v", id, err)
	}
	view := raatiViewFromRecord(&res, nanos)

	w.raati.mu.Lock()
	if w.raati.running {
		w.raati.mu.Unlock()
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a deliberation is in progress"))
	}
	w.raati.view = view
	w.raati.mu.Unlock()
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("raati"))
	return nil
}

// raatiRecordNanos parses and validates a record id — strictly the
// build.WriteRaatiRecord shape, so a client-supplied id can never
// traverse paths — returning its timestamp.
func raatiRecordNanos(id string) (int64, bool) {
	rest, ok := strings.CutPrefix(id, "raati-")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ".json")
	if !ok || rest == "" {
		return 0, false
	}
	nanos, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || nanos <= 0 {
		return 0, false
	}
	return nanos, true
}

// raatiHistory lists the newest persisted deliberations for the board's
// archive, newest first. Unparseable records are skipped, not fatal.
func raatiHistory(limit int) []ctrlproto.RaatiHistoryItem {
	entries, err := os.ReadDir(filepath.Join(config.TervaHome(), "raati"))
	if err != nil {
		return nil
	}
	type rec struct {
		id    string
		nanos int64
	}
	var recs []rec
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if nanos, ok := raatiRecordNanos(e.Name()); ok {
			recs = append(recs, rec{e.Name(), nanos})
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].nanos > recs[j].nanos })
	if len(recs) > limit {
		recs = recs[:limit]
	}
	out := make([]ctrlproto.RaatiHistoryItem, 0, len(recs))
	for _, r := range recs {
		raw, err := os.ReadFile(filepath.Join(config.TervaHome(), "raati", r.id))
		if err != nil {
			continue
		}
		var res raati.Result
		if err := json.Unmarshal(raw, &res); err != nil {
			continue
		}
		item := ctrlproto.RaatiHistoryItem{
			ID:       r.id,
			When:     time.Unix(0, r.nanos).UTC().Format(time.RFC3339),
			Question: res.Question,
			Class:    string(res.Class),
			Decision: string(res.Outcome.Decision),
			Degraded: res.Outcome.Degraded,
			Tally: &ctrlproto.RaatiTally{
				Approve: res.Outcome.Tally.Approve,
				Reject:  res.Outcome.Tally.Reject,
				Abstain: res.Outcome.Tally.Abstain,
				Absent:  res.Outcome.Tally.Absent,
			},
		}
		for _, m := range res.Outcome.Minority {
			item.Minority = append(item.Minority, m.Unit)
		}
		out = append(out, item)
	}
	return out
}

// raatiViewFromRecord rebuilds the board view from a persisted record:
// final ballots become the blocks, with the blind verdict badge kept
// where a revision happened.
func raatiViewFromRecord(res *raati.Result, nanos int64) ctrlproto.RaatiView {
	panel := make([]raati.Unit, 0, len(res.Units))
	for _, u := range res.Units {
		panel = append(panel, raati.Unit{Name: u.Name, Persona: u.Persona})
	}
	accents := panelAccents(panel)
	v := ctrlproto.RaatiView{
		Question: res.Question,
		Class:    string(res.Class),
		Archived: true,
		When:     time.Unix(0, nanos).UTC().Format(time.RFC3339),
		Decision: string(res.Outcome.Decision),
		Degraded: res.Outcome.Degraded,
		Tally: &ctrlproto.RaatiTally{
			Approve: res.Outcome.Tally.Approve,
			Reject:  res.Outcome.Tally.Reject,
			Abstain: res.Outcome.Tally.Abstain,
			Absent:  res.Outcome.Tally.Absent,
		},
	}
	for i, u := range res.Units {
		unit := ctrlproto.RaatiUnit{Name: u.Name, Accent: accents[u.Name]}
		if u.Provider != "" || u.Model != "" {
			unit.Binding = u.Provider + "/" + u.Model
		}
		if i < len(res.Final) {
			b := res.Final[i]
			if b.Absent {
				unit.Status = "absent"
				unit.Why = b.Rationale
			} else {
				unit.Status = "voted"
				unit.Verdict = string(b.Verdict)
				unit.Confidence = b.Confidence
				unit.Rationale = b.Rationale
			}
		}
		if i < len(res.Blind) && !res.Blind[i].Absent {
			unit.Blind = string(res.Blind[i].Verdict)
		}
		v.Units = append(v.Units, unit)
	}
	for _, m := range res.Outcome.Minority {
		v.Minority = append(v.Minority, ctrlproto.RaatiVoice{Unit: m.Unit, Rationale: m.Rationale})
	}
	for _, q := range res.Inquiries {
		v.Inquiries = append(v.Inquiries, ctrlproto.RaatiInquiry{
			Unit: q.Unit, Question: q.Question, Answer: q.Answer, Source: q.Source, Round: q.Round,
		})
	}
	return v
}

// raatiEvidenceSpec is what the convene form submitted alongside the
// question: pasted evidence and/or the conversation-attachment mode,
// assembled into the panel's evidence bundle in the background (the
// summary mode costs a model pass).
type raatiEvidenceSpec struct {
	user         string             // pasted evidence, capped with disclosure
	conversation string             // "" | "summary" | "full"
	messages     []provider.Message // transcript snapshot at convene time
}

// raatiConvene starts a deliberation and returns immediately; progress
// reaches every browser via surface_updated pushes. The run's lifetime
// is bound to w.ctx (never the request), like every background worker.
func (w *Workspace) raatiConvene(s *wsSession, args map[string]string) error {
	if w.swarm == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("no swarm engine"))
	}
	question := strings.TrimSpace(args["question"])
	if question == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: missing question"))
	}
	// Config loads up front: a named profile fills every form field
	// the client omitted, BEFORE any of them parse below.
	uc, _ := config.LoadConfig()
	var prof raati.Profile
	if name := strings.TrimSpace(args["profile"]); name != "" {
		var perr error
		if prof, perr = raati.ProfileFor(build.RaatiProfiles(uc), name); perr != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "convene: %v", perr)
		}
		raatiProfileArgDefaults(args, prof)
	}
	class, err := raati.ParseClass(args["class"])
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	singleRound := args["single_round"] == "true"
	// inquire modes: "" off; "true"/"record" = the clerk answers from
	// the record (rung 1); "convener" additionally surfaces what the
	// record can't answer as ask dialogs on the convening session
	// (rung 2) — which needs a session to ask.
	inquire := strings.TrimSpace(args["inquire"])
	switch inquire {
	case "", "record", "convener":
	case "true":
		inquire = "record"
	case "off":
		// Explicit off: the form sends it to override a profile that
		// sets an inquiry mode (an omitted arg would be filled above).
		inquire = ""
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: inquire must be record or convener"))
	}
	if inquire == "convener" && s == nil {
		inquire = "record"
	}
	maxRounds := 0
	switch strings.TrimSpace(args["rounds"]) {
	case "", "2":
	case "3":
		maxRounds = 3
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: rounds must be 2 or 3"))
	}
	level, levelViaAuto := 0, false
	if v := strings.TrimSpace(args["level"]); v != "" {
		if level, err = strconv.Atoi(v); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: bad level %q", v))
		}
	} else if lvl, ok, auto := prof.PickLevel(tools.HighestRaatiLevel(tools.SpareHostLadder(w.provider, uc.Raati.SpareHost, build.SwarmTierMap(uc.SwarmTiers), build.RaatiLevel2Bindings(uc)), build.SwarmTierMap(uc.SwarmTiers), build.RaatiLevel2Bindings(uc), len(raati.DefaultPanel()))); ok {
		// The profile's level (auto resolves against the workspace's
		// own provider — a form-picked provider override doesn't move
		// the ladder check). The gate honesty check runs once the seats
		// are known, below; an explicit form pick is the human's call.
		level, levelViaAuto = lvl, auto
	}
	spec := raatiEvidenceSpec{user: args["evidence"]}
	switch mode := strings.TrimSpace(args["conversation"]); mode {
	case "", "summary", "full":
		spec.conversation = mode
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: bad conversation mode %q", mode))
	}
	if spec.conversation != "" && s != nil {
		// Snapshot NOW: the panel judges the conversation as it stood
		// when convened, not whatever the session becomes meanwhile.
		spec.messages = s.agent.Messages()
	}

	// Binding overrides from the form: level 0 may pin an exact
	// provider+model for the whole panel; level 1 may pick WHICH
	// provider's tier ladder is seated. Level 2 seats are config-owned.
	prov := strings.TrimSpace(args["provider"])
	model := strings.TrimSpace(args["model"])
	switch level {
	case 0:
		if (prov == "") != (model == "") {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: set provider and model together, or neither"))
		}
	case 1:
		if model != "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: level 1 picks a provider; its tier ladder picks the models"))
		}
	}
	seats := len(raati.DefaultPanel())
	var seatPool []raati.Binding
	if level >= 2 {
		if prov != "" || model != "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("convene: level 2 seats are chosen per seat (or come from raati.level2)"))
		}
		// The form may seat the panel explicitly (provider_N/model_N),
		// overriding the config's raati.level2 for this convening only.
		if seatPool, err = raatiSeatOverrides(args, seats); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
		}
	}
	hostProv, hostModel := w.provider, w.model
	if prov != "" {
		hostProv = prov
	}
	if model != "" {
		hostModel = model
	}

	// Seat the rigor level before claiming the board, so a
	// misconfigured level fails the action without disturbing the pane.
	// Pool precedence: the form's explicit seats, else the profile's
	// pinned seats, else the config's raati.level2.
	level2 := build.RaatiLevel2Bindings(uc)
	if len(prof.Seats) > 0 {
		if verr := prof.ValidSeats(seats); verr != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "convene: profile %q: %v", args["profile"], verr)
		}
		level2 = prof.Seats
	}
	if seatPool != nil {
		level2 = seatPool
	}
	// The ladder provider for level 1: an explicit form pick is the
	// human's call and stands; otherwise raati.spare_host may move the
	// ladder off the session's account (tools.SpareHostLadder).
	ladderProv := hostProv
	if prov == "" {
		ladderProv = tools.SpareHostLadder(hostProv, uc.Raati.SpareHost, build.SwarmTierMap(uc.SwarmTiers), build.RaatiLevel2Bindings(uc))
	}
	pool, err := tools.ResolveRaatiBindings(level, hostProv, hostModel, ladderProv, build.SwarmTierMap(uc.SwarmTiers), level2, seats)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	// Read the resolved SEATS, not the level: a ladder that is one model at
	// several thinking efforts is a real advisory panel and not three
	// independent judges, so it cannot hold an auto-resolved gate.
	if rerr := tools.RefuseCorrelatedGate(args["profile"], class, pool, levelViaAuto); rerr != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "convene: %v", rerr)
	}
	// Seat order: the form's explicit pick wins, else the user config,
	// else the shuffled-per-convene default.
	seatOrderRaw := strings.TrimSpace(args["seat_order"])
	if seatOrderRaw == "" {
		seatOrderRaw = uc.Raati.SeatOrder
	}
	seatOrder, err := raati.SeatOrderFor(seatOrderRaw, pool)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	seatMap := uc.Raati.SeatMap
	if len(prof.SeatMap) > 0 {
		seatMap = prof.SeatMap
	}

	hook := raatiBoardHook{w}
	if !hook.Begin(question, string(class), tools.RaatiLevelName(level), string(seatOrder)) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a deliberation is already in progress"))
	}
	go w.runRaati(hook, raati.Config{
		Engine:      raati.SwarmEngine{Swarm: w.swarm},
		Class:       class,
		Bindings:    pool,
		SeatOrder:   seatOrder,
		SeatMap:     seatMap,
		SingleRound: singleRound,
		MaxRounds:   maxRounds,
		OnEvent:     hook.Event,
	}, s, question, spec, inquire)
	return nil
}

// raatiProfileArgDefaults fills convene-form args the client omitted
// from the named profile — the board half of the precedence rule
// "explicit call beats profile beats config". Args are strings, so
// "unset" is the empty string; the form's checkbox knobs send nothing
// when unchecked, which means a profile that sets one cannot be
// un-set from the form (same limitation as the tool's omitted bools).
// Profile SEATS and LEVEL don't ride the args map: seats replace the
// config's level2 pool directly in raatiConvene (under the form's
// explicit per-seat picks), and level resolves there too — auto levels
// need the config picture, which strings can't carry.
func raatiProfileArgDefaults(args map[string]string, prof raati.Profile) {
	fill := func(k, v string) {
		if v != "" && strings.TrimSpace(args[k]) == "" {
			args[k] = v
		}
	}
	fill("class", prof.Class)
	if prof.SingleRound != nil && *prof.SingleRound {
		fill("single_round", "true")
	}
	fill("inquire", prof.Inquire)
	if prof.Converge != nil && *prof.Converge {
		fill("rounds", "3")
	}
	fill("seat_order", prof.SeatOrder)
}

func (w *Workspace) runRaati(hook raatiBoardHook, cfg raati.Config, s *wsSession, question string, spec raatiEvidenceSpec, inquire string) {
	// The clerk pass takes real time; the board says so instead of
	// sitting silently at "running" with no units.
	if spec.conversation == "summary" && len(spec.messages) > 0 {
		w.setRaatiPhase("briefing")
	}
	evidence := w.assembleRaatiEvidence(question, spec)
	w.setRaatiPhase("")
	if inquire != "" {
		// The clerk answers strictly from the record the panel already
		// judges: the question and the assembled evidence bundle. Closed
		// over here because the bundle only exists after assembly. In
		// convener mode, what the record can't answer is then asked of
		// the convening session (rung 2) before the docket returns.
		answer := w.raati.answer
		if answer == nil {
			answer = w.raatiClerkAnswer
		}
		askConvener := inquire == "convener"
		cfg.AnswerInquiries = func(ctx context.Context, qs []raati.Inquiry) []raati.Inquiry {
			w.setRaatiPhase("inquiry")
			defer w.setRaatiPhase("")
			qs = answer(ctx, question, evidence, qs)
			if askConvener {
				qs = w.raatiAskConvener(ctx, s, question, qs)
			}
			return qs
		}
	}
	res, err := raati.Convene(w.ctx, cfg, question, evidence)
	hook.End(err)

	if err == nil && res != nil {
		if _, werr := build.WriteRaatiRecord(res); werr != nil {
			w.diag(i18n.T("could not persist the deliberation record: %s", werr.Error()))
		}
	}
}

const (
	// raatiUserEvidenceCap bounds pasted evidence (matches the tool).
	raatiUserEvidenceCap = 32 * 1024
	// raatiConversationCap bounds the "full" conversation attachment —
	// the knob that keeps a huge session from being injected into every
	// unit's every round.
	raatiConversationCap = 24 * 1024
	// raatiSummaryInputCap bounds what the summarizer itself reads.
	raatiSummaryInputCap = 96 * 1024
)

// assembleRaatiEvidence builds the panel's evidence bundle from the
// form's submission: pasted evidence first, then the conversation
// attachment per its mode. Every bound is disclosed to the panel —
// what was truncated or unavailable is said, never silently dropped.
func (w *Workspace) assembleRaatiEvidence(question string, spec raatiEvidenceSpec) string {
	var sb strings.Builder
	if user := strings.TrimSpace(spec.user); user != "" {
		if len(user) > raatiUserEvidenceCap {
			user = user[:raatiUserEvidenceCap] + "\n" + i18n.P("raati.evidence.truncated32", "[evidence truncated at 32KiB — the panel judges what it was shown]")
		}
		fmt.Fprintf(&sb, "--- %s ---\n%s\n\n", i18n.P("raati.evidence.submitted", "evidence submitted with the question"), user)
	}
	switch spec.conversation {
	case "full":
		conv, truncated := renderConversationTail(spec.messages, raatiConversationCap)
		if conv == "" {
			break
		}
		fmt.Fprintf(&sb, "--- %s ---\n", i18n.P("raati.evidence.conversation", "the conversation this question arose from (most recent part)"))
		if truncated {
			sb.WriteString(i18n.P("raati.evidence.conv_truncated", "[older conversation omitted for length]"))
			sb.WriteString("\n")
		}
		sb.WriteString(conv)
		sb.WriteString("\n")
	case "summary":
		conv, _ := renderConversationTail(spec.messages, raatiSummaryInputCap)
		if conv == "" {
			break
		}
		summarize := w.raati.summarize
		if summarize == nil {
			summarize = w.raatiSummarizeConversation
		}
		summary, err := summarize(question, conv)
		if err != nil {
			// The deliberation proceeds on what it has; the gap is
			// disclosed to the panel and to the operator.
			w.diag(i18n.T("raati: conversation summary failed: %s", err.Error()))
			fmt.Fprintf(&sb, "--- %s ---\n%s\n",
				i18n.P("raati.evidence.summary_failed_hdr", "conversation summary"),
				i18n.P("raati.evidence.summary_failed", "[a summary of the originating conversation was requested but could not be produced; the panel deliberates without it]"))
			break
		}
		fmt.Fprintf(&sb, "--- %s ---\n%s\n", i18n.P("raati.evidence.summary", "the conversation this question arose from (summarized for this panel)"), summary)
	}
	return strings.TrimSpace(sb.String())
}

// renderConversationTail renders the newest user/assistant text
// exchanges, oldest-first, keeping within cap bytes by dropping from
// the FRONT (the panel wants the recent conversation, not the ancient
// one). Tool traffic and non-text blocks are omitted. The bool reports
// whether older material was dropped.
func renderConversationTail(msgs []provider.Message, limit int) (string, bool) {
	var lines []string
	for _, m := range msgs {
		var role string
		switch m.Role {
		case provider.RoleUser:
			role = "user"
		case provider.RoleAssistant:
			role = "assistant"
		default:
			continue
		}
		var b strings.Builder
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				b.WriteString(tb.Text)
			}
		}
		if txt := strings.TrimSpace(b.String()); txt != "" {
			lines = append(lines, role+": "+txt)
		}
	}
	total := 0
	start := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		total += len(lines[i]) + 1
		if total > limit {
			break
		}
		start = i
	}
	if start >= len(lines) {
		return "", len(lines) > 0
	}
	return strings.Join(lines[start:], "\n"), start > 0
}

// raatiSummarizeConversation runs one tool-less model turn that briefs
// the panel on the originating conversation — on a throwaway agent, so
// the live session's transcript is untouched. Uses the workspace's
// default binding (the summarizer is the convener's clerk, not a
// panelist).
func (w *Workspace) raatiSummarizeConversation(question, conversation string) (string, error) {
	r, err := build.Resolve(w.args, true)
	if err != nil {
		return "", err
	}
	ag := core.NewAgent(r.NewClient(), r.Model,
		i18n.P("raati.summarizer.system", "You prepare evidence briefs for a deliberation panel. You summarize faithfully and never recommend a verdict."), nil)
	prompt := i18n.P("raati.summarizer.prompt",
		"A three-member deliberation panel will decide the following question:\n%s\n\nSummarize the conversation below as an evidence brief for that panel: the relevant facts, constraints, decisions already made, and points of genuine disagreement. Do not argue for a verdict. Keep it under 400 words.\n\nConversation:\n%s",
		question, conversation)
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	if err := run.Print(ctx, ag, prompt, nil, &buf); err != nil {
		return "", err
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return "", fmt.Errorf("empty summary")
	}
	const summaryCap = 8 * 1024
	if len(out) > summaryCap {
		out = out[:summaryCap]
	}
	return out, nil
}

// raatiInquiryAnswerCap bounds one clerk answer; the digest is a
// docket, not an essay.
const raatiInquiryAnswerCap = 600

// notInRecord is the clerk's contract token for a question the record
// cannot answer — parsed, never guessed around.
const raatiNotInRecord = "NOT_IN_RECORD"

// raatiClerkAnswer is the inquiry gap's rung-1 clerk: one tool-less
// model turn answering the panel's docket STRICTLY from the record
// (the question + the assembled evidence bundle — exactly what the
// panel judges). Questions the record cannot answer come back
// unanswered; a clerk failure degrades to a fully-unanswered docket,
// never to a stalled deliberation.
func (w *Workspace) raatiClerkAnswer(ctx context.Context, question, evidence string, qs []raati.Inquiry) []raati.Inquiry {
	unanswered := func() []raati.Inquiry {
		for i := range qs {
			qs[i].Source = raati.SourceUnanswered
		}
		return qs
	}
	r, err := build.Resolve(w.args, true)
	if err != nil {
		return unanswered()
	}
	ag := core.NewAgent(r.NewClient(), r.Model,
		i18n.P("raati.clerk.system", "You are the clerk of a deliberation panel. You answer the panel's questions STRICTLY from the record provided — never from your own knowledge, never by guessing, and you never recommend a verdict. For any question the record does not answer, reply exactly NOT_IN_RECORD for that item."), nil)
	record := evidence
	if strings.TrimSpace(record) == "" {
		record = i18n.P("raati.clerk.no_record", "(no evidence was submitted — only the question itself is on the record)")
	}
	var list strings.Builder
	for i, q := range qs {
		fmt.Fprintf(&list, "%d. (%s) %s\n", i+1, q.Unit, q.Question)
	}
	prompt := i18n.P("raati.clerk.prompt",
		"The deliberation panel is deciding:\n%s\n\nThe record before the panel:\n%s\n\nThe panel's questions:\n%s\nAnswer each question from the record only. Reply with a fenced code block tagged `answers` holding a JSON array of strings, one per question, in order; use exactly NOT_IN_RECORD where the record does not answer.",
		question, record, list.String())
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	if err := run.Print(cctx, ag, prompt, nil, &buf); err != nil {
		return unanswered()
	}
	answers, ok := parseClerkAnswers(buf.String(), len(qs))
	if !ok {
		return unanswered()
	}
	for i := range qs {
		a := strings.TrimSpace(answers[i])
		if a == "" || strings.Contains(a, raatiNotInRecord) {
			qs[i].Source = raati.SourceUnanswered
			continue
		}
		if len(a) > raatiInquiryAnswerCap {
			a = a[:raatiInquiryAnswerCap]
		}
		qs[i].Answer, qs[i].Source = a, raati.SourceRecord
	}
	return qs
}

// parseClerkAnswers extracts the clerk's fenced `answers` JSON array;
// a malformed or wrong-length reply fails the whole docket (the caller
// records it unanswered) — a misaligned answer list must never assign
// answer N to question M.
func parseClerkAnswers(text string, want int) ([]string, bool) {
	const open = "```answers"
	start := strings.LastIndex(text, open)
	if start < 0 {
		return nil, false
	}
	body := text[start+len(open):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}
	var answers []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &answers); err != nil {
		return nil, false
	}
	if len(answers) != want {
		return nil, false
	}
	return answers, true
}

// setRaatiPhase publishes a transient deliberation phase on the board
// ("briefing", "inquiry", or "" to clear), broadcasting the change so
// clients never sit on a silently working board.
func (w *Workspace) setRaatiPhase(phase string) {
	w.raati.mu.Lock()
	changed := w.raati.view.Phase != phase
	w.raati.view.Phase = phase
	w.raati.mu.Unlock()
	if changed {
		w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("raati"))
	}
}

// raatiInquiryAskTimeout bounds the whole convener docket (rung 2):
// the per-question dialogs share one budget, and the deliberation
// resumes with whatever is still open when it runs out — a raati never
// hangs on a human.
const raatiInquiryAskTimeout = 5 * time.Minute

// raatiAskConvener is the inquiry gap's rung 2: questions the record
// couldn't answer surface as ask dialogs on the convening session
// (free-text), first client to answer wins. A dismissal, an empty
// reply, or the shared deadline leaves the question on the record as
// open — fail open, never stall.
//
// The whole docket goes over as ONE ask, so the convener sees what is
// being asked before answering any of it and submits once, instead of
// being interrupted per question with no view of the rest. A docket
// longer than [core.MaxAskQuestions] is split into consecutive asks
// rather than one unreadable dialog; they share the single deadline.
func (w *Workspace) raatiAskConvener(ctx context.Context, s *wsSession, question string, qs []raati.Inquiry) []raati.Inquiry {
	if s == nil {
		return qs
	}
	ask := w.raati.ask
	if ask == nil {
		ask = func(ctx context.Context, s *wsSession, set []core.UserQuestion) ([]core.UserAnswer, error) {
			return (&webAsker{s: s}).Ask(ctx, set)
		}
	}
	// Index the open questions so answers can be written back to the
	// right inquiry: the set skips the ones the clerk already answered.
	var open []int
	for i := range qs {
		if qs[i].Source == raati.SourceUnanswered {
			open = append(open, i)
		}
	}
	if len(open) == 0 {
		return qs
	}

	deadline, cancel := context.WithTimeout(ctx, raatiInquiryAskTimeout)
	defer cancel()
	for start := 0; start < len(open); start += core.MaxAskQuestions {
		if deadline.Err() != nil {
			break
		}
		batch := open[start:min(start+core.MaxAskQuestions, len(open))]
		set := make([]core.UserQuestion, 0, len(batch))
		for _, i := range batch {
			set = append(set, core.UserQuestion{
				Question: i18n.T("The raati clerk could not answer this from the record — %s asks: %s\n\nAnswer from your own knowledge, or dismiss to leave it on the record as open.",
					qs[i].Unit, qs[i].Question),
				// The asking unit names the tab. These questions carry two
				// paragraphs of framing each, so without a slug a docket of
				// eight is eight anonymous numbers.
				Slug:        qs[i].Unit,
				AllowCustom: true,
			})
		}
		answers, err := ask(deadline, s, set)
		if err != nil {
			continue
		}
		answers = core.PadAnswers(answers, len(batch))
		for n, i := range batch {
			if answers[n].Declined {
				continue
			}
			a := strings.TrimSpace(answers[n].Answer)
			if a == "" {
				continue
			}
			if len(a) > raatiInquiryAnswerCap {
				a = a[:raatiInquiryAnswerCap]
			}
			qs[i].Answer, qs[i].Source = a, raati.SourceConvener
		}
	}
	return qs
}

// raatiBoardHook is the board's claim/feed/release seam — shared by
// browser-convened runs and the raati_convene tool (it implements
// tools.RaatiBoardHook), so an agent's deliberation renders on the
// same live board a human's does.
type raatiBoardHook struct{ w *Workspace }

func (h raatiBoardHook) Begin(question, class, binding, seatOrder string) bool {
	w := h.w
	w.raati.mu.Lock()
	if w.raati.running {
		w.raati.mu.Unlock()
		return false
	}
	w.raati.running = true
	w.raati.accents = panelAccents(raati.DefaultPanel())
	w.raati.view = ctrlproto.RaatiView{
		Running:   true,
		Question:  question,
		Class:     class,
		SeatOrder: seatOrder,
		Binding:   binding,
	}
	w.raati.mu.Unlock()
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("raati"))
	return true
}

func (h raatiBoardHook) Event(ev raati.Event) { h.w.applyRaatiEvent(ev) }

func (h raatiBoardHook) End(err error) {
	w := h.w
	w.raati.mu.Lock()
	w.raati.running = false
	w.raati.view.Running = false
	if err != nil {
		w.raati.view.Err = err.Error()
	}
	w.raati.mu.Unlock()
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("raati"))
}

// raatiConveneEnabled is the raati_convene tool's live opt-in read.
func raatiConveneEnabled() bool {
	uc, err := config.LoadConfig()
	return err == nil && uc.Raati.ConveneTool
}

// raatiSpareHost is the tool's live read of raati.spare_host: seat
// auto-resolved panels off the session's provider account so panel
// traffic does not evict the session's provider-side prompt cache.
func raatiSpareHost() bool {
	uc, err := config.LoadConfig()
	return err == nil && uc.Raati.SpareHost
}

// applyRaatiEvent folds one coordinator event into the board and
// pushes the change. Called synchronously from the convening
// goroutine, so events arrive in order.
func (w *Workspace) applyRaatiEvent(ev raati.Event) {
	w.raati.mu.Lock()
	foldRaatiEvent(&w.raati.view, w.raati.accents, ev)
	w.raati.mu.Unlock()
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("raati"))
}

// foldRaatiEvent is the pure state fold from the coordinator's event
// feed onto the wire view (kept free of Workspace so it unit-tests
// without one).
func foldRaatiEvent(v *ctrlproto.RaatiView, accents map[string]string, ev raati.Event) {
	switch ev.Kind {
	case raati.EventSeated:
		// Upsert by unit name: the per-turn seat order reseats a unit
		// for the final round (new agent, new binding, same seat). The
		// held provisional verdict stays on the block; only the binding
		// changes and the seat returns to deliberating.
		if u := unitByName(v, ev.Unit); u != nil {
			u.Binding = ev.Binding
			u.Status = "deliberating"
			return
		}
		v.Units = append(v.Units, ctrlproto.RaatiUnit{
			Name:    ev.Unit,
			Accent:  accents[ev.Unit],
			Binding: ev.Binding,
			Status:  "deliberating",
		})
	case raati.EventRound:
		v.Round = ev.Round
		if ev.Round > 1 {
			// Cross-examination reopens the floor: voted units deliberate
			// again; their provisional ballot stays visible on the block.
			for i := range v.Units {
				if v.Units[i].Status == "voted" {
					v.Units[i].Status = "deliberating"
				}
			}
		}
	case raati.EventInquiry:
		v.Inquiries = append(v.Inquiries, ctrlproto.RaatiInquiry{
			Unit:     ev.Unit,
			Question: ev.Why,
			Answer:   ev.Answer,
			Source:   ev.Source,
			Round:    ev.Round,
		})
	case raati.EventVoted:
		if ev.Ballot == nil {
			return
		}
		u := unitByName(v, ev.Unit)
		if u == nil {
			return
		}
		u.Status = "voted"
		u.Verdict = string(ev.Ballot.Verdict)
		u.Confidence = ev.Ballot.Confidence
		u.Rationale = ev.Ballot.Rationale
		if ev.Round == 1 {
			u.Blind = string(ev.Ballot.Verdict)
		}
	case raati.EventAbsent:
		u := unitByName(v, ev.Unit)
		if u == nil {
			return
		}
		u.Status = "absent"
		u.Why = ev.Why
	case raati.EventDecided:
		if ev.Outcome == nil {
			return
		}
		v.Running = false
		v.Decision = string(ev.Outcome.Decision)
		v.Degraded = ev.Outcome.Degraded
		v.Tally = &ctrlproto.RaatiTally{
			Approve: ev.Outcome.Tally.Approve,
			Reject:  ev.Outcome.Tally.Reject,
			Abstain: ev.Outcome.Tally.Abstain,
			Absent:  ev.Outcome.Tally.Absent,
		}
		v.Minority = nil
		for _, b := range ev.Outcome.Minority {
			v.Minority = append(v.Minority, ctrlproto.RaatiVoice{Unit: b.Unit, Rationale: b.Rationale})
		}
	}
}

func unitByName(v *ctrlproto.RaatiView, name string) *ctrlproto.RaatiUnit {
	for i := range v.Units {
		if v.Units[i].Name == name {
			return &v.Units[i]
		}
	}
	return nil
}

// panelAccents resolves each panelist persona's accent color for the
// board blocks. Best-effort: a persona that fails to resolve simply
// renders without an accent.
func panelAccents(panel []raati.Unit) map[string]string {
	out := make(map[string]string, len(panel))
	for _, u := range panel {
		if p, err := persona.LoadByName(u.Persona); err == nil {
			out[u.Name] = p.AccentColor
		}
	}
	return out
}
