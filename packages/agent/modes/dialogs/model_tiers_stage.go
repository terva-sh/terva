package dialogs

// Stage 3 of the /model dialog: the swarm tier ladder for one provider — which
// model a sub-agent gets for `tier: weak`, `medium` or `strong`, and how hard
// it thinks.
//
// It shows what each rung RESOLVES to, not what config holds. An empty
// swarm_tiers is the ordinary case and says nothing about whether the ladder is
// right: google's medium and strong rungs once resolved to image-generation
// models with config completely empty, and a screen that listed overrides would
// have shown three blank rows while it was live.

import (
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// ShowTiers installs a ladder the host fetched and switches to the tier stage.
func (d *ModelDialog) ShowTiers(v ctrlproto.ModelTiersView) {
	d.tiers = v
	if d.tierCursor >= len(v.Rungs) {
		d.tierCursor = 0
	}
	d.tierRung = ""
	d.stage = stageTiers
}

// TierStageActive reports whether the ladder is on screen, so the host knows a
// refreshed view should be delivered to ShowTiers rather than dropped.
func (d *ModelDialog) TierStageActive() bool { return d.active && d.stage == stageTiers }

func (d *ModelDialog) handleTierKey(k tui.Key) modelDialogAction {
	switch k.Kind {
	case tui.KeyEsc:
		// Back to the provider list rather than closing: the ladder was entered
		// from there, and esc that closed the whole dialog would make a glance
		// at one provider's tiers cost the model list too.
		d.stage = stageProvider
		return modelDialogAction{}
	case tui.KeyUp:
		if d.tierCursor > 0 {
			d.tierCursor--
		}
	case tui.KeyDown:
		if d.tierCursor < len(d.tiers.Rungs)-1 {
			d.tierCursor++
		}
	case tui.KeyEnter:
		// Pick a model for this rung, in the model list the dialog already has.
		// Reusing it is the point: the rung is filled from the same catalogue,
		// with the same filtering, that picking a session model uses.
		r, ok := d.currentRung()
		if !ok {
			return modelDialogAction{}
		}
		d.tierRung = r.Rung
		d.enterProvider(providerRow{name: d.tiers.Provider, label: d.tiers.Provider})
		return modelDialogAction{}
	case tui.KeyCtrlT:
		// Cycle the rung's thinking level. Echoes Pinned rather than the
		// resolved model: sending the resolved id would freeze a rung that had
		// been tracking the family rule, and sending nothing would drop a pin.
		r, ok := d.currentRung()
		if !ok {
			return modelDialogAction{}
		}
		return modelDialogAction{
			TierSet:   true,
			Provider:  d.tiers.Provider,
			Rung:      r.Rung,
			Model:     r.Pinned,
			Reasoning: nextTierThinking(r, d.tiers.Provider),
		}
	case tui.KeyRune:
		if k.Rune == 'r' || k.Rune == 'R' {
			r, ok := d.currentRung()
			if !ok {
				return modelDialogAction{}
			}
			return modelDialogAction{TierReset: true, Provider: d.tiers.Provider, Rung: r.Rung}
		}
	}
	return modelDialogAction{}
}

func (d *ModelDialog) currentRung() (ctrlproto.ModelTierRung, bool) {
	if d.tierCursor < 0 || d.tierCursor >= len(d.tiers.Rungs) {
		return ctrlproto.ModelTierRung{}, false
	}
	return d.tiers.Rungs[d.tierCursor], true
}

// nextTierThinking advances a rung's level around the ladder its own model
// understands, ending at "" (leave it to the child). The options come from the
// resolved model rather than a fixed list, for the same reason the model editor
// asks: rungs that reach a given model as one wire value are one choice, and
// offering both halves asks the user to pick between two spellings.
func nextTierThinking(r ctrlproto.ModelTierRung, providerID string) string {
	var opts []string
	if m, err := provider.FindModel(providerID, r.Model); err == nil {
		for _, rung := range provider.ReasoningLadderFor(m) {
			if rung.SameAs == "" {
				opts = append(opts, rung.Level)
			}
		}
	}
	if len(opts) == 0 {
		return ""
	}
	for i, o := range opts {
		if o == r.Reasoning {
			if i+1 < len(opts) {
				return opts[i+1]
			}
			return ""
		}
	}
	return opts[0]
}

// tierRows renders the ladder.
//
// The row is measured as PLAIN text and coloured afterwards. truncateLineSafe
// counts runes and knows nothing about escapes, so truncating an
// already-coloured line can cut through the middle of one and leak the tail
// onto the screen.
func (d *ModelDialog) tierRows(th tui.Theme, width int) []string {
	out := []string{
		th.FG256(th.Muted, "  "+i18n.T("the model a sub-agent gets for each swarm tier")),
		"",
	}
	// One column width for every row, from the widest model, so the tags line
	// up instead of stepping in and out with each id's length.
	modelW := 0
	for _, r := range d.tiers.Rungs {
		if n := len([]rune(tierModelText(r))); n > modelW {
			modelW = n
		}
	}
	for i, r := range d.tiers.Rungs {
		marker := "  "
		if i == d.tierCursor {
			marker = "> "
		}
		model := tierModelText(r)
		var tags []string
		if r.Label != "" && r.Label != r.Model {
			tags = append(tags, r.Label)
		}
		if r.Reasoning != "" {
			tags = append(tags, i18n.T("thinking %s", r.Reasoning))
		}
		if r.Source != "" {
			tags = append(tags, r.Source)
		}
		// Padded only when something follows it, so a row with no tags does not
		// carry a tail of trailing spaces.
		head := marker + padRight(r.Rung, 8) + model
		if len(tags) > 0 {
			head = marker + padRight(r.Rung, 8) + padRight(model, modelW)
		}
		tail := ""
		if len(tags) > 0 {
			tail = "  (" + strings.Join(tags, ", ") + ")"
		}
		if len([]rune(head+tail)) > width {
			out = append(out, truncateLineSafe(head+tail, width))
			continue
		}
		out = append(out, head+th.FG256(th.Muted, tail))
	}
	return out
}

// tierModelText is the model column for a rung. A rung that resolves to
// nothing is spelled out rather than left as a dash: it means a sub-agent
// asking for this tier silently runs on the HOST model, at host cost and host
// speed, which is worth saying rather than implying.
func tierModelText(r ctrlproto.ModelTierRung) string {
	if r.Model == "" {
		return i18n.T("— (falls back to the host model)")
	}
	return r.Model
}

// tierHint is the key legend for the stage.
func (d *ModelDialog) tierHint() string {
	return i18n.T("↑/↓, enter pick model, ctrl+t thinking, r reset rung, esc back")
}

// The glyph column on the stage-1 provider list, so the state of a ladder is
// visible without opening it. This is the same question stage 3 answers, asked
// of every provider at once: is each rung filled, and is it filled by config or
// by a built-in rule.
//
// Shape carries the whole meaning, with no colour at all. The row is coloured
// as a unit — muted, or the cursor highlight — so a per-glyph colour would have
// to be an escape sequence inside the string that PadHighlight then measures
// and pads around, and neither it nor truncateLineSafe is ANSI-aware. Three
// distinguishable shapes need none of that, and they survive a muted palette, a
// light terminal, and a colourblind reader besides.
const (
	tierGlyphOverride = "●"
	tierGlyphBuiltin  = "○"
	// Not "·": this dialog already spends that character as a header separator
	// ("model · provider", "tiers · google"), and a glyph that also appears as
	// punctuation two lines up is not a glyph.
	tierGlyphEmpty = "-"
)

// SetTierSummaries installs every provider's ladder for the glyph column. The
// host fetches them; passing nil (or never calling this) drops the column and
// its legend, which is why a session that cannot read ladders still opens.
func (d *ModelDialog) SetTierSummaries(views map[string]ctrlproto.ModelTiersView) {
	d.tierSummary = views
}

// tierGlyphs renders one provider row's ladder, one glyph per rung in ladder
// order. Empty for the ★ favorites row: tiers belong to a provider, and that
// row spans all of them.
func (d *ModelDialog) tierGlyphs(r providerRow) string {
	if r.fav {
		return ""
	}
	v, ok := d.tierSummary[r.name]
	if !ok || len(v.Rungs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, rung := range v.Rungs {
		switch rung.Source {
		case "override":
			b.WriteString(tierGlyphOverride)
		case "built-in":
			b.WriteString(tierGlyphBuiltin)
		default:
			// No source means nothing resolved, which is the state worth
			// seeing: a `tier:` spawn against this rung silently inherits the
			// host model instead of getting the sub-agent that was asked for.
			b.WriteString(tierGlyphEmpty)
		}
	}
	return b.String()
}

// tierLegend names the rungs the glyphs stand for, in the same order. It reads
// the rung names off the summary rather than hardcoding them, so a ladder that
// grows a rung cannot leave the legend describing the old one.
func (d *ModelDialog) tierLegend() string {
	var rungs []string
	for _, r := range d.filteredProviders() {
		if v, ok := d.tierSummary[r.name]; ok && len(v.Rungs) > 0 && !r.fav {
			for _, rung := range v.Rungs {
				rungs = append(rungs, rung.Rung)
			}
			break
		}
	}
	if len(rungs) == 0 {
		return ""
	}
	return i18n.T("  tiers %s: %s set, %s built-in, %s empty",
		strings.Join(rungs, "/"), tierGlyphOverride, tierGlyphBuiltin, tierGlyphEmpty)
}

// UpdateTierSummary replaces one provider's row in the glyph column, after a
// rung was pinned or reset.
func (d *ModelDialog) UpdateTierSummary(provider string, v ctrlproto.ModelTiersView) {
	if d.tierSummary == nil {
		// Nothing was ever loaded, so there is no column to keep current, and
		// seeding it from one edit would draw glyphs for a single provider and
		// blanks for the rest — which reads as "the others have no ladder".
		return
	}
	d.tierSummary[provider] = v
}

// tierGlyphWidth is how many glyph cells every provider row reserves: the
// longest ladder in the summary. Rungs are per provider on the wire, so a
// provider with a shorter ladder pads rather than shifting the rows below it.
func (d *ModelDialog) tierGlyphWidth() int {
	w := 0
	for _, v := range d.tierSummary {
		if n := len(v.Rungs); n > w {
			w = n
		}
	}
	return w
}
