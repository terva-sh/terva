package tui

// Tests for the shared-file cards: the transcript's rendering of the files an
// agent handed to the user through share_file.
//
// The behaviour that matters is placement and survival. A card belongs to the
// call that produced it (one tool-role message batches a whole step), it must
// outlive the reduced tool displays, and it must not be swallowed by the render
// cache when the live fold appends it to a message that is already on screen.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// pinnedNow is the clock the expiry tests read, so "6d left" is an assertion
// rather than a race against the store's TTL.
var pinnedNow = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

// sharedMeta builds the Meta bag the agent loop writes for a step's shares.
func sharedMeta(t *testing.T, files ...SharedFile) map[string]string {
	t.Helper()
	raw, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{MetaSharedKey: string(raw)}
}

// The whole point of the feature: a share_file call renders something the user
// can see, naming the file, its kind, and its size.
func TestSharedFileRendersACard(t *testing.T) {
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"pr-body.md"}`),
		{
			Role:    provider.RoleTool,
			Content: []provider.Content{toolResult("t1", false, "Shared pr-body.md with the user")},
			Meta: sharedMeta(t, SharedFile{
				ID: "shr_a", CallID: "t1", Name: "pr-body.md", Kind: "document", Size: 2048,
			}),
		},
	}}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "pr-body.md") {
		t.Fatalf("no card naming the shared file:\n%s", plain)
	}
	if !strings.Contains(plain, "document") || !strings.Contains(plain, "2.0 KB") {
		t.Errorf("card does not carry the kind and size:\n%s", plain)
	}
}

// A caption is the agent's one line about what it handed over, and it is the
// only part of the card the model actually authored.
func TestSharedFileCardShowsTheCaption(t *testing.T) {
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"chart.png"}`),
		{
			Role:    provider.RoleTool,
			Content: []provider.Content{toolResult("t1", false, "shared")},
			Meta: sharedMeta(t, SharedFile{
				ID: "shr_a", CallID: "t1", Name: "chart.png", Kind: "image",
				Caption: "the latency spike, annotated",
			}),
		},
	}}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "the latency spike, annotated") {
		t.Errorf("caption missing from the card:\n%s", plain)
	}
}

// One tool-role message carries a whole step's results. Each card must land
// under the call that produced it, or a step that shared two files puts both
// under whichever box renders first.
func TestSharedFileCardsFollowTheirOwnCall(t *testing.T) {
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "t1", Name: "share_file", Arguments: json.RawMessage(`{"path":"a.md"}`)},
			provider.ToolCallBlock{ID: "t2", Name: "share_file", Arguments: json.RawMessage(`{"path":"b.md"}`)},
		}},
		{
			Role: provider.RoleTool,
			Content: []provider.Content{
				toolResult("t1", false, "shared a"),
				toolResult("t2", false, "shared b"),
			},
			Meta: sharedMeta(t,
				SharedFile{ID: "shr_a", CallID: "t1", Name: "alpha.md", Kind: "document"},
				SharedFile{ID: "shr_b", CallID: "t2", Name: "beta.md", Kind: "document"},
			),
		},
	}}

	rows := v.Build(80)
	alpha, beta := -1, -1
	for i, row := range rows {
		plain := stripANSI(row)
		if strings.Contains(plain, "alpha.md") {
			alpha = i
		}
		if strings.Contains(plain, "beta.md") {
			beta = i
		}
	}
	if alpha < 0 || beta < 0 {
		t.Fatalf("both cards should render, got alpha=%d beta=%d:\n%s", alpha, beta, stripANSI(strings.Join(rows, "\n")))
	}
	// Each card sits under its own call's box, so the first call's card must
	// come before the second call's. Both landing together is exactly the
	// batching bug the CallID filter exists to prevent.
	if alpha >= beta {
		t.Errorf("cards are out of order: alpha at %d, beta at %d", alpha, beta)
	}
	if beta-alpha < 2 {
		t.Errorf("cards at rows %d and %d are adjacent — the second call's box is missing between them", alpha, beta)
	}
}

// The reduced tool displays hide tool MECHANICS. A shared file is not
// mechanics: it is the thing the user asked for.
func TestSharedFileCardSurvivesTheReducedDisplays(t *testing.T) {
	for _, mode := range []ToolDisplayMode{ToolDisplayMinimal, ToolDisplayHidden} {
		v := View{Theme: Dark, ToolDisplay: mode, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
			toolCall("t1", "share_file", `{"path":"report.pdf"}`),
			{
				Role:    provider.RoleTool,
				Content: []provider.Content{toolResult("t1", false, "shared")},
				Meta: sharedMeta(t, SharedFile{
					ID: "shr_a", CallID: "t1", Name: "report.pdf", Kind: "document",
				}),
			},
		}}
		plain := stripANSI(strings.Join(v.Build(80), "\n"))
		if !strings.Contains(plain, "report.pdf") {
			t.Errorf("%v hid the shared file itself:\n%s", mode, plain)
		}
	}
}

