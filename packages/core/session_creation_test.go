package core

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// metaRowsOf returns every meta row in a session file, in file order, decoded
// raw rather than through any of the readers under test here.
func metaRowsOf(t *testing.T, path string) []SessionMeta {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	var out []SessionMeta
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			Type string       `json:"type"`
			Meta *SessionMeta `json:"meta"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Type != "meta" || row.Meta == nil {
			continue
		}
		out = append(out, *row.Meta)
	}
	if len(out) == 0 {
		t.Fatalf("%s has no meta rows", path)
	}
	return out
}

// driveEveryMetaWriter calls every method that appends a meta row, with values
// that differ from the ones the session was created with.
//
// TestEveryMetaWriterIsExercisedByTheCreationGate reads THIS function's source
// to find out which methods it calls, so a new meta writer fails that census
// until it is added here — and adding it here is what puts it in front of
// TestOnlyCreationFixedFieldsSurviveTheMetaTimeline.
func driveEveryMetaWriter(t *testing.T, s *Session) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("UpdateModel", s.UpdateModel("anthropic", "claude-elsewhere"))
	must("UpdateReasoning", s.UpdateReasoning("high"))
	must("StampVersion", s.StampVersion("v99.0.0"))
	must("SetCreationSpec", s.SetCreationSpec("kartoittaja-c", "play", "card-ref", map[string]string{"actor": "persona"}, 3))
	must("SetBackground", s.SetBackground("backdrop-id"))
	must("SetNote", s.SetNote("an author's note"))
	must("SetUserPersona", s.SetUserPersona("name", "description", "gender", "pronouns"))
	must("SetCoordination", s.SetCoordination("solo"))
	must("SetWorld", s.SetWorld("world-id"))
	must("SetParent", s.SetParent("parent-id"))
	must("SetCast", s.SetCast(map[string]string{"actor": "persona"}, map[string]CastRoute{"actor": {Provider: "openai", Model: "gpt-5"}}))
	must("SetWorldLore", s.SetWorldLore([]WorldLoreEntry{{Keys: []string{"key"}, Content: "content"}}))
	must("bumpFormatForAmend", s.bumpFormatForAmend())
}

// TestOnlyCreationFixedFieldsSurviveTheMetaTimeline is the licence for
// ReadSessionCreation reading only the opening row.
//
// Meta rows are an append-only last-wins timeline and writeMetaLocked emits a
// copy of the WHOLE struct every time, so the opening row is not a header — it
// is the session before anything happened to it. Every field of SessionCreation
// must therefore be one that no meta writer touches, checked reflectively so a
// field ADDED to SessionCreation is checked without anyone remembering this test
// exists.
//
// The floors matter as much as the comparison: a driver that stopped writing
// rows, or writers that all became no-ops, would satisfy "nothing drifted" while
// proving nothing.
func TestOnlyCreationFixedFieldsSurviveTheMetaTimeline(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "created-with")
	if err != nil {
		t.Fatal(err)
	}
	// A session closed with no appended messages is DELETED, and this one has to
	// survive to be read back.
	if err := s.AppendMessage(mvMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	driveEveryMetaWriter(t, s)
	_ = s.Close()

	metas := metaRowsOf(t, path)
	if len(metas) < 10 {
		t.Fatalf("only %d meta rows — the driver is not exercising the writers", len(metas))
	}

	drifted := driftedFields(t, metas[0], metas[len(metas)-1])
	if len(drifted) < 15 {
		t.Fatalf("only %d field(s) differ between the first and last meta row (%v) — "+
			"the writers are not changing anything, so agreement proves nothing", len(drifted), drifted)
	}

	creation := reflect.TypeOf(SessionCreation{})
	if creation.NumField() == 0 {
		t.Fatal("SessionCreation has no fields")
	}
	for i := range creation.NumField() {
		name := creation.Field(i).Name
		if _, ok := reflect.TypeOf(SessionMeta{}).FieldByName(name); !ok {
			t.Errorf("SessionCreation.%s has no SessionMeta field of the same name, so nothing here can check it", name)
			continue
		}
		want := reflect.ValueOf(metas[0]).FieldByName(name).Interface()
		for row, m := range metas {
			got := reflect.ValueOf(m).FieldByName(name).Interface()
			if !reflect.DeepEqual(got, want) {
				t.Errorf("SessionCreation.%s is not creation-fixed: meta row 0 says %v, row %d says %v — "+
					"reading it from the opening row is a lie, so it does not belong on this type",
					name, want, row, got)
				break
			}
		}
	}

	// And the reader hands back what the rows say.
	created, err := ReadSessionCreation(path)
	if err != nil {
		t.Fatalf("ReadSessionCreation: %v", err)
	}
	for i := range creation.NumField() {
		name := creation.Field(i).Name
		want := reflect.ValueOf(metas[0]).FieldByName(name)
		got := reflect.ValueOf(created).Field(i)
		if !want.IsValid() || !reflect.DeepEqual(got.Interface(), want.Interface()) {
			t.Errorf("ReadSessionCreation.%s = %v, the file says %v", name, got.Interface(), want.Interface())
		}
	}
}

// driftedFields lists the JSON field names on which two meta rows disagree,
// counting a field present in one and absent from the other.
func driftedFields(t *testing.T, a, b SessionMeta) []string {
	t.Helper()
	as, bs := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ab, &as); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bb, &bs); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]json.RawMessage{as, bs} {
		for k := range m {
			if seen[k] {
				continue
			}
			seen[k] = true
			if string(as[k]) != string(bs[k]) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// metaWriterExemptions are methods that reach writeMeta without being a way to
// CHANGE the session's metadata, so driving them would add nothing.
//
// Bounded on purpose, and checked for staleness below: an exemption naming a
// method that no longer writes meta is a licence nobody is using, and a licence
// nobody is using is one that quietly covers the wrong thing later.
var metaWriterExemptions = map[string]string{
	"writeMeta": "plumbing — its body is writeMetaLocked, and every writer below funnels through it",
}

// TestEveryMetaWriterIsExercisedByTheCreationGate is a census, not a list: it
// finds the meta writers by parsing the package, so a method added tomorrow
// enrolls itself.
//
// Without it, TestOnlyCreationFixedFieldsSurviveTheMetaTimeline would be a
// snapshot of the writers that existed when it was written — and the one that
// rewrote CWD would be precisely the one nobody thought to add.
func TestEveryMetaWriterIsExercisedByTheCreationGate(t *testing.T) {
	writers := metaWritingMethods(t)
	if len(writers) < 10 {
		t.Fatalf("found only %d meta-writing method(s) — the scan is not finding them: %v", len(writers), writers)
	}
	for name, why := range metaWriterExemptions {
		if !writers[name] {
			t.Errorf("metaWriterExemptions excuses %q (%s), but nothing by that name writes a meta row any more — drop the entry", name, why)
		}
	}

	driven := methodsCalledByTheDriver(t)
	for name := range writers {
		if _, exempt := metaWriterExemptions[name]; exempt {
			continue
		}
		if !driven[name] {
			t.Errorf("%s writes a meta row but driveEveryMetaWriter never calls it, so no gate has ever seen whether it "+
				"disturbs a SessionCreation field — add it there, or exempt it with a reason", name)
		}
	}
	for name := range driven {
		if !writers[name] && name != "AppendMessage" {
			t.Errorf("driveEveryMetaWriter calls %s, which no longer writes a meta row — the driver is carrying dead weight", name)
		}
	}
}

// metaWritingMethods returns the names of every *Session method whose body calls
// writeMeta or writeMetaLocked.
func metaWritingMethods(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	forEachCoreFile(t, func(_ string, file *ast.File) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue // a plain func has no session to mutate; newSessionAt CREATES the first row
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "writeMeta" || sel.Sel.Name == "writeMetaLocked" {
					out[fn.Name.Name] = true
				}
				return true
			})
		}
	})
	return out
}

// methodsCalledByTheDriver reads driveEveryMetaWriter's own source and reports
// which methods it calls. Reading the source rather than trusting a hand-kept
// list is the point: a name in a list can be there without the call being there.
//
// It reads the source as a SYNTAX TREE, not as text. The first version found the
// function by string search and cut its body at the first "\n}\n" — which is a
// line ending this repository does not pin for .go files. On a CRLF checkout the
// bytes are "\r\n}\r\n", that delimiter never matched, and the cut FAILED OPEN:
// the scan ran to the end of the file and enrolled every s.Method( it passed,
// including calls belonging to other tests. It reported Close as driven, and the
// Windows release gate was the first thing that ever said so.
//
// The tree has no such failure mode, and it also cannot match a method name that
// merely appears inside a comment or a string literal.
func methodsCalledByTheDriver(t *testing.T) map[string]bool {
	t.Helper()
	return driverCallsIn(t, "session_creation_test.go")
}

// driverCallsIn is the half that takes a path, so a guard can point it at a copy
// with different line endings. Nothing else should need it.
func driverCallsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "driveEveryMetaWriter" {
			body = fn.Body
			break
		}
	}
	if body == nil {
		t.Fatal("driveEveryMetaWriter is gone; this census has nothing to read")
	}
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "s" {
			out[sel.Sel.Name] = true
		}
		return true
	})
	return out
}

// TestTheDriverCensusDoesNotDependOnLineEndings is the guard for the defect
// above. .gitattributes pins a handful of fixtures to LF and says nothing about
// .go, so a Windows runner checks this very file out with CRLF — and a census
// that reads its own source has to survive that.
//
// The teeth are in the second half: it is not enough that the CRLF copy yields
// SOMETHING, it must yield exactly what the LF copy yields. The broken version
// returned a strict superset, so a test that only checked "not empty" would have
// passed on the bug it exists to catch.
func TestTheDriverCensusDoesNotDependOnLineEndings(t *testing.T) {
	lf, err := os.ReadFile("session_creation_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(lf, []byte("\r\n")) {
		t.Skip("this checkout is already CRLF; the comparison would be against itself")
	}
	dir := testsupport.TempDir(t)
	crlf := filepath.Join(dir, "session_creation_test.go")
	if err := os.WriteFile(crlf, bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	want, got := driverCallsIn(t, "session_creation_test.go"), driverCallsIn(t, crlf)
	if len(want) < 10 {
		t.Fatalf("the LF scan found only %d call(s) — it is not reading the driver: %v", len(want), want)
	}
	if !reflect.DeepEqual(want, got) {
		var extra, missing []string
		for k := range got {
			if !want[k] {
				extra = append(extra, k)
			}
		}
		for k := range want {
			if !got[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(extra)
		sort.Strings(missing)
		t.Errorf("the census reads differently under CRLF — extra %v, missing %v; it must not depend on line endings",
			extra, missing)
	}
}

// metaReaderCalls invokes every reader enrolled in
// TestEveryMetaReaderFoldsTheWholeTimeline and returns the meta each produced.
// The key is the function's name, matched against the package scan.
func metaReaderCalls(t *testing.T, path string) map[string]SessionMeta {
	t.Helper()
	out := map[string]SessionMeta{}

	_, m := describeSessionMeta(path)
	out["describeSessionMeta"] = m

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, m = describeSessionFromMeta(path, f)
	out["describeSessionFromMeta"] = m

	_, m, err = ReadReplayRows(path)
	if err != nil {
		t.Fatalf("ReadReplayRows: %v", err)
	}
	out["ReadReplayRows"] = m

	m, _, err = StreamReplayMessages(t.Context(), path, 0, func(int, provider.Message) {})
	if err != nil {
		t.Fatalf("StreamReplayMessages: %v", err)
	}
	out["StreamReplayMessages"] = m

	m, _, err = StreamReplayRows(t.Context(), path, 0, func(int, ReplayRow) {})
	if err != nil {
		t.Fatalf("StreamReplayRows: %v", err)
	}
	out["StreamReplayRows"] = m

	return out
}

// TestEveryMetaReaderFoldsTheWholeTimeline: a reader that hands back a whole
// SessionMeta must have folded the whole file, because every field but the
// SessionCreation three is stamped by a row written after the first.
//
// A reader that wants to stop early may — by returning SessionCreation, which
// cannot carry a field that goes stale. That is why ReadSessionCreation is not
// in this census: the narrow type is the exemption.
//
// This is a behaviour census over a scan: the enrolled set is checked against
// the functions the package actually declares, so a new path-shaped SessionMeta
// reader fails here until it is driven, and is then held to last-wins.
func TestEveryMetaReaderFoldsTheWholeTimeline(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "created-with")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(mvMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	driveEveryMetaWriter(t, s)
	_ = s.Close()

	metas := metaRowsOf(t, path)
	first, last := metas[0], metas[len(metas)-1]
	if len(driftedFields(t, first, last)) < 15 {
		t.Fatal("the first and last meta rows agree; a folding reader is indistinguishable from a first-row one here")
	}

	declared := metaReturningFuncs(t)
	got := metaReaderCalls(t, path)
	for name := range declared {
		if _, ok := got[name]; !ok {
			t.Errorf("%s returns a SessionMeta read from a file but metaReaderCalls never invokes it, so nothing checks "+
				"whether it folds the timeline or stops at the opening row", name)
		}
	}
	for name := range got {
		if !declared[name] {
			t.Errorf("metaReaderCalls drives %s, which the package no longer declares as a SessionMeta reader", name)
		}
	}

	for name, m := range got {
		if m.Model != last.Model || m.Persona != last.Persona || m.Parent != last.Parent || m.World != last.World {
			t.Errorf("%s stopped short of the end of the timeline: got model=%q persona=%q parent=%q world=%q, "+
				"the file's last meta row says model=%q persona=%q parent=%q world=%q",
				name, m.Model, m.Persona, m.Parent, m.World, last.Model, last.Persona, last.Parent, last.World)
		}
	}
}

// metaReturningFuncs names every non-method function in the package that returns
// a SessionMeta — the shape "read a session's metadata out of a file".
func metaReturningFuncs(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	forEachCoreFile(t, func(_ string, file *ast.File) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.Results == nil {
				continue
			}
			for _, res := range fn.Type.Results.List {
				if id, ok := res.Type.(*ast.Ident); ok && id.Name == "SessionMeta" {
					out[fn.Name.Name] = true
				}
			}
		}
	})
	return out
}

// forEachCoreFile parses every non-test .go file in this package.
func forEachCoreFile(t *testing.T, fn func(path string, file *ast.File)) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	n := 0
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		fn(p, file)
		n++
	}
	if n < 10 {
		t.Fatalf("parsed only %d source file(s) in packages/core — the scan is looking in the wrong place", n)
	}
}

// TestTheSessionTreeLinksAParentStampedAfterCreation: SetParent's own doc says it
// is "the stamper for the paths that create first and learn their lineage after"
// — the next-scene path in workspace.go. The tree read Parent from the OPENING
// meta row, which for exactly those sessions is empty, so every one of them drew
// as a parentless root and the lineage the author had just recorded was invisible.
func TestTheSessionTreeLinksAParentStampedAfterCreation(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	msg := mvMsg(provider.RoleUser, "u0")

	parent, err := NewSession(root, cwd, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.AppendMessage(msg); err != nil {
		t.Fatal(err)
	}
	parentID := parent.Meta.ID
	_ = parent.Close()

	child, err := NewSession(root, cwd, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := child.AppendMessage(msg); err != nil {
		t.Fatal(err)
	}
	if err := child.SetParent(parentID); err != nil {
		t.Fatal(err)
	}
	childID := child.Meta.ID
	_ = child.Close()

	roots := BuildSessionTree(root, cwd)
	if len(roots) != 1 {
		t.Fatalf("BuildSessionTree returned %d roots, want 1 — the child hung off nothing", len(roots))
	}
	if roots[0].Meta.ID != parentID {
		t.Fatalf("the root is %s, want the parent %s", roots[0].Meta.ID, parentID)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].Meta.ID != childID {
		t.Fatalf("the parent has %d child/children, want the one whose lineage was stamped after creation", len(roots[0].Children))
	}
}

// TestFindSessionByIDRefusesTheEmptyID: it reads the opening row, and a file
// with no meta row reports an empty id. Without the guard, looking up "" returns
// the first unreadable file in the directory as a confident answer.
func TestFindSessionByIDRefusesTheEmptyID(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	dir := SessionsDir(root, cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(dir, "20260101-120000-aaaaaaaa.jsonl")
	if err := os.WriteFile(junk, []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FindSessionByID(root, cwd, ""); got != "" {
		t.Errorf("FindSessionByID(root, cwd, \"\") = %q, want \"\"", got)
	}
}
