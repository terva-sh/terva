package modes

// modelPicker is the shared filter/scroll/render core behind the
// /model picker and the rescue picker (TUI plan Phase 1.2). The two
// dialogs were ~80% copy-paste and had drifted: rescue was missing
// capability filters, page keys, stable column widths, and the
// vision glyph. Both now ride the same core and differ only in how
// they build the candidate catalog and what their selection means.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

type modelPicker struct {
	all     []provider.Model // full catalog, sorted (favorites first)
	view    []provider.Model // filtered view shown to the user
	cursor  int
	current string // currently active model id ("" = no you-are-here marker)
	query   string // live filter text typed by the user
	maxRows int    // scroll window height

	// favorites are "provider/id" keys pinned to the top of the list and
	// flagged with a ★. Set before setCatalog (nil = no favorites, e.g. the
	// rescue picker), so setCatalog's signature stays unchanged.
	favorites map[string]bool

	// Column widths are computed once in setCatalog across the entire
	// catalog so the layout stays stable while the user scrolls or
	// filters. Recomputing per visible window would make the columns
	// jitter left/right whenever rows entered or left the band.
	provW int
	idW   int
}

// setCatalog installs the candidate models (sorting them), remembers
// the current model id for the ● marker and cursor snap, and resets
// the filter.
func (p *modelPicker) setCatalog(models []provider.Model, current string, maxRows int) {
	p.all = sortModelsFavFirst(models, p.favorites)
	p.current = current
	p.query = ""
	p.maxRows = maxRows
	p.provW, p.idW = columnWidths(p.all)
	p.refilter()
}

