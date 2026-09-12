package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The feature ships off: no ticket_stores key means no stores and no refusals,
// so the ticket tools discover the workspace store exactly as before.
func TestResolveTicketStoresDefaultsToNothing(t *testing.T) {
	for name, entries := range map[string]map[string]string{
		"nil":   nil,
		"empty": {},
	} {
		stores, refused := ResolveTicketStores(entries, testsupport.TempDir(t))
		if len(stores) != 0 || len(refused) != 0 {
			t.Errorf("%s config: got %d stores and %d refusals, want the feature inert", name, len(stores), len(refused))
		}
	}
}

// The shapes the ref grammar does allow, so the rule above reads "this shape"
// rather than "almost nothing".
func TestResolveTicketStoresAcceptsTheNamesTheRefGrammarAllows(t *testing.T) {
	base := testsupport.TempDir(t)
	entries := map[string]string{}
	for _, name := range []string{"personal", "ops", "work-2", "a", "x9"} {
		entries[name] = filepath.Join(base, name)
	}

	stores, refused := ResolveTicketStores(entries, testsupport.TempDir(t))

	if len(refused) != 0 {
		t.Fatalf("refused %v, want every [a-z0-9-]+ name accepted", refused)
	}
	if len(stores) != len(entries) {
		t.Errorf("kept %d of %d valid names", len(stores), len(entries))
	}
}

func TestResolveTicketStoresAcceptsAbsolutePaths(t *testing.T) {
	base := testsupport.TempDir(t)
	personal := filepath.Join(base, "personal")
	ops := filepath.Join(base, "ops")

	stores, refused := ResolveTicketStores(map[string]string{
		"ops":      ops,
		"personal": personal,
	}, testsupport.TempDir(t))

	if len(refused) != 0 {
		t.Fatalf("refused %v, want both accepted", refused)
	}
	// Sorted by name, so a status row and a tool listing agree run to run.
	if len(stores) != 2 || stores[0].Name != "ops" || stores[1].Name != "personal" {
		t.Fatalf("got %+v, want ops then personal", stores)
	}
	if stores[0].Path != ops || stores[1].Path != personal {
		t.Errorf("paths did not survive: %+v", stores)
	}
}

// Nothing in terva creates or proposes a store, so a path that does not exist
// is still a valid configuration. The switch reports the absence instead.
func TestResolveTicketStoresDoesNotRequireTheStoreToExist(t *testing.T) {
	missing := filepath.Join(testsupport.TempDir(t), "not", "created", "yet")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("precondition: %q should not exist", missing)
	}

	stores, refused := ResolveTicketStores(map[string]string{"later": missing}, testsupport.TempDir(t))

	if len(refused) != 0 {
		t.Fatalf("refused %v, want an absent path to survive config load", refused)
	}
	if len(stores) != 1 || stores[0].Path != missing {
		t.Fatalf("got %+v, want the absent path kept", stores)
	}
}

func TestResolveTicketStoresExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory on this machine: %v", err)
	}
	stores, refused := ResolveTicketStores(map[string]string{"personal": "~/tickets"}, testsupport.TempDir(t))
	if len(refused) != 0 {
		t.Fatalf("refused %v, want ~/ accepted", refused)
	}
	want := filepath.Join(home, "tickets")
	if len(stores) != 1 || stores[0].Path != want {
		t.Fatalf("got %+v, want %q", stores, want)
	}
}

func TestResolveTicketStoresRefusals(t *testing.T) {
	tervaHome := testsupport.TempDir(t)
	insideHome := filepath.Join(tervaHome, "tickets")

	cases := []struct {
		why     string
		name    string
		path    string
		wantSub string
	}{
		{"a relative path is ambiguous against which cwd", "rel", "tickets", "absolute"},
		{"a bare name is relative too", "bare", "./store", "absolute"},
		{"an empty path names nothing", "blank", "   ", "empty"},
		// The ref grammar in .tickets/CONVENTIONS.md: a store name is also the
		// <store> of a ticket:<store>/<id> reference, and a case mismatch there
		// reports as a ref nobody carries rather than as an error.
		{"an upper-case name splits a qualified ref in two", "Work", "/tmp/work-tickets", "lower case"},
		{"a space is not in [a-z0-9-]", "my store", "/tmp/work-tickets", "lower case"},
		{"an underscore is not in [a-z0-9-] either", "my_store", "/tmp/work-tickets", "lower case"},
		{
			"the workspace store is discovered, not configured",
			WorkspaceTicketStoreName, insideHome + "-elsewhere", "cannot be redirected",
		},
		{
			"the state directory is the one tree the sandbox denies outright",
			"state", insideHome, "terva state directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stores, refused := ResolveTicketStores(map[string]string{tc.name: tc.path}, tervaHome)
			if len(stores) != 0 {
				t.Fatalf("accepted %+v, want a refusal because %s", stores, tc.why)
			}
			if len(refused) != 1 {
				t.Fatalf("got %d refusals, want exactly 1", len(refused))
			}
			if !strings.Contains(refused[0].Reason, tc.wantSub) {
				t.Errorf("reason %q does not contain %q, so it does not tell the user what to fix", refused[0].Reason, tc.wantSub)
			}
		})
	}
}

// ticket_stores names a directory outside the jail that the ticket tools may
// write. A cloned repository that could set it would choose where an agent
// writes, so the key belongs to the user layer alone.
func TestTicketStoresIsUserLayerOnly(t *testing.T) {
	// Structural half. If somebody adds the key to ProjectConfig, this fails
	// before any merge logic is even reached.
	if _, found := jsonTags(t, reflect.TypeOf(ProjectConfig{}))["ticket_stores"]; found {
		t.Fatal("ProjectConfig declares ticket_stores; a project must never name a directory that the ticket tools will then write")
	}

	// Behavioural half. A project that writes the key anyway gets nothing,
	// trusted or not, so the guarantee does not rest on the struct alone.
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)
	writeProjectConfig(t, cwd, `{"ticket_stores":{"theirs":"/tmp/attacker-store"}}`)

	for _, trusted := range []bool{false, true} {
		if got := ResolveConfig(cwd, trusted).Config.TicketStores; len(got) != 0 {
			t.Fatalf("trusted=%v: project config set ticket_stores %v; a cloned repo must not choose where an agent writes", trusted, got)
		}
	}
}

// The other half: the user's own ticket_stores DOES survive resolution, so the
// guarantee above reads "a project cannot" rather than "the key does nothing".
// Without this, deleting the field would pass the test above.
func TestUserTicketStoresSurvivesResolve(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"ticket_stores":{"personal":"/home/someone/tickets"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)

	got := ResolveConfig(cwd, true).Config.TicketStores
	if len(got) != 1 || got["personal"] != "/home/someone/tickets" {
		t.Fatalf("the user's own ticket_stores did not survive resolution: %v", got)
	}
}

// A refusal must not take the healthy entries with it: one bad path disables
// one store, not the feature.
func TestResolveTicketStoresKeepsGoodEntriesBesideBadOnes(t *testing.T) {
	good := filepath.Join(testsupport.TempDir(t), "good")
	stores, refused := ResolveTicketStores(map[string]string{
		"good": good,
		"bad":  "relative/path",
	}, testsupport.TempDir(t))

	if len(stores) != 1 || stores[0].Name != "good" {
		t.Fatalf("got %+v, want only the good store", stores)
	}
	if len(refused) != 1 || refused[0].Name != "bad" {
		t.Fatalf("got refusals %+v, want only the bad one", refused)
	}
}
