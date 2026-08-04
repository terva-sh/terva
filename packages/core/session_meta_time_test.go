package core

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Meta rows are an append-only TIMELINE of what changed, and every writer says so
// — but they carried only SessionMeta.Started, which is the session's birth and
// therefore identical on all of them. A reader could see THAT the model changed
// and never WHEN, so a settings change could not be aligned against the message
// timeline at all. In a dogfooding review that made six model switches
// unclassifiable: deliberate verification, or a picker that never confirmed it?
func TestMetaRowsCarryTheirOwnTimestamp(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-4-8", "v1")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	if err := s.UpdateModel("openai-codex", "gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	// A session with no messages is reclaimed by Close, so give it one.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	var stamps []time.Time
	for _, line := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		var row struct {
			Type string     `json:"type"`
			At   *time.Time `json:"at"`
			Meta *struct {
				Model string `json:"model"`
			} `json:"meta"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Type != "meta" {
			continue
		}
		if row.At == nil {
			t.Fatalf("a meta row has no `at` stamp — the timeline is unreadable:\n%s", line)
		}
		if row.At.Before(before.Add(-time.Minute)) || row.At.After(time.Now().UTC().Add(time.Minute)) {
			t.Errorf("`at` = %s, not a plausible write time", row.At)
		}
		stamps = append(stamps, *row.At)
	}
	if len(stamps) < 2 {
		t.Fatalf("expected the creation row and the model-switch row, got %d meta rows", len(stamps))
	}
	// The stamps must be able to ORDER the timeline, which is the whole point.
	for i := 1; i < len(stamps); i++ {
		if stamps[i].Before(stamps[i-1]) {
			t.Errorf("meta stamps go backwards: %s then %s", stamps[i-1], stamps[i])
		}
	}
}

// Every row kind carries `at` now — see TestEveryRowCarriesAStamp, which
// enumerates nothing and so cannot fall out of step with the writers.

// Usage rows are stamped for the same reason meta rows are, plus one: the
// questions asked of them are about rates — the idle gap before a dispatch, and
// whether a cache collapse began before or after an error in the sidecar. Row
// order alone answers neither. See sessionLine.At.
func TestUsageRowsCarryAStamp(t *testing.T) {
	dir := testsupport.TempDir(t)
	before := time.Now().UTC()
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-4-8", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(provider.Usage{InputTokens: 10}, provider.Usage{InputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendDelegatedUsage(provider.Usage{InputTokens: 5}, provider.Usage{InputTokens: 15}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	var stamps []time.Time
	for _, line := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		var row struct {
			Type string     `json:"type"`
			At   *time.Time `json:"at"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Type != "usage" {
			continue
		}
		if row.At == nil {
			t.Fatalf("a usage row has no `at` stamp — gaps between dispatches stay unmeasurable:\n%s", line)
		}
		if row.At.Before(before.Add(-time.Minute)) || row.At.After(time.Now().UTC().Add(time.Minute)) {
			t.Errorf("`at` = %s, not a plausible write time", row.At)
		}
		stamps = append(stamps, *row.At)
	}
	// Both writers funnel through appendUsage, so the DELEGATED one is stamped
	// too — that is the half a second writer would silently drop.
	if len(stamps) != 2 {
		t.Fatalf("expected the own-spend and delegated rows to both be stamped, got %d", len(stamps))
	}
	if stamps[1].Before(stamps[0]) {
		t.Errorf("usage stamps go backwards: %s then %s", stamps[0], stamps[1])
	}
}