// refilter rebuilds view from all according to query, and snaps the
// cursor to either the current model (if visible) or the first row.
//
// Query words starting with ':' are capability filters (":img",
// ":reasoning" — see capFilterToken); the rest fuzzy-matches
// provider/id/name. Tokens must be split off BEFORE
// normalizeModelQuery, which strips the ':' marker.
func (p *modelPicker) refilter() {
	var capFilters []provider.Capability
	var textParts []string
	for _, tok := range strings.Fields(p.query) {
		if strings.HasPrefix(tok, ":") {
			if c, ok := capFilterToken(tok[1:]); ok {
				capFilters = append(capFilters, c)
				continue
			}
			// Unrecognized (or still being typed) token: no effect.
			continue
		}
		textParts = append(textParts, tok)
	}
	needle := normalizeModelQuery(strings.Join(textParts, " "))

	out := make([]provider.Model, 0, len(p.all))
	for _, m := range p.all {
		if needle != "" && !strings.Contains(normalizeModelQuery(m.Provider+" "+m.ID+" "+m.DisplayName), needle) {
			continue
		}
		matched := true
		for _, c := range capFilters {
			if !m.Has(c) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		out = append(out, m)
	}
	p.view = out
	p.cursor = 0
	for i, m := range p.view {
		if p.current != "" && m.ID == p.current {
			p.cursor = i
			break
		}
	}
}

// cursorToKey moves the cursor to the first view row whose modelKey matches
// key; a no-op when key is empty or not in the current view. The shared way to
// keep a selection on the same model across a re-sort or catalog refresh
// (setCatalog/refilter otherwise snap to the active model or row 0).
func (p *modelPicker) cursorToKey(key string) {
	if key == "" {
		return
	}
	for i, m := range p.view {
		if modelKey(m) == key {
			p.cursor = i
			return
		}
	}
}

// reload re-installs the catalog (re-sorting for the current favorites) while
// preserving the live filter and the highlighted model — for a favorite-toggle
// re-sort or a background-discovery refresh. Plain setCatalog drops both.
func (p *modelPicker) reload(models []provider.Model) {
	q := p.query
	sel := ""
	if m, ok := p.selected(); ok {
		sel = modelKey(m)
	}
	p.setCatalog(models, p.current, p.maxRows)
	if q != "" {
		p.query = q
		p.refilter()
	}
	p.cursorToKey(sel)
}

// handleNavKey consumes cursor movement and filter editing, and
// reports whether it handled the key. Enter/Esc are left to the
// owning dialog, whose semantics differ.
func (p *modelPicker) handleNavKey(k tui.Key) bool {
	switch k.Kind {
	case tui.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
	case tui.KeyDown:
		if p.cursor < len(p.view)-1 {
			p.cursor++
		}
	case tui.KeyPageUp:
		if len(p.view) > 0 {
			p.cursor -= p.maxRows
			if p.cursor < 0 {
				p.cursor = 0
			}
		}
	case tui.KeyPageDown:
		if len(p.view) > 0 {
			p.cursor += p.maxRows
			if p.cursor >= len(p.view) {
				p.cursor = len(p.view) - 1
			}
		}
	case tui.KeyBackspace:
		if len(p.query) > 0 {
			// Drop one rune from the query.
			r := []rune(p.query)
			p.query = string(r[:len(r)-1])
			p.refilter()
		}
	case tui.KeyRune:
		if k.Alt || k.Ctrl {
			return false
		}
		// Only printable ASCII is useful for narrowing.
		if k.Rune >= 0x20 && k.Rune < 0x7f {
			p.query += string(k.Rune)
			p.refilter()
		}
	default:
		return false
	}
	return true
}

// selected returns the model under the cursor.
func (p *modelPicker) selected() (provider.Model, bool) {
	if len(p.view) == 0 || p.cursor < 0 || p.cursor >= len(p.view) {
		return provider.Model{}, false
	}
	return p.view[p.cursor], true
}

// hintLine builds the muted hint row: the query-aware match count
// while filtering, otherwise the dialog's base hint.
func (p *modelPicker) hintLine(base string) string {
	if p.query == "" {
		return base
	}
	if len(p.view) == 1 {
		return i18n.T("filter: %s (%d match)", p.query, len(p.view))
	}
	return i18n.T("filter: %s (%d matches)", p.query, len(p.view))
}

// renderRows returns the model rows in the scroll window (cursor row
// highlighted) plus the more-above/below and position indicators.
// The caller renders its own header, hint, and empty states.
func (p *modelPicker) renderRows(th tui.Theme, width int) []string {
	var lines []string
	start, end := cursorWindow(p.cursor, len(p.view), p.maxRows)
	for i := start; i < end; i++ {
		m := p.view[i]
		reason := " "
		if m.Has(provider.CapReasoning) {
			reason = "✦"
		}
		// Vision marker, same column family as the reasoning glyph.
		// Most models carry it (the capability defaults true); its
		// absence is the signal — a text-only model in a session full
		// of screenshots is worth noticing before picking it.
		vision := " "
		if m.Has(provider.CapImageInput) {
			vision = "◈"
		}
		tag := ""
		switch {
		case m.Speculative:
			tag = i18n.T("[speculative] ")
		case m.Source == "user":
			// Carries a models.json override (applyUserOverrides stamps
			// Source="user"). Flags which models have custom settings.
			tag = i18n.T("[edited] ")
		case m.Source == "live":
			tag = i18n.T("[live] ")
		}
		curMark := "  "
		if p.current != "" && m.ID == p.current {
			curMark = "● "
		}
		favMark := "  "
		if p.favorites[modelKey(m)] {
			favMark = "★ "
		}
		plain := fmt.Sprintf(" %s%s%s   %s %s%s  %s%s",
			curMark, favMark,
			padRight(m.Provider, p.provW),
			padRight(m.ID, p.idW),
			reason, vision, tag, m.DisplayName)
		if i == p.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}

	if start > 0 {
		lines = append(lines, windowMoreAbove(th, start))
	}
	if end < len(p.view) {
		lines = append(lines, windowMoreBelow(th, len(p.view), end))
	}
	if len(p.view) > p.maxRows {
		lines = append(lines, th.FG256(th.Muted, fmt.Sprintf("   (%d/%d)", p.cursor+1, len(p.view))))
	}
	return lines
}

// capFilterToken maps a ":token" spelling (without the colon) to the
// capability it filters on. Several aliases per capability because the
// picker is typed blind.
func capFilterToken(tok string) (provider.Capability, bool) {
	switch strings.ToLower(tok) {
	case "img", "image", "images", "vision":
		return provider.CapImageInput, true
	case "reasoning", "reason", "thinking":
		return provider.CapReasoning, true
	case "imggen", "imagegen", "image-gen", "image-out", "imageout":
		return provider.CapImageOutput, true
	}
	return "", false
}

// sortedModels returns a fresh slice sorted by provider, then model id.
func sortedModels(in []provider.Model) []provider.Model {
	out := append([]provider.Model(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// modelKey is the "provider/id" identity used for favorites (ids can collide
// across providers, so the provider must be part of the key).
func modelKey(m provider.Model) string { return m.Provider + "/" + m.ID }

// sortModelsFavFirst sorts like sortedModels but floats favorited models to
// the top (in their natural order among themselves). A nil/empty fav map gives
// exactly sortedModels' order, so non-favorite callers are unaffected.
func sortModelsFavFirst(in []provider.Model, fav map[string]bool) []provider.Model {
	out := append([]provider.Model(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := fav[modelKey(out[i])], fav[modelKey(out[j])]
		if fi != fj {
			return fi // favorites first
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// toggleFavorite flips the selected model's favorite flag in the local set,
// re-sorts so it floats to/from the top, and keeps the cursor on it. Returns
// the model, its new state, and whether anything was toggled (false when the
// list is empty). The caller persists the change.
func (p *modelPicker) toggleFavorite() (provider.Model, bool, bool) {
	m, ok := p.selected()
	if !ok {
		return provider.Model{}, false, false
	}
	if p.favorites == nil {
		p.favorites = map[string]bool{}
	}
	key := modelKey(m)
	on := !p.favorites[key]
	if on {
		p.favorites[key] = true
	} else {
		delete(p.favorites, key)
	}
	// reload re-sorts for the changed favorites and keeps the cursor on m.
	p.reload(p.all)
	return m, on, true
}

// normalizeModelQuery lowercases and strips punctuation so fuzzy
// substring matching works on both the query and haystacks. "opus46"
// and "opus-4-6" both become "opus46".
func normalizeModelQuery(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r + ('a' - 'A'))
		}
	}
	return sb.String()
}

// columnWidths returns the display-cell width of the longest provider
// name and the longest model id in rows. Each column is clamped to a
// minimum so a single-row filter still renders sensibly.
func columnWidths(rows []provider.Model) (provW, idW int) {
	const minProv, minID = 6, 12
	provW, idW = minProv, minID
	for _, m := range rows {
		if w := runewidth.StringWidth(m.Provider); w > provW {
			provW = w
		}
		if w := runewidth.StringWidth(m.ID); w > idW {
			idW = w
		}
	}
	return
}

// padRight returns s padded with spaces on the right so its display
// width equals w. Truncates with an ellipsis when s is too wide to
// avoid blowing out the column.
func padRight(s string, w int) string {
	cw := runewidth.StringWidth(s)
	if cw == w {
		return s
	}
	if cw < w {
		return s + strings.Repeat(" ", w-cw)
	}
	return runewidth.Truncate(s, w, "…")
}
