package dialogs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// WorkflowDialog is the /workflows panel: the runs the host knows about, and
// one run opened across three tabs — Overview, Script, Results. It is the
// terminal twin of the web board's workflow lane, over the same two read-only
// verbs, so a run can be inspected from wherever the operator already is.
//
// The gap it closes: reading a run used to mean leaving the TUI for a shell
// (`terva workflow show`), and a run launched from a terminal that has since
// closed was on disk and unreachable in practice. The first real run cost $3.37
// to redo work already journaled, because nobody could find it.
//
// The SCRIPT tab is the one that justifies the panel over a list command: the
// source is recorded verbatim at launch, so what actually ran can be read back
// without shell access to the host that ran it — and without trusting that the
// file at script_at still says what it said.
//
// Rendering lives here rather than in a shared renderer (the worktree/tasks
// pattern) because the engine-side formatter is behind the terva_workflows
// build tag, and this package must not depend on it. The wire types are the
// contract instead.
type WorkflowDialog struct {
	active   bool
	selected int
	vp       Viewport

	// open is the run currently opened, "" while the list is showing. Kept as an
	// id rather than an index so a refresh that reorders the list (a new run
	// lands newest-first) cannot silently swap which run is on screen.
	open string
	tab  wfTab

	listFn func() []ctrlproto.WorkflowRunInfo
	viewFn func() *ctrlproto.WorkflowRunView
	// loading distinguishes "the fetch has not answered yet" from "this run has
	// no results", which look identical in an empty view and mean opposite things.
	loading bool
	err     string

	// MaxRows caps the body height; the overlay sets it from the terminal size
	// each frame so a long list stays inside the bottom band. 0 = unlimited.
	MaxRows int
}

type wfTab int

const (
	wfOverview wfTab = iota
	wfScript
	wfResults
)

var wfTabs = []wfTab{wfOverview, wfScript, wfResults}

func (t wfTab) label() string {
	switch t {
	case wfScript:
		return i18n.T("Script")
	case wfResults:
		return i18n.T("Results")
	default:
		return i18n.T("Overview")
	}
}

// ChromeRows is the non-body rows Render emits: header, the blank, the footer
// and the closing rule.
func (d *WorkflowDialog) ChromeRows() int { return 4 }

func NewWorkflowDialog() *WorkflowDialog { return &WorkflowDialog{} }

func (d *WorkflowDialog) Active() bool { return d != nil && d.active }

// Open shows the run list over the live cache.
func (d *WorkflowDialog) Open(listFn func() []ctrlproto.WorkflowRunInfo, viewFn func() *ctrlproto.WorkflowRunView) {
	d.active = true
	d.selected = 0
	d.vp.Reset()
	d.open = ""
	d.tab = wfOverview
	d.err = ""
	d.loading = false
	d.listFn = listFn
	d.viewFn = viewFn
}

func (d *WorkflowDialog) Close() {
	d.active = false
	d.listFn = nil
	d.viewFn = nil
	d.open = ""
	d.err = ""
	d.loading = false
	d.selected = 0
	d.vp.Reset()
}

// SetError shows a fetch failure in place of the body. Cleared by any
// navigation, so a transient error does not stick to a panel that has moved on.
func (d *WorkflowDialog) SetError(msg string) {
	d.err = msg
	d.loading = false
}

// Opened reports the run id currently open ("" on the list), so the host knows
// which run a refresh should re-fetch.
func (d *WorkflowDialog) Opened() string { return d.open }

// WorkflowAction reports what the host should do after a key.
type WorkflowAction struct {
	Close bool
	// Refresh re-lists the runs.
	Refresh bool
	// OpenRun names a run to fetch; the host fills the cache and the panel
	// renders it on the next frame.
	OpenRun string
}

func (d *WorkflowDialog) runs() []ctrlproto.WorkflowRunInfo {
	if d.listFn == nil {
		return nil
	}
	return d.listFn()
}

