package tools

// Criterion 3 of TKT-01M26CWP: the store registry either resolves a qualified
// ref or refuses it naming the unregistered store, rather than reporting it as
// absent. The last clause is the one under test. git-ticket compares a
// reference identifier exactly, so an unregistered store already reads as a ref
// that nobody carries, and every test here asserts that the store gets named.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func TestParseQualifiedTicketRef(t *testing.T) {
	cases := []struct {
		name      string
		ref       string
		wantStore string
		wantID    string
		wantOK    bool
	}{
		{"a qualified ref splits at the first slash", "ticket:work/TKT-01M26CWP", "work", "TKT-01M26CWP", true},
		{"a bare ref names this store and needs no lookup", "ticket:TKT-01M26CWP", "", "", false},
		{"the namespace is compared without regard to case", "TICKET:work/TKT-01M26CWP", "work", "TKT-01M26CWP", true},
		{"another namespace is not ours", "proposal:work/thing", "", "", false},
		{"a ref with no namespace is not ours", "work/TKT-01M26CWP", "", "", false},
		{"an empty store half resolves nothing", "ticket:/TKT-01M26CWP", "", "", false},
		{"an empty id half resolves nothing", "ticket:work/", "", "", false},
		// The identifier is compared exactly, so the store name keeps its
		// case here and the resolver refuses it later. Repairing it in the
		// parser would report a match that no git-ticket lookup makes.
		{"a mixed-case name reaches the resolver unchanged", "ticket:Work/TKT-01M26CWP", "Work", "TKT-01M26CWP", true},
		{"only the first slash separates, the rest is the id", "ticket:work/a/b", "work", "a/b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, id, ok := parseQualifiedTicketRef(tc.ref)
			if ok != tc.wantOK || store != tc.wantStore || id != tc.wantID {
				t.Errorf("parseQualifiedTicketRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.ref, store, id, ok, tc.wantStore, tc.wantID, tc.wantOK)
			}
		})
	}
}

// A configured store resolves to the path the user named.
func TestQualifiedRefResolvesAConfiguredStore(t *testing.T) {
	c := &TicketCore{Stores: []config.TicketStore{{Name: "personal", Path: "/srv/personal"}}}

	got := c.resolveTicketStoreRef("personal")

	if got.Path != "/srv/personal" {
		t.Errorf("a configured store must resolve to its path, got %q", got.Path)
	}
	if got.Note != "" {
		t.Errorf("a store that resolved must carry no note, got %q", got.Note)
	}
}

// The refusal names the store. This is the criterion: an unregistered store
// must not read as a reference that nobody carries.
func TestQualifiedRefNamesAnUnregisteredStore(t *testing.T) {
	c := &TicketCore{Stores: []config.TicketStore{{Name: "personal", Path: "/srv/personal"}}}

	got := c.resolveTicketStoreRef("archive")

	if got.Path != "" {
		t.Fatalf("an unregistered store must resolve to no path, got %q", got.Path)
	}
	if !strings.Contains(got.Note, `"archive"`) {
		t.Errorf("the refusal must name the unregistered store, got %q", got.Note)
	}
	if !strings.Contains(got.Note, "personal") {
		t.Errorf("the refusal must name the stores this session does have, got %q", got.Note)
	}
	if !strings.Contains(got.Note, "ticket_stores") {
		t.Errorf("the refusal must name the config key that repairs it, got %q", got.Note)
	}
}

// The session with no registry at all is the one the lookup most needs to
// answer, and it is the case build.go wires deliberately: the core takes the
// registry whether or not ticket_store registers.
func TestQualifiedRefNamesTheStoreWhenNoRegistryExists(t *testing.T) {
	c := &TicketCore{}

	got := c.resolveTicketStoreRef("archive")

	if !strings.Contains(got.Note, `"archive"`) {
		t.Errorf("the refusal must name the store even with an empty registry, got %q", got.Note)
	}
	if !strings.Contains(got.Note, "names no store") {
		t.Errorf("the refusal must say the registry is empty rather than imply a typo, got %q", got.Note)
	}
}

// The reserved name always means the store of this repository, whatever
// ticket_store selected. A selection belongs to this session, and a ref is
// committed text that outlives it.
func TestQualifiedRefResolvesTheWorkspaceStore(t *testing.T) {
	dir := seedStore(t, "Workspace ticket", nil)
	other := seedStore(t, "Other ticket", nil)
	c := &TicketCore{CWD: dir, Card: &TicketCard{CWD: dir}}
	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: other}); err != nil {
		t.Fatal(err)
	}

	got := c.resolveTicketStoreRef(config.WorkspaceTicketStoreName)

	if got.Note != "" {
		t.Fatalf("the workspace store must resolve, got note %q", got.Note)
	}
	if !strings.HasPrefix(got.Path, dir) {
		t.Errorf("the workspace store must resolve under %q, got %q", dir, got.Path)
	}
	if strings.HasPrefix(got.Path, other) {
		t.Errorf("the workspace store must not follow the ticket_store selection, got %q", got.Path)
	}
}

