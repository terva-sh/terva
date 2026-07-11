package dialogs

// ResetsDialog is the /resets modal: it lists the provider's banked
// usage-reset credits (codex "Full reset (Weekly + 5 hr)" grants) and lets the
// user redeem one — the terminal-side equivalent of the desktop app's redeem
// button, which is the only place OpenAI surfaces the feature.
//
// Redeeming spends a scarce, irreversible credit, so the dialog gates it behind
// an explicit two-step confirm and NEVER redeems on its own: the list step
// selects, an enter arms a confirmation showing exactly which credit, and only
// a 'y' there emits the consume action the interactive layer performs. Every
// other key backs out. There is no auto-redeem path anywhere.

import (
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// resetsPhase is the dialog's mode: browsing the list, confirming a redeem,
// waiting on the redeem round-trip, or showing its outcome.
type resetsPhase int

const (
	resetsList resetsPhase = iota
	resetsConfirm
	resetsBusy
	resetsDone
)

type ResetsDialog struct {
	active     bool
	providerID string
	phase      resetsPhase
	loading    bool // initial list fetch in flight
	supported  bool // provider exposes resets at all
	resets     []ctrlproto.ResetInfo
	cursor     int    // index into resets (list phase)
	err        string // last error (list or consume)
	result     string // outcome line after a redeem
	now        time.Time
}

// ResetsAction is what HandleKey asks the interactive layer to do. Consume
// carries the credit id to redeem; the layer performs the carrier call (off
// the UI goroutine) and reports back via SetBusy/Finish.
type ResetsAction struct {
	Consume  bool
	CreditID string
}

func NewResetsDialog() *ResetsDialog { return &ResetsDialog{} }

func (d *ResetsDialog) Active() bool { return d != nil && d.active }

// Open shows the dialog in its loading state; the interactive layer kicks the
// list fetch and calls SetList when it lands. providerID names the current
// provider for the "no resets" line.
func (d *ResetsDialog) Open(providerID string) {
	d.active = true
	d.providerID = providerID
	d.phase = resetsList
	d.loading = true
	d.supported = false
	d.resets = nil
	d.cursor = 0
	d.err = ""
	d.result = ""
	d.now = time.Now()
}

// SetList applies a fetched list result (clearing the loading state). Called on
// the main goroutine after the background list fetch.
func (d *ResetsDialog) SetList(res ctrlproto.ResetsListResult, err error) {
	if !d.Active() {
		return
	}
	d.loading = false
	d.now = time.Now()
	if err != nil {
		d.err = err.Error()
		return
	}
	d.err = ""
	d.supported = res.Supported
	d.resets = res.Resets
	d.clampCursor()
}

// SetBusy flips the dialog into its in-flight state while a redeem round-trips,
// so the user sees the action was accepted and can't fire a second one.
func (d *ResetsDialog) SetBusy() {
	if d.Active() {
		d.phase = resetsBusy
	}
}

// Finish reports a redeem outcome. On success it shows how many windows cleared
// and marks the credit spent; on error it surfaces the message. Either way the
// dialog moves to its terminal phase (esc to close).
func (d *ResetsDialog) Finish(res ctrlproto.ResetConsumeResult, err error) {
	if !d.Active() {
		return
	}
	d.phase = resetsDone
	d.now = time.Now()
	if err != nil {
		d.err = err.Error()
		d.result = ""
		return
	}
	d.err = ""
	if res.WindowsReset == 1 {
		d.result = i18n.T("Redeemed — 1 usage window cleared.")
	} else {
		d.result = i18n.T("Redeemed — %d usage windows cleared.", res.WindowsReset)
	}
	// Reflect the spend in the list so a reopen (or the done view) shows it.
	for i := range d.resets {
		if d.resets[i].ID == res.Reset.ID {
			d.resets[i] = res.Reset
		}
	}
}

func (d *ResetsDialog) Close() {
	d.active = false
	d.resets = nil
	d.err = ""
	d.result = ""
}

// available returns the indices of credits that can be redeemed right now.
func (d *ResetsDialog) selectedCredit() (ctrlproto.ResetInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.resets) {
		return ctrlproto.ResetInfo{}, false
	}
	return d.resets[d.cursor], true
}

func (d *ResetsDialog) clampCursor() {
	// Land the cursor on the first redeemable credit so enter is meaningful
	// without arrowing; fall back to 0 when none are available.
	for i, r := range d.resets {
		if r.Status == providerAvailable {
			d.cursor = i
			return
		}
	}
	d.cursor = 0
}

// providerAvailable mirrors provider.ResetAvailable without importing the
// provider package into the dialog (the wire status string is the contract).
const providerAvailable = "available"

// HandleKey advances the dialog. It returns an action only when the user
// confirms a redeem; every other key is handled internally (or ignored).
func (d *ResetsDialog) HandleKey(k tui.Key) ResetsAction {
	if !d.Active() {
		return ResetsAction{}
	}
	switch d.phase {
	case resetsList:
		return d.handleListKey(k)
	case resetsConfirm:
		return d.handleConfirmKey(k)
	case resetsBusy:
		// Ignore input while the redeem is in flight (esc still closes via the
		// overlay's ctrlC path if the user insists).
		return ResetsAction{}
	case resetsDone:
		if k.Kind == tui.KeyEsc || k.Kind == tui.KeyEnter {
			d.Close()
		}
		return ResetsAction{}
	}
	return ResetsAction{}
}

