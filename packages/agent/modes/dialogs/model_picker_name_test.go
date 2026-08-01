package dialogs

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

const longOllamaID = "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL"

// A renamed model leads with the operator's name and trails with the id.
// Both have to be on the row: the name is what they want to read, the id is
// the only spelling that appears in logs, sessions, and --model.
func TestPickerRowSwapsNameAndIDWhenRenamed(t *testing.T) {
	renamed := provider.Model{
		Provider: "ollama", ID: longOllamaID,
		DisplayName: "Qwen Coder", DisplayNameSet: true,
	}
	if got := pickerLead(renamed); got != "Qwen Coder" {
		t.Errorf("lead = %q, want the operator's name", got)
	}
	if got := pickerTrail(renamed); got != longOllamaID {
		t.Errorf("trail = %q, want the id", got)
	}

	// A catalog display name is NOT a rename: it stays in the trailing
	// column, because it is longer than the id it would displace.
	catalog := provider.Model{Provider: "anthropic", ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5 (latest)"}
	if got := pickerLead(catalog); got != "claude-sonnet-4-5" {
		t.Errorf("catalog lead = %q, want the id", got)
	}
	if got := pickerTrail(catalog); got != "Claude Sonnet 4.5 (latest)" {
		t.Errorf("catalog trail = %q, want the display name", got)
	}
}

// One long id used to widen the id column for EVERY row — columnWidths took an
// unclamped max. The clamp is what makes the list readable on a box with a
// handful of ollama models; padRight ellipsizes the overrun.
func TestPickerIDColumnIsClamped(t *testing.T) {
	rows := []provider.Model{
		{Provider: "ollama", ID: longOllamaID},
		{Provider: "ollama", ID: "llama3"},
	}
	_, idW := columnWidths(rows)
	if idW > 32 {
		t.Errorf("id column %d wide; one long id must not blow out the row", idW)
	}
	if idW < 12 {
		t.Errorf("id column %d is below the minimum", idW)
	}

	// And renaming actually shrinks it, because the lead is measured.
	rows[0].DisplayName, rows[0].DisplayNameSet = "Qwen", true
	_, renamedW := columnWidths(rows)
	if renamedW >= idW {
		t.Errorf("renaming should shrink the lead column: %d -> %d", idW, renamedW)
	}
}

// The rendered row carries both spellings and stays inside the clamp.
func TestPickerRenderShowsNameAndID(t *testing.T) {
	p := &modelPicker{maxRows: 5}
	p.setCatalog([]provider.Model{{
		Provider: "ollama", ID: longOllamaID,
		DisplayName: "Qwen Coder", DisplayNameSet: true,
	}}, "", 5)

	lines := p.renderRows(tui.Theme{}, 200)
	if len(lines) == 0 {
		t.Fatal("no rows rendered")
	}
	row := lines[0]
	if !strings.Contains(row, "Qwen Coder") {
		t.Errorf("row is missing the operator's name: %q", row)
	}
	if !strings.Contains(row, longOllamaID) {
		t.Errorf("row dropped the id entirely: %q", row)
	}
	// The name must come first — that is the whole point of the swap.
	if strings.Index(row, "Qwen Coder") > strings.Index(row, longOllamaID) {
		t.Errorf("id leads the name: %q", row)
	}
}

// An un-renamed long id still gets ellipsized in the lead column rather than
// pushing the row off the terminal.
func TestPickerLongIDIsEllipsized(t *testing.T) {
	p := &modelPicker{maxRows: 5}
	p.setCatalog([]provider.Model{{Provider: "ollama", ID: longOllamaID}}, "", 5)

	lines := p.renderRows(tui.Theme{}, 200)
	if len(lines) == 0 {
		t.Fatal("no rows rendered")
	}
	plain := widgets.StripANSIBytes(lines[0])
	if !strings.Contains(plain, "…") {
		t.Errorf("expected the clamped column to ellipsize: %q", plain)
	}
	// Nothing trails an un-renamed model (its display name IS the id), so the
	// whole row is the clamped lead plus the fixed columns.
	if w := runewidth.StringWidth(strings.TrimRight(plain, " ")); w > 60 {
		t.Errorf("row is %d cells wide; the clamp is not holding: %q", w, plain)
	}
}

// Search has to find a renamed model by EITHER spelling.
func TestPickerFiltersByNameAndID(t *testing.T) {
	p := &modelPicker{maxRows: 5}
	p.setCatalog([]provider.Model{
		{Provider: "ollama", ID: longOllamaID, DisplayName: "Qwen Coder", DisplayNameSet: true},
		{Provider: "ollama", ID: "llama3"},
	}, "", 5)

	for _, needle := range []string{"qwen coder", "unsloth", "Q4_K_XL"} {
		p.query = needle
		p.refilter()
		if len(p.view) != 1 || p.view[0].ID != longOllamaID {
			t.Errorf("query %q matched %d rows, want just the renamed model", needle, len(p.view))
		}
	}
}
