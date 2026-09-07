package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// ticketToolStore seeds a real store with n tickets, because the tools call
// the ticket package for real and a fixture would only re-test the mocks.
func ticketToolStore(t *testing.T, n int) *TicketCore {
	t.Helper()
	dir := testsupport.TempDir(t)
	// A store refuses a write with no actor, so the seed declares one.
	s, err := ticket.Init(dir, ticket.InitOptions{Actor: ticket.Actor{ID: "agent:test", Name: "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		_, err := s.Create(context.Background(), ticket.CreateOptions{
			Title:    fmt.Sprintf("Paging ticket %02d", i),
			Type:     "task",
			Priority: "normal",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return &TicketCore{CWD: dir}
}

func ticketResultText(t *testing.T, res core.ToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("result content blocks: %d", len(res.Content))
	}
	tb, ok := res.Content[0].(provider.TextBlock)
	if !ok {
		t.Fatalf("result content is %T, not a text block", res.Content[0])
	}
	return tb.Text
}

func ticketPageFrom(t *testing.T, res core.ToolResult) ticketPageResult {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool refused: %s", ticketResultText(t, res))
	}
	var page ticketPageResult
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// ticket_get answers the lifecycle question before ticket_transition can
// refuse it. The table used to live in .tickets/CONVENTIONS.md and in the
// text of a failed write, so a model either read a document or spent a
// write to learn where a ticket may go.
func TestTicketGetNamesTheNextStatuses(t *testing.T) {
	tc := ticketToolStore(t, 1)
	list := &TicketListTool{TicketCore: tc}
	res, _ := list.Execute(context.Background(), nil, nil)
	id := ticketPageFrom(t, res).Tickets[0].ID

	get := &TicketGetTool{TicketCore: tc}
	res, err := get.Execute(context.Background(), json.RawMessage(`{"ref":"`+id+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var full ticketFull
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &full); err != nil {
		t.Fatal(err)
	}
	if full.Status != "draft" {
		t.Fatalf("a fresh ticket is %q, not draft", full.Status)
	}
	if !slices.Equal(full.NextStatuses, ticket.PermittedTransitions("draft")) {
		t.Fatalf("next_statuses = %v, want %v", full.NextStatuses, ticket.PermittedTransitions("draft"))
	}
	// The value is the transition table and not a guess: a draft may go to
	// ready, and it may never go straight to in-progress.
	if !slices.Contains(full.NextStatuses, "ready") || slices.Contains(full.NextStatuses, "in-progress") {
		t.Errorf("draft next_statuses = %v", full.NextStatuses)
	}
}

// The cursor walk: three pages of a seven-ticket store, with next_offset
// carrying the position and the last page dropping it.
func TestTicketListPaging(t *testing.T) {
	tc := ticketToolStore(t, 7)
	list := &TicketListTool{TicketCore: tc}

	res, err := list.Execute(context.Background(), json.RawMessage(`{"limit":3}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	page := ticketPageFrom(t, res)
	if page.Total != 7 || len(page.Tickets) != 3 || !page.More || page.NextOffset != 3 {
		t.Fatalf("first page: total=%d rows=%d more=%v next=%d", page.Total, len(page.Tickets), page.More, page.NextOffset)
	}

	res, err = list.Execute(context.Background(), json.RawMessage(`{"offset":3,"limit":3}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	page = ticketPageFrom(t, res)
	if len(page.Tickets) != 3 || !page.More || page.NextOffset != 6 {
		t.Fatalf("second page: rows=%d more=%v next=%d", len(page.Tickets), page.More, page.NextOffset)
	}

	res, err = list.Execute(context.Background(), json.RawMessage(`{"offset":6,"limit":3}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	page = ticketPageFrom(t, res)
	if len(page.Tickets) != 1 || page.More || page.NextOffset != 0 {
		t.Fatalf("last page: rows=%d more=%v next=%d", len(page.Tickets), page.More, page.NextOffset)
	}
}

// The bounds that keep a thousand-ticket store out of the context window:
// no limit means 50, and no limit may exceed 200.
func TestTicketPageDefaultAndCap(t *testing.T) {
	ts := make([]*ticket.Ticket, 250)
	for i := range ts {
		ts[i] = &ticket.Ticket{ID: fmt.Sprintf("TKT-%03d", i), Title: "t"}
	}
	if page := ticketPageOf(ts, 0, 0); len(page.Tickets) != ticketPageDefault || !page.More {
		t.Fatalf("default limit: rows=%d more=%v", len(page.Tickets), page.More)
	}
	if page := ticketPageOf(ts, 0, 1000); len(page.Tickets) != ticketPageMax || page.NextOffset != ticketPageMax {
		t.Fatalf("cap: rows=%d next=%d", len(page.Tickets), page.NextOffset)
	}
	if page := ticketPageOf(ts, 999, 10); len(page.Tickets) != 0 || page.More {
		t.Fatalf("offset past the end: rows=%d more=%v", len(page.Tickets), page.More)
	}
}

// ticket_search pages like ticket_list and refuses an empty text, because a
// search with no needle is ticket_list with extra steps.
func TestTicketSearchPagingAndRequiredText(t *testing.T) {
	tc := ticketToolStore(t, 5)
	search := &TicketSearchTool{TicketCore: tc}

	res, err := search.Execute(context.Background(), json.RawMessage(`{"text":"Paging","limit":2}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	page := ticketPageFrom(t, res)
	if page.Total != 5 || len(page.Tickets) != 2 || !page.More || page.NextOffset != 2 {
		t.Fatalf("search page: total=%d rows=%d more=%v next=%d", page.Total, len(page.Tickets), page.More, page.NextOffset)
	}

	res, err = search.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("empty text did not refuse")
	}
}

// ticket_get returns the revision — the read evidence slice 3's write
// preconditions will demand — and a store error comes back as IsError text
// the model can read.
func TestTicketGetRevisionAndMiss(t *testing.T) {
	tc := ticketToolStore(t, 1)
	get := &TicketGetTool{TicketCore: tc}

	list := &TicketListTool{TicketCore: tc}
	res, err := list.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := ticketPageFrom(t, res)
	if len(page.Tickets) != 1 {
		t.Fatalf("seed store: %d tickets", len(page.Tickets))
	}

	res, err = get.Execute(context.Background(), json.RawMessage(`{"ref":"`+page.Tickets[0].ID+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var full ticketFull
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &full); err != nil {
		t.Fatal(err)
	}
	if full.Revision == "" {
		t.Error("ticket_get returned no revision")
	}

	res, err = get.Execute(context.Background(), json.RawMessage(`{"ref":"TKT-NOPE"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(ticketResultText(t, res), "ticket_not_found") {
		t.Errorf("miss: IsError=%v text=%q", res.IsError, ticketResultText(t, res))
	}
}

// A fresh store checks clean, and the strict flag only tightens the verdict.
func TestTicketCheckOK(t *testing.T) {
	tc := ticketToolStore(t, 2)
	check := &TicketCheckTool{TicketCore: tc}

	res, err := check.Execute(context.Background(), json.RawMessage(`{"strict":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK     bool `json:"ok"`
		Strict bool `json:"strict"`
	}
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !out.Strict {
		t.Fatalf("check: ok=%v strict=%v (%s)", out.OK, out.Strict, ticketResultText(t, res))
	}
}
