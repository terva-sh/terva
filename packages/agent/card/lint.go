package card

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"terva.sh/terva/packages/i18n"
)

// Finding is one deterministic card-lint result — a fact or problem found by
// static analysis, with no model involved. Lint is the substrate the Stage card
// doctor reads FIRST to orient toward the most critical fixes (see
// docs/proposals/stage-card-doctor.md); the web character sheet renders these
// same findings. Severity is "warn" (a real problem) or "info" (a fact worth
// surfacing). Field names the offending card field; Detail carries the offending
// snippet when one exists.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
}

// Severity levels. Kept small on purpose: a lint finding is either a problem to
// fix or a fact to know.
const (
	SevWarn = "warn"
	SevInfo = "info"
)

// oversizedTokens is the per-field estimate above which a definition field is
// flagged as a standing prompt cost. ~4 chars/token, so ~3200 chars.
const oversizedTokens = 800

var (
	// A well-formed macro token: {{ ident }}. The expander (macros.go) also
	// accepts the legacy <BOT>/<USER> angle forms, but authored cards use braces.
	reMacroToken = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)
	reMacroOpen  = regexp.MustCompile(`\{\{`)
	// Author rules addressed AT the character, embedded in a definition field —
	// they steer every turn from a field meant to describe, not instruct.
	reDirective = regexp.MustCompile(`(?i)\{\{char\}\}\s*('s\s+\w+|\s+(will|must|should|shall|is only|never|always))`)
)

// knownMacros are the {{...}} names the expander resolves; anything else
// well-formed is passed through as literal text (unknown-macro).
var knownMacros = map[string]bool{"char": true, "user": true, "original": true}

func estTokens(s string) int { return (utf8.RuneCountInString(s) + 3) / 4 }

type lintField struct{ name, text string }

// Lint runs the deterministic card checks and returns findings sorted by
// severity (warn first) then field. It never errors — an unparseable card never
// reaches here (ParseJSON gates that). This is the card doctor's orientation
// pass and the character sheet's badge source.
func Lint(c Card) []Finding {
	var out []Finding

	// The fields whose text can carry macros.
	macroFields := []lintField{
		{"description", c.Description},
		{"personality", c.Personality},
		{"scenario", c.Scenario},
		{"first_mes", c.FirstMes},
		{"mes_example", c.MesExample},
		{"system_prompt", c.SystemPrompt},
		{"post_history_instructions", c.PostHistoryInstructions},
	}
	for i, g := range c.AlternateGreetings {
		macroFields = append(macroFields, lintField{fmt.Sprintf("alternate_greetings[%d]", i), g})
	}
	for _, f := range macroFields {
		for _, snip := range malformedMacros(f.text) {
			out = append(out, Finding{
				Rule: "malformed-macro", Severity: SevWarn, Field: f.name,
				Message: i18n.T("A macro is malformed and will be sent to the model literally instead of expanding."),
				Detail:  snip,
			})
		}
		for _, m := range reMacroToken.FindAllStringSubmatch(f.text, -1) {
			if !knownMacros[strings.ToLower(m[1])] {
				out = append(out, Finding{
					Rule: "unknown-macro", Severity: SevInfo, Field: f.name,
					Message: i18n.T("An unrecognized macro is not expanded and will be sent literally."),
					Detail:  m[0],
				})
			}
		}
	}

	// Definition fields that ride the prompt every turn — a large one is a
	// standing token cost.
	for _, f := range []lintField{
		{"description", c.Description},
		{"personality", c.Personality},
		{"scenario", c.Scenario},
		{"mes_example", c.MesExample},
		{"system_prompt", c.SystemPrompt},
		{"post_history_instructions", c.PostHistoryInstructions},
	} {
		if t := estTokens(f.text); t > oversizedTokens {
			out = append(out, Finding{
				Rule: "oversized-field", Severity: SevWarn, Field: f.name,
				Message: i18n.T("Field is large (~%d tokens) and is included in the prompt every turn.", t),
			})
		}
	}

	// Structural facts.
	if strings.TrimSpace(c.FirstMes) == "" && len(c.AlternateGreetings) == 0 {
		out = append(out, Finding{
			Rule: "missing-greeting", Severity: SevWarn, Field: "first_mes",
			Message: i18n.T("The card has no greeting — a chat opens with nothing from the character."),
		})
	}
	if strings.TrimSpace(c.Personality) == "" {
		out = append(out, Finding{
			Rule: "empty-personality", Severity: SevInfo, Field: "personality",
			Message: i18n.T("Personality is empty; the card relies on the description alone."),
		})
	}
	if strings.TrimSpace(c.MesExample) == "" {
		out = append(out, Finding{
			Rule: "no-example-dialogue", Severity: SevInfo, Field: "mes_example",
			Message: i18n.T("No example dialogue — the model has less grounding for the character's voice."),
		})
	}
	for _, f := range []lintField{{"description", c.Description}, {"personality", c.Personality}} {
		if snip := reDirective.FindString(f.text); snip != "" {
			out = append(out, Finding{
				Rule: "embedded-directive", Severity: SevInfo, Field: f.name,
				Message: i18n.T("Roleplay rules are embedded in this field — they steer every turn; a system prompt or post-history is the stronger home."),
				Detail:  strings.TrimSpace(snip),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SevWarn // warn before info
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// malformedMacros finds {{-opened tokens that are NOT a well-formed {{ident}} —
// e.g. "{{user)}" (a typo, closing paren instead of a brace) — which the
// expander leaves as literal text. It returns a short snippet per offender.
func malformedMacros(s string) []string {
	var bad []string
	for _, loc := range reMacroOpen.FindAllStringIndex(s, -1) {
		rest := s[loc[0]:]
		// A well-formed token starting exactly here is fine.
		if m := reMacroToken.FindStringIndex(rest); m != nil && m[0] == 0 {
			continue
		}
		// Snippet: up to the first space or the first "}}" (+2), capped at 20.
		end := len(rest)
		if i := strings.IndexByte(rest, ' '); i > 0 && i < end {
			end = i
		}
		if i := strings.Index(rest, "}}"); i >= 0 && i+2 < end {
			end = i + 2
		}
		if end > 20 {
			end = 20
		}
		bad = append(bad, strings.TrimSpace(rest[:end]))
	}
	return bad
}
