package modes

import (
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// rescueDialog offers a quick model swap when the active turn fails
// with a recoverable provider error (auth/rate/temporary). It looks
// and behaves like modelDialog — both ride the shared modelPicker
// core — but its candidate list excludes the pair that just failed
// and the selection carries the prompt to retry. The candidate list
// is built dynamically from the providers the user is currently
// logged in to — no static fallback config.
type rescueDialog struct {
	active   bool
	p        modelPicker
	failedAt string // failed provider/model pair, e.g. "kimi/kimi-for-coding"
	reason   string // short human-readable reason ("token expired", "rate limited", ...)
	prompt   string // the user prompt that should be retried on Select
}

type rescueDialogAction struct {
	Select   bool
	Provider string
	Model    string
	Prompt   string
	Close    bool
}

func newRescueDialog() *rescueDialog { return &rescueDialog{} }

// Open shows the dialog. current is the currently active model id so
// it can be excluded from the candidate list. loggedInProviders is
// the set of provider names with usable credentials right now.
func (d *rescueDialog) Open(current string, loggedInProviders []string, failedProvider, failedModel, reason, prompt string) {
	d.active = true
	d.reason = reason
	d.prompt = prompt
	d.failedAt = strings.TrimSpace(failedProvider + "/" + failedModel)

	provSet := map[string]bool{}
	for _, p := range loggedInProviders {
		provSet[p] = true
	}
	var filtered []provider.Model
	for _, m := range provider.Active() {
		if !provSet[m.Provider] {
			continue
		}
		// Drop the exact failed pair so users can't retry on the
		// model that just failed.
		if m.Provider == failedProvider && m.ID == failedModel {
			continue
		}
		// Drop the currently-active model id (if it differs from
		// the failed one for some reason). The picker is meant to
		// help the user move *off* the broken pair.
		if m.ID == current {
			continue
		}
		filtered = append(filtered, m)
	}
	// current is "" here on purpose: the active model is excluded
	// from the list, so a you-are-here marker would never match.
	d.p.setCatalog(filtered, "", 12)
}

func (d *rescueDialog) Close()       { d.active = false }
func (d *rescueDialog) Active() bool { return d != nil && d.active }

// Render mirrors modelDialog so a rescue prompt feels identical to
// every other picker in the TUI.
func (d *rescueDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	header := "rescue turn"
	if d.failedAt != "" && d.failedAt != "/" {
		header = i18n.T("rescue turn — %s failed", d.failedAt)
	}
	lines = append(lines, frameHeader(th, header, width))

	if d.reason != "" {
		lines = append(lines, th.FG256(th.Warning, "  "+d.reason))
	}

	hint := d.p.hintLine("retry this turn with another model (↑/↓, enter, esc to cancel) - type to filter")
	lines = append(lines, th.FG256(th.Muted, hint))

	if len(d.p.view) == 0 {
		if len(d.p.all) == 0 {
			lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("no other models available — log in to another provider with /login")))
		} else {
			lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("no models match %q", d.p.query)))
		}
		lines = append(lines, frameRule(th, width))
		return lines
	}

	lines = append(lines, d.p.renderRows(th, width)...)
	lines = append(lines, frameRule(th, width))
	return lines
}

func (d *rescueDialog) HandleKey(k tui.Key) rescueDialogAction {
	if d.p.handleNavKey(k) {
		return rescueDialogAction{}
	}
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return rescueDialogAction{Close: true}
	case tui.KeyEnter:
		m, ok := d.p.selected()
		prompt := d.prompt
		d.Close()
		if !ok {
			return rescueDialogAction{Close: true}
		}
		return rescueDialogAction{Select: true, Provider: m.Provider, Model: m.ID, Prompt: prompt}
	}
	return rescueDialogAction{}
}
