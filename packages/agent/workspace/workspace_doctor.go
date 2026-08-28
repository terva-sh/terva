package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// doctorPersona is the built-in card-craft persona whose charter is the card
// doctor's system prompt, resolved by its ASCII file stem (the display name is
// "Seppä"). A user can shadow it with their own persona of the same
// name/stem (user tier wins), the same as any other persona.
const doctorPersona = "seppa"

// editorPersona is the built-in story-editor persona (display name
// "Toimittaja") the doctor's EDITOR mode runs as (Worlds W4): promotion from
// play — enriching a character's card from the scenes they appeared in.
const editorPersona = "toimittaja"

// doctorFields maps a proposal's field name to the current value of that field
// on a card. It is the allow-list of fields the doctor may edit, and the source
// of every proposal's Before. A proposal naming anything else is dropped.
//
// Alternate greetings are addressed by INDEX — alternate_greetings[0] — because
// the proposal contract is one field, one whole string, and a greeting IS one
// whole string. That makes them fit the existing before/after shape, the
// existing negotiation loop, and the existing removal flag with no new proposal
// type: only the naming had to change. Tags and pure metadata stay out of
// scope; they are lists of atoms, not prose an editor can improve.
func doctorFields(c card.Card) map[string]string {
	f := map[string]string{
		"name":                      c.Name,
		"description":               c.Description,
		"personality":               c.Personality,
		"scenario":                  c.Scenario,
		"first_mes":                 c.FirstMes,
		"mes_example":               c.MesExample,
		"system_prompt":             c.SystemPrompt,
		"post_history_instructions": c.PostHistoryInstructions,
		"creator_notes":             c.CreatorNotes,
	}
	for i, g := range c.AlternateGreetings {
		f[greetingField(i)] = g
	}
	// ONE append slot, at the index one past the end. That is what lets the
	// doctor CREATE a greeting rather than only rewrite existing ones: an empty
	// current value is still an editable field, so a proposal naming it passes
	// the allow-list and reads as "add this".
	//
	// Exactly one, not several. Proposals are accepted individually, so two
	// append slots let an author accept the second and decline the first,
	// leaving a hole at the end of the array that nothing downstream expects. A
	// second greeting is one "Save & ask again" away, which costs a round and
	// cannot produce a gap.
	f[greetingField(len(c.AlternateGreetings))] = ""
	return f
}

// greetingField names the nth alternate greeting as a proposal field. One
// spelling, used by the allow-list, the prompt, and the tests, so the wire name
// cannot drift from what the model is told to emit.
func greetingField(i int) string { return fmt.Sprintf("alternate_greetings[%d]", i) }

// greetingIndex is greetingField's inverse: the index a field addresses, and
// whether it is a greeting field at all.
func greetingIndex(field string) (int, bool) {
	rest, ok := strings.CutPrefix(field, "alternate_greetings[")
	if !ok {
		return 0, false
	}
	num, ok := strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(num)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}

