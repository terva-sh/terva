package workspace

// The session doctor (SD1 — docs/proposals/session-doctor.md): Dramaturgi
// reads the played session whole — the scene, who is on stage, what lore is
// already recorded — and returns typed proposals in the card doctor's
// negotiation shape. Each kind is gated on an apply verb that exists today:
// lore_entry and open_thread land through world.lore.put, cast_promotion
// through cards.import + cast.add (surfaced as a prefilled editor
// client-side, never a silent import), and scene_state (SD4) through
// world.lore.put on the reserved pin name — an update, not an append.
// Nothing here applies anything.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// dramaturgPersona is the built-in session-doctor persona, resolved by its
// ASCII file stem (display name "Dramaturgi"), user-shadowable like Seppä and
// Toimittaja.
const dramaturgPersona = "dramaturgi"

// dramaturgMaxTranscript bounds the scene evidence — the most generous of the
// side-channel budgets: the dramaturg mines the WHOLE session for structure,
// not just the recent beat, and a full-transcript read is exactly what its
// spend posture (booked, on demand, never in the background) is priced for.
const dramaturgMaxTranscript = 24000

// sessionDoctorMaxProposals caps one round. A doctor that returns forty
// proposals has stopped triaging — and the accept sheet stops being a
// decision surface and becomes a chore.
const sessionDoctorMaxProposals = 12

// SessionsDoctor implements sessions.doctor: one bounded model call over the
// session's evidence, booked against the session, parsed into typed
// proposals. Runs on the session's own model unless a per-generation
// override (Phase 7) picks another.
func (w *Workspace) SessionsDoctor(ctx context.Context, sess string, p ctrlproto.SessionDoctorParams) (ctrlproto.SessionDoctorResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.SessionDoctorResult{}, err
	}
	return sessionsDoctor(ctx, s, p)
}

// sessionsDoctor is the whole verb after session resolution — gate, evidence,
// the one model call, booking, parse. Split from the wrapper so the entire
// spend-and-book path runs under test with a scripted client (the split the
// card doctor's Workspace-level client resolution can't have).
func sessionsDoctor(ctx context.Context, s *wsSession, p ctrlproto.SessionDoctorParams) (ctrlproto.SessionDoctorResult, error) {
	if s.sess == nil || s.sess.Meta.Experience == "" {
		return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the session doctor reads a chat or play session"))
	}
	ag := s.agent
	if ag == nil || ag.Client == nil {
		return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not logged in"))
	}
	pers, err := persona.Resolve(dramaturgPersona)
	if err != nil || strings.TrimSpace(pers.Charter) == "" {
		return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the %s persona is unavailable", dramaturgPersona))
	}
	// Default to the session's live client + model; an override resolves fresh.
	cl := ag.Client
	_, model := s.currentModel()
	if strings.TrimSpace(p.Model) != "" {
		oc, om, err := s.ws.overrideClient(s.argsSnapshot(), p.Provider, p.Model)
		if err != nil {
			return ctrlproto.SessionDoctorResult{}, err
		}
		cl, model = oc, om
	}

	boundName, _ := s.boundCharacter()
	roster := sortedNames(s.castRefs())
	lore := s.sess.Meta.WorldLore
	msgs := ag.Messages()

	// The small hands (SD2/SD3): a focused run narrows the evidence and the
	// ask to one purpose. Validated here, before any tokens are spent.
	promote := strings.TrimSpace(p.Promote)
	if p.Focus != nil && promote != "" {
		return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "focus and promote are different asks — pick one")
	}
	if p.Focus != nil && (*p.Focus < 0 || *p.Focus >= len(msgs)) {
		return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "no message at index %d", *p.Focus)
	}
	if promote != "" {
		if strings.EqualFold(promote, boundName) {
			return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s is the main character — nothing to promote", promote)
		}
		for _, n := range roster {
			if strings.EqualFold(n, promote) {
				return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s is already on stage", promote)
			}
		}
	}

	var user string
	switch {
	case p.Focus != nil:
		user = renderFocusEvidence(s.playerLabel(), boundName, roster, lore, msgs, *p.Focus)
	case promote != "":
		census := dramaturgCensusReport{} // the ask names its subject; no census needed
		user = renderDramaturgEvidence(s.playerLabel(), boundName, roster, lore, msgs, len(msgs), census) +
			"\nTHE AUTHOR'S REQUEST\nDraft exactly ONE cast_promotion proposal for " + promote +
			", seeded from their played lines and how the scene treats them. Nothing else — no lore, no threads.\n"
	default:
		census := dramaturgCensus(msgs, censusKnownNames(boundName, s.playerLabel(), roster, lore))
		user = renderDramaturgEvidence(s.playerLabel(), boundName, roster, lore, msgs, len(msgs), census)
	}
	user += renderSessionDoctorDecisions(p.Decisions)

	req := provider.Request{
		Model:     model,
		System:    pers.Charter + "\n\n" + dramaturgTask,
		MaxTokens: 8000,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: user}},
			Time:    time.Now(),
		}},
	}
	out, usage, err := streamText(ctx, cl, req)
	s.recordSideChannelUsage(usage)
	if err != nil {
		return ctrlproto.SessionDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "session doctor: %v", err)
	}
	res, err := parseSessionDoctorResult(out, lore, roster, boundName)
	if err != nil {
		return res, err
	}
	// Announce a shadowed dramaturg (see MachinePersonaNotice) — the
	// narrowing below keeps notes, so this survives focus/promote runs too.
	res.Note = persona.MachineNotice(dramaturgPersona, pers, res.Note)
	// A narrowed run keeps only what its button promised: mark-as-lore returns
	// only lore/threads (no promotions, no scene-state rewrites), promote
	// returns only the named character's promotion.
	if p.Focus != nil || promote != "" {
		kept := res.Proposals[:0]
		for _, pr := range res.Proposals {
			if p.Focus != nil && pr.Kind != ctrlproto.SessionProposalLore && pr.Kind != ctrlproto.SessionProposalThread {
				continue
			}
			if promote != "" && (pr.Kind != ctrlproto.SessionProposalPromotion || !strings.EqualFold(pr.Character, promote)) {
				continue
			}
			kept = append(kept, pr)
		}
		res.Proposals = kept
	}
	return res, nil
}

