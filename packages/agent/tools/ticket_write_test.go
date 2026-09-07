package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
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

// blocks_on and references, the two fields .tickets/CONVENTIONS.md asks for
// and the tools could not write before. The epic gets its children first,
// because a childless parent with blocks_on children is a check warning.
func TestTicketBlocksOnAndReferences(t *testing.T) {
	tc := ticketToolStore(t, 0)
	tc.ActorID = "agent:terva/testbot"
	tc.ActorName = "Testbot"
	ctx := context.Background()

	create := &TicketCreateTool{TicketCore: tc}
	mk := func(t *testing.T, args map[string]any) ticketWriteOut {
		t.Helper()
		raw, _ := json.Marshal(args)
		res, err := create.Execute(ctx, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("create refused: %s", ticketResultText(t, res))
		}
		var out ticketWriteOut
		if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// A create carries its references, through the second write the tool
	// makes for them, and the result echoes them back in terva's casing.
	epic := mk(t, map[string]any{
		"title": "An epic with slices", "type": "epic",
		"references": []map[string]any{
			{"ref": "plan:ticket-tool-surface", "path": "docs/plans/handoff-ticket-tool-surface.md"},
			{"ref": "idea:no-path"},
		},
	})
	if len(epic.References) != 2 {
		t.Fatalf("create landed %d references: %+v", len(epic.References), epic.References)
	}
	if epic.References[0].Ref != "plan:ticket-tool-surface" || epic.References[0].Path != "docs/plans/handoff-ticket-tool-surface.md" {
		t.Errorf("first reference = %+v", epic.References[0])
	}
	if epic.References[1].Path != "" {
		t.Errorf("a ref with no path should carry none: %+v", epic.References[1])
	}
	if epic.BlocksOn != "" {
		t.Errorf("blocks_on defaults to none, and the row omits it: %q", epic.BlocksOn)
	}

	child := mk(t, map[string]any{"title": "The first slice", "parent": epic.ID})
	if child.Parent != epic.ID {
		t.Fatalf("child parent = %q, want %q", child.Parent, epic.ID)
	}

	// The epic can now gate on its children, and the store agrees.
	update := &TicketUpdateTool{TicketCore: tc}
	args, _ := json.Marshal(map[string]any{
		"ref": epic.ID, "if_revision": epic.Revision,
		"blocks_on":         "children",
		"add_references":    []map[string]any{{"ref": "plan:ticket-tool-surface", "path": "docs/plans/git-ticket.md"}},
		"remove_references": []string{"idea:no-path"},
	})
	res, err := update.Execute(ctx, args, nil)
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
	if upd.BlocksOn != "children" {
		t.Errorf("blocks_on = %q, want children", upd.BlocksOn)
	}
	// AddReference on a ref the ticket holds replaces its path, and the
	// removal takes the other one, so one reference remains.
	if len(upd.References) != 1 || upd.References[0].Path != "docs/plans/git-ticket.md" {
		t.Fatalf("references after the update: %+v", upd.References)
	}

	// The gate the epic now carries is the one check blesses: it names a
	// child that exists, so blocks_on_no_children never fires. The store
	// does report a stale epics.md, which is what ticket_fix repairs.
	rep := ticketCheckReport(t, tc, true)
	if len(rep.Errors) != 0 {
		t.Errorf("check found errors: %+v", rep.Errors)
	}
	for _, w := range rep.Warnings {
		if w.Code == "blocks_on_no_children" {
			t.Errorf("the epic has a child, and check still warned: %+v", w)
		}
	}
}

// ticketCheckReport runs ticket_check and decodes it.
func ticketCheckReport(t *testing.T, tc *TicketCore, strict bool) ticketCheckOut {
	t.Helper()
	check := &TicketCheckTool{TicketCore: tc}
	args, _ := json.Marshal(map[string]any{"strict": strict})
	res, err := check.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("check refused: %s", ticketResultText(t, res))
	}
	var out ticketCheckOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type ticketCheckOut struct {
	OK       bool            `json:"ok"`
	Strict   bool            `json:"strict"`
	Errors   []ticketFinding `json:"errors"`
	Warnings []ticketFinding `json:"warnings"`
}

// ticket_fix repairs the one finding an agent hits constantly: a store that
// gained an epic has a stale epics.md, and check --fix is the only repair.
// The dry run reads the same plan and leaves the finding standing.
func TestTicketFixRepairsTheEpicsIndex(t *testing.T) {
	tc := ticketToolStore(t, 0)
	tc.ActorID = "agent:terva/testbot"
	tc.ActorName = "Testbot"
	ctx := context.Background()

	create := &TicketCreateTool{TicketCore: tc}
	res, err := create.Execute(ctx, json.RawMessage(`{"title":"An epic the index has not seen","type":"epic"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("create refused: %s", ticketResultText(t, res))
	}
	if !ticketHasFinding(ticketCheckReport(t, tc, true).Warnings, "epics_index_stale") {
		t.Fatal("a new epic should leave epics.md stale, and check should say so")
	}

	fix := &TicketFixTool{TicketCore: tc}
	run := func(t *testing.T, args string) ticketFixOut {
		t.Helper()
		res, err := fix.Execute(ctx, json.RawMessage(args), nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("fix refused: %s", ticketResultText(t, res))
		}
		var out ticketFixOut
		if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The dry run plans the rewrite, writes nothing, and the finding it
	// would clear is still in its own report.
	preview := run(t, `{"dry_run":true}`)
	if !preview.DryRun || len(preview.Repairs) != 1 {
		t.Fatalf("dry run: dry_run=%v repairs=%+v", preview.DryRun, preview.Repairs)
	}
	if preview.Repairs[0].Kind != "rewrite" || preview.Repairs[0].To != "epics.md" {
		t.Errorf("planned repair = %+v", preview.Repairs[0])
	}
	if !ticketHasFinding(preview.Warnings, "epics_index_stale") {
		t.Errorf("a dry run writes nothing, so the finding stands: %+v", preview.Warnings)
	}
	if !ticketHasFinding(ticketCheckReport(t, tc, true).Warnings, "epics_index_stale") {
		t.Error("the dry run changed the store")
	}

	// The real pass clears it, and a second pass has nothing left to do.
	done := run(t, `{}`)
	if done.DryRun || len(done.Repairs) != 1 || !done.OK {
		t.Fatalf("repair pass: dry_run=%v ok=%v repairs=%+v", done.DryRun, done.OK, done.Repairs)
	}
	if ticketHasFinding(done.Warnings, "epics_index_stale") {
		t.Errorf("the repair did not clear the finding: %+v", done.Warnings)
	}
	if again := run(t, `{}`); len(again.Repairs) != 0 {
		t.Errorf("a repaired store still had repairs: %+v", again.Repairs)
	}
	if rep := ticketCheckReport(t, tc, true); !rep.OK {
		t.Errorf("strict check after the repair: errors=%+v warnings=%+v", rep.Errors, rep.Warnings)
	}
}

type ticketFixOut struct {
	DryRun   bool            `json:"dry_run"`
	OK       bool            `json:"ok"`
	Repairs  []ticketRepair  `json:"repairs"`
	Errors   []ticketFinding `json:"errors"`
	Warnings []ticketFinding `json:"warnings"`
}

func ticketHasFinding(fs []ticketFinding, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}

// Everything CreateOptions carries that the tool used to drop: the two
// checkbox sections, the plan, the milestone, and the assignees. The store
// has no mutation for assignees, so a create that could not set them left
// no way to set them at all.
func TestTicketCreateReachesEveryField(t *testing.T) {
	tc := ticketToolStore(t, 0)
	tc.ActorID = "agent:terva/testbot"
	tc.ActorName = "Testbot"
	ctx := context.Background()

	create := &TicketCreateTool{TicketCore: tc}
	args, _ := json.Marshal(map[string]any{
		"title": "A ticket that fills its sections", "type": "chore",
		"milestone":           "v0.105.0",
		"assignees":           []string{"agent:terva/mieli"},
		"implementation_plan": "Write it, then gate it.",
		"acceptance_criteria": []string{"The tool writes the section", "check stays quiet"},
		"definition_of_done":  []string{"The PR is merged"},
	})
	res, err := create.Execute(ctx, args, nil)
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
	if made.Milestone != "v0.105.0" || len(made.Assignees) != 1 {
		t.Errorf("milestone=%q assignees=%v", made.Milestone, made.Assignees)
	}

	get := &TicketGetTool{TicketCore: tc}
	res, err = get.Execute(ctx, json.RawMessage(`{"ref":"`+made.ID+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var full ticketFull
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &full); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.ImplementationPlan, "Write it, then gate it.") {
		t.Errorf("implementation plan = %q", full.ImplementationPlan)
	}
	// The store writes checklist entries as unchecked boxes, in order.
	if !strings.Contains(full.AcceptanceCriteria, "- [ ] The tool writes the section") ||
		!strings.Contains(full.AcceptanceCriteria, "- [ ] check stays quiet") {
		t.Errorf("acceptance criteria = %q", full.AcceptanceCriteria)
	}
	if !strings.Contains(full.DefinitionOfDone, "- [ ] The PR is merged") {
		t.Errorf("definition of done = %q", full.DefinitionOfDone)
	}
}

// The backport path of .tickets/CONVENTIONS.md: a create may file work that
// is already over. It may not file work into ready, because that promotion
// belongs to a person.
func TestTicketCreateBackportStatus(t *testing.T) {
	tc := ticketToolStore(t, 0)
	ctx := context.Background()
	create := &TicketCreateTool{TicketCore: tc}

	res, err := create.Execute(ctx, json.RawMessage(`{"title":"Work that shipped last month","status":"done"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("a done backport refused: %s", ticketResultText(t, res))
	}
	var done ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &done); err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" {
		t.Errorf("status = %q, want done", done.Status)
	}

	res, err = create.Execute(ctx, json.RawMessage(`{"title":"Work nobody will do","status":"archived","reason":"the plan changed"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("an archived backport refused: %s", ticketResultText(t, res))
	}
	var archived ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &archived); err != nil {
		t.Fatal(err)
	}
	if archived.Status != "archived" {
		t.Errorf("status = %q, want archived", archived.Status)
	}
	// The store puts a create reason in the archive block and in Notes, not
	// in status_reason, which is where a transition reason goes.
	get := &TicketGetTool{TicketCore: tc}
	res, err = get.Execute(ctx, json.RawMessage(`{"ref":"`+archived.ID+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var full ticketFull
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &full); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.Notes, "created archived: the plan changed") {
		t.Errorf("the archive reason is not in Notes: %q", full.Notes)
	}

	// ready is the gate a person owns, and the store keeps it shut.
	res, err = create.Execute(ctx, json.RawMessage(`{"title":"Straight to the queue","status":"ready"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a create into ready should refuse")
	}
}

// The schema may not advertise a value the store refuses. ticket_create
// used to offer "feature" as an example type, and this store's set is
// [task bug chore spike epic], so the suggestion failed with invalid_field.
// Reading the enum out of the library is what keeps the two in step.
func TestTicketSchemasCarryTheStoreVocabulary(t *testing.T) {
	tc := ticketToolStore(t, 0)
	create := &TicketCreateTool{TicketCore: tc}
	update := &TicketUpdateTool{TicketCore: tc}

	for _, tool := range []core.Tool{create, update} {
		var schema struct {
			Properties map[string]struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
			t.Fatal(err)
		}
		if got := schema.Properties["type"].Enum; !slices.Equal(got, ticket.Types) {
			t.Errorf("%s type enum = %v, want %v", tool.Name(), got, ticket.Types)
		}
		if got := schema.Properties["priority"].Enum; !slices.Equal(got, ticket.Priorities) {
			t.Errorf("%s priority enum = %v, want %v", tool.Name(), got, ticket.Priorities)
		}
		// The limits are in the prose, and CI runs check in strict mode, so
		// a warning an agent could have avoided becomes a red gate.
		desc := schema.Properties["title"].Description
		for _, n := range []int{ticket.TitleWarn, ticket.TitleMax} {
			if !strings.Contains(desc, strconv.Itoa(n)) {
				t.Errorf("%s title description does not name %d: %q", tool.Name(), n, desc)
			}
		}
	}
}

// A bad blocks_on refuses, and the refusal names the values that work.
func TestTicketBlocksOnRefusesUnknownValue(t *testing.T) {
	tc := ticketToolStore(t, 0)
	create := &TicketCreateTool{TicketCore: tc}
	res, err := create.Execute(context.Background(), json.RawMessage(`{"title":"Bad gate","blocks_on":"parents"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("blocks_on parents should refuse")
	}
	if msg := ticketResultText(t, res); !strings.Contains(msg, "children") {
		t.Errorf("refusal should name the valid values: %q", msg)
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