func (d *WorkflowDialog) HandleKey(k tui.Key) WorkflowAction {
	switch k.Kind {
	case tui.KeyEsc:
		// Esc backs out one level: from a run to the list, from the list to gone.
		// Closing outright from a run would make the panel a one-shot viewer and
		// force a re-open to compare two runs.
		if d.open != "" {
			d.open = ""
			d.err = ""
			d.loading = false
			d.vp.Reset()
			return WorkflowAction{}
		}
		return WorkflowAction{Close: true}
	case tui.KeyEnter:
		if d.open == "" {
			rs := d.runs()
			if d.selected >= 0 && d.selected < len(rs) {
				d.open = rs[d.selected].ID
				d.tab = wfOverview
				d.vp.Reset()
				d.err = ""
				d.loading = true
				return WorkflowAction{OpenRun: d.open}
			}
		}
	case tui.KeyRune:
		switch k.Rune {
		case 'r', 'R':
			d.err = ""
			if d.open != "" {
				d.loading = true
				return WorkflowAction{OpenRun: d.open}
			}
			return WorkflowAction{Refresh: true}
		case '\t':
			d.cycleTab(1)
		}
	case tui.KeyTab:
		d.cycleTab(1)
	case tui.KeyShiftTab:
		d.cycleTab(-1)
	case tui.KeyLeft:
		d.cycleTab(-1)
	case tui.KeyRight:
		d.cycleTab(1)
	case tui.KeyUp, tui.KeyMouseWheelUp:
		if d.open != "" {
			d.vp.HandleKey(k)
		} else if d.selected > 0 {
			d.selected--
		}
	case tui.KeyDown, tui.KeyMouseWheelDown:
		if d.open != "" {
			d.vp.HandleKey(k)
		} else if n := len(d.runs()); d.selected < n-1 {
			d.selected++
		}
	default:
		// PgUp/PgDn/Home/End only mean anything inside an opened run; on the
		// list the selection is the cursor and there is no body to scroll.
		if d.open != "" {
			d.vp.HandleKey(k)
		}
	}
	return WorkflowAction{}
}

// cycleTab moves between tabs, but only inside an opened run — on the list
// there is nothing to tab through, and ←/→ there would silently do nothing
// visible while looking like it should.
func (d *WorkflowDialog) cycleTab(delta int) {
	if d.open == "" {
		return
	}
	n := len(wfTabs)
	d.tab = wfTabs[((int(d.tab)+delta)%n+n)%n]
	d.vp.Reset()
}

func (d *WorkflowDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var title, footer string
	var body []string
	if d.open == "" {
		title, body, footer = d.renderList(th)
	} else {
		title, body, footer = d.renderRun(th, width)
	}

	out := []string{FrameHeader(th, title, width)}
	d.vp.Fit(len(body), d.MaxRows)
	if d.MaxRows > 0 && d.vp.Scrollable() {
		if d.open == "" {
			// One body line per run, so the selection maps 1:1 and the cursor
			// must stay inside the window (the WorktreeDialog rule).
			d.vp.RevealPadded(d.selected, cursorPadRows)
		}
		start, end := d.vp.Window()
		body = body[start:end]
		footer = i18n.T("↑/↓ scroll · %s", footer)
	} else {
		d.vp.Reset()
	}
	out = append(out, body...)
	out = append(out, "", th.FG256(th.Muted, "  "+footer))
	out = append(out, FrameRule(th, width))
	return out
}

func (d *WorkflowDialog) renderList(th tui.Theme) (title string, body []string, footer string) {
	rs := d.runs()
	if d.selected >= len(rs) {
		d.selected = len(rs) - 1
	}
	if d.selected < 0 {
		d.selected = 0
	}
	title = i18n.T("Workflow runs")
	if d.err != "" {
		return title, []string{th.FG256(th.Error, "  "+d.err)}, i18n.T("r refresh · esc close")
	}
	if len(rs) == 0 {
		return title, []string{th.FG256(th.Muted, i18n.T("  no workflow runs recorded yet — `terva workflow run <script.js>` records one."))},
			i18n.T("r refresh · esc close")
	}
	title = i18n.T("Workflow runs (%d)", len(rs))
	for i, r := range rs {
		cursor := "  "
		if i == d.selected {
			cursor = "▶ "
		}
		line := fmt.Sprintf("%s%-14s %-9s %-7s %9s  %s",
			cursor, r.ID, wfStatusWord(r.Status), wfProgress(r), wfCost(r.CostUSD), wfName(r))
		switch {
		case i == d.selected:
			line = th.FG256(th.Accent, line)
		case r.Status == "failed" || r.Status == "crashed":
			line = th.FG256(th.Error, line)
		case r.Status == "running":
			line = th.FG256(th.Accent, line)
		case r.Resumable:
			// Resumable is the actionable state: completed work is sitting on
			// disk that a resume would replay instead of pay for again.
			line = th.FG256(th.Warning, line)
		default:
			line = th.FG256(th.FG, line)
		}
		body = append(body, line)
	}
	return title, body, i18n.T("↑/↓ select · ↵ open · r refresh · esc close")
}

