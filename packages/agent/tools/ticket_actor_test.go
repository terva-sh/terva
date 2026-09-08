package tools

// The repair a message prints has to be a repair. These tests read the
// printed text and act on it, rather than asserting that some words are
// present, because a message can name a step that does not work.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gtcli "github.com/terva-sh/git-ticket/cli"
	ticket "github.com/terva-sh/git-ticket/ticket"
)

// The snippet claims to be the shape git-ticket writes. Render a config
// holding the same actor and compare, so a change to the emitter's
// indentation or field names fails here instead of handing a user YAML
// that does not match their file.
func TestActorRosterSnippetMatchesWhatTheStoreWrites(t *testing.T) {
	cfg := ticket.Config{
		Actors: []ticket.Actor{{ID: "human:you", Name: "Your Name"}},
	}
	rendered := string(ticket.RenderConfig(cfg))

	if !strings.Contains(rendered, ActorRosterSnippet) {
		t.Errorf("the snippet is not what the store renders.\nsnippet:\n%s\nrendered:\n%s",
			ActorRosterSnippet, rendered)
	}
}

// The hint has to carry the snippet, because the next test acts on the
// hint and would otherwise prove nothing about what a user reads.
func TestActorRepairHintCarriesTheSnippet(t *testing.T) {
	hint := ActorRepairHint("/somewhere/.tickets/config.yml")

	if !strings.Contains(hint, ActorRosterSnippet) {
		t.Errorf("the hint does not print the snippet:\n%s", hint)
	}
	if !strings.Contains(hint, "/somewhere/.tickets/config.yml") {
		t.Errorf("the hint does not name the file to edit:\n%s", hint)
	}
}

// The YAML has to sit at column zero. A person pastes it into config.yml
// where actors: is a top-level key, so a leading space on that line makes
// the file invalid and the advice worse than none.
func TestActorRosterSnippetIsFlushLeft(t *testing.T) {
	if strings.HasPrefix(ActorRosterSnippet, " ") || strings.HasPrefix(ActorRosterSnippet, "\t") {
		t.Errorf("the snippet is indented, so a paste would break config.yml:\n%q", ActorRosterSnippet)
	}
	for _, line := range strings.Split(ActorRepairHint("/p/config.yml"), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "actors:") && line != "actors:" {
			t.Errorf("the hint indents the actors: line, so a paste would break config.yml: %q", line)
		}
	}
}

// The refusal reaches /ticket's status line, which renders one string as
// one row. A newline there puts the rest on its own alignment and the
// message scatters across the screen. That shipped once, so it is a
// regression guard rather than a style rule.
func TestTicketUIRefusalIsOneLine(t *testing.T) {
	s := storeForTest(t, "")

	err := ticketStoreCanWrite(gtcli.UIParams{Store: s})
	if err == nil {
		t.Fatal("precondition: the store should have been refused")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the refusal carries a newline, so a status line will scatter it:\n%q", err.Error())
	}
}

// Same rule for the line helper itself, so a caller can trust it.
func TestActorRepairLineIsOneLine(t *testing.T) {
	line := ActorRepairLine("/somewhere/.tickets/config.yml")

	if strings.Contains(line, "\n") {
		t.Errorf("ActorRepairLine is not one line:\n%q", line)
	}
	if !strings.Contains(line, "/somewhere/.tickets/config.yml") {
		t.Errorf("the line does not name the file to edit: %q", line)
	}
}

// The one-line advice is shorter than the block, and shorter advice is
// where a wrong claim hides. It says one `- id:` line is enough, so this
// makes exactly that edit and then writes a real ticket.
func TestActorRepairLineAdviceActuallyRepairsAStore(t *testing.T) {
	s := storeForTest(t, "")
	configPath := filepath.Join(s.Path(), "config.yml")

	if err := ticketStoreCanWrite(gtcli.UIParams{Store: s}); err == nil {
		t.Fatal("precondition: a bare init should have produced a store with no actor")
	}

	// The advice names `- id: human:you` under `actors:`, and nothing else.
	// ActorRosterMinimal is those bytes.
	line := ActorRepairLine(configPath)
	if !strings.Contains(line, "- id: human:you") || !strings.Contains(line, "actors:") {
		t.Fatalf("the advice changed, so this test repairs the wrong thing: %q", line)
	}

	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}
	after := strings.Replace(string(before), "actors: []\n", ActorRosterMinimal, 1)
	if after == string(before) {
		t.Fatal("the repair changed nothing, so the rest of this test would prove nothing")
	}
	if err := os.WriteFile(configPath, []byte(after), 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}

	repaired, err := ticket.Open(s.Path())
	if err != nil {
		t.Fatalf("the repaired config.yml does not parse: %v", err)
	}
	if err := ticketStoreCanWrite(gtcli.UIParams{Store: repaired}); err != nil {
		t.Fatalf("one id line did not satisfy git-ticket's own rule: %v", err)
	}

	var out, errB bytes.Buffer
	code := gtcli.Run(
		[]string{"--store", repaired.Path(), "create", "--title", "after the short repair"},
		gtcli.Env{Dir: filepath.Dir(repaired.Path()), Getenv: os.Getenv, Stdout: &out, Stderr: &errB},
	)
	if code != 0 {
		t.Fatalf("one id line is not a working repair: create exit %d, stderr %q\nconfig.yml:\n%s",
			code, errB.String(), after)
	}
	if got := createdByID(t, repaired.Path()); got != "human:you" {
		t.Errorf("the short repair recorded %q, want human:you", got)
	}
}