// The branch writer builds a new file by hand instead of appending through the
// live session, so it is the one place the stamping rule can quietly stop
// applying. A guard that only ever reads a live session would not notice.
//
// It also pins the one case where the two times are MEANT to disagree: a copied
// message keeps the payload time of the moment it was originally made, while
// `at` records when this branch materialized.
func TestBranchedRowsAreStampedToo(t *testing.T) {
	root := testsupport.TempDir(t)
	parent, err := NewSession(root, "/project", "anthropic", "claude-opus-4-8", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-72 * time.Hour)
	for _, txt := range []string{"first", "first reply", "second"} {
		if err := parent.AppendMessage(provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: txt}},
			Time:    old,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An amend is what forces the branch to RE-SERIALIZE the prefix instead of
	// copying the parent's rows verbatim. Without it this test passes against a
	// writer with the stamping removed, because verbatim copies inherit the
	// parent's stamps — it would assert the rule while exercising none of it.
	edited := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "edited"}},
		Time:    old,
	}
	if err := parent.AppendAmend(AmendReplace, 1, &edited, "edit"); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC().Add(-time.Minute)
	branchPath, err := BranchSession(parent.Path, root, "/project", "0.0.0-test", 2)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(branchPath)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	for _, line := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		var row struct {
			Type    string     `json:"type"`
			At      *time.Time `json:"at"`
			Message *struct {
				Time time.Time `json:"time"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		rows++
		if row.At == nil {
			t.Errorf("the branch writer emitted an unstamped %q row:\n%s", row.Type, line)
			continue
		}
		if row.At.Before(before) {
			t.Errorf("a branched %q row is stamped %s, before the branch was taken", row.Type, row.At)
		}
		if row.Message != nil && !row.Message.Time.Equal(old) {
			t.Errorf("a copied message lost its original time: want %s, got %s", old, row.Message.Time)
		}
	}
	if rows == 0 {
		t.Fatal("the branch file had no rows to check")
	}
}

// Every row carries a stamp, and the guard NAMES NO KINDS — it reads back
// whatever the writers below produced and holds all of it to the rule. A guard
// with a list is a guard that silently stops covering the next row kind someone
// adds; this one enrolls it automatically, and the first run is the audit.
//
// The rule is universal because the diagnostic rows are the ones that needed it
// most and had it least. Attributing tool calls to a provider era after a
// mid-session model switch, with no clock on those rows, left only the call-id
// prefix (`toolu_` against `call_`) — which works by coincidence between two
// providers and collapses the moment they share a wire format.
func TestEveryRowCarriesAStamp(t *testing.T) {
	dir := testsupport.TempDir(t)
	before := time.Now().UTC().Add(-time.Minute)
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-4-8", "v1")
	if err != nil {
		t.Fatal(err)
	}
	// A spread of writers, deliberately across both funnels (writeLine and
	// writeLineLocked) and both stamping paths (explicit and filled-in).
	if err := s.UpdateModel("openai-codex", "gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(provider.Usage{InputTokens: 10}, provider.Usage{InputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStall(StallRecord{Axis: "churn", Tool: "edit", Detail: "invalid args"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendPrefixDivergence(PrefixDivergence{Rung: 3, Label: "message 0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTail(TailRecord{Blocks: []TailBlock{{ID: "host", Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendToolGroupActivation("web"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTransport(provider.TransportInfo{RemoteAddr: "10.0.0.1:443", Proto: "HTTP/2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRetry(RetryRecord{Provider: "anthropic", Attempt: 1, Max: 3, Err: "overloaded"}); err != nil {
		t.Fatal(err)
	}
	// A session with no messages is reclaimed by Close, so give it one.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		var row struct {
			Type string     `json:"type"`
			At   *time.Time `json:"at"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		seen[row.Type] = true
		if row.At == nil {
			t.Errorf("a %q row has no `at` stamp; its place in the timeline is unreadable:\n%s", row.Type, line)
			continue
		}
		if row.At.Before(before) || row.At.After(time.Now().UTC().Add(time.Minute)) {
			t.Errorf("a %q row stamped %s, not a plausible write time", row.Type, row.At)
		}
	}
	// Guard the guard: a session that somehow wrote almost nothing would pass the
	// loop above vacuously.
	if len(seen) < 8 {
		t.Fatalf("only %d row kinds reached the file (%v); the rule was barely exercised", len(seen), seen)
	}
}
