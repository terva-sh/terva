package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The write loop of slice 3: create, read the revision back, mutate under
// it, and watch a stale precondition refuse with the current revision in
// the message.
func TestTicketWriteLoop(t *testing.T) {
	tc := ticketToolStore(t, 0)
	tc.ActorID = "agent:terva/testbot"
	tc.ActorName = "Testbot"

	create := &TicketCreateTool{TicketCore: tc}
	res, err := create.Execute(context.Background(), json.RawMessage(`{"title":"Write loop ticket","labels":["tui"]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("create refused: %s", ticketResultText(t, res))
	}
	var made ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &made); err != nil {
		t.Fatal(err)
	}
	if made.ID == "" || made.Revision == "" || made.Status != "draft" {
		t.Fatalf("create returned id=%q revision=%q status=%q", made.ID, made.Revision, made.Status)
	}

	// A fresh revision applies, atomically, and the result carries the new one.
	update := &TicketUpdateTool{TicketCore: tc}
	args, _ := json.Marshal(map[string]any{
		"ref": made.ID, "if_revision": made.Revision,
		"priority": "high", "add_labels": []string{"tools"},
	})
	res, err = update.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("update refused: %s", ticketResultText(t, res))
	}
	var upd ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &upd); err != nil {
		t.Fatal(err)
	}
	if upd.Priority != "high" || upd.Revision == made.Revision || upd.Revision == "" {
		t.Fatalf("update: priority=%q revision=%q (was %q)", upd.Priority, upd.Revision, made.Revision)
	}

	// The revision from before the update is now stale, and the refusal
	// names the current one so recovery needs no extra probing.
	res, err = update.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("stale revision did not refuse")
	}
	msg := ticketResultText(t, res)
	if !strings.Contains(msg, "stale_revision") || !strings.Contains(msg, upd.Revision) {
		t.Errorf("stale refusal should carry the code and the current revision: %q", msg)
	}
}

// The precondition is required by the schema contract, not just documented:
// an empty if_revision refuses before any file is touched.
func TestTicketWriteRequiresRevision(t *testing.T) {
	tc := ticketToolStore(t, 1)
	list := &TicketListTool{TicketCore: tc}
	res, _ := list.Execute(context.Background(), nil, nil)
	page := ticketPageFrom(t, res)
	id := page.Tickets[0].ID

	comment := &TicketCommentTool{TicketCore: tc}
	res, err := comment.Execute(context.Background(), json.RawMessage(`{"ref":"`+id+`","text":"hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(ticketResultText(t, res), "if_revision") {
		t.Errorf("missing if_revision should refuse and name the field: %q", ticketResultText(t, res))
	}
}

// Transition, claim, release, and comment, each under a fresh revision,
// with the actor identity recorded where the format puts it.
func TestTicketTransitionClaimComment(t *testing.T) {
	tc := ticketToolStore(t, 1)
	tc.ActorID = "agent:terva/testbot"
	tc.ActorName = "Testbot"

	list := &TicketListTool{TicketCore: tc}
	res, _ := list.Execute(context.Background(), nil, nil)
	page := ticketPageFrom(t, res)
	id := page.Tickets[0].ID

	get := &TicketGetTool{TicketCore: tc}
	rev := func() string {
		res, err := get.Execute(context.Background(), json.RawMessage(`{"ref":"`+id+`"}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		var full ticketFull
		if err := json.Unmarshal([]byte(ticketResultText(t, res)), &full); err != nil {
			t.Fatal(err)
		}
		return full.Revision
	}

	transition := &TicketTransitionTool{TicketCore: tc}
	args, _ := json.Marshal(map[string]any{"ref": id, "if_revision": rev(), "status": "ready"})
	res, err := transition.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("transition refused: %s", ticketResultText(t, res))
	}
	var out ticketWriteOut
	_ = json.Unmarshal([]byte(ticketResultText(t, res)), &out)
	if out.Status != "ready" {
		t.Fatalf("transition landed on %q", out.Status)
	}

	claim := &TicketClaimTool{TicketCore: tc}
	args, _ = json.Marshal(map[string]any{"ref": id, "if_revision": rev(), "branch": "feat/x"})
	res, err = claim.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("claim refused: %s", ticketResultText(t, res))
	}
	// A fresh struct per parse: claimed_by rides omitempty, so an absent
	// key would leave a reused struct's old value in place and fake a
	// still-held claim.
	var claimed ticketWriteOut
	_ = json.Unmarshal([]byte(ticketResultText(t, res)), &claimed)
	if claimed.ClaimedBy != "agent:terva/testbot" {
		t.Fatalf("claimed_by = %q, want the injected actor", claimed.ClaimedBy)
	}

	args, _ = json.Marshal(map[string]any{"ref": id, "if_revision": rev(), "release": true})
	res, err = claim.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("release refused: %s", ticketResultText(t, res))
	}
	var released ticketWriteOut
	_ = json.Unmarshal([]byte(ticketResultText(t, res)), &released)
	if released.ClaimedBy != "" {
		t.Fatalf("release left claimed_by = %q", released.ClaimedBy)
	}

	comment := &TicketCommentTool{TicketCore: tc}
	args, _ = json.Marshal(map[string]any{"ref": id, "if_revision": rev(), "text": "a work record", "kind": "note"})
	res, err = comment.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("comment refused: %s", ticketResultText(t, res))
	}
	gres, err := get.Execute(context.Background(), json.RawMessage(`{"ref":"`+id+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var full ticketFull
	if err := json.Unmarshal([]byte(ticketResultText(t, gres)), &full); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.Notes, "a work record") || !strings.Contains(full.Notes, "agent:terva/testbot") {
		t.Errorf("note text or actor missing from Notes: %q", full.Notes)
	}
}
