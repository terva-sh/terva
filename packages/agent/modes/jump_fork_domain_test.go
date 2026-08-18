package modes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

func jfUser(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func jfAsst(text string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func jfMirror() provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: core.ToolImageMirrorPrefix}},
		Meta:    map[string]string{"tool_image_mirror": "true"},
	}
}

// jfForkFixture writes a real session whose transcript carries a tool-image
// mirror between two typed turns, and hands back an Interactive wired the way
// production wires it, with paged-back history in the display domain so the two
// domains genuinely disagree.
//
//	on disk / carrier:  0 real-1   1 a1   2 <mirror>   3 real-2   4 a2
//	revealed (display): older-1, older-a  -> prepended, and the mirror dropped
func jfForkFixture(t *testing.T) (*Interactive, string, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)

	msgs := []provider.Message{jfUser("real-1"), jfAsst("a1"), jfMirror(), jfUser("real-2"), jfAsst("a2")}

	sess, err := core.NewSession(home, cwd, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	path := sess.Path
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	i := newCtrlprotoTestInteractive()
	i.jumpDialog = dialogs.NewJumpDialog()
	i.cfg.Terminal = tuitest.NewFakeTerm(80, 24)
	i.cfg.TervaHome = home
	i.cfg.CWD = cwd
	i.cfg.Version = "test"
	i.cfg.CurrentSessionPath = func() string { return path }
	i.carrierMessages = msgs
	// Paged-back history: display-only, prepended, and unforkable.
	i.revealed = []provider.Message{jfUser("older-1"), jfAsst("older-a")}
	i.view.Messages = filterHiddenTranscriptMessages(i.displayTranscript())
	i.keymap = i.buildGlobalKeymap()
	i.overlays = i.buildOverlays()
	return i, home, cwd
}

// branchFileBesides returns the session file in root/cwd that is not src.
func branchFileBesides(t *testing.T, root, cwd, src string) string {
	t.Helper()
	entries, err := os.ReadDir(core.SessionsDir(root, cwd))
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(core.SessionsDir(root, cwd), e.Name())
		if !e.IsDir() && p != src {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) != 1 {
		t.Fatalf("expected exactly one branch file beside the source, found %d: %v", len(out), out)
	}
	return out[0]
}

// TestForkingTheSecondTurnCutsAtTheSecondTurn drives the production path — the
// /fork verb, the real overlay registry, a real key — because the gates in
// package dialogs build their own inputs and so prove only that the DIALOG can
// carry a domain, not that the two call sites hand it the right one.
//
// The picker's second row is "real-2", which sits at index 3 of the effective
// transcript and index 4 of the slice the chat paints. A branch cut at 4
// includes the assistant turn after it and drops nothing the user picked; a
// branch cut at 5 would swallow it. The ForkPoint the file records is the
// assertion, not the row the picker drew.
func TestForkingTheSecondTurnCutsAtTheSecondTurn(t *testing.T) {
	i, home, cwd := jfForkFixture(t)
	src := i.cfg.CurrentSessionPath()

	i.doSessionFork()
	if !i.jumpDialog.Active() {
		t.Fatal("/fork did not open the picker")
	}
	rows := i.jumpDialog.Targets()
	if len(rows) != 2 {
		var got []string
		for _, r := range rows {
			got = append(got, r.Preview)
		}
		t.Fatalf("the fork picker offers %d rows, want the 2 turns the user typed: %q", len(rows), got)
	}
	// Move to the second turn and take it.
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyDown})
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEnter})

	branch := branchFileBesides(t, home, cwd, src)
	bs, bmsgs, err := core.OpenSession(branch)
	if err != nil {
		t.Fatalf("open branch: %v", err)
	}
	defer bs.Close()

	if bs.Meta.ForkPoint != 4 {
		t.Errorf("branch ForkPoint = %d, want 4 — the cut landed on a different message than the turn that was picked",
			bs.Meta.ForkPoint)
	}
	if len(bmsgs) != 4 {
		t.Fatalf("branch carries %d messages, want 4 (real-1, a1, mirror, real-2)", len(bmsgs))
	}
	last := bmsgs[len(bmsgs)-1]
	if !core.IsUserTurn(last) || firstTextOf(last) != "real-2" {
		t.Errorf("the branch ends on %q, want the picked turn real-2", firstTextOf(last))
	}
}