func (d *WorkflowDialog) renderRun(th tui.Theme, width int) (title string, body []string, footer string) {
	var v *ctrlproto.WorkflowRunView
	if d.viewFn != nil {
		v = d.viewFn()
	}
	title = i18n.T("Workflow %s", d.open)
	if v != nil && v.Run.Name != "" {
		title = i18n.T("Workflow %s — %s", d.open, v.Run.Name)
	}
	body = append(body, d.tabRow(th))
	body = append(body, "")

	switch {
	case d.err != "":
		body = append(body, th.FG256(th.Error, "  "+d.err))
		return title, body, i18n.T("r retry · esc back")
	case v == nil && d.loading:
		body = append(body, th.FG256(th.Muted, i18n.T("  loading…")))
		return title, body, i18n.T("esc back")
	case v == nil:
		body = append(body, th.FG256(th.Muted, i18n.T("  that run could not be read.")))
		return title, body, i18n.T("r retry · esc back")
	}

	switch d.tab {
	case wfScript:
		body = append(body, d.scriptLines(th, *v, width)...)
	case wfResults:
		body = append(body, d.resultLines(th, *v, width)...)
	default:
		body = append(body, d.overviewLines(th, *v)...)
	}
	return title, body, i18n.T("←/→ tab · ↑/↓ scroll · r reload · esc back")
}

func (d *WorkflowDialog) tabRow(th tui.Theme) string {
	var parts []string
	for _, t := range wfTabs {
		if t == d.tab {
			parts = append(parts, th.FG256(th.Accent, tui.Bold("["+t.label()+"]")))
			continue
		}
		parts = append(parts, th.FG256(th.Muted, " "+t.label()+" "))
	}
	return "  " + strings.Join(parts, " ")
}

func (d *WorkflowDialog) overviewLines(th tui.Theme, v ctrlproto.WorkflowRunView) []string {
	r := v.Run
	var out []string
	// An absent field is omitted, not blanked: a run in flight has no end time,
	// a cheap one no cost. Filtering at append keeps interior gaps from opening
	// up in the middle of the list — the reason this is not a slice of rows with
	// the empties stripped afterwards.
	row := func(k, val string) {
		if val == "" {
			return
		}
		out = append(out, "  "+th.FG256(th.Muted, fmt.Sprintf("%-11s", k))+val)
	}
	row(i18n.T("status"), wfStatusWord(r.Status))
	row(i18n.T("agents"), wfProgress(r))
	if r.Cached > 0 {
		row(i18n.T("replayed"), fmt.Sprintf("%d", r.Cached))
	}
	row(i18n.T("cost"), wfCost(r.CostUSD))
	row(i18n.T("started"), r.Started)
	row(i18n.T("ended"), r.Ended)
	row(i18n.T("cwd"), r.CWD)
	row(i18n.T("script"), r.ScriptAt)
	if r.Err != "" {
		out = append(out, "", "  "+th.FG256(th.Error, r.Err))
	}
	if len(v.InFlight) > 0 {
		out = append(out, "", "  "+th.FG256(th.Muted, i18n.T("in flight")))
		for _, a := range v.InFlight {
			name := a.Label
			if name == "" {
				name = a.AgentID
			}
			out = append(out, "    "+th.FG256(th.Accent, name))
		}
	}
	if r.Resumable {
		// The one line an operator acts on: say what resuming would save and how
		// to do it, since the panel itself cannot launch a run.
		out = append(out, "", "  "+th.FG256(th.Warning,
			i18n.T("%d completed agent(s) are on disk — `terva workflow run <script> --resume %s` replays them instead of paying again.",
				r.Completed, r.ID)))
	}
	return out
}

