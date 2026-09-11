package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/testsupport"
)

// labelStore seeds a store, gives its config the labels declared, and creates
// one ticket for each entry of carried. The two arguments are what separate
// the three cases this file covers: a declared vocabulary, a vocabulary the
// tickets imply, and neither.
func labelStore(t *testing.T, declared []string, carried [][]string) *TicketCore {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := ticket.Init(dir, ticket.InitOptions{Actor: ticket.Actor{ID: "agent:test", Name: "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(declared) > 0 {
		writeConfigLabels(t, dir, declared)
	}
	for i, labels := range carried {
		if _, err := s.Create(context.Background(), ticket.CreateOptions{
			Title:    fmt.Sprintf("Labelled ticket %02d", i),
			Type:     "task",
			Priority: "normal",
			Labels:   labels,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return &TicketCore{CWD: dir}
}

// writeConfigLabels declares a vocabulary the way a person would, by editing
// config.yml. InitOptions carries no label field, so this is the only route.
func writeConfigLabels(t *testing.T, dir string, labels []string) {
	t.Helper()
	path := filepath.Join(dir, ".tickets", "config.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var block strings.Builder
	block.WriteString("labels:\n")
	for _, l := range labels {
		block.WriteString("  - " + l + "\n")
	}
	out := strings.Replace(string(b), "labels: []\n", block.String(), 1)
	if out == string(b) {
		t.Fatalf("config.yml holds no empty label list to replace:\n%s", b)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// labelsPropOf reads the labels property off the tool's real schema, so the
// test proves the wiring and not just the builder.
func labelsPropOf(t *testing.T, tc *TicketCore) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal((&TicketCreateTool{TicketCore: tc}).Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	var prop map[string]any
	if err := json.Unmarshal(schema.Properties["labels"], &prop); err != nil {
		t.Fatal(err)
	}
	return prop
}

func itemsEnum(prop map[string]any) ([]string, bool) {
	items, ok := prop["items"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := items["enum"].([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, fmt.Sprint(v))
	}
	return out, true
}

// A declared vocabulary is a rule the store already enforces, so the schema
// enforces it too. The order is the config's own, because a person wrote it.
func TestLabelSchemaEnumeratesADeclaredVocabulary(t *testing.T) {
	tc := labelStore(t, []string{"tui", "core", "docs"}, nil)

	prop := labelsPropOf(t, tc)
	enum, ok := itemsEnum(prop)
	if !ok {
		t.Fatalf("a declared vocabulary carries no enum: %v", prop)
	}
	if strings.Join(enum, ",") != "tui,core,docs" {
		t.Fatalf("enum = %v, want the config's order", enum)
	}
	if desc := fmt.Sprint(prop["description"]); !strings.Contains(desc, "these labels only") {
		t.Fatalf("description does not say the set is closed: %q", desc)
	}
}

// An empty list in config.yml means the store accepts anything. The labels
// the tickets carry are therefore examples, and an enum here would invent a
// restriction the store never asked for.
func TestLabelSchemaDerivesExamplesFromUse(t *testing.T) {
	tc := labelStore(t, nil, [][]string{
		{"tools", "docs"},
		{"tools"},
		{"tools"},
		{"docs"},
		{"tui"},
	})

	prop := labelsPropOf(t, tc)
	if enum, ok := itemsEnum(prop); ok {
		t.Fatalf("a store that declares nothing carries an enum: %v", enum)
	}
	desc := fmt.Sprint(prop["description"])
	if !strings.Contains(desc, "accepts any label") {
		t.Fatalf("description does not say any label is accepted: %q", desc)
	}
	// Most used first, so the label a model should reach for reads first.
	if want := "tools, docs, tui"; !strings.Contains(desc, want) {
		t.Fatalf("description = %q, want the labels ranked %q", desc, want)
	}

	// The examples are not a limit. A label no ticket carries still lands.
	res, err := (&TicketCreateTool{TicketCore: tc}).Execute(context.Background(),
		json.RawMessage(`{"title":"A ticket with a new label","labels":["provider"]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("the store refused a label outside the examples: %s", ticketResultText(t, res))
	}
}

// A store with no declared labels and no tickets has no vocabulary to teach.
// Schema runs at registration, so this case must fail nothing.
func TestLabelSchemaFallsBackOnAnEmptyStore(t *testing.T) {
	tc := labelStore(t, nil, nil)

	prop := labelsPropOf(t, tc)
	if enum, ok := itemsEnum(prop); ok {
		t.Fatalf("an empty store carries an enum: %v", enum)
	}
	if desc := fmt.Sprint(prop["description"]); !strings.Contains(desc, "declares no vocabulary") {
		t.Fatalf("description = %q, want the fallback", desc)
	}
}

// Schema is called before the registry knows whether a store exists, and a
// directory with none must not panic or block.
func TestLabelSchemaSurvivesNoStoreAtAll(t *testing.T) {
	tc := &TicketCore{CWD: testsupport.TempDir(t)}

	prop := labelsPropOf(t, tc)
	if _, ok := itemsEnum(prop); ok {
		t.Fatal("a directory with no store carries an enum")
	}
	if desc := fmt.Sprint(prop["description"]); !strings.Contains(desc, "declares no vocabulary") {
		t.Fatalf("description = %q, want the fallback", desc)
	}
}

// The rank decides what a model reads first, and a tie has to break the same
// way on every build or the schema churns between sessions.
func TestRankLabelsOrdersByUseThenName(t *testing.T) {
	got := rankLabels([]*ticket.Ticket{
		{Labels: []string{"beta", "alpha", "gamma"}},
		{Labels: []string{"gamma", "alpha"}},
		{Labels: []string{"gamma", " "}},
	})
	if want := "gamma,alpha,beta"; strings.Join(got, ",") != want {
		t.Fatalf("rank = %v, want %s", got, want)
	}
}

func TestRankLabelsCapsTheLongTail(t *testing.T) {
	var one []string
	for i := 0; i < ticketLabelCap+5; i++ {
		one = append(one, fmt.Sprintf("label-%02d", i))
	}
	got := rankLabels([]*ticket.Ticket{{Labels: one}})
	if len(got) != ticketLabelCap {
		t.Fatalf("kept %d labels, want the cap of %d", len(got), ticketLabelCap)
	}
}