// A name that cannot be a registry key at all earns its own reason. This is
// the case the proposal calls expensive: git-ticket compares the identifier
// exactly, so ticket:Work/X never joins ticket:work/X and the miss would
// otherwise report as absence.
func TestQualifiedRefRefusesANameThatCannotBeARegistryKey(t *testing.T) {
	c := &TicketCore{Stores: []config.TicketStore{{Name: "work", Path: "/srv/work"}}}

	got := c.resolveTicketStoreRef("Work")

	if got.Path != "" {
		t.Fatalf("a mixed-case name must not resolve, got %q", got.Path)
	}
	if !strings.Contains(got.Note, "lower case") {
		t.Errorf("the refusal must give the name rule, got %q", got.Note)
	}
	if !strings.Contains(got.Note, "ticket:work/") {
		t.Errorf("the refusal must offer the lower-case form, got %q", got.Note)
	}
}

// A name that is neither lower case nor repairable by lowering gets the rule
// and no suggestion, because a suggestion that is also invalid is noise.
func TestQualifiedRefOffersNoRepairForAnUnfixableName(t *testing.T) {
	c := &TicketCore{}

	got := c.resolveTicketStoreRef("my_store")

	if !strings.Contains(got.Note, "lower case") {
		t.Errorf("the refusal must give the name rule, got %q", got.Note)
	}
	if strings.Contains(got.Note, "may have meant") {
		t.Errorf("lowering my_store fixes nothing, so the refusal must offer no repair, got %q", got.Note)
	}
}

// End to end through ticket_get: a ticket carrying a qualified ref comes back
// with the store named. The bare ref beside it proves the annotation is not
// sprayed over every reference.
func TestTicketGetAnnotatesAQualifiedRef(t *testing.T) {
	dir := seedStore(t, "Workspace ticket", nil)
	c := &TicketCore{
		CWD:     dir,
		ActorID: "agent:test",
		Card:    &TicketCard{CWD: dir},
		Stores:  []config.TicketStore{{Name: "personal", Path: "/srv/personal"}},
	}
	id := seedTicketWithRefs(t, c,
		"ticket:personal/TKT-01M26CWPN8T2YG3AFMRJPEGW3Y",
		"ticket:archive/TKT-01M26CWPN8T2YG3AFMRJPEGW3Y",
		"ticket:TKT-01M1VXVX2KZFZ8ZKB6XBGR8010",
	)

	var out struct {
		References []ticketReference `json:"references"`
	}
	body := ticketToolText(t, runTicketTool(t, &TicketGetTool{TicketCore: c}, map[string]any{"ref": id}))
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("ticket_get returned no JSON: %v (body %q)", err, body)
	}
	// The control. Without this the test passes when the create dropped the
	// references and there is nothing left to annotate.
	if len(out.References) != 3 {
		t.Fatalf("the ticket must carry the three references it was created with, got %d: %+v", len(out.References), out.References)
	}

	byRef := map[string]ticketReference{}
	for _, r := range out.References {
		byRef[r.Ref] = r
	}

	resolved := byRef["ticket:personal/TKT-01M26CWPN8T2YG3AFMRJPEGW3Y"]
	if resolved.Store != "personal" || resolved.StorePath != "/srv/personal" {
		t.Errorf("a ref naming a configured store must resolve, got %+v", resolved)
	}

	refused := byRef["ticket:archive/TKT-01M26CWPN8T2YG3AFMRJPEGW3Y"]
	if refused.StorePath != "" {
		t.Errorf("a ref naming an unregistered store must resolve to no path, got %+v", refused)
	}
	if !strings.Contains(refused.StoreNote, `"archive"`) {
		t.Errorf("a ref naming an unregistered store must name it, got %q", refused.StoreNote)
	}

	bare := byRef["ticket:TKT-01M1VXVX2KZFZ8ZKB6XBGR8010"]
	if bare.Store != "" || bare.StorePath != "" || bare.StoreNote != "" {
		t.Errorf("a bare ref names a ticket in this store and must carry no lookup, got %+v", bare)
	}
}

// seedTicketWithRefs creates a ticket carrying refs, through terva's own create
// tool rather than the library, because the library's CreateOptions has no
// references field and the tool is the path a session actually takes.
func seedTicketWithRefs(t *testing.T, c *TicketCore, refs ...string) string {
	t.Helper()
	rows := make([]map[string]string, 0, len(refs))
	for _, r := range refs {
		rows = append(rows, map[string]string{"ref": r})
	}
	body := ticketToolText(t, runTicketTool(t, &TicketCreateTool{TicketCore: c}, map[string]any{
		"title":      "A ticket that names one in another store",
		"references": rows,
	}))
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.ID == "" {
		t.Fatalf("ticket_create returned no id: %v (body %q)", err, body)
	}
	return out.ID
}

func runTicketTool(t *testing.T, tool core.Tool, args map[string]any) core.ToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("%s failed: %v", tool.Name(), err)
	}
	if res.IsError {
		t.Fatalf("%s refused: %s", tool.Name(), ticketToolText(t, res))
	}
	return res
}

func ticketToolText(t *testing.T, res core.ToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, blk := range res.Content {
		if tb, ok := blk.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}