// focusWindow is how many messages either side of the marked one a focused
// run reads — the moment and its immediate neighborhood, not the session.
const focusWindow = 6

// renderFocusEvidence is the mark-as-lore evidence (SD2): the marked message
// quoted as such, its neighborhood for grounding, and the recorded lore for
// the delta guard. No census — the author already pointed.
func renderFocusEvidence(playerLabel, boundName string, roster []string, lore []core.WorldLoreEntry, msgs []provider.Message, focus int) string {
	lo := focus - focusWindow
	if lo < 0 {
		lo = 0
	}
	hi := focus + focusWindow + 1
	if hi > len(msgs) {
		hi = len(msgs)
	}
	var b strings.Builder
	b.WriteString(renderDramaturgEvidence(playerLabel, boundName, roster, lore, msgs[lo:hi], len(msgs), dramaturgCensusReport{}))
	b.WriteString("\nTHE MARKED MOMENT — the author pointed at this message\n")
	quoted := messageProse(msgs[focus])
	if quoted == "" {
		quoted = "(no prose)"
	}
	b.WriteString(quoted + "\n")
	b.WriteString("\nTHE AUTHOR'S REQUEST\nDraft the lore_entry (or open_thread) proposals — one to three — that capture what THIS moment establishes. Only this moment; the neighborhood is context, not material.\n")
	return b.String()
}

