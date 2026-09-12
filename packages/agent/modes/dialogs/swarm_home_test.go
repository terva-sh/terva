package dialogs

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/tui"
)

// pinHome fixes the home directory shortenPath writes as "~". Without it every
// expectation below would depend on whose machine ran the test.
func pinHome(t *testing.T, home string) {
	t.Helper()
	prev := userHomeDir
	userHomeDir = func() string { return home }
	t.Cleanup(func() { userHomeDir = prev })
}

// The shortener is what makes a path fit an 18-column cell and still say where
// it points. Each case below is a shape the dashboard actually hands it.
func TestShortenPathCollapsesLikeFish(t *testing.T) {
	pinHome(t, "/home/tester")
	cases := []struct {
		name  string
		path  string
		width int
		want  string
	}{
		{"a path that already fits is left alone", "/home/tester/src/terva", 30, "~/src/terva"},
		{"the home directory itself", "/home/tester", 18, "~"},
		{"heads collapse, the last component survives", "/home/tester/workspace/git-worktrees/terva-fixes", 18, "~/w/g/terva-fixes"},
		{"an absolute path keeps its root", "/usr/local/share/terva", 12, "/u/l/s/terva"},
		{"a hidden directory keeps its dot", "/home/tester/.local/state/terva/worktrees", 20, "~/.l/s/t/worktrees"},
		{"a last component over budget keeps its tail", "/home/tester/a/verylongdirectoryname-here", 10, "…name-here"},
		{"empty in, empty out", "", 18, ""},
		{"no width, no output", "/home/tester/src/terva", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shortenPath(c.path, c.width)
			if got != c.want {
				t.Errorf("shortenPath(%q, %d) = %q, want %q", c.path, c.width, got, c.want)
			}
			if runeLen(got) > c.width {
				t.Errorf("shortenPath(%q, %d) = %q, which is %d runes and over budget", c.path, c.width, got, runeLen(got))
			}
		})
	}
}

// Origin is the project the agent belongs to. Dir is where it happens to run,
// and for a reloaded agent Dir is the reader's own cwd whatever project the
// agent came from, so the cell must never prefer it.
func TestSwarmHomeCellPrefersOriginOverDir(t *testing.T) {
	pinHome(t, "/home/tester")
	cases := []struct {
		name string
		row  swarm.AgentSnapshot
		want string
	}{
		{
			"a leased agent is marked",
			swarm.AgentSnapshot{Leased: true, Origin: "/home/tester/workspace/git-worktrees/terva-fixes"},
			"*~/w/g/terva-fixes",
		},
		{
			"origin wins over the directory the agent runs in",
			swarm.AgentSnapshot{Origin: "/home/tester/src/terva", Dir: "/tmp/some-lease"},
			"~/src/terva",
		},
		{
			"a record older than Origin falls back to Dir",
			swarm.AgentSnapshot{Dir: "/home/tester/src/terva"},
			"~/src/terva",
		},
		{
			"neither field set",
			swarm.AgentSnapshot{},
			"-",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := swarmHomeCell(c.row, swarmHomeCellWidth)
			if got != c.want {
				t.Errorf("swarmHomeCell = %q, want %q", got, c.want)
			}
			if runeLen(got) > swarmHomeCellWidth {
				t.Errorf("swarmHomeCell = %q, which is %d runes and over the %d cell", got, runeLen(got), swarmHomeCellWidth)
			}
		})
	}
}

// The regression the column exists for. Swarm.Reload walks one root for every
// project and buildDetachedAgent overwrites Dir with the live RepoRoot, so a
// row built on Dir reports another project's agent as living in the reader's
// tree.
func TestReloadedAgentDoesNotClaimTheReadersTree(t *testing.T) {
	pinHome(t, "/home/tester")
	row := swarm.AgentSnapshot{
		ID:       "port-the-parser-991",
		Status:   swarm.StatusDetached,
		Activity: "detached",
		Started:  time.Now().Add(-3 * time.Hour),
		// What Reload leaves behind: the reader's cwd in Dir, the agent's real
		// project in Origin.
		Dir:    "/home/tester/workspace/terva-fixes",
		Origin: "/home/tester/src/other-project",
	}
	body := formatSwarmRow(row, 200)
	if !strings.Contains(body, "other-project") {
		t.Errorf("row does not name the project the agent belongs to:\n%q", body)
	}
	if strings.Contains(body, "terva-fixes") {
		t.Errorf("row claims the reader's own tree for a reloaded agent:\n%q", body)
	}
}

// The optional groups yield one at a time, and in a fixed order. Computed from
// the width constants rather than written out, so shrinking a cell moves the
// thresholds and does not silently invert the order.
func TestSwarmColumnsDropInOrder(t *testing.T) {
	all := swarmRowFixedWidth + swarmMetadataWidth + swarmHomeWidth + swarmProgressWidth + swarmMinActivityCol
	metaHome := swarmRowFixedWidth + swarmMetadataWidth + swarmHomeWidth + swarmMinActivityCol
	meta := swarmRowFixedWidth + swarmMetadataWidth + swarmMinActivityCol
	prog := swarmRowFixedWidth + swarmProgressWidth + swarmMinActivityCol

	cases := []struct {
		name                           string
		width                          int
		metadata, home, progressWanted bool
	}{
		{"everything fits", all, true, true, true},
		{"the counters yield first", all - 1, true, true, false},
		{"home holds at its own threshold", metaHome, true, true, false},
		{"home yields next", metaHome - 1, true, false, false},
		{"metadata holds at its own threshold", meta, true, false, false},
		// Below the metadata threshold the row falls back to the narrow layout
		// that predates both HOME and metadata: counters and activity only.
		{"metadata yields last", meta - 1, false, false, true},
		{"the narrow counter layout holds", prog, false, false, true},
		{"nothing optional survives", prog - 1, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			metadata, home, progress := swarmLayout(c.width)
			if metadata != c.metadata || home != c.home || progress != c.progressWanted {
				t.Errorf("swarmLayout(%d) = (metadata=%v, home=%v, progress=%v), want (%v, %v, %v)",
					c.width, metadata, home, progress, c.metadata, c.home, c.progressWanted)
			}
		})
	}
}

