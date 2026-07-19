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
	"terva.sh/terva/packages/provider"
)

// doctorPersona is the built-in card-craft persona whose charter is the card
// doctor's system prompt, resolved by its ASCII file stem (the display name is
// "Seppä"). A user can shadow it with their own persona of the same
// name/stem (user tier wins), the same as any other persona.
const doctorPersona = "seppa"

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
// persona and returns structured per-field edit proposals.
func (w *Workspace) CardsDoctor(ctx context.Context, p ctrlproto.DoctorParams) (ctrlproto.DoctorResult, error) {
	sc, err := w.cardStore().Get(p.ID)
	if err != nil {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	persona, err := build.ResolvePersona(doctorPersona)
	if err != nil || strings.TrimSpace(persona.Charter) == "" {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "the card doctor persona is unavailable")
	}
	cl, model, ok := w.doctorClient()
	if !ok {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "no usable credential for a card-doctor model")
	}

	findings := card.Lint(sc.Card)
	fields := doctorFields(sc.Card)
	system := persona.Charter + "\n\n" + doctorTask
	user := renderDoctorPrompt(fields, findings, p.Decisions)

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
	stream, err := cl.Stream(ctx, req)
	if err != nil {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "card doctor: %v", err)
	}
	var sb strings.Builder
	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventTextDelta:
			sb.WriteString(e.Delta)
		case provider.EventDone:
			if e.Err != nil {
				return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "card doctor: %v", e.Err)
			}
		}
	}

	res, err := parseDoctorResult(sb.String(), fields)
	if err != nil {
		return ctrlproto.DoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", err)
	}
	return res, nil
}

// doctorClient resolves the workspace's default provider/model as a usable
// client for the doctor pass, mirroring titleClient but keeping the workspace's
// configured model (card-craft wants a capable model, not the title model).
func (w *Workspace) doctorClient() (provider.Client, string, bool) {
	r, err := build.Resolve(w.args, true)
	if err != nil || !r.HasCredential() {
		return nil, "", false
	}
	return r.NewClient(), r.Model, true
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
- Address the lint findings first, then propose taste improvements. Use "warn" severity for a lint warning you are fixing, "info" for a lint info, "suggestion" for an improvement the lint did not flag.
- Make the smallest edit that does the job; keep the author's tone and intent.
- If the author declined a previous proposal with a reason, honor it: withdraw or revise toward what they want, don't re-propose the same change.
- If the card is already in good shape, return an empty "proposals" array and say so in "note".`

// renderDoctorPrompt assembles the user message: the card's editable fields, the
// deterministic lint findings, and (on a follow-up round) the author's decisions.
func renderDoctorPrompt(fields map[string]string, findings []card.Finding, decisions []ctrlproto.DoctorDecision) string {
	var b strings.Builder
	b.WriteString("CHARACTER CARD\n")
	// Stable field order so the prompt is deterministic.
	for _, f := range []string{"name", "description", "personality", "scenario", "first_mes", "mes_example", "system_prompt", "post_history_instructions", "creator_notes"} {
		v := strings.TrimSpace(fields[f])
		if v == "" {
			b.WriteString(f + ": (empty)\n")
			continue
		}
		b.WriteString(f + ":\n" + v + "\n\n")
	}

	b.WriteString("\nDETERMINISTIC LINT FINDINGS\n")
	if len(findings) == 0 {
		b.WriteString("(none)\n")
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
		b.WriteString("\nRevise your proposals in light of these decisions: keep the accepted changes out of the new list, and for each decline, either withdraw it or offer a different edit that respects the stated reason.\n")
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
		return ctrlproto.DoctorResult{}, fmt.Errorf("the card doctor returned no usable suggestions")
	}
	var parsed struct {
		Note      string                     `json:"note"`
		Proposals []ctrlproto.DoctorProposal `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ctrlproto.DoctorResult{}, fmt.Errorf("the card doctor returned malformed suggestions")
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