const dramaturgTask = `You are being run as a one-shot session doctor. You receive the played scene, who is on stage, the World lore already recorded, a mechanical census, and — on a follow-up round — the author's decisions on your previous proposals.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short overall remark, or an empty string>",
  "proposals": [
    {
      "id": "<short stable id, e.g. p1>",
      "kind": "<lore_entry | open_thread | cast_promotion | scene_state | scene_break | lore_retire>",
      "rationale": "<what in the scene earned this — name the moment>",

      "name": "<lore_entry/open_thread: a short title; lore_retire: the EXACT name of the recorded entry to retire>",
      "content": "<lore_entry/open_thread: the knowledge, one tight paragraph of scene truth; scene_state: the full replacement card>",
      "keys": ["<trigger keywords; omit for always-on>"],
      "audience": ["<who knows this, by name; omit for world-shared>"],

      "character": "<cast_promotion: the character's name>",
      "description": "<cast_promotion: who they are, from the scene>",
      "personality": "<cast_promotion: how they act, from their played lines>",
      "first_mes": "<cast_promotion: an opening line in their established voice>"
    }
  ]
}

Rules:
- The played scene is your only warrant; every rationale names the moment that earned the proposal.
- Existing lore outranks you: never propose an entry a recorded one already covers — propose only the deltas.
- Respect scope: knowledge only some characters hold gets an audience; never widen a secret into world-shared lore.
- open_thread is a promised callback (a verdict due, a visit at first light) — give it the keys that will surface it when it comes due.
- cast_promotion is only for a character the scene actually played or fully specced in prose — never the main character, never someone already on stage.
- scene_state is the pinned state card: the in-fiction date and time, the location, ledger facts (debts, prices, supplies), and active routines — a handful of short lines, nothing else. Propose it (at most ONE, "content" only) when the scene has state worth pinning and no card holds it, or when the scene has moved past what the pinned card says; your content REPLACES the whole card, so carry forward what still holds.
- scene_break says this scene has reached a chapter boundary and the story should continue in a fresh one. Propose it (at most ONE) only on a real ending — a departure, a night's end, a decision made and acted on, a promise that will be kept elsewhere — never mid-beat and never merely because the scene is long. "name" titles the next scene, "content" says what ends here and what the next scene opens on.
- lore_retire drops a RECORDED entry the scene has played past — "name" must match one exactly, and nothing else is read. The case it exists for: an entry written to SET UP something that has since happened, still phrased as if it were pending ("is expected to", "will arrive", "should be asked about X"). Once the arrival is played and X has been asked, that entry is no longer background — it contradicts the scene, and it keeps firing on keys the scene uses constantly. Retire it, and record what actually happened as a separate lore_entry if it is worth keeping. Do NOT retire an entry merely because it went unmentioned, nor one that is still true but currently inactive; and never the pinned scene-state card or the story-so-far recap, which have their own lifecycles.
- Numbers, prices, dates, and signed terms are the knowledge most easily lost: prefer capturing them exactly — running state belongs on the scene_state card, settled facts in lore entries.
- If the author declined a previous proposal with a reason, honor it: withdraw or revise, don't re-argue.
- If the session has established nothing worth keeping yet, return an empty "proposals" array and say so in "note".`

// renderDramaturgEvidence assembles the doctor's grounding: the scene
// (speaker-attributed), who is on stage, the lore that already exists (so the
// doctor proposes deltas), and the mechanical census. A free function for
// testability, like renderEditorEvidence.
// totalMsgs is the session's FULL message count, which a focused run's
// windowed transcript no longer carries — the pin's drift is measured against
// the scene played, not against the slice the doctor was handed.
func renderDramaturgEvidence(playerLabel, boundName string, roster []string, lore []core.WorldLoreEntry, transcript []provider.Message, totalMsgs int, census dramaturgCensusReport) string {
	var b strings.Builder
	b.WriteString("THE PLAYED SCENE (most recent last) — your only warrant\n")
	charLabel := boundName
	if charLabel == "" {
		charLabel = "Narrator"
	}
	tail := renderSceneTail(transcript, playerLabel, charLabel, dramaturgMaxTranscript)
	if tail == "" {
		tail = "(the scene has not started yet)"
	}
	b.WriteString(tail + "\n")

	b.WriteString("\nON STAGE\n")
	b.WriteString("- " + playerLabel + " (the player)\n")
	if boundName != "" {
		b.WriteString("- " + boundName + " (the main character)\n")
	}
	for _, n := range roster {
		b.WriteString("- " + n + " (on the roster)\n")
	}

	// The pinned scene-state card leaves the lore list and gets its own block:
	// "do not re-record" is the wrong rule for it (a scene_state proposal is an
	// UPDATE), and the doctor needs to see the current card to propose one.
	sceneState := ""
	rest := make([]core.WorldLoreEntry, 0, len(lore))
	for _, e := range lore {
		if core.IsSceneState(e.Name) {
			sceneState = e.Content
			continue
		}
		rest = append(rest, e)
	}

	// The header carries the drift (SD6). "(current)" was a claim the doctor had
	// no way to check: the pin is only rewritten by hand, by world_note, or by an
	// accepted proposal, so it can sit unchanged while the scene plays past it —
	// and the doctor, reading a card labeled current, had no reason to propose
	// the update its own rules already allow. Naming the gap is what turns
	// "or when the scene has moved past what the pinned card says" from a rule
	// the doctor must infer into one it can see.
	b.WriteString("\nTHE PINNED SCENE-STATE CARD")
	switch turns, pinned := scenePinDrift(lore, totalMsgs); {
	case !pinned:
		b.WriteString("\n(nothing pinned yet)\n")
	case turns == 0:
		b.WriteString(" (written this turn — current)\n" + sceneState + "\n")
	default:
		b.WriteString(fmt.Sprintf(" — last written %d message(s) ago; check it against the scene above and propose a scene_state update if the scene has moved past it\n", turns))
		b.WriteString(sceneState + "\n")
	}

	// "Do not re-record" was the only instruction this list carried, which made
	// it read as a set of facts that could only ever grow. Naming retirement
	// here is what makes the played-past entry visible as a decision at all.
	b.WriteString("\nWORLD LORE ALREADY RECORDED — do not re-record any of this; retire (lore_retire) any the scene has played past\n")
	if len(rest) == 0 {
		b.WriteString("(nothing recorded yet)\n")
	}
	for _, e := range rest {
		scope := "shared"
		if len(e.Audience) > 0 {
			scope = "known to " + strings.Join(e.Audience, ", ")
		}
		b.WriteString("- " + e.Name + " [" + scope + "]: " + e.Content + "\n")
	}

	b.WriteString("\nMECHANICAL CENSUS (computed, not judged — you decide what deserves structure)\n")
	if len(census.Recurring) == 0 && len(census.Singletons) == 0 {
		b.WriteString("(nothing flagged)\n")
	}
	if len(census.Recurring) > 0 {
		b.WriteString("Names that recur in the scene but exist in neither the cast nor the lore:\n")
		for _, n := range census.Recurring {
			b.WriteString("- " + n + "\n")
		}
	}
	if len(census.Singletons) > 0 {
		b.WriteString("Numeric facts stated exactly once (the state most easily lost):\n")
		for _, n := range census.Singletons {
			b.WriteString("- …" + n + "…\n")
		}
	}
	return b.String()
}

