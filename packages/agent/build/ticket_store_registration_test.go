package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// writeTicketStoresConfig writes a user config naming the given stores into
// home. Separate from seedTicketStoresConfig because toolUniverse owns its own
// TERVA_HOME and only needs the file.
func writeTicketStoresConfig(t *testing.T, home string, stores map[string]string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"ticket_stores": stores})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedTicketStoresConfig writes a TERVA_HOME holding a user config with the
// given ticket_stores map, and returns that home.
func seedTicketStoresConfig(t *testing.T, stores map[string]string) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if stores != nil {
		writeTicketStoresConfig(t, home, stores)
	}
	return home
}

// seedConfiguredTicketStore creates a real store and names it in home's config,
// so a registry built afterwards carries ticket_store.
func seedConfiguredTicketStore(t *testing.T, home string) string {
	t.Helper()
	other := testsupport.TempDir(t)
	if _, err := ticket.Init(other, ticket.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTicketStoresConfig(t, home, map[string]string{"personal": other})
	return other
}

// The feature ships off. With no ticket_stores key the session carries no
// ticket_store tool, so a user who configured none pays no schema cost.
func TestTicketStoreToolIsOffByDefault(t *testing.T) {
	seedTicketStoresConfig(t, nil)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)

	if _, ok := reg["ticket_store"]; ok {
		t.Error("ticket_store registered with no ticket_stores configured; the feature must ship off")
	}
	// The siblings must still be there, so the absence above is the gate and
	// not a broken ticket block.
	if _, ok := reg["ticket_get"]; !ok {
		t.Fatal("ticket_get missing, so this test proves nothing about the ticket_store gate")
	}
}

func TestTicketStoreToolRegistersWhenConfigured(t *testing.T) {
	other := testsupport.TempDir(t)
	if _, err := ticket.Init(other, ticket.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	seedTicketStoresConfig(t, map[string]string{"personal": other})

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)

	if _, ok := reg["ticket_store"]; !ok {
		t.Fatal("ticket_store missing though a usable store is configured")
	}
}

// An entry that fails validation leaves nothing to select, so the tool stays
// away rather than advertising a switch that can only return to where it is.
func TestTicketStoreToolStaysAwayWhenEveryEntryIsRefused(t *testing.T) {
	seedTicketStoresConfig(t, map[string]string{"bad": "relative/path"})

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)

	if _, ok := reg["ticket_store"]; ok {
		t.Error("ticket_store registered though every configured entry was refused")
	}
}

// BindCard is a wiring step, and a host that forgets it leaves the card
// describing the workspace store forever while the tools write elsewhere.
// The card hangs off the core, so this walks the built registry to reach it.
func TestBuiltTicketCardFollowsTheCore(t *testing.T) {
	seedTicketStoresConfig(t, nil)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)

	tc := tools.TicketCoreFor(reg)
	if tc == nil {
		t.Fatal("the built registry carries no ticket core")
	}
	if tc.Card == nil {
		t.Fatal("the built core carries no card")
	}
	if tc.Card.Open == nil {
		t.Error("the built card is unbound, so it would discover the workspace store even after a ticket_store switch")
	}
}