// The render cache is keyed by a hash of the message. A share ARRIVES LATE —
// the live fold appends it to a tool-role message already in the transcript —
// so a hash that ignores it serves back the cached, cardless row forever.
//
// The share is RELABELLED (share_file's `name` argument), so the string being
// asserted on appears nowhere but the card: the box label already carries the
// call's path argument, and looking for that would pass on the label alone
// whether or not a card was ever rendered.
func TestSharedFileCardDefeatsTheRenderCache(t *testing.T) {
	const cardOnly = "relabelled-deliverable.md"
	msg := provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{toolResult("t1", false, "shared")},
	}
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"late.md"}`),
		msg,
	}}
	if plain := stripANSI(strings.Join(v.Build(80), "\n")); strings.Contains(plain, cardOnly) {
		t.Fatalf("the card cannot be there before the share is recorded:\n%s", plain)
	}

	// The share lands on the message that is already rendered and cached.
	msg.Meta = sharedMeta(t, SharedFile{ID: "shr_a", CallID: "t1", Name: cardOnly, Kind: "document"})
	v.Messages[1] = msg

	if plain := stripANSI(strings.Join(v.Build(80), "\n")); !strings.Contains(plain, cardOnly) {
		t.Errorf("the cache served a stale cardless row:\n%s", plain)
	}
}

// An expired share keeps its card — the transcript still records what was
// handed over — but must say so, rather than send the user hunting for bytes
// the sweeper already took.
func TestSharedFileCardMarksAnExpiredShare(t *testing.T) {
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"old.md"}`),
		{
			Role:    provider.RoleTool,
			Content: []provider.Content{toolResult("t1", false, "shared")},
			Meta: sharedMeta(t, SharedFile{
				ID: "shr_a", CallID: "t1", Name: "old.md", Kind: "document",
				ExpiresAt: pinnedNow.Add(-time.Hour).Format(time.RFC3339),
			}),
		},
	}}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "old.md") {
		t.Fatalf("an expired share still belongs in the transcript:\n%s", plain)
	}
	if !strings.Contains(plain, "expired") {
		t.Errorf("the card does not say the bytes are gone:\n%s", plain)
	}
}

// An expiry the daemon did not send is UNKNOWN, and a renderer that treats
// unknown as expired tells the user their file is gone when it is not.
func TestShareExpiryDistinguishesUnknownFromExpired(t *testing.T) {
	if got := shareExpiry("", pinnedNow); got != "" {
		t.Errorf("a missing expiry rendered as %q, want silence", got)
	}
	if got := shareExpiry("not a timestamp", pinnedNow); got != "" {
		t.Errorf("an unparseable expiry rendered as %q, want silence", got)
	}
	if got := shareExpiry(pinnedNow.Add(-time.Second).Format(time.RFC3339), pinnedNow); got != "expired" {
		t.Errorf("a past expiry = %q, want expired", got)
	}
	if got := shareExpiry(pinnedNow.Add(6*24*time.Hour).Format(time.RFC3339), pinnedNow); got != "6d left" {
		t.Errorf("expiry = %q, want 6d left", got)
	}
	if got := shareExpiry(pinnedNow.Add(90*time.Minute).Format(time.RFC3339), pinnedNow); got != "1h left" {
		t.Errorf("expiry = %q, want 1h left", got)
	}
}

// A record that will not parse costs the user a card. Costing them the whole
// transcript instead would be the worse trade.
func TestSharedFileCardToleratesAMalformedRecord(t *testing.T) {
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"x.md"}`),
		{
			Role:    provider.RoleTool,
			Content: []provider.Content{toolResult("t1", false, "the result still renders")},
			Meta:    map[string]string{MetaSharedKey: `{"not":"an array"`},
		},
	}}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "the result still renders") {
		t.Errorf("a malformed share record cost the user the transcript:\n%s", plain)
	}
}

// A turn that shared nothing must render exactly as it did before this feature
// existed — no stray rows, no blank card.
func TestNoSharesRenderNoCard(t *testing.T) {
	msgs := []provider.Message{
		toolCall("t1", "bash", `{"command":"go build"}`),
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t1", false, "ok")}},
	}
	beforeView := View{Theme: Dark, Messages: msgs}
	afterView := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: msgs}
	before := beforeView.Build(80)
	after := afterView.Build(80)
	if len(before) != len(after) {
		t.Errorf("a shareless turn changed height: %d rows before, %d after", len(before), len(after))
	}
}

func TestHumanShareBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, ""},
		{-1, ""},
		{980, "980 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	}
	for _, c := range cases {
		if got := humanShareBytes(c.in); got != c.want {
			t.Errorf("humanShareBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