// TestForkingTheFirstTurnIsNotShiftedByTheMirror: the mirror sits between the
// two typed turns, so a picker that RENUMBERED instead of skipping would send
// the first row to the mirror's index and cut the branch one message long.
func TestForkingTheFirstTurnIsNotShiftedByTheMirror(t *testing.T) {
	i, home, cwd := jfForkFixture(t)
	src := i.cfg.CurrentSessionPath()

	i.doSessionFork()
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEnter})

	branch := branchFileBesides(t, home, cwd, src)
	bs, bmsgs, err := core.OpenSession(branch)
	if err != nil {
		t.Fatalf("open branch: %v", err)
	}
	defer bs.Close()

	if bs.Meta.ForkPoint != 1 || len(bmsgs) != 1 {
		t.Errorf("branch ForkPoint = %d with %d message(s), want 1 and 1 — real-1 alone", bs.Meta.ForkPoint, len(bmsgs))
	}
}

// TestAPlainJumpAfterADismissedForkDoesNotFork is the failure the deleted bool
// made possible: pendingFork had one setter and four clearers, and
// openJumpDialog was not one of them. With the purpose bound to Open there is
// no state left to leak — reopening for /jump cannot fork, whatever happened
// before it.
func TestAPlainJumpAfterADismissedForkDoesNotFork(t *testing.T) {
	i, home, cwd := jfForkFixture(t)
	src := i.cfg.CurrentSessionPath()

	i.doSessionFork()
	// Reopen for a plain /jump WITHOUT going through any of the exits that used
	// to clear the flag.
	i.openJumpDialog(nil)
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEnter})

	entries, err := os.ReadDir(core.SessionsDir(home, cwd))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a plain /jump created %d extra session file(s) — it forked", len(entries)-1)
	}
	if entries[0].Name() != filepath.Base(src) {
		t.Fatalf("the one session file is %q, not the source", entries[0].Name())
	}
}

// TestEveryJumpPurposeIsHandled is a census over the constants, not a list of
// them: the overlay switch has no default arm, so a purpose nobody wired
// silently does nothing on select. This names it instead.
func TestEveryJumpPurposeIsHandled(t *testing.T) {
	declared := jumpPurposeConstants(t)
	if len(declared) < 2 {
		t.Fatalf("found %d JumpPurpose constant(s) — the scan is not finding them: %v", len(declared), declared)
	}
	handled := jumpPurposeSwitchCases(t)
	for _, name := range declared {
		if !handled[name] {
			t.Errorf("dialogs.%s is a JumpPurpose but the jump overlay's switch has no arm for it, and the switch has no "+
				"default — a selection made for it would silently do nothing", name)
		}
	}
	for name := range handled {
		found := false
		for _, d := range declared {
			if d == name {
				found = true
			}
		}
		if !found {
			t.Errorf("the jump overlay handles dialogs.%s, which is no longer a JumpPurpose", name)
		}
	}
}

// jumpPurposeConstants reads the JumpPurpose constant block out of the dialogs
// package source.
func jumpPurposeConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dialogs/jump_dialog.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse jump_dialog.go: %v", err)
	}
	var out []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		// Whole-block, not per-spec: in an iota block only the FIRST spec names
		// the type, and the rest inherit it. Deciding per spec instead let the
		// scan run on past the block's end and enrol the next const in the file
		// as a JumpPurpose — which this census reported on its first run.
		named := false
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "JumpPurpose" {
				named = true
			}
		}
		if !named {
			continue
		}
		for _, spec := range gd.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok {
				for _, id := range vs.Names {
					out = append(out, id.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// jumpPurposeSwitchCases reads the arms of the switch in the jump overlay.
func jumpPurposeSwitchCases(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "overlay_registry.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse overlay_registry.go: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Purpose" {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			if len(cc.List) == 0 {
				t.Error("the jump overlay's purpose switch grew a default arm — an unwired purpose would take the " +
					"action beside it rather than fail this census")
			}
			for _, e := range cc.List {
				if s, ok := e.(*ast.SelectorExpr); ok {
					out[s.Sel.Name] = true
				}
			}
		}
		return true
	})
	return out
}

func firstTextOf(m provider.Message) string {
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			return tb.Text
		}
	}
	return ""
}

var _ = dialogs.JumpFork
