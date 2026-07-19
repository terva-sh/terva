package build

import (
	"fmt"
	"sort"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// PromptSizeRow attributes prompt weight to one source within a section: its
// byte length and an approximate token count (terva's canonical bytes/4).
type PromptSizeRow struct {
	Section string `json:"section"`
	Source  string `json:"source"`
	Bytes   int    `json:"bytes"`
	Tokens  int    `json:"tokens"`
	Detail  string `json:"detail,omitempty"` // e.g. a tool's one-line description
}

// PromptSizes is the byte/token attribution of an assembled prompt — where the
// weight actually goes, section by section and source by source. Tool schemas
// are attributed per tool at their real wire cost (name + description + JSON
// schema), not the manifest's name-only logical view, because that overhead is
// exactly what H2 asks the harness to make visible.
//
// It is a diagnostic and a LOWER BOUND: an offline dump cannot see the live
// per-turn ephemeral context (the task card, extension ephemeral) or the
// extension/MCP tool schemas that merge at runtime, so a real turn is heavier
// than what is reported here.
type PromptSizes struct {
	Rows []PromptSizeRow `json:"rows"`
}

// BuildPromptSizes attributes the weight of the prompt that would be sent for
// the given history. It reuses BuildPromptManifest for the text sections (so
// the two can never disagree about what is present) and computes the tools
// section from the registry at real schema weight.
func (r *Resolved) BuildPromptSizes(msgs []provider.Message) PromptSizes {
	m := r.BuildPromptManifest(msgs)
	var rows []PromptSizeRow
	for _, sec := range m.Sections {
		if sec.Name == sectionTools {
			continue // computed from the registry below, at real schema weight
		}
		for _, seg := range sec.Segments {
			rows = append(rows, PromptSizeRow{
				Section: sec.Name,
				Source:  seg.Source,
				Bytes:   len(seg.Text),
				Tokens:  approxTokens(len(seg.Text)),
			})
		}
	}
	rows = append(rows, toolSizeRows(r.ToolRegistry)...)
	return PromptSizes{Rows: rows}
}

// toolSizeRows attributes each registered tool its real advertised weight —
// name + description + schema, the fields Registry.Specs serializes to the
// wire — heaviest first so schema bloat is obvious at a glance.
func toolSizeRows(reg core.Registry) []PromptSizeRow {
	specs := reg.Specs()
	rows := make([]PromptSizeRow, 0, len(specs))
	for _, s := range specs {
		b := len(s.Name) + len(s.Description) + len(s.Schema)
		rows = append(rows, PromptSizeRow{
			Section: sectionTools,
			Source:  "tool:" + s.Name,
			Bytes:   b,
			Tokens:  approxTokens(b),
			Detail:  firstLine(s.Description),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Bytes > rows[j].Bytes })
	return rows
}

// approxTokens mirrors lore.ApproxTokens (bytes/4) for a raw byte count, so the
// diagnostic uses the same estimate terva uses everywhere else.
func approxTokens(bytes int) int { return bytes / 4 }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// sectionOrder is the canonical top-to-bottom prompt order for the rollup.
var sectionOrder = []string{sectionSystem, sectionMessages, sectionTail, sectionTools}

// Text renders the attribution as a section rollup plus a by-source breakdown
// for the two regions where controllable bloat lives: the system prompt and
// the tool schemas.
func (s PromptSizes) Text() string {
	total := 0
	perSection := map[string]int{}
	for _, r := range s.Rows {
		total += r.Bytes
		perSection[r.Section] += r.Bytes
	}

	var b strings.Builder
	b.WriteString("PROMPT COMPOSITION — approximate weight\n")
	b.WriteString("(offline lower bound: excludes live per-turn ephemeral context and extension/MCP tool schemas that merge at runtime)\n\n")

	b.WriteString(fmt.Sprintf("%-10s %12s %10s %7s\n", "SECTION", "BYTES", "~TOKENS", "SHARE"))
	for _, name := range orderedSections(perSection) {
		bytes := perSection[name]
		b.WriteString(fmt.Sprintf("%-10s %12s %10s %6s%%\n", name, commafy(bytes), commafy(approxTokens(bytes)), share(bytes, total)))
	}
	b.WriteString(fmt.Sprintf("%-10s %12s %10s %6s%%\n", "TOTAL", commafy(total), commafy(approxTokens(total)), "100"))

	writeBreakdown(&b, "tools — by weight (name + description + schema)", s.rowsFor(sectionTools), true)
	writeBreakdown(&b, "system — by source", s.rowsFor(sectionSystem), false)
	return b.String()
}

// orderedSections returns the sections present, canonical order first, then any
// unexpected section names sorted (so a new section can't vanish from the view).
func orderedSections(present map[string]int) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range sectionOrder {
		if _, ok := present[name]; ok {
			out = append(out, name)
			seen[name] = true
		}
	}
	var extra []string
	for name := range present {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func (s PromptSizes) rowsFor(section string) []PromptSizeRow {
	var out []PromptSizeRow
	for _, r := range s.Rows {
		if r.Section == section {
			out = append(out, r)
		}
	}
	return out
}

// writeBreakdown lists rows heaviest-first for one section. In a classified
// section (system, tail) each row also names its portability class, derived from
// the source exactly as the manifest derives it — so "which of these bytes would
// reach a foreign worker?" is answerable from the weight view, not just the text
// one. Reading the two columns together is the point: a heavy segment that is
// harness-local is weight a worker will never carry.
func writeBreakdown(b *strings.Builder, title string, rows []PromptSizeRow, withDetail bool) {
	if len(rows) == 0 {
		return
	}
	b.WriteString("\n" + title + "\n")
	for _, r := range rows {
		label := strings.TrimPrefix(r.Source, "tool:")
		// 22, not 18: `restricted-workspace` is 20 characters and had been
		// shunting its own row's numbers out of column since before there was a
		// third column to notice it.
		line := fmt.Sprintf("  %-22s %10s %9s", label, commafy(r.Bytes), "~"+commafy(r.Tokens))
		if classifiedSection(r.Section) {
			line += fmt.Sprintf("  %-15s", PortabilityOf(r.Source))
		}
		if withDetail && r.Detail != "" {
			line += "  " + r.Detail
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
}

func share(part, total int) string {
	if total == 0 {
		return "0"
	}
	return fmt.Sprintf("%d", (part*100+total/2)/total) // rounded
}

// commafy formats a non-negative int with thousands separators.
func commafy(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return s + "," + strings.Join(parts, ",")
}
