package dialogs

import (
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// ReasoningDialog picks the thinking depth for THIS session — the per-session
// override, not the global default. It is the surface where the ladder can
// actually be explained: the budgets differ by an order of magnitude across it,
// and the top two rungs ("maximum" and "max") differ only on some models, which
// is not something a bare list of words conveys.
type ReasoningDialog struct {
	active bool
	rows   []reasoningRow
	cursor int
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to the standalone constant.
	MaxRows int
}

const reasoningFallbackRows = 9

type reasoningRow struct {
	// level is the raw value sent to the daemon: a ladder rung, or "" for the
	// inherit row.
	level    string
	label    string
	detail   string
	selected bool
}

func NewReasoningDialog() *ReasoningDialog { return &ReasoningDialog{} }

// ChromeRows is the non-body rows Render emits: the header and the footer rule.
func (d *ReasoningDialog) ChromeRows() int { return 3 }

// Open builds the ladder for a session. override is the session's own level
// ("" when it inherits), global is the workspace default, and model is the
// session's current model — used only to say whether "max" means anything here.
func (d *ReasoningDialog) Open(override, global string, model provider.Model) {
	d.active = true
	d.cursor = 0
	d.rows = d.rows[:0]

	inheritDetail := i18n.T("follow the global setting")
	if g := strings.TrimSpace(global); g != "" {
		inheritDetail = i18n.T("follow the global setting (now: %s)", g)
	} else {
		// With no global level the fall-through is the MODEL's own default, and
		// saying "global" there would name something that isn't deciding.
		if md := strings.TrimSpace(model.DefaultReasoning); md != "" {
			inheritDetail = i18n.T("follow the model's default (%s)", md)
		}
	}
	d.rows = append(d.rows, reasoningRow{
		level: "", label: i18n.T("inherit"), detail: inheritDetail,
		selected: override == "",
	})

	// Every rung stays selectable so the ladder keeps one shape across models,
	// but a rung that lands on the same wire value as another says so —
	// otherwise the dialog offers a choice that is not one.
	//
	// The ladder itself is built in packages/provider, not here: the web picker
	// renders the same rows from the same computation over ctrlproto, and two
	// implementations of "what does this rung do" is precisely the drift this
	// whole surface exists to prevent.
	// A nil ladder means the model takes no thinking setting at all. The rows
	// are still built — the ladder keeps one shape across models — and the zero
	// ReasoningEffect carries Supported=false, which is what makes the detail
	// say so rather than say "off".
	ladder := provider.ReasoningLadderFor(model)
	for i, lv := range provider.ReasoningLevels {
		rung := provider.ReasoningRung{Level: lv}
		if i < len(ladder) {
			rung = ladder[i]
		}
		d.rows = append(d.rows, reasoningRow{
			level: lv, label: lv,
			detail:   reasoningDetail(lv, model, rung.Effect, rung.SameAs),
			selected: override == lv,
		})
	}
	for i, r := range d.rows {
		if r.selected {
			d.cursor = i
		}
	}
}

// reasoningDetail is the one-line explanation for a rung: what this model
// actually receives when you pick it.
//
// It reports the WIRE VALUE rather than a ladder-wide token budget, because
// the two are different things on most models and the budget was the wrong one
// nearly everywhere: the Codex/Responses route and Gemini 3 take an effort
// enum and no budget at all, so "~8k tokens of thinking" described a number the
// request never carried. effect comes from provider.ReasoningEffectFor, which
// delegates to the same functions the request builders call.
//
// same, when non-empty, names the lower rung this one collapses onto.
func reasoningDetail(level string, model provider.Model, effect provider.ReasoningEffect, same string) string {
	if !effect.Supported {
		return i18n.T("this model takes no thinking setting")
	}
	if effect.Off() {
		return i18n.T("no thinking")
	}

	var base string
	switch {
	case effect.Budget > 0:
		base = i18n.T("~%dk tokens of thinking", effect.Budget/1024)
	case effect.Effort != "":
		base = i18n.T("effort: %s", effect.Effort)
	default:
		base = i18n.T("thinking on")
	}

	// The top rung keeps its own note: "max" is the one place a user is
	// choosing a tier that may not exist here, and naming the effort alone
	// would not say so.
	if level == "max" && same == "" && provider.MaxIsNative(model) {
		return base + i18n.T(" — native on this model")
	}
	if same != "" {
		return base + i18n.T(" — same as %s on this model", same)
	}
	return base
}

func (d *ReasoningDialog) Close()       { d.active = false }
func (d *ReasoningDialog) Active() bool { return d != nil && d.active }

// HandleKey advances the dialog and RETURNS the chosen level, rather than
// leaving the caller to read it back afterwards.
//
// Deliberate: enter both selects and closes, so any "what is selected?" getter
// would have to be called before the close to work, and a caller that read it
// after — the natural order — would silently get nothing. Returning the value
// makes that ordering unexpressible.
//
// chose is true only on enter; level is the raw ladder rung, or "" for the
// inherit row (which is a real choice, not the absence of one, so the two
// results cannot be distinguished by the level alone).
func (d *ReasoningDialog) HandleKey(k tui.Key) (level string, chose bool) {
	if !d.Active() {
		return "", false
	}
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.rows)-1 {
			d.cursor++
		}
	case tui.KeyEnter:
		if d.cursor < 0 || d.cursor >= len(d.rows) {
			d.Close()
			return "", false
		}
		lv := d.rows[d.cursor].level
		d.Close()
		return lv, true
	}
	return "", false
}

func (d *ReasoningDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	out := []string{FrameHeader(th, i18n.T("reasoning for this session (enter to set, esc to close)"), width)}

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = reasoningFallbackRows
	}
	start, end := visibleWindow(d.cursor, len(d.rows), maxRows)
	if start > 0 {
		out = append(out, WindowMoreAbove(th, start))
	}
	for i := start; i < end; i++ {
		row := formatReasoningRow(d.rows[i], width-2)
		if i == d.cursor {
			out = append(out, th.PadHighlight("  "+row, width))
		} else {
			out = append(out, "  "+th.FG256(th.Muted, row))
		}
	}
	if end < len(d.rows) {
		out = append(out, WindowMoreBelow(th, len(d.rows), end))
	}
	out = append(out, FrameRule(th, width))
	return out
}

func formatReasoningRow(r reasoningRow, maxWidth int) string {
	// The marker column tells you which level is in force at a glance, which is
	// the question the dialog is usually opened to answer.
	mark := "  "
	if r.selected {
		mark = "▸ "
	}
	left := mark + padRunes(truncateLineSafe(r.label, 12), 12) + "  "
	room := maxWidth - runeLen(left)
	if room < 8 {
		room = 8
	}
	return left + truncateLineSafe(r.detail, room)
}
