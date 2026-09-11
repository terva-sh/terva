package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// editTicketThroughRegistry runs the BUILT edit tool against a ticket file and
// returns what the model would read. Going through the registry rather than a
// hand-made EditTool is the whole point: the tools package can prove the warner
// works, and only this can prove the build turned it on.
func editTicketThroughRegistry(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	rel := filepath.Join(".tickets", "tickets", "TKT-A.md")
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("- [ ] one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: dir}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ed := r.ToolRegistry["edit"]
	if ed == nil {
		t.Fatal("no edit tool in the built registry")
	}
	args, _ := json.Marshal(map[string]any{
		"path":  filepath.ToSlash(rel),
		"edits": []map[string]any{{"oldText": "- [ ] one", "newText": "- [x] one"}},
	})
	res, err := ed.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// The direct-edit warning rides the same gate as the prompt addendum: it names
// ticket_update and ticket_transition, so it must speak only where the model can
// call them. A registry without the ticket write tools stays silent, which is
// what keeps a repository with no store from being nagged about the only route
// it has.
func TestTicketEditWarningRidesTheRegistryGate(t *testing.T) {
	t.Run("store present, the warning speaks", func(t *testing.T) {
		got := editTicketThroughRegistry(t, ticketStoreDir(t))
		if !strings.Contains(got, "ticket store") {
			t.Errorf("a session with the ticket tools did not warn on a direct edit: %q", got)
		}
		if !strings.Contains(got, "ticket_update") {
			t.Errorf("the warning did not name the tool for the job: %q", got)
		}
	})

	// A .tickets directory with a ticket-shaped file in it, and no store: no
	// config.yml, so nothing registers the ticket tools. The path looks exactly
	// like the case above, which is what makes this a test of the GATE rather
	// than of the path check the tools package already covers.
	t.Run("no store, the warning stays quiet", func(t *testing.T) {
		got := editTicketThroughRegistry(t, testsupport.TempDir(t))
		if strings.Contains(got, "ticket store") {
			t.Errorf("a session with no ticket tools warned about a route it cannot offer: %q", got)
		}
	})
}
