package modes

import (
	"strings"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/i18n"
)

// resetLoreFired clears the "fired last turn" record — invoked wherever the
// transcript is reset (/clear, /new) so the record can't outlive the
// conversation it describes.
func (i *Interactive) resetLoreFired() {
	if i.cfg.LoreFiredReset != nil {
		i.cfg.LoreFiredReset()
	}
}

// slashLore renders this run's active lore entries as an inline block above
// the chat (like /help), cleared on the next prompt. Lore is otherwise silent
// in-session, so this is the observability surface for what's loaded.
func (i *Interactive) slashLore() {
	th := i.cfg.Theme
	var entries []lore.Entry
	if i.cfg.LoreList != nil {
		entries = i.cfg.LoreList()
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
	if i.cfg.LoreFired != nil {
		if fired := i.cfg.LoreFired(); len(fired) > 0 {
			rows = append(rows, "", th.FG256(th.Accent, "  "+i18n.T("fired last turn")))
			rows = append(rows, th.FG256(th.Muted, "    "+strings.Join(fired, ", ")))
		}
	}
	if i.cfg.LoreDropped != nil {
		if dropped := i.cfg.LoreDropped(); len(dropped) > 0 {
			rows = append(rows, "", th.FG256(th.Accent, "  "+i18n.T("dropped by token budget last turn")))
			rows = append(rows, th.FG256(th.Muted, "    "+strings.Join(dropped, ", ")))
		}
	}

	i.mu.Lock()
	i.helpBlock = rows
	i.statusErr = ""
	i.statusOK = ""
	i.scrollOffset = 0
	i.mu.Unlock()
	i.invalidate()
}