// CardsDoctor runs the LLM card doctor over a stored card: it feeds the card and
// its deterministic lint (plus any prior-round decisions) to the card-craft
// persona and returns structured per-field edit proposals. With p.Session set it
// runs in EDITOR mode instead (Worlds W4): the Toimittaja persona, grounded in
// that session's scene and the character's World lore — promotion from play.
func (w *Workspace) CardsDoctor(ctx context.Context, p ctrlproto.DoctorParams) (ctrlproto.DoctorResult, error) {
	sc, err := w.cardStore().Get(p.ID)
	if err != nil {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	var s *wsSession
	if strings.TrimSpace(p.Session) != "" {
		s, err = w.resolve(p.Session)
		if err != nil {
			return ctrlproto.DoctorResult{}, err
		}
		if s.sess == nil || s.sess.Meta.Experience == "" {
			return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the editor grounds in a chat or play session"))
		}
	}
	return cardsDoctor(ctx, w, s, sc.Card, p)
}

// cardsDoctor is the whole verb after the card and (optional) session are
// resolved: persona, evidence, model resolution, the one model call. Split from
// the wrapper the way sessionsDoctor was, so the client resolution the outer
// method could never reach under test — the split doctorRun was carved out to
// dodge — now runs with a scripted session. A nil s is a plain Library-scoped
// lint run; a non-nil s is EDITOR mode (Worlds W4), grounded in that scene.
func cardsDoctor(ctx context.Context, w *Workspace, s *wsSession, c card.Card, p ctrlproto.DoctorParams) (ctrlproto.DoctorResult, error) {
	personaName, task := doctorPersona, i18n.P("stage.doctor.task", doctorTask)
	scene := ""
	if s != nil {
		personaName, task = editorPersona, i18n.P("stage.editor.task", editorTask)
		charName := strings.TrimSpace(c.Name)
		boundName, _ := s.boundCharacter()
		scene = renderEditorEvidence(charName, boundName, s.playerLabel(),
			worldLoreFor(s.sess.Meta.WorldLore, charName), s.agent.Messages())
	}
	pers, err := persona.Resolve(personaName)
	if err != nil || strings.TrimSpace(pers.Charter) == "" {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the %s persona is unavailable", personaName))
	}
	// Resolve the client for this run. In EDITOR mode (grounded in a session) the
	// default is the session's OWN live client + model — the model the author is
	// playing that scene on — the same rule the session doctor follows; a plain
	// Library-scoped lint run has no session and falls back to the workspace
	// default. Either way a per-generation override the caller picked (Phase 7)
	// wins.
	var cl provider.Client
	var model string
	if s != nil {
		ag := s.agent
		if ag == nil || ag.Client == nil {
			return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not logged in"))
		}
		cl = ag.Client
		_, model = s.currentModel()
		if strings.TrimSpace(p.Model) != "" {
			cl, model, err = w.overrideClient(s.argsSnapshot(), p.Provider, p.Model)
		}
	} else {
		prov, mdl := p.Provider, p.Model
		// No explicit override: if THIS card carries its own default model, the
		// doctor honors it — the same authority (effectiveDefaultModel) the session
		// seed uses, so a card's default propagates to its Library-scoped doctor run
		// too, not just to the chats it starts. A card with no default keeps the
		// workspace fallback overrideClient already applies for an empty model.
		if strings.TrimSpace(mdl) == "" {
			if dp, dm, src := w.effectiveDefaultModel(p.ID, ""); src == ctrlproto.DefaultSourceCard {
				prov, mdl = dp, dm
			}
		}
		cl, model, err = w.overrideClient(w.args, prov, mdl)
	}
	if err != nil {
		return ctrlproto.DoctorResult{}, err
	}

	findings := card.Lint(c)
	fields := doctorFields(c)
	system := pers.Charter + "\n\n" + task
	user := renderDoctorPrompt(fields, findings, p.Decisions, p.Steer)
	if scene != "" {
		user += "\n" + scene
	}

	res, err := doctorRun(ctx, cl, model, system, user, fields, s)
	if err != nil {
		return res, err
	}
	// Post-#398 a user persona slugging to the machine stem genuinely
	// shadows the built-in here — announce it rather than run silently on
	// an identity the author may have forgotten they overrode.
	res.Note = persona.MachineNotice(personaName, pers, res.Note)
	return res, nil
}

// doctorRun is the doctor's one model call: stream, book, parse. Split from
// CardsDoctor the way titleUpgradeDue was split from its caller — the part
// that spends and books tokens is testable without resolving a real
// credential. This drain predated streamText's usage contract, so a doctor
// pass — the priciest one-off completion the daemon runs — spent unrecorded.
// The spend lands on the grounding session when there is one (editor mode);
// a plain Library-scoped run has no session meter, and nil is the deliberate,
// visible form of that gap (recordSideChannelUsage is nil-safe).
func doctorRun(ctx context.Context, cl provider.Client, model, system, user string, fields map[string]string, s *wsSession) (ctrlproto.DoctorResult, error) {
	req := provider.Request{
		Model:     model,
		System:    system,
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
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "card doctor: %v", err)
	}
	res, err := parseDoctorResult(out, fields)
	if err != nil {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", err)
	}
	return res, nil
}

