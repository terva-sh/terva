package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// scriptedUsageClient streams a fixed reply plus a scripted usage row per turn,
// so a test can drive core's cache-cliff detector through the real stream loop
// rather than by calling it directly. The detector is fed from the stream loop
// in production and nowhere else, and a test that reached past it would not
// notice the feed being cut.
type scriptedUsageClient struct {
	turn  int
	usage []provider.Usage
}

func (c *scriptedUsageClient) Name() string { return "fake" }

func (c *scriptedUsageClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	u := provider.Usage{}
	if c.turn < len(c.usage) {
		u = c.usage[c.turn]
	}
	c.turn++
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventUsage{Usage: u}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// A provider-side cache collapse leaves a durable trace, and leaves TWO rows —
// one opening the run and one closing it — however many dispatches the run
// lasted.
//
// Both halves are load-bearing and they pull in opposite directions. Without
// any row, the detector is observer-only: it raises a sticky note that dies
// with the process, so a finished session cannot say whether it ever fired, and
// nothing announces that a session is collapsed RIGHT NOW — which is the only
// moment the experiment that would explain the collapse can run. Without the
// transitions-only filter, a 95-dispatch run writes 95 rows, because the
// detector deliberately re-fires on every collapse past the threshold to keep
// the note's numbers current. The file wants the boundaries; the note wants
// every update; the filter is what lets both have what they want.
//
// The usage script walks the detector through exactly that shape: arm, build a
// streak to the firing threshold, fire AGAIN past it, then recover.
func TestWireHeadlessSessionPersistWritesCacheCliffTransitionsOnly(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "prov", "fake-model", "test")
	if err != nil {
		t.Fatal(err)
	}

	client := &scriptedUsageClient{usage: []provider.Usage{
		// Arms the detector: a real cache read proves the provider caches at
		// all, and leaves a prompt big enough for its re-read to matter.
		{InputTokens: 100_000, CacheReadTokens: 20_000},
		// Collapsed, but one short of the big-prompt threshold.
		{InputTokens: 120_000, CacheReadTokens: 1_000},
		// Fires.
		{InputTokens: 120_000, CacheReadTokens: 1_000},
		// Fires again — the event the file must NOT turn into a second row.
		{InputTokens: 120_000, CacheReadTokens: 1_000},
		// Served from cache again: the run ends and the close row lands.
		{InputTokens: 1_000, CacheReadTokens: 130_000},
	}}

	ag := core.NewAgent(client, "fake-model", "", core.Registry{})
	// Core's zero value is off; the shipped ON default lives in the build
	// funnel's engine features. The detector short-circuits without it, so a
	// test that forgot this would pass vacuously with no rows at all.
	ag.SetPrefixDivergenceRecording(true)
	build.WireHeadlessSessionPersist(ag, sess)

	for i := 0; i < len(client.usage); i++ {
		if err := ag.Prompt(context.Background(), "go", nil, func(core.AgentEvent) {}); err != nil {
			t.Fatalf("Prompt %d: %v", i+1, err)
		}
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := readSessionCliffRows(t, sess.Path)
	if len(rows) != 2 {
		t.Fatalf("want exactly 2 cliff rows (open + close) for a run that fired twice, got %d: %+v", len(rows), rows)
	}
	if !rows[0].Ongoing {
		t.Errorf("first row must open the run: %+v", rows[0])
	}
	if rows[0].Dispatches < 2 {
		t.Errorf("open row must carry the run length that tripped the threshold, got %+v", rows[0])
	}
	if rows[1].Ongoing {
		t.Errorf("second row must close the run: %+v", rows[1])
	}
	// The close row reports the totals the run REACHED. The detector's
	// end-of-run event is the zero CacheCliff by contract, so a recorder that
	// wrote the event through would claim the collapse wasted nothing.
	if rows[1].Dispatches <= rows[0].Dispatches {
		t.Errorf("close row must carry the run's final length (> the opening %d), got %+v", rows[0].Dispatches, rows[1])
	}
	if rows[1].RereadTokens <= 0 {
		t.Errorf("close row must carry the tokens the provider re-read, got %+v", rows[1])
	}
}

// A session that never cliffs writes no cliff row. The row's value is that its
// presence means something; a detector that announced calm would be noise in
// every healthy session on disk.
func TestWireHeadlessSessionPersistWritesNoCliffRowWhenHealthy(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "prov", "fake-model", "test")
	if err != nil {
		t.Fatal(err)
	}

	client := &scriptedUsageClient{usage: []provider.Usage{
		{InputTokens: 100_000, CacheReadTokens: 20_000},
		{InputTokens: 2_000, CacheReadTokens: 118_000},
		{InputTokens: 2_000, CacheReadTokens: 119_000},
		{InputTokens: 2_000, CacheReadTokens: 120_000},
	}}

	ag := core.NewAgent(client, "fake-model", "", core.Registry{})
	ag.SetPrefixDivergenceRecording(true)
	build.WireHeadlessSessionPersist(ag, sess)

	for i := 0; i < len(client.usage); i++ {
		if err := ag.Prompt(context.Background(), "go", nil, func(core.AgentEvent) {}); err != nil {
			t.Fatalf("Prompt %d: %v", i+1, err)
		}
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if rows := readSessionCliffRows(t, sess.Path); len(rows) != 0 {
		t.Fatalf("a healthy session must write no cliff row, got %+v", rows)
	}
}

// cliffRow mirrors the on-disk shape from outside core, so the test reads what
// a analyst (or `jq`) would read rather than core's unexported struct.
type cliffRow struct {
	Ongoing      bool `json:"ongoing"`
	Dispatches   int  `json:"dispatches"`
	RereadTokens int  `json:"reread_tokens"`
}

func readSessionCliffRows(t *testing.T, path string) []cliffRow {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []cliffRow
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			Type  string    `json:"type"`
			Cliff *cliffRow `json:"cliff"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == "cliff" && row.Cliff != nil {
			found = append(found, *row.Cliff)
		}
	}
	return found
}
