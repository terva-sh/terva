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

// stampedRowKinds are the row kinds that carry `at`. Every other kind already
// has a time of its own (messages) or is never read as a timeline, and adding a
// field to one would change bytes that other readers pin.
//
// Keep this list and the guard below in step: adding a kind here is a deliberate
// format change, and the test failing is how an accidental one gets noticed.
var stampedRowKinds = map[string]bool{"meta": true, "usage": true}

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

// Rows outside stampedRowKinds must stay byte-identical.
func TestUnstampedRowsAreUnchanged(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-4-8", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(provider.Usage{InputTokens: 10}, provider.Usage{InputTokens: 10}); err != nil {
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
	for _, line := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		var row struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		if !stampedRowKinds[row.Type] && strings.Contains(line, `"at":`) {
			t.Errorf("a %q row grew an `at` field: %s", row.Type, line)
		}
	}
}