func (d *WorkflowDialog) scriptLines(th tui.Theme, v ctrlproto.WorkflowRunView, width int) []string {
	if strings.TrimSpace(v.Script) == "" {
		return []string{th.FG256(th.Muted, i18n.T("  this run recorded no script source."))}
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(v.Script, "\n"), "\n") {
		out = append(out, "  "+th.FG256(th.FG, wfClip(l, width-4)))
	}
	if len(v.Args) > 0 && string(v.Args) != "null" {
		out = append(out, "", "  "+th.FG256(th.Muted, i18n.T("args")))
		for _, l := range strings.Split(wfPrettyJSON(v.Args), "\n") {
			out = append(out, "  "+th.FG256(th.FG, wfClip(l, width-4)))
		}
	}
	return out
}

func (d *WorkflowDialog) resultLines(th tui.Theme, v ctrlproto.WorkflowRunView, width int) []string {
	if len(v.Results) == 0 {
		return []string{th.FG256(th.Muted, i18n.T("  no journaled results — no agent has completed yet."))}
	}
	var out []string
	for _, res := range v.Results {
		label := res.Label
		if label == "" {
			// A run from before labels existed, or a call that passed none. The
			// generated id is unreadable but it is the only handle there is.
			label = res.AgentID
		}
		head := "  " + th.FG256(th.Accent, label)
		if res.Bytes > 0 {
			head += th.FG256(th.Muted, "  "+wfBytes(res.Bytes))
		}
		out = append(out, head)
		// The body is shown, not collapsed: a terminal panel that hides the
		// finding behind its size would be a list command with extra steps, and
		// the scroll already bounds what is on screen.
		for _, l := range strings.Split(wfPrettyJSON(res.Result), "\n") {
			out = append(out, "    "+th.FG256(th.FG, wfClip(l, width-6)))
		}
		out = append(out, "")
	}
	return out
}

// --- small formatters -------------------------------------------------------

// wfStatusWord spells the wire status for a human. "incomplete" is the honest
// one and the one that needs explaining: it covers both a run still going and a
// run whose process died, because the record cannot tell them apart.
func wfStatusWord(s string) string {
	switch s {
	case "done":
		return i18n.T("done")
	case "failed":
		return i18n.T("failed")
	case "running":
		return i18n.T("running")
	case "crashed":
		return i18n.T("crashed")
	case "incomplete":
		return i18n.T("incomplete")
	default:
		return s
	}
}

func wfProgress(r ctrlproto.WorkflowRunInfo) string {
	base := fmt.Sprintf("%d/?", r.Completed)
	if r.Agents > 0 {
		// Agents is only known once the run closed, so an unfinished run has a
		// completed count and no total. "4/?" says that; "4/0" would be a lie.
		base = fmt.Sprintf("%d/%d", r.Completed, r.Agents)
	}
	if r.Running > 0 {
		// "+4" is the difference between a run to wait for and one to resume.
		base += fmt.Sprintf("+%d", r.Running)
	}
	return base
}

func wfCost(c float64) string {
	if c <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.4f", c)
}

func wfName(r ctrlproto.WorkflowRunInfo) string {
	if r.Name != "" {
		return r.Name
	}
	return i18n.T("(unnamed)")
}

func wfBytes(n int) string {
	if n >= 1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}

// wfPrettyJSON indents a journaled result for reading. A result that is not
// JSON (an agent's plain-text return) is passed through as-is rather than
// mangled — the journal stores whatever the agent produced.
func wfPrettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// A JSON string result renders as a quoted one-liner, which for a prose
	// deliverable is the worst of both. Unquote it back into readable lines
	// BEFORE indenting — json.Indent leaves a bare string untouched.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func wfClip(s string, w int) string {
	if w < 8 || len(s) <= w {
		return s
	}
	return s[:w-1] + "…"
}