const doctorTask = `You are being run as a one-shot card doctor. You receive a character card's fields, the deterministic lint findings, sometimes an instruction from the author about what they want changed, and — on a follow-up round — their decisions on your previous proposals.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short overall remark, or an empty string>",
  "proposals": [
    {
      "id": "<short stable id, e.g. p1>",
      "field": "<one of: name, description, personality, scenario, first_mes, mes_example, system_prompt, post_history_instructions, creator_notes, or alternate_greetings[N]>",
      "severity": "<warn | info | suggestion>",
      "rationale": "<short, concrete reason for the change>",
      "after": "<the COMPLETE new value for that field — it replaces the field wholesale>",
      "remove": <optional, true ONLY when the proposal is to DELETE this field's content; then "after" must be "">
    }
  ]
}

Rules:
- "after" must be the entire new value of the field, not a fragment — the app replaces the field with it verbatim.
- To propose deleting a field's content, set "remove": true and leave "after" empty. An empty "after" WITHOUT "remove" is discarded, so a removal has to be stated, never implied.
- When the author gives an instruction for this pass, it outranks your own taste: propose what they asked for first, then lint fixes, then improvements of your own.
- Only propose fields from the list above. Preserve {{char}} / {{user}} macros; fix broken ones to that exact form.
- Alternate greetings are addressed one at a time by index: alternate_greetings[0], alternate_greetings[1], and so on, exactly as the card dump names them. Each is a COMPLETE alternative opening — an interchangeable substitute for first_mes, not a continuation of it.
- One index past the last existing greeting is shown as empty; proposing there ADDS a greeting. Use it when the card would genuinely play better with another way in — a different mood, a different moment of first contact — not to pad the count. At most one addition per round.
- Example dialogue (mes_example) uses the <START> convention: every example conversation begins with a <START> line — even a single one. If examples run together without it, insert <START> before each so they read as separate examples, not one merged block.
- Address the lint findings first, then propose taste improvements. Use "warn" severity for a lint warning you are fixing, "info" for a lint info, "suggestion" for an improvement the lint did not flag.
- Make the smallest edit that does the job; keep the author's tone and intent.
- If the author declined a previous proposal with a reason, honor it: withdraw or revise toward what they want, don't re-propose the same change.
- If the card is already in good shape, return an empty "proposals" array and say so in "note".`