// The test that decides the change. Take a store that refuses every
// write, apply the repair exactly as the message gives it, and then
// perform a real write. A wrong field name, a wrong indent, or advice
// that simply does not work fails here.
func TestActorRepairHintActuallyRepairsAStore(t *testing.T) {
	s := storeForTest(t, "")
	configPath := filepath.Join(s.Path(), "config.yml")

	// Precondition: the store is the broken one a user gets from a bare
	// init, and a write really is refused before the repair.
	if err := ticketStoreCanWrite(gtcli.UIParams{Store: s}); err == nil {
		t.Fatal("precondition: a bare init should have produced a store with no actor")
	}

	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}
	if !strings.Contains(string(before), "actors: []") {
		t.Fatalf("precondition: config.yml does not hold the empty roster this repairs:\n%s", before)
	}

	// Apply the repair the message describes: replace the empty list
	// with the snippet the user is told to paste.
	if !strings.Contains(ActorRepairHint(configPath), "actors: []") {
		t.Fatal("the hint no longer names actors: [] as the text to replace, so this test repairs the wrong thing")
	}
	after := strings.Replace(string(before), "actors: []\n", ActorRosterSnippet, 1)
	if after == string(before) {
		t.Fatal("the repair changed nothing, so the rest of this test would prove nothing")
	}
	if err := os.WriteFile(configPath, []byte(after), 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}

	// The store must now accept a real write, by git-ticket's own rule
	// and then in fact.
	repaired, err := ticket.Open(s.Path())
	if err != nil {
		t.Fatalf("the repaired config.yml does not parse: %v", err)
	}
	if err := ticketStoreCanWrite(gtcli.UIParams{Store: repaired}); err != nil {
		t.Fatalf("the repair did not satisfy git-ticket's own rule: %v", err)
	}

	var out, errB bytes.Buffer
	code := gtcli.Run(
		[]string{"--store", repaired.Path(), "create", "--title", "after the repair"},
		gtcli.Env{Dir: filepath.Dir(repaired.Path()), Getenv: os.Getenv, Stdout: &out, Stderr: &errB},
	)
	if code != 0 {
		t.Fatalf("the repair the message prints does not work: create exit %d, stderr %q\nconfig.yml:\n%s",
			code, errB.String(), after)
	}

	// An exit code of zero is not enough, and a probe proved it. A roster
	// entry that misspells the id key still parses, still satisfies
	// DefaultActor, and still writes the ticket. The store records
	// created_by.id as "" and keeps the name, so the ticket lands with no
	// attribution and the command reports success. The snippet therefore
	// has to be judged by the attribution it produces.
	if got := createdByID(t, repaired.Path()); got != "human:you" {
		t.Errorf("the repair produced a write recorded as %q, want human:you", got)
	}
}

// createdByID reads created_by.id off the one ticket the store holds.
// created_by is a nested map, so the value is on the line below the key
// and reading the key's own line yields "" for every ticket.
func createdByID(t *testing.T, storePath string) string {
	t.Helper()
	draft := filepath.Join(storePath, "draft")
	entries, err := os.ReadDir(draft)
	if err != nil {
		t.Fatalf("read %s: %v", draft, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(draft, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if line != "created_by:" {
				continue
			}
			for _, sub := range lines[i+1:] {
				if !strings.HasPrefix(sub, " ") {
					break
				}
				kv := strings.TrimSpace(sub)
				if rest, ok := strings.CutPrefix(kv, "id:"); ok {
					return strings.Trim(strings.TrimSpace(rest), `"`)
				}
			}
			t.Fatalf("%s records a created_by with no id", e.Name())
		}
		t.Fatalf("%s records no created_by", e.Name())
	}
	t.Fatalf("the store holds no ticket, so the write did not land")
	return ""
}
