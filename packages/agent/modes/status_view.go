package modes

// The /status inline block: the operator's view of the same facts the
// model-facing terva_status tool reports — running build, provider/model,
// session identity, live context usage, spend — without spending a turn
// (or needing tools at all). Rendered like /help and /lore: appended
// above the chat, cleared on the next prompt.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// statusFacts is everything /status renders, gathered by slashStatus and
// kept as plain data so the renderer is testable without an Interactive.
type statusFacts struct {
	Version   string // build display string, no leading "v"; "" = unknown
	Uptime    time.Duration
	Provider  string
	Model     string
	ModelName string // the operator's models.json name; "" = they set none
	Auth      string // rendered as-is ("subscription (oauth)", "api key"); "" = omit
	Reasoning string
	CWD       string
	Trusted   bool
	SessionID string
	SessPath  string

	ContextTokens int // last turn's real context size (0 = no turn yet)
	Window        int // model context window in tokens (0 = unknown)
	Cumulative    core.WireUsage
	Windows       []ctrlproto.UsageWindowInfo
}

// slashStatus renders the harness status block. Live session facts come
// from the carrier's Context verb (the same daemon truth the web panel
// reads); build identity and launch facts are local — in the default TUI
// the daemon is this process.
func (i *Interactive) slashStatus() {
	f := statusFacts{
		Version:   strings.TrimSpace(i.cfg.Version),
		Uptime:    time.Since(buildinfo.Started()).Round(time.Second),
		Provider:  i.cfg.Provider,
		Model:     i.cfg.Model,
		Reasoning: i.effectiveReasoning(),
		CWD:       i.cfg.CWD,
		Trusted:   i.cfg.Trusted,
	}
	if f.Version == "" {
		f.Version = buildinfo.Get().String()
	}
	if i.cfg.CurrentSessionPath != nil {
		f.SessPath = i.cfg.CurrentSessionPath()
		if f.SessPath != "" {
			f.SessionID = strings.TrimSuffix(filepath.Base(f.SessPath), filepath.Ext(f.SessPath))
		}
	}
	subscription := i.cfg.AuthMethod == "oauth"
	if i.cfg.Carrier != nil {
		if bd, err := i.cfg.Carrier.Context(context.Background(), i.carrierSession()); err == nil {
			if bd.Provider != "" {
				f.Provider = bd.Provider
			}
			if bd.Model != "" {
				f.Model = bd.Model
			}
			f.ContextTokens, f.Window = bd.ContextTokens, bd.Window
			f.Cumulative = bd.Cumulative
			f.Windows = bd.UsageWindows
			subscription = subscription || bd.Subscription
		}
	}
	// Resolved after the carrier has had its say, so a session that switched
	// model mid-flight names the model it is actually on.
	if m, err := provider.FindModel(f.Provider, f.Model); err == nil && m.DisplayNameSet {
		f.ModelName = m.DisplayName
	}
	switch {
	case subscription:
		f.Auth = i18n.T("subscription (oauth)")
	case i.cfg.AuthMethod == "apikey":
		f.Auth = i18n.T("api key")
	}

	rows := statusRows(i.cfg.Theme, f)
	i.mu.Lock()
	i.helpBlock = rows
	i.statusErr = ""
	i.statusOK = ""
	i.scrollOffset = 0
	i.mu.Unlock()
	i.invalidate()
}

// statusRows paints the /status block from gathered facts.
func statusRows(th tui.Theme, f statusFacts) []string {
	row := func(label, value string) string {
		return th.FG256(th.Muted, fmt.Sprintf("    %-10s", label)) + th.FG256(th.FG, value)
	}
	rows := []string{th.FG256(th.Accent, "  "+i18n.T("terva status"))}

	ver := f.Version
	if ver == "" {
		ver = i18n.T("(unknown — unstamped build)")
	} else {
		ver = "v" + ver
	}
	rows = append(rows, row(i18n.T("version"), ver))
	rows = append(rows, row(i18n.T("uptime"), f.Uptime.String()))

	if f.Provider != "" || f.Model != "" {
		model := f.Model
		// Name AND id, never name instead of id: /status is the view you open
		// to find out what you are actually talking to, and a nickname alone
		// can't answer that.
		if f.ModelName != "" {
			model = f.ModelName + " (" + f.Model + ")"
		}
		if f.Provider != "" {
			model = f.Provider + " / " + model
		}
		rows = append(rows, row(i18n.T("model"), model))
	}
	if f.Auth != "" {
		rows = append(rows, row(i18n.T("auth"), f.Auth))
	}
	if f.Reasoning != "" {
		rows = append(rows, row(i18n.T("thinking"), f.Reasoning))
	}
	if f.CWD != "" {
		cwd := f.CWD
		if f.Trusted {
			cwd += " " + i18n.T("(trusted)")
		}
		rows = append(rows, row(i18n.T("cwd"), cwd))
	}
	switch {
	case f.SessionID != "":
		rows = append(rows, row(i18n.T("session"), f.SessionID))
		rows = append(rows, row(i18n.T("file"), f.SessPath))
	default:
		rows = append(rows, row(i18n.T("session"), i18n.T("none (live-only conversation; not persisted)")))
	}

	switch {
	case f.Window > 0 && f.ContextTokens > 0:
		rows = append(rows, row(i18n.T("context"), i18n.T("%s / %s tokens (%.1f%% of window), as of the last turn",
			humanCount(f.ContextTokens), humanCount(f.Window),
			float64(f.ContextTokens)/float64(f.Window)*100)))
	case f.Window > 0:
		rows = append(rows, row(i18n.T("context"), i18n.T("window %s tokens; no turn has completed yet", humanCount(f.Window))))
	}

	totalIn := f.Cumulative.Input + f.Cumulative.CacheRead + f.Cumulative.CacheWrite
	if totalIn > 0 || f.Cumulative.Output > 0 {
		totals := i18n.T("%s in / %s out", humanCount(totalIn), humanCount(f.Cumulative.Output))
		// Reasoning is inside the out figure, not beside it, so it reads as a
		// breakdown rather than an addend. Shown only when the provider
		// actually reported it: a silent "0 thinking" on Anthropic — which
		// keeps thinking inside output_tokens and never breaks it out — would
		// state a fact terva does not have.
		if f.Cumulative.ReasoningKnown && f.Cumulative.Reasoning > 0 {
			totals += i18n.T(" (%s thinking)", humanCount(f.Cumulative.Reasoning))
		}
		if f.Cumulative.CostUSD > 0 {
			totals += fmt.Sprintf(", $%.4f", f.Cumulative.CostUSD)
		}
		rows = append(rows, row(i18n.T("totals"), totals))
	}

	for _, w := range f.Windows {
		if w.UsedPercent < 0 {
			continue
		}
		val := i18n.T("%.0f%% used", w.UsedPercent)
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			val += "  ·  " + i18n.T("resets %s", t.Local().Format("15:04"))
		}
		label := w.Label
		if label == "" {
			label = w.Kind
		}
		rows = append(rows, row(label, val))
	}
	return rows
}