// editorTask is the EDITOR mode's contract: the same JSON proposal shape as the
// doctor (one parser, one negotiation loop, one client sheet), with promotion
// rules — every edit traces to the played scene.
const editorTask = `You are being run as a one-shot character editor. You receive a character card's fields, the deterministic lint findings, the PLAYED SCENE the character appeared in, what the character knows of the world, sometimes an instruction from the author about what they want changed, and — on a follow-up round — their decisions on your previous proposals.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short overall remark, or an empty string>",
  "proposals": [
    {
      "id": "<short stable id, e.g. p1>",
      "field": "<one of: name, description, personality, scenario, first_mes, mes_example, system_prompt, post_history_instructions, creator_notes, or alternate_greetings[N]>",
      "severity": "<warn | info | suggestion>",
      "rationale": "<what in the scene warrants this — name the moment>",
      "after": "<the COMPLETE new value for that field — it replaces the field wholesale>",
      "remove": <optional, true ONLY when the proposal is to DELETE this field's content; then "after" must be "">
    }
  ]
}

Rules:
- The played scene is your warrant: every proposal's rationale must trace to something that actually happened in it. Do not invent facts the scene doesn't show.
- Removal ("remove": true with an empty "after") is for reconciling a contradiction the scene settled, or for what the author explicitly asked you to cut — not for trimming a card you find overlong. An empty "after" without "remove" is discarded.
- Alternate greetings are addressed one at a time by index: alternate_greetings[0], alternate_greetings[1], and so on, exactly as the card dump names them. Each is a COMPLETE alternative opening — an interchangeable substitute for first_mes, not a continuation of it.
- One index past the last existing greeting is shown as empty; proposing there ADDS a greeting. The scene is the warrant here too: add one when the played scene showed a way this character opens that the card cannot currently produce. At most one addition per round.
- When the author gives an instruction for this pass, it outranks your own reading of the scene: propose what they asked for first, still grounded in what was played.
- ENRICH, don't rewrite: extend fields with what play established (voice, relationships, learned facts, example dialogue lifted from their actual lines); keep the author's tone, format conventions, and every {{char}}/{{user}} macro. "after" is still the entire new value of the field.
- A minimal character may grow personality/example dialogue from nothing; a rich one should change only where the scene genuinely moved them.
- Any example dialogue (mes_example) you add or extend uses the <START> convention: every example conversation begins with a <START> line, even a single one.
- If the scene contradicts the card, flag it ("warn") and propose the reconciliation the author most plausibly intends — never silently pick a side.
- If the author declined a previous proposal with a reason, honor it: withdraw or revise, don't re-propose.
- If there is not enough played material to justify edits, return an empty "proposals" array and say so in "note".`

// renderEditorEvidence assembles the editor's grounding block: the played scene
// (speaker-attributed — a routed/directed line is labeled with its actor, an
// ordinary reply with the BOUND character, who may differ from the character
// being enriched) and the World lore this character is cleared for. A free
// function for testability.
func renderEditorEvidence(charName, boundName, playerLabel string, lore []core.WorldLoreEntry, transcript []provider.Message) string {
	var b strings.Builder
	b.WriteString("\n" + i18n.P("stage.editor.scene", "THE PLAYED SCENE (most recent last) — your primary source") + "\n")
	tail := renderSceneTail(transcript, playerLabel, boundName, editorMaxTranscript)
	if tail == "" {
		tail = frameSceneNotStarted()
	}
	b.WriteString(tail + "\n")
	b.WriteString("\n" + i18n.P("stage.editor.knows", "WHAT %s KNOWS OF THIS WORLD (their lore)", strings.ToUpper(charName)) + "\n")
	if len(lore) == 0 {
		b.WriteString(i18n.P("stage.editor.nothing_recorded", "(nothing recorded)") + "\n")
	}
	for _, e := range lore {
		b.WriteString("- " + e.Name + ": " + e.Content + "\n")
	}
	return b.String()
}

// editorMaxTranscript bounds the editor's scene evidence — more generous than a
// drafting tail (the editor mines the whole recent scene for voice), still
// most-recent anchored.
const editorMaxTranscript = 16000

// renderSceneTail is renderTranscriptTail with speaker attribution: an
// assistant message carrying MetaActor (a directed or routed line) is labeled
// with its actor — "who said what" is exactly what the editor mines.
func renderSceneTail(msgs []provider.Message, playerLabel, charLabel string, budget int) string {
	var lines []string
	for _, m := range msgs {
		text := messageProse(m)
		if text == "" {
			continue
		}
		lines = append(lines, speakerLabel(m, playerLabel, charLabel)+": "+text)
	}
	start, total := 0, 0
	for i := len(lines) - 1; i >= 0; i-- {
		total += len(lines[i]) + 1
		if total > budget {
			start = i + 1
			break
		}
	}
	if start >= len(lines) {
		if len(lines) == 0 {
			return ""
		}
		start = len(lines) - 1
	}
	return strings.Join(lines[start:], "\n")
}

