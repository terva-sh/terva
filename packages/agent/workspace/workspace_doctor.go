package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
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
// on a card. It is the allow-list of fields the doctor may edit — the editable
// TEXT fields the Stage editor exposes (arrays like alternate_greetings/tags and
// pure metadata are out of scope for a before/after text edit). A proposal
// naming anything else is dropped.
func doctorFields(c card.Card) map[string]string {
	return map[string]string{
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
	persona, err := build.ResolvePersona(personaName)
	if err != nil || strings.TrimSpace(persona.Charter) == "" {
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
	system := persona.Charter + "\n\n" + task
	user := renderDoctorPrompt(fields, findings, p.Decisions)
	if scene != "" {
		user += "\n" + scene
	}

	return doctorRun(ctx, cl, model, system, user, fields, s)
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

const doctorTask = `You are being run as a one-shot card doctor. You receive a character card's fields, the deterministic lint findings, and — on a follow-up round — the author's decisions on your previous proposals.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short overall remark, or an empty string>",
  "proposals": [
    {
      "id": "<short stable id, e.g. p1>",
      "field": "<one of: name, description, personality, scenario, first_mes, mes_example, system_prompt, post_history_instructions, creator_notes>",
      "severity": "<warn | info | suggestion>",
      "rationale": "<short, concrete reason for the change>",
      "after": "<the COMPLETE new value for that field — it replaces the field wholesale>"
    }
  ]
}

Rules:
- "after" must be the entire new value of the field, not a fragment — the app replaces the field with it verbatim.
- Only propose fields from the list above. Preserve {{char}} / {{user}} macros; fix broken ones to that exact form.
- Example dialogue (mes_example) uses the <START> convention: every example conversation begins with a <START> line — even a single one. If examples run together without it, insert <START> before each so they read as separate examples, not one merged block.
- Address the lint findings first, then propose taste improvements. Use "warn" severity for a lint warning you are fixing, "info" for a lint info, "suggestion" for an improvement the lint did not flag.
- Make the smallest edit that does the job; keep the author's tone and intent.
- If the author declined a previous proposal with a reason, honor it: withdraw or revise toward what they want, don't re-propose the same change.
- If the card is already in good shape, return an empty "proposals" array and say so in "note".`

// editorTask is the EDITOR mode's contract: the same JSON proposal shape as the
// doctor (one parser, one negotiation loop, one client sheet), with promotion
// rules — every edit traces to the played scene.
const editorTask = `You are being run as a one-shot character editor. You receive a character card's fields, the deterministic lint findings, the PLAYED SCENE the character appeared in, what the character knows of the world, and — on a follow-up round — the author's decisions on your previous proposals.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short overall remark, or an empty string>",
  "proposals": [
    {
      "id": "<short stable id, e.g. p1>",
      "field": "<one of: name, description, personality, scenario, first_mes, mes_example, system_prompt, post_history_instructions, creator_notes>",
      "severity": "<warn | info | suggestion>",
      "rationale": "<what in the scene warrants this — name the moment>",
      "after": "<the COMPLETE new value for that field — it replaces the field wholesale>"
    }
  ]
}

Rules:
- The played scene is your warrant: every proposal's rationale must trace to something that actually happened in it. Do not invent facts the scene doesn't show.
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

// renderDoctorPrompt assembles the user message: the card's editable fields, the
// deterministic lint findings, and (on a follow-up round) the author's decisions.
func renderDoctorPrompt(fields map[string]string, findings []card.Finding, decisions []ctrlproto.DoctorDecision) string {
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

	if len(decisions) > 0 {
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
	}
	return b.String()
}

// parseDoctorResult extracts the JSON object from the model's reply and validates
// it: proposals must name an editable field and carry an `after`. `before` is
// filled from the card's actual current value (not the model's echo) so the
// diff the user sees is trustworthy. Proposals whose `after` equals the current
// value (a no-op) are dropped.
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
		if strings.TrimSpace(p.After) == "" || p.After == current {
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
			After:     p.After,
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
