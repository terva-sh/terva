package testsupport

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Catalog sync can retire IDs in the hiding example or invalidate its counts.
// Check the baked catalog without requiring network access.
func TestOpenCodeGoDocSnippetMatchesCatalog(t *testing.T) {
	docBytes, err := os.ReadFile("../../docs/models.md")
	if err != nil {
		t.Fatal(err)
	}
	// Normalize CRLF so the regexes also match Windows checkouts.
	doc := strings.ReplaceAll(string(docBytes), "\r\n", "\n")
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
	block := blockRE.FindStringSubmatch(doc)
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

	claimRE := regexp.MustCompile(`OpenCode Go serves (\d+)\s+models; this example leaves (\d+) visible`)
	claim := claimRE.FindStringSubmatch(doc)
	if claim == nil {
		t.Fatal(`could not find the "OpenCode Go serves N models; this example leaves ` +
			`M visible" claim in docs/models.md — if the sentence was reworded, update ` +
			"this test so the numbers stay checked.")
	}
	claimedServed, claimedVisible := atoiOrFatal(t, claim[1]), atoiOrFatal(t, claim[2])

	if len(served) != claimedServed {
		t.Errorf("docs/models.md says opencode-go serves %d models; the catalog carries %d.\n"+
			"A resync moved the catalog. Re-derive the hidden list and update the prose.",
			claimedServed, len(served))
	}
	if visible := len(served) - len(hidden); visible != claimedVisible {
		t.Errorf("docs/models.md says %d models remain visible; hiding the snippet's %d "+
			"of the catalog's %d leaves %d.", claimedVisible, len(hidden), len(served), visible)
	}
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