// renderDecisions renders the follow-up round's decisions block: what the doctor
// proposed last time, and what the author did with each proposal.
//
// Shared by all three doctors (card, session, World). It used to exist TWICE —
// inline in renderDoctorPrompt with every string i18n.P-wrapped, and again as
// renderSessionDoctorDecisions in raw English, which the session AND World
// doctors both called. So two of the three surfaces shipped this paragraph
// untranslated whatever the daemon's locale, while the copy that knew better sat
// one file away. The duplication was not the bug; it was the carrier for it.
//
// The wording here is the card doctor's, because that is the text the five
// stage.doctor.* keys are already translated against. The session/World variant
// said the same thing in slightly different words and is now retired.
//
// Returns "" for an empty decision set, so a caller may append unconditionally.
func renderDecisions(decisions []ctrlproto.DoctorDecision) string {
	if len(decisions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + i18n.P("stage.doctor.decisions", "YOUR PREVIOUS PROPOSALS AND THE AUTHOR'S DECISIONS") + "\n")
	for _, d := range decisions {
		verdict := i18n.P("stage.doctor.accepted", "ACCEPTED")
		if !d.Accepted {
			verdict = i18n.P("stage.doctor.declined", "DECLINED")
			if strings.TrimSpace(d.Reason) != "" {
				verdict = i18n.P("stage.doctor.declined_reason", "DECLINED — reason: %s", strings.TrimSpace(d.Reason))
			}
		}
		label := d.ProposalID
		if d.Field != "" {
			label += " (" + d.Field + ")"
		}
		b.WriteString("- " + label + ": " + verdict + "\n")
	}
	b.WriteString("\n" + i18n.P("stage.doctor.revise", "Revise your proposals in light of these decisions: keep the accepted changes out of the new list, and for each decline, either withdraw it or offer a different edit that respects the stated reason.") + "\n")
	return b.String()
}

