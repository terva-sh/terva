package testsupport

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The opencode-go hiding example in docs/models.md must still describe the
// catalog it is derived from.
//
// That snippet lists nine model ids to hide, and the prose around it claims
// arithmetic: the gateway serves 33 models, promotes 24, and the extra nine are
// the difference. All of it was computed from the catalog on the day it was
// written, and none of it is self-maintaining. The doc says so itself — "it goes
// stale the moment OpenCode retires or adds a model, and nothing will announce
// that" — and this test is the thing that announces it.
//
// The failure it prevents is a reader pasting nine lines that no longer mean
// what the page says they mean: an id the gateway retired (a rule matching
// nothing, silently), or a model added since (visible when the page promises it
// was trimmed away). Neither breaks a build, and neither is visible without
// re-deriving by hand, which is exactly the sort of chore that does not happen.
//
// Deliberately NOT a check against opencode.ai. A test that reaches the network
// fails on a plane, and the marketing page is not a contract we can pin. This
// asserts something narrower and fully in-tree: the doc and the baked catalog
// agree with each other. When `just models-sync` moves the catalog, this fails
// until someone re-derives the snippet, which is the whole point.
//
// Scoped to this one snippet on purpose. It is the only place in the docs that
// enumerates model ids rather than demonstrating a pattern, so it is the only
// one that rots this way; a second such example should extend this test rather
// than get its own.
func TestOpenCodeGoDocSnippetMatchesCatalog(t *testing.T) {
	doc, err := os.ReadFile("../../docs/models.md")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile("../../packages/provider/catalog_builtin.go")
	if err != nil {
		t.Fatal(err)
	}

	// ---- what the catalog actually carries ----

	idRE := regexp.MustCompile(`\{Provider: "opencode-go", ID: "([^"]+)"`)
	served := map[string]bool{}
	for _, m := range idRE.FindAllStringSubmatch(string(catalog), -1) {
		served[m[1]] = true
	}
	if len(served) == 0 {
		t.Fatal("no opencode-go entries found in catalog_builtin.go — the scan is " +
			"broken, not the tree. Fix this test's regex before trusting it again.")
	}

	// ---- what the snippet says to hide ----

	blockRE := regexp.MustCompile("(?s)#### When there is no pattern to write.*?```json\n(.*?)\n```")
	block := blockRE.FindStringSubmatch(string(doc))
	if block == nil {
		t.Fatal("could not find the opencode-go hiding snippet in docs/models.md " +
			`(heading "#### When there is no pattern to write" followed by a json block). ` +
			"If the section was renamed or removed, update this test to match — do not " +
			"delete the check, or the snippet silently stops being verified.")
	}
	var cfg struct {
		HiddenModels []string `json:"hidden_models"`
	}
	if err := json.Unmarshal([]byte(block[1]), &cfg); err != nil {
		t.Fatalf("the snippet in docs/models.md is not valid JSON, so nobody can paste "+
			"it into config.json: %v", err)
	}
	if len(cfg.HiddenModels) == 0 {
		t.Fatal("the snippet parsed but lists no models — the scan is broken, not the tree.")
	}

	// ---- every hidden id must still exist ----

	var retired []string
	hidden := map[string]bool{}
	for _, rule := range cfg.HiddenModels {
		id := strings.TrimPrefix(rule, "opencode-go/")
		if id == rule {
			t.Errorf("snippet rule %q is not an opencode-go key; this example is meant to "+
				"enumerate that one provider", rule)
			continue
		}
		hidden[id] = true
		if !served[id] {
			retired = append(retired, id)
		}
	}
	sort.Strings(retired)
	if len(retired) > 0 {
		t.Errorf("the snippet hides %d model(s) the catalog no longer carries: %s\n"+
			"Those rules now match nothing. Re-derive the list against the catalog and "+
			"update docs/models.md.", len(retired), strings.Join(retired, ", "))
	}

	// ---- the prose arithmetic must still hold ----

	claimRE := regexp.MustCompile(`OpenCode Go serves (\d+)\s+models while its landing page promotes (\d+)`)
	claim := claimRE.FindStringSubmatch(string(doc))
	if claim == nil {
		t.Fatal(`could not find the "OpenCode Go serves N models while its landing page ` +
			`promotes M" claim in docs/models.md — if the sentence was reworded, update ` +
			"this test so the numbers stay checked.")
	}
	claimedServed, claimedPromoted := atoiOrFatal(t, claim[1]), atoiOrFatal(t, claim[2])

	if len(served) != claimedServed {
		t.Errorf("docs/models.md says opencode-go serves %d models; the catalog carries %d.\n"+
			"A resync moved the catalog. Re-derive the hidden list and update the prose.",
			claimedServed, len(served))
	}
	if visible := len(served) - len(hidden); visible != claimedPromoted {
		t.Errorf("docs/models.md says %d models remain visible; hiding the snippet's %d "+
			"of the catalog's %d leaves %d.", claimedPromoted, len(hidden), len(served), visible)
	}

	// The prose also spells the count in words, which drifts independently of
	// the digits above: retire one hidden model and update the numbers, and
	// "the extra nine" can survive as the only wrong thing on the page.
	wordRE := regexp.MustCompile(`The extra ([a-z]+) are`)
	if w := wordRE.FindStringSubmatch(string(doc)); w != nil {
		if n, ok := numberWords[w[1]]; !ok {
			t.Errorf("cannot read the spelled count %q in docs/models.md; add it to "+
				"numberWords or reword the sentence", w[1])
		} else if n != len(hidden) {
			t.Errorf("docs/models.md says %q (%d) extra models; the snippet hides %d",
				w[1], n, len(hidden))
		}
	}
}

var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
	"seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20,
}

func atoiOrFatal(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}