func (d *ResetsDialog) handleListKey(k tui.Key) ResetsAction {
	switch {
	case k.Kind == tui.KeyEsc:
		d.Close()
	case k.Kind == tui.KeyUp:
		d.moveCursor(-1)
	case k.Kind == tui.KeyDown:
		d.moveCursor(1)
	case k.Kind == tui.KeyEnter:
		if r, ok := d.selectedCredit(); ok && r.Status == providerAvailable {
			d.phase = resetsConfirm
		}
	}
	return ResetsAction{}
}

func (d *ResetsDialog) handleConfirmKey(k tui.Key) ResetsAction {
	switch {
	case k.Kind == tui.KeyRune && (k.Rune == 'y' || k.Rune == 'Y'):
		if r, ok := d.selectedCredit(); ok && r.Status == providerAvailable {
			return ResetsAction{Consume: true, CreditID: r.ID}
		}
		d.phase = resetsList
	default:
		// n, esc, or anything else backs out to the list without spending.
		d.phase = resetsList
	}
	return ResetsAction{}
}

func (d *ResetsDialog) moveCursor(delta int) {
	if len(d.resets) == 0 {
		return
	}
	d.cursor = (d.cursor + delta + len(d.resets)) % len(d.resets)
}

func (d *ResetsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	lines := []string{FrameHeader(th, i18n.T("usage resets"), width), ""}

	switch {
	case d.loading:
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("fetching resets…")))
	case d.err != "" && d.phase != resetsDone:
		lines = append(lines, th.FG256(th.Error, "  "+d.err))
	case !d.supported:
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("%s doesn't offer usage resets.", d.providerLabel())))
	case len(d.resets) == 0:
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("no reset credits on this account.")))
	default:
		lines = append(lines, d.renderBody(th)...)
	}

	lines = append(lines, "", th.FG256(th.Muted, "  "+d.footer()), FrameRule(th, width))
	return lines
}

func (d *ResetsDialog) renderBody(th tui.Theme) []string {
	var lines []string
	switch d.phase {
	case resetsConfirm:
		r, _ := d.selectedCredit()
		lines = append(lines,
			th.FG256(th.Warning, "  "+i18n.T("Redeem this reset credit? It cannot be undone.")),
			"",
			"  "+th.FG256(th.Accent, credTitle(r)),
			th.FG256(th.Muted, "  "+d.creditMeta(r)),
		)
	case resetsBusy:
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("redeeming…")))
	case resetsDone:
		if d.err != "" {
			lines = append(lines, th.FG256(th.Error, "  "+d.err))
		} else {
			lines = append(lines, th.FG256(th.Accent, "  "+d.result))
		}
	default: // resetsList
		avail := 0
		for i, r := range d.resets {
			lines = append(lines, d.renderRow(th, i, r))
			if r.Status == providerAvailable {
				avail++
			}
		}
		lines = append(lines, "", th.FG256(th.Muted, fmt.Sprintf("  %s", i18n.T("%d available", avail))))
	}
	return lines
}

func (d *ResetsDialog) renderRow(th tui.Theme, i int, r ctrlproto.ResetInfo) string {
	marker := "  "
	if i == d.cursor {
		marker = th.FG256(th.Accent, "▸ ")
	}
	status := r.Status
	color := th.Muted
	switch r.Status {
	case providerAvailable:
		color = th.Accent
	case "redeemed":
		status = "spent"
	}
	label := credTitle(r)
	return marker + th.FG256(color, fmt.Sprintf("%-9s", status)) + " " +
		label + "  " + th.FG256(th.Muted, d.creditMeta(r))
}

// creditMeta is the grant/expiry line, with an "(expired)" note when a
// still-available credit is past its expiry (a clock-relative display concern
// the wire status intentionally leaves out).
func (d *ResetsDialog) creditMeta(r ctrlproto.ResetInfo) string {
	parts := []string{}
	if exp := parseWireTime(r.ExpiresAt); !exp.IsZero() {
		if r.Status == providerAvailable && exp.Before(d.now) {
			parts = append(parts, i18n.T("expired %s", exp.Format("2006-01-02")))
		} else {
			parts = append(parts, i18n.T("expires %s", exp.Format("2006-01-02")))
		}
	}
	if r.Status == "redeemed" {
		if rd := parseWireTime(r.RedeemedAt); !rd.IsZero() {
			parts = append(parts, i18n.T("redeemed %s", rd.Format("2006-01-02")))
		}
	}
	return strings.Join(parts, "  ")
}

func (d *ResetsDialog) footer() string {
	switch d.phase {
	case resetsConfirm:
		return i18n.T("y redeem · n/esc cancel")
	case resetsBusy:
		return i18n.T("working…")
	case resetsDone:
		return i18n.T("esc")
	default:
		if len(d.resets) > 1 {
			return i18n.T("↑/↓ select · enter redeem · esc close")
		}
		return i18n.T("enter redeem · esc close")
	}
}

func (d *ResetsDialog) providerLabel() string {
	if d.providerID != "" {
		return d.providerID
	}
	return "this provider"
}

func credTitle(r ctrlproto.ResetInfo) string {
	if r.Title != "" {
		return r.Title
	}
	return i18n.T("reset credit")
}

func parseWireTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