// renderDoctorPrompt assembles the user message: the card's editable fields, the
// deterministic lint findings, (on a follow-up round) the author's decisions,
// and — last, so it is the final thing read before the model answers — the
// author's own instruction for this pass, when they gave one.
func renderDoctorPrompt(fields map[string]string, findings []card.Finding, decisions []ctrlproto.DoctorDecision, steer string) string {
	var b strings.Builder
	b.WriteString(i18n.P("stage.doctor.card", "CHARACTER CARD") + "\n")
	// Stable field order so the prompt is deterministic. The field names are the
	// card spec's ids — structural, never translated.
	for _, f := range []string{"name", "description", "personality", "scenario", "first_mes", "mes_example", "system_prompt", "post_history_instructions", "creator_notes"} {
		v := strings.TrimSpace(fields[f])
		if v == "" {
			b.WriteString(f + ": " + i18n.P("stage.doctor.empty", "(empty)") + "\n")
			continue
		}
		b.WriteString(f + ":\n" + v + "\n\n")
	}
	// The alternate greetings, in index order, under the exact names a proposal
	// must use. The empty append slot is shown too — a doctor that cannot see
	// where the array ends has no way to know which index means "add one", and
	// the card most in need of a second opening is the one that has none.
	for i := 0; ; i++ {
		f := greetingField(i)
		v, ok := fields[f]
		if !ok {
			break
		}
		if strings.TrimSpace(v) == "" {
			b.WriteString(f + ": " + i18n.P("stage.doctor.empty_slot", "(empty — this index does not exist yet; propose here to ADD a greeting)") + "\n")
			continue
		}
		b.WriteString(f + ":\n" + v + "\n\n")
	}

	b.WriteString("\n" + i18n.P("stage.doctor.lint", "DETERMINISTIC LINT FINDINGS") + "\n")
	if len(findings) == 0 {
		b.WriteString(i18n.P("stage.doctor.none", "(none)") + "\n")
	}
	for _, f := range findings {
		line := "- [" + f.Severity + "]"
		if f.Field != "" {
			line += " " + f.Field + ":"
		}
		line += " " + f.Message
		if f.Detail != "" {
			line += " — " + f.Detail
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(renderDecisions(decisions))

	// The author's instruction goes LAST, after the decisions: it is standing
	// direction for the whole round rather than an answer to one proposal, and
	// it should be the final thing read before the model answers.
	if s := strings.TrimSpace(steer); s != "" {
		b.WriteString("\n" + i18n.P("stage.doctor.steer", "WHAT THE AUTHOR ASKED FOR — your primary warrant this pass") + "\n")
		b.WriteString(s + "\n")
		b.WriteString("\n" + i18n.P("stage.doctor.steer_rule", "Work toward this first: propose the edits it calls for — new text, changed text, or a removal — before any lint fix or taste improvement of your own. Where it conflicts with a lint finding or your own judgement, follow the author and say why in the rationale. If it asks for something a card field cannot express, say so in \"note\" rather than inventing one.") + "\n")
	}
	return b.String()
}

// parseDoctorResult extracts the JSON object from the model's reply and validates
// it: proposals must name an editable field and carry an `after`. `before` is
// filled from the card's actual current value (not the model's echo) so the
// diff the user sees is trustworthy. Proposals whose `after` equals the current
// value (a no-op) are dropped, as are empty ones — unless they carry `remove`,
// the explicit "clear this field", which is dropped only when the field is
// already empty.
func parseDoctorResult(raw string, fields map[string]string) (ctrlproto.DoctorResult, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return ctrlproto.DoctorResult{}, i18n.Errorf("the card doctor returned no usable suggestions")
	}
	var parsed struct {
		Note      string                     `json:"note"`
		Proposals []ctrlproto.DoctorProposal `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ctrlproto.DoctorResult{}, i18n.Errorf("the card doctor returned malformed suggestions")
	}
	out := ctrlproto.DoctorResult{Note: strings.TrimSpace(parsed.Note)}
	seen := map[string]bool{}
	for i, p := range parsed.Proposals {
		field := strings.TrimSpace(p.Field)
		current, editable := fields[field]
		if !editable {
			continue // the doctor may only touch known editable fields
		}
		after := p.After
		if p.Remove {
			// A removal IS an empty value, so it must skip the empty-after drop
			// below. The echoed text is discarded rather than trusted: a model
			// that says "remove" and then repeats the old value would otherwise
			// apply as a no-op edit the user was told was a deletion.
			after = ""
			if strings.TrimSpace(current) == "" {
				continue // nothing there to clear
			}
		} else if strings.TrimSpace(after) == "" || after == current {
			continue // empty or no-op edit
		}
		id := strings.TrimSpace(p.ID)
		if id == "" || seen[id] {
			id = fmt.Sprintf("p%d", i+1)
		}
		seen[id] = true
		sev := strings.TrimSpace(p.Severity)
		if sev != "warn" && sev != "info" {
			sev = "suggestion"
		}
		out.Proposals = append(out.Proposals, ctrlproto.DoctorProposal{
			ID:        id,
			Field:     field,
			Severity:  sev,
			Rationale: strings.TrimSpace(p.Rationale),
			Before:    current, // authoritative current value, not the model's echo
			After:     after,
			Remove:    p.Remove,
		})
	}
	// Deterministic order: warnings first, then by field.
	sort.SliceStable(out.Proposals, func(i, j int) bool {
		a, b := out.Proposals[i], out.Proposals[j]
		if (a.Severity == "warn") != (b.Severity == "warn") {
			return a.Severity == "warn"
		}
		return a.Field < b.Field
	})
	return out, nil
}

// extractJSONObject pulls the first balanced-looking JSON object out of a model
// reply that may wrap it in ```json fences or surround it with prose: it returns
// the substring from the first '{' to the last '}'. Empty if there is none.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