// A header that promises HOME above rows that dropped it is worse than no
// header. Same contract the counters and metadata already hold to.
func TestHeaderAdvertisesHomeExactlyWhenRowsFillIt(t *testing.T) {
	pinHome(t, "/home/tester")
	// A sentinel that survives the shortener untouched: it is one component,
	// and it fits the cell whole.
	const sentinel = "/zzhomezz"
	row := swarm.AgentSnapshot{
		ID: "alpha-1", Status: swarm.StatusRunning, Activity: "idle",
		Started: time.Now(), Turns: 7, ToolCalls: 9,
		Provider: "provider-x", Model: "model-x", Reasoning: "low",
		Origin: sentinel,
	}
	for w := 40; w <= 200; w += 3 {
		header := swarmListHeader(w - 2)
		body := formatSwarmRow(row, w-2)
		headerHasHome := strings.Contains(header, "HOME")
		bodyHasHome := strings.Contains(body, sentinel)
		if headerHasHome != swarmHomeFits(w-2) {
			t.Fatalf("width %d: header home=%v but layout home=%v\nheader: %q",
				w, headerHasHome, swarmHomeFits(w-2), header)
		}
		if bodyHasHome != headerHasHome {
			t.Fatalf("width %d: header home=%v but row home=%v\nheader: %q\nrow:    %q",
				w, headerHasHome, bodyHasHome, header, body)
		}
		if n := runeLen(body); n > w-2 {
			t.Fatalf("width %d: row is %d runes, over the %d budget: %q", w, n, w-2, body)
		}
	}
}

// The transcript view prints dir. For a leased agent the project is somewhere
// else, and for a reloaded one dir is the reader's own cwd, so both facts have
// to be on the screen beside it.
func TestTranscriptNamesTheProjectAndTheLostLease(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "alpha-1", Task: "port the parser", Status: swarm.StatusDetached,
		Activity: "detached", Started: time.Now().Add(-2 * time.Hour),
		Leased: true,
		Dir:    "/home/tester/workspace/terva-fixes",
		Origin: "/home/tester/src/other-project",
		Lines:  []string{"working on it"},
	}}
	d := NewSwarmDialog()
	d.OpenViewing("alpha-1", staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	out := strings.Join(d.Render(tui.Theme{}, 100), "\n")

	for _, want := range []string{"dir: /home/tester/workspace/terva-fixes", "project: /home/tester/src/other-project", "lost its own worktree"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript header missing %q:\n%s", want, out)
		}
	}
}

// An agent running in its own project has nothing extra to say, and a line
// repeating dir under another name is noise.
func TestTranscriptStaysQuietWhenTheProjectIsTheDirectory(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "alpha-1", Task: "port the parser", Status: swarm.StatusRunning,
		Activity: "thinking", Started: time.Now(),
		Dir:    "/home/tester/src/terva",
		Origin: "/home/tester/src/terva",
		Lines:  []string{"working on it"},
	}}
	d := NewSwarmDialog()
	d.OpenViewing("alpha-1", staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	out := strings.Join(d.Render(tui.Theme{}, 100), "\n")

	if strings.Contains(out, "project:") {
		t.Fatalf("transcript repeated dir as a project line:\n%s", out)
	}
}

// transcriptEditorCursorRow counts renderTranscript's header rows by hand, so
// every conditional row the header gained has to be counted there too. A row
// only one of them knows about puts the caret below the editor.
func TestTranscriptCaretCountsTheProjectRow(t *testing.T) {
	const width = 100
	const marker = "ZZMARKERZZ"
	cases := []struct {
		name   string
		origin string
	}{
		{"no extra header rows", "/home/tester/src/terva"},
		{"a project row above the status line", "/home/tester/src/other-project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := []swarm.AgentSnapshot{{
				ID: "alpha-1", Task: "port the parser", Status: swarm.StatusRunning,
				Activity: "thinking", Started: time.Now(),
				Dir:    "/home/tester/src/terva",
				Origin: c.origin,
				Lines:  []string{"working on it"},
			}}
			d := NewSwarmDialog()
			send := func(id, text string) error { return nil }
			if !d.OpenViewing("alpha-1", staticSnapshots(rows...), nil, nil, nil, send, nil, "") {
				t.Fatal("OpenViewing did not find the agent")
			}
			for _, r := range marker {
				d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
			}
			padded := PadDialogFrame(d.Render(tui.Theme{}, width))
			row, _ := d.CursorPos(width)
			if row < 0 || row >= len(padded) {
				t.Fatalf("caret row %d outside the %d painted rows", row, len(padded))
			}
			if got := widgets.StripANSIBytes(padded[row]); !strings.Contains(got, marker) {
				t.Fatalf("caret row %d = %q, want the editor line", row, got)
			}
		})
	}
}
