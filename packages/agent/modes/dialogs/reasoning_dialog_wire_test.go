package dialogs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
)

func mustModel(t *testing.T, provider_, id string) provider.Model {
	t.Helper()
	m, err := modelreg.FindModel(provider_, id)
	if err != nil {
		t.Fatalf("FindModel(%q, %q): %v", provider_, id, err)
	}
	return m
}

func detailFor(t *testing.T, d *ReasoningDialog, rung string) string {
	t.Helper()
	for _, r := range d.rows {
		if r.label == rung {
			return r.detail
		}
	}
	t.Fatalf("no row labelled %q", rung)
	return ""
}

// The bug this replaces: the dialog printed a ladder-wide token budget for
// every model, so a Codex/Responses model — which takes an effort enum and no
// budget at all — was told "~8k tokens of thinking" for a request that carried
// no such number. Nothing in the suite could see it, because the suite never
// looked at what the row said for that model.
func TestEffortWireModelsAreNeverDescribedInTokens(t *testing.T) {
	for _, tc := range []struct{ provider_, id string }{
		{"openai-codex", "gpt-5.6-sol"},
		// A Gemini 3.x model: that generation takes thinkingLevel (an enum) and
		// no budget. Was gemini-3-pro-preview until Google retired it upstream
		// (404 "no longer available") and it left the catalog.
		{"google", "gemini-3.1-pro-preview"},
		{"groq", "openai/gpt-oss-120b"},
	} {
		d := NewReasoningDialog()
		d.Open("", "", mustModel(t, tc.provider_, tc.id))
		for _, r := range d.rows {
			if strings.Contains(r.detail, "tokens of thinking") {
				t.Errorf("%s row %q promises a token budget, but this model takes an effort enum: %q",
					tc.id, r.label, r.detail)
			}
		}
	}
}

// The other half, which keeps the fix above from being "delete the budget
// wording": a model that genuinely takes a budget must still show one, and it
// must be the CLAMPED value the builder sends rather than the ladder constant.
func TestBudgetWireModelsStillShowTheirClampedBudget(t *testing.T) {
	m := mustModel(t, "anthropic", "claude-haiku-4-5")
	m.MaxOutput = 32000
	reg := provider.NewRegistry()
	reg.SetUserModels([]provider.Model{m})
	m, err := reg.FindModel(m.Provider, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	d := NewReasoningDialog()
	d.Open("", "", m)

	medium := detailFor(t, d, "medium")
	if !strings.Contains(medium, "tokens of thinking") {
		t.Errorf("a budget-wire model lost its budget wording: %q", medium)
	}

	// maximum's ladder value is 32768, but this model's output cap forces the
	// builder lower. The row must show what is sent, not what the ladder says.
	got := detailFor(t, d, "maximum")
	if strings.Contains(got, "32k") {
		t.Errorf("maximum row shows the unclamped ladder budget: %q", got)
	}
	want := provider.ReasoningEffectFor(m, "maximum")
	if want.Budget == 0 {
		t.Fatal("fixture no longer sends a budget — pick another budget-wire model")
	}
	wire := clampedBudgetOnWire(t, reg, m.ID)
	if want.Budget != wire {
		t.Fatalf("display budget = %d, wire budget = %d", want.Budget, wire)
	}
	if !strings.Contains(got, fmt.Sprintf("~%dk tokens of thinking", wire/1024)) {
		t.Fatalf("display %q does not describe wire budget %d", got, wire)
	}
}

func clampedBudgetOnWire(t *testing.T, cat provider.ModelCatalog, id string) int {
	t.Helper()
	captured := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		captured <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer srv.Close()
	client := provider.WithCatalog(provider.NewAnthropic("synthetic", srv.URL), cat)
	events, err := client.Stream(t.Context(), provider.Request{Model: id, Reasoning: "maximum", ReasoningSet: true})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	var wire struct {
		Thinking struct {
			Budget int `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(<-captured, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Thinking.Budget
}

// A rung that lands on the same wire value as another says so, and names the
// rung a user would recognize rather than whichever came first in the ladder.
func TestCollapsedRungsNameTheCanonicalRung(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("", "", mustModel(t, "groq", "openai/gpt-oss-120b"))

	// minimum and low both send effort "low"; "low" is the one named.
	if got := detailFor(t, d, "minimum"); !strings.Contains(got, "same as low") {
		t.Errorf("minimum does not point at low: %q", got)
	}
	// ...and the canonical rung itself carries no annotation, or the dialog
	// would claim low is the same as itself.
	if got := detailFor(t, d, "low"); strings.Contains(got, "same as") {
		t.Errorf("the canonical rung is annotated as a duplicate: %q", got)
	}
	// The top collapses onto high, which is the rung that actually differs.
	for _, rung := range []string{"maximum", "max"} {
		if got := detailFor(t, d, rung); !strings.Contains(got, "same as high") {
			t.Errorf("%s does not point at high: %q", rung, got)
		}
	}
}

// A model whose top rung IS native must not be annotated as a duplicate — the
// two notes are mutually exclusive, and running them together was how the max
// row could have said both.
func TestNativeMaxIsNotAlsoCalledADuplicate(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("", "", mustModel(t, "openai-codex", "gpt-5.6-sol"))
	got := detailFor(t, d, "max")
	if !strings.Contains(got, "native") {
		t.Errorf("gpt-5.6 max row lost its native note: %q", got)
	}
	if strings.Contains(got, "same as") {
		t.Errorf("gpt-5.6 max row is both native and a duplicate: %q", got)
	}
}
