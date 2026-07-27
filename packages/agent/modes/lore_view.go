package modes

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/i18n"
)

// slashLore renders this run's active lore entries as an inline block above
// the chat (like /help), cleared on the next prompt. Lore is otherwise silent
// in-session, so this is the observability surface for what's loaded.
func (i *Interactive) slashLore() {
	th := i.cfg.Theme
	ctx := context.Background()

	// What is AUTHORED rides the lore surface; what FIRED rides the context
	// one. Two surfaces because they answer different questions — the first is
	// a property of the files on disk, the second of the last turn — and the
	// activation trace was built for the Usage pane, which reads the context
	// breakdown anyway.
	//
	// Worth stating because this went wrong once: the fired/dropped sections
	// below were dark for releases, and a comment here said the wire carried no
	// per-turn firing. It did (ContextBreakdown.LoreFired, populated per turn
	// and covered by TestContextBreakdownLoreFired) — just not on the surface
	// this function was looking at.
	var entries []lore.Entry
	if sf, err := i.cfg.Carrier.Surface(ctx, i.carrierSession(), "lore"); err == nil && sf.Lore != nil {
		for _, e := range sf.Lore.Entries {
			entries = append(entries, lore.Entry{Name: e.Name, Keys: e.Keys, Constant: e.Constant, Source: e.Source})
		}
	}
	var fired, dropped []string
	if bd, err := i.cfg.Carrier.Context(ctx, i.carrierSession()); err == nil {
		for _, e := range bd.LoreFired {
			label := e.Name
			if label == "" {
				label = e.Source
			}
			// A triggered entry earned its place by matching; saying which key
			// matched is the difference between "this fired" and "this fired
			// because you said X", which is what someone debugging lore wants.
			if !e.Constant && len(e.Keys) > 0 {
				label += " (" + strings.Join(e.Keys, ", ") + ")"
			}
			if e.Dropped {
				dropped = append(dropped, label)
			} else {
				fired = append(fired, label)
			}
		}
	}

	rows := []string{th.FG256(th.Accent, "  "+i18n.T("lore — active entries (this run)"))}
	if len(entries) == 0 {
		rows = append(rows, th.FG256(th.Muted, "    "+i18n.T("(none — author under .terva/lore or $TERVA_HOME/lore; --no-lore disables)")))
	} else {
		for _, e := range entries {
			name := e.Name
			if name == "" {
				name = e.Source
			}
			trigger := "always"
			if !e.Constant {
				trigger = i18n.T("keys: %s", strings.Join(e.Keys, ", "))
			}
			meta := "  ·  " + trigger
			if e.Source != "" {
				meta += "  ·  " + e.Source
			}
			rows = append(rows, th.FG256(th.FG, "    "+name)+th.FG256(th.Muted, meta))
		}
	}
	if len(fired) > 0 {
		rows = append(rows, "", th.FG256(th.Accent, "  "+i18n.T("fired last turn")))
		rows = append(rows, th.FG256(th.Muted, "    "+strings.Join(fired, ", ")))
	}
	// Never folded into the line above: a dropped entry did NOT reach the model,
	// and reading it as "fired" is exactly the silent truncation the trace
	// exists to prevent.
	if len(dropped) > 0 {
		rows = append(rows, "", th.FG256(th.Accent, "  "+i18n.T("dropped by token budget last turn")))
		rows = append(rows, th.FG256(th.Muted, "    "+strings.Join(dropped, ", ")))
	}

	i.mu.Lock()
	i.helpBlock = rows
	i.statusErr = ""
	i.statusOK = ""
	i.scrollOffset = 0
	i.mu.Unlock()
	i.invalidate()
}