// renderSessionDoctorDecisions mirrors the card doctor's decisions block: the
// follow-up round's prompt is self-contained.
func renderSessionDoctorDecisions(decisions []ctrlproto.DoctorDecision) string {
	if len(decisions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nYOUR PREVIOUS PROPOSALS AND THE AUTHOR'S DECISIONS\n")
	for _, d := range decisions {
		verdict := "ACCEPTED"
		if !d.Accepted {
			verdict = "DECLINED"
			if strings.TrimSpace(d.Reason) != "" {
				verdict += " — reason: " + strings.TrimSpace(d.Reason)
			}
		}
		label := d.ProposalID
		if d.Field != "" {
			label += " (" + d.Field + ")"
		}
		b.WriteString("- " + label + ": " + verdict + "\n")
	}
	b.WriteString("\nRevise in light of these decisions: keep accepted proposals out of the new list, and for each decline, either withdraw it or offer something different that respects the stated reason.\n")
	return b.String()
}

// dramaturgCensusReport is the mechanical grounding the census computes.
type dramaturgCensusReport struct {
	// Recurring are proper-name candidates appearing censusRecurringMin+ times
	// in the scene while existing in neither the cast, the lore names, nor the
	// lore audiences — the walk-on who may have earned structure.
	Recurring []string
	// Singletons are short context snippets around numeric tokens that appear
	// exactly once — prices, counts, dates: what compaction loses first.
	Singletons []string
}

const (
	censusRecurringMin = 3
	censusMaxItems     = 8
)

// censusKnownNames collects every name the census must not flag: the player
// and bound character, the roster, and every lore name and audience member.
func censusKnownNames(boundName, playerLabel string, roster []string, lore []core.WorldLoreEntry) map[string]bool {
	known := map[string]bool{}
	addName := func(n string) {
		for _, w := range strings.Fields(strings.ToLower(strings.TrimSpace(n))) {
			known[w] = true
		}
	}
	addName(boundName)
	addName(playerLabel)
	for _, n := range roster {
		addName(n)
	}
	for _, e := range lore {
		addName(e.Name)
		for _, a := range e.Audience {
			addName(a)
		}
	}
	return known
}

var (
	censusNameRe = regexp.MustCompile(`\b[A-Z][a-z]{2,}\b`)
	censusNumRe  = regexp.MustCompile(`\b\d[\d,.]*[a-z]{0,3}\b`)
)

// dramaturgCensus computes the mechanical grounding from the transcript
// prose. Heuristic on purpose: it points, the model judges. A capitalized
// word opening a sentence is skipped (it is usually just a sentence), and
// only words seen mid-sentence count toward recurrence.
func dramaturgCensus(msgs []provider.Message, known map[string]bool) dramaturgCensusReport {
	nameCount := map[string]int{}
	numCount := map[string]int{}
	numContext := map[string]string{}
	for _, m := range msgs {
		text := messageProse(m)
		if text == "" {
			continue
		}
		for _, loc := range censusNameRe.FindAllStringIndex(text, -1) {
			word := text[loc[0]:loc[1]]
			if known[strings.ToLower(word)] {
				continue
			}
			// Sentence-start heuristic: look back past spaces/quotes/asterisks
			// for a terminator or the text's start.
			j := loc[0] - 1
			for j >= 0 && strings.ContainsRune(" \"'*“”‘’", rune(text[j])) {
				j--
			}
			if j < 0 || strings.ContainsRune(".!?\n", rune(text[j])) {
				continue
			}
			nameCount[word]++
		}
		for _, loc := range censusNumRe.FindAllStringIndex(text, -1) {
			tok := text[loc[0]:loc[1]]
			numCount[tok]++
			if _, ok := numContext[tok]; !ok {
				start := loc[0] - 30
				if start < 0 {
					start = 0
				}
				end := loc[1] + 30
				if end > len(text) {
					end = len(text)
				}
				numContext[tok] = strings.TrimSpace(strings.ReplaceAll(text[start:end], "\n", " "))
			}
		}
	}
	var rep dramaturgCensusReport
	for w, n := range nameCount {
		if n >= censusRecurringMin {
			rep.Recurring = append(rep.Recurring, w)
		}
	}
	sort.Slice(rep.Recurring, func(i, j int) bool {
		if nameCount[rep.Recurring[i]] != nameCount[rep.Recurring[j]] {
			return nameCount[rep.Recurring[i]] > nameCount[rep.Recurring[j]]
		}
		return rep.Recurring[i] < rep.Recurring[j]
	})
	if len(rep.Recurring) > censusMaxItems {
		rep.Recurring = rep.Recurring[:censusMaxItems]
	}
	var singles []string
	for tok, n := range numCount {
		if n == 1 {
			singles = append(singles, numContext[tok])
		}
	}
	sort.Strings(singles)
	if len(singles) > censusMaxItems {
		singles = singles[:censusMaxItems]
	}
	rep.Singletons = singles
	return rep
}

// parseSessionDoctorResult extracts and validates the doctor's reply. Kinds
// outside the vocabulary are dropped; a lore/thread proposal colliding with an
// existing entry name is dropped (the same append-only posture world_note
// enforces); a promotion of the bound character or an existing roster member
// is dropped. Validation is server-side so every client sees the same
// contract.
func parseSessionDoctorResult(raw string, lore []core.WorldLoreEntry, roster []string, boundName string) (ctrlproto.SessionDoctorResult, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return ctrlproto.SessionDoctorResult{}, i18n.Errorf("the session doctor returned no usable proposals")
	}
	var parsed struct {
		Note      string                      `json:"note"`
		Proposals []ctrlproto.SessionProposal `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ctrlproto.SessionDoctorResult{}, i18n.Errorf("the session doctor returned malformed proposals")
	}
	loreNames := map[string]bool{}
	// recorded is the lore as it stands, frozen before the round is parsed.
	// lore_retire validates against THIS, never against loreNames — which grows
	// as lore_entry proposals are accepted into the round, and would otherwise
	// let the doctor retire an entry it had just proposed in the same breath.
	recorded := map[string]core.WorldLoreEntry{} // lowered name → the entry as recorded
	for _, e := range lore {
		loreNames[strings.ToLower(e.Name)] = true
		recorded[strings.ToLower(strings.TrimSpace(e.Name))] = e
	}
	retiring := map[string]bool{}
	onStage := map[string]bool{strings.ToLower(boundName): true}
	for _, n := range roster {
		onStage[strings.ToLower(n)] = true
	}
	out := ctrlproto.SessionDoctorResult{Note: strings.TrimSpace(parsed.Note)}
	seen := map[string]bool{}
	seenState, seenBreak := false, false
	for i, p := range parsed.Proposals {
		if len(out.Proposals) >= sessionDoctorMaxProposals {
			break
		}
		p.Rationale = strings.TrimSpace(p.Rationale)
		// A lore/thread proposal addressed to the reserved pin name IS a
		// scene-state proposal, whatever the doctor called it — retag rather
		// than let the collision rule drop an update the author should see.
		if (p.Kind == ctrlproto.SessionProposalLore || p.Kind == ctrlproto.SessionProposalThread) && core.IsSceneState(p.Name) {
			p.Kind = ctrlproto.SessionProposalState
		}
		switch p.Kind {
		case ctrlproto.SessionProposalLore, ctrlproto.SessionProposalThread:
			p.Name = strings.TrimSpace(p.Name)
			p.Content = strings.TrimSpace(p.Content)
			if p.Name == "" || p.Content == "" || loreNames[strings.ToLower(p.Name)] {
				continue
			}
			loreNames[strings.ToLower(p.Name)] = true // no duplicates within a round either
			p.Keys = trimList(p.Keys)
			p.Audience = dedupeNames(p.Audience)
			p.Character, p.Description, p.Personality, p.FirstMes = "", "", "", ""
		case ctrlproto.SessionProposalPromotion:
			p.Character = strings.TrimSpace(p.Character)
			if p.Character == "" || onStage[strings.ToLower(p.Character)] || strings.TrimSpace(p.Description) == "" {
				continue
			}
			onStage[strings.ToLower(p.Character)] = true
			p.Name, p.Content, p.Keys, p.Audience = "", "", nil, nil
		case ctrlproto.SessionProposalState:
			// One card, one proposal: a second scene_state in the same round
			// could only contradict the first. Name is forced to the pin;
			// keys/audience are meaningless on it (always-on, shared).
			p.Content = strings.TrimSpace(p.Content)
			if p.Content == "" || seenState {
				continue
			}
			seenState = true
			p.Name = core.SceneStateName
			p.Keys, p.Audience = nil, nil
			p.Character, p.Description, p.Personality, p.FirstMes = "", "", "", ""
		case ctrlproto.SessionProposalRetire:
			// The one destructive kind, so it is the strictly validated one: it
			// may only name an entry that actually exists (a hallucinated name
			// would apply to nothing, or worse, to something the doctor never
			// read), never a reserved name, and never twice in a round. Nothing
			// but Name survives — a retirement has no content to review, and a
			// leftover payload would render as if it did.
			key := strings.ToLower(strings.TrimSpace(p.Name))
			e, exists := recorded[key]
			if !exists || retiring[key] || core.IsSceneState(e.Name) || core.IsStorySoFar(e.Name) {
				continue
			}
			retiring[key] = true
			// Answer with the RECORDED name, content, and keys — not the
			// doctor's paraphrase of them. The author is being asked to delete
			// something, so the sheet must show what is actually on file: the
			// text that will disappear, and the keys that explain why it kept
			// firing. Sourcing them from the record also means a doctor that
			// misquoted an entry cannot mislead the decision.
			p.Name, p.Content, p.Keys = e.Name, strings.TrimSpace(e.Content), e.Keys
			p.Audience = e.Audience
			p.Character, p.Description, p.Personality, p.FirstMes = "", "", "", ""
		case ctrlproto.SessionProposalBreak:
			// One boundary per round — a scene cannot end in two places. Unlike
			// every other kind this one writes nothing on accept: it opens the
			// next-scene flow, which drafts and commits under the author's eye.
			p.Name = strings.TrimSpace(p.Name)
			p.Content = strings.TrimSpace(p.Content)
			if p.Name == "" || seenBreak {
				continue
			}
			seenBreak = true
			p.Keys, p.Audience = nil, nil
			p.Character, p.Description, p.Personality, p.FirstMes = "", "", "", ""
		default:
			continue // an unknown kind has no apply verb — nothing to accept
		}
		id := strings.TrimSpace(p.ID)
		if id == "" || seen[id] {
			id = fmt.Sprintf("p%d", i+1)
		}
		seen[id] = true
		p.ID = id
		out.Proposals = append(out.Proposals, p)
	}
	return out, nil
}
