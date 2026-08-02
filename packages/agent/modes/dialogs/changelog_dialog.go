package dialogs

import (
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// ChangelogDialog is the one-shot release-notes overlay shown the
// first time a user launches a new terva version. The body is the
// markdown from the GitHub release page, rendered through the same
// pipeline used for assistant messages so code fences + bold +
// links look right.
//
// Any key dismisses; the parent Interactive then persists the
// version-shown marker so the dialog never reappears for that
// version.
type ChangelogDialog struct {
	active  bool
	version string
	url     string
	body    string
	vp      Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to changelogFallbackRows.
	MaxRows int
}

// changelogFallbackRows is the body height when no host has sized this dialog
// (tests, direct callers). ChromeRows is measured by TestDialogChromeIsDeclared.
const changelogFallbackRows = 18

// ChromeRows is the non-body rows Render emits: header, the version/url lines,
// the more-below indicator and the closing rule. Declared here, beside Render,
// because Render is what decides it.
func (d *ChangelogDialog) ChromeRows() int { return 5 }

func NewChangelogDialog() *ChangelogDialog { return &ChangelogDialog{} }

// Open populates and shows the dialog.
func (d *ChangelogDialog) Open(version, url, body string) {
	d.active = true
	d.version = version
	d.url = url
	d.body = strings.TrimSpace(body)
	d.vp.Reset()
}

// Close hides the dialog.
func (d *ChangelogDialog) Close() { d.active = false }

// Active reports whether the overlay is visible and consuming keys.
func (d *ChangelogDialog) Active() bool { return d != nil && d.active }

// Version reports the release the dialog was opened for, or "" when it
// has never been opened. Nil-safe, like Active.
func (d *ChangelogDialog) Version() string {
	if d == nil {
		return ""
	}
	return d.version
}

// HandleKey: any key (other than scroll) closes the dialog. Returns
// closed=true when the user dismissed; the parent uses that as the
// signal to persist LastChangelogShown.
func (d *ChangelogDialog) HandleKey(k tui.Key) (closed bool) {
	if !d.Active() {
		return false
	}
	// Scroll keys are handled in common (↑/↓, PgUp/PgDn, Home/End); ANY other
	// key dismisses, which is this dialog's whole interaction.
	if d.vp.HandleKey(k) {
		return false
	}
	d.Close()
	return true
}

// Render returns the dialog lines.
func (d *ChangelogDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	title := i18n.T("terva %s \u2014 release notes (any key to dismiss)", d.version)
	out := []string{FrameHeaderColor(th, title, width, th.Accent)}
	if d.url != "" {
		out = append(out, "  "+th.FG256(th.Muted, d.url))
		out = append(out, "")
	}

	var bodyLines []string
	for _, l := range strings.Split(d.body, "\n") {
		if strings.HasPrefix(l, "\x00H:") {
			// Heading: render in accent color, bold.
			heading := strings.TrimPrefix(l, "\x00H:")
			bodyLines = append(bodyLines, th.FG256(th.Accent, tui.Bold(heading)))
		} else {
			// Regular line: render through markdown for bullet points etc.
			rendered := tui.RenderMarkdown(l, th, width-4)
			for _, rl := range strings.Split(rendered, "\n") {
				if len(rl) > 0 && rl[0] == tui.FlushLeftSentinel {
					rl = rl[1:]
				}
				bodyLines = append(bodyLines, rl)
			}
		}
	}

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = changelogFallbackRows
	}
	// Indent before windowing so the shared renderer's more-above/below markers
	// keep their own alignment rather than inheriting the body's.
	for i, line := range bodyLines {
		bodyLines[i] = "    " + line
	}
	d.vp.Fit(len(bodyLines), maxRows)
	out = append(out, d.vp.Rows(th, bodyLines)...)
	out = append(out, FrameRuleColor(th, width, th.Accent))
	return out
}
