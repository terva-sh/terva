package provider

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// goEditCall is the defect exactly as it arrived from a live Anthropic stream:
// an `edit` call carrying Go source, indented with the real tab bytes the model
// typed. RFC 8259 forbids an unescaped byte below 0x20 inside a string, so this
// is a syntax error — and it is the single most likely thing a coding model
// produces, because tab-indented source is the payload every edit carries.
const goEditCall = "{\"path\":\"skills.go\",\"edits\":[{\"oldText\":\"func f() error {\n\treturn nil\n}\",\"newText\":\"func f() error {\n\treturn errors.New(\\\"no\\\")\n}\"}]}"

func TestTheLiveFailureIsRepairedIntoTheCallTheModelMeant(t *testing.T) {
	if json.Valid([]byte(goEditCall)) {
		t.Fatal("fixture is already valid JSON — it no longer reproduces the bug it was captured from")
	}
	repaired := RepairToolArguments(goEditCall)
	if !json.Valid([]byte(repaired)) {
		t.Fatalf("still unparseable after repair: %q", repaired)
	}

	var got struct {
		Path  string `json:"path"`
		Edits []struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(repaired), &got); err != nil {
		t.Fatalf("unmarshal repaired: %v", err)
	}
	// The repair must recover the model's INTENT, not merely something that
	// parses: the tabs have to survive as tabs, or the edit would no longer
	// match the tab-indented bytes on disk and would fail a second time.
	if want := "func f() error {\n\treturn nil\n}"; got.Edits[0].OldText != want {
		t.Errorf("oldText = %q, want %q", got.Edits[0].OldText, want)
	}
	if !strings.Contains(got.Edits[0].NewText, "errors.New(\"no\")") {
		t.Errorf("newText lost its escaped quotes: %q", got.Edits[0].NewText)
	}
	if got.Path != "skills.go" {
		t.Errorf("path = %q", got.Path)
	}
}

// Valid input must come back byte-identical. Anything else would mean the
// repair can alter calls that were never broken.
func TestValidArgumentsAreNeverTouched(t *testing.T) {
	for _, in := range []string{
		`{}`,
		`{"path":"a.go","edits":[]}`,
		// Legal JSON whitespace: these control bytes sit BETWEEN tokens, where
		// they are not an error, and a model that pretty-prints is not wrong.
		"{\n\t\"path\": \"a.go\"\n}",
		// Already-escaped control characters inside a string.
		`{"text":"a\tb\nc"}`,
		// A backslash immediately before a quote must not flip string tracking.
		`{"text":"ends with a backslash \\"}`,
	} {
		if got := RepairToolArguments(in); got != in {
			t.Errorf("RepairToolArguments(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestEveryControlByteInsideAStringIsEscaped(t *testing.T) {
	raw := "{\"t\":\"tab\there\nnewline\rcr\bbs\ffeed\x01one\"}"
	repaired := RepairToolArguments(raw)
	if !json.Valid([]byte(repaired)) {
		t.Fatalf("not valid after repair: %q", repaired)
	}
	var got struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal([]byte(repaired), &got); err != nil {
		t.Fatal(err)
	}
	if want := "tab\there\nnewline\rcr\bbs\ffeed\x01one"; got.T != want {
		t.Errorf("round trip = %q, want %q", got.T, want)
	}
	// The rare bytes with no short form must use \u00xx rather than being
	// dropped, or the repair would silently change the model's string.
	if !strings.Contains(repaired, `\u0001`) {
		t.Errorf("0x01 was not escaped as \\u0001: %q", repaired)
	}
}

// Broken some OTHER way — truncated mid-stream, or a lost leading delta (the
// session that prompted this carried two of those). The original must come
// back untouched so each provider's own fallback still decides, rather than a
// half-repair that is still unparseable being passed off as fixed.
func TestIrreparableInputIsReturnedUnchanged(t *testing.T) {
	for _, in := range []string{
		`{"path":"a.go",`,          // truncated
		`command":"cd /tmp"}`,      // lost leading delta
		`{"a":1} trailing`,         // garbage after the value
		"{\"unterminated\":\"a\tb", // control byte AND unterminated
	} {
		if got := RepairToolArguments(in); got != in {
			t.Errorf("RepairToolArguments(%q) = %q, want the original back", in, got)
		}
	}
}

// An empty buffer is the "model called a no-argument tool" case, which every
// caller turns into {} itself. The repair must not invent a value.
func TestEmptyStaysEmpty(t *testing.T) {
	if got := RepairToolArguments(""); got != "" {
		t.Errorf("RepairToolArguments(\"\") = %q", got)
	}
}

// The whole defect class: an invalid RawMessage does not just fail at the tool,
// it makes the ENTIRE assistant message unmarshalable, so the turn cannot be
// written to the session at all. This is why the transcript that prompted this
// fix holds a tool_result with no assistant message before it.
func TestRepairedArgumentsLetTheAssistantTurnPersist(t *testing.T) {
	marshals := func(args string) bool {
		_, err := json.Marshal(struct {
			Arguments json.RawMessage `json:"arguments"`
		}{json.RawMessage(args)})
		return err == nil
	}
	if marshals(goEditCall) {
		t.Fatal("the raw fixture marshalled — it no longer demonstrates the transcript loss")
	}
	if !marshals(RepairToolArguments(goEditCall)) {
		t.Error("the repaired call still cannot be written to a session row")
	}
}

// Self-enrolling census: every place a streamed argument buffer is FINALISED
// must route through FinalizeToolArguments. It finds the sites by scanning the
// package rather than listing them, so a provider added later is enrolled by
// existing here — and one that hand-rolls the conversion fails this test with
// its own file and line.
//
// The required seam is FinalizeToolArguments rather than RepairToolArguments
// because repairing is only half the contract. The other half is that
// ToolCallBlock.Arguments is ALWAYS valid JSON, which the whole codebase now
// relies on when marshalling a message — a provider that repaired but then
// assigned an unrepairable buffer straight through would satisfy a
// repair-shaped census and still lose assistant turns off the transcript.
//
// EventToolArgs is deliberately exempt, and the exemption is the rule rather
// than a hole in it: that event carries a mid-stream FRAGMENT to the UI, and a
// fragment is almost never valid JSON standing alone — there is nothing to
// repair until the buffer is whole. The repair belongs at finalisation, once,
// which is exactly where a ToolCallBlock is built.
func TestEveryStreamedArgumentBufferIsRepaired(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	checked := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// Match `<something>.<buf>.String()` where the receiver is an
				// argument buffer — the shape every provider uses to finalise
				// streamed tool arguments.
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "String" || len(call.Args) != 0 {
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok || !isArgumentBuffer(inner.Sel.Name) {
					return true
				}
				if inEventToolArgs(file, call) {
					return true
				}
				checked++
				if !wrappedIn(file, call, "FinalizeToolArguments") {
					t.Errorf("%s: %s.String() is not wrapped in FinalizeToolArguments — this provider can emit a ToolCallBlock whose Arguments are not valid JSON, which makes every enclosing json.Marshal fail and drops the assistant turn off the transcript",
						fset.Position(call.Pos()), inner.Sel.Name)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("census matched no argument buffers at all — the shape it scans for moved, so this guard is now vacuous")
	}
	t.Logf("checked %d streamed argument buffer(s)", checked)
}

// inEventToolArgs reports whether call sits inside an EventToolArgs literal —
// a mid-stream fragment bound for the UI, not a finalised argument set.
func inEventToolArgs(file *ast.File, call *ast.CallExpr) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		id, ok := lit.Type.(*ast.Ident)
		if !ok || id.Name != "EventToolArgs" {
			return true
		}
		ast.Inspect(lit, func(m ast.Node) bool {
			if m == call {
				found = true
			}
			return !found
		})
		return !found
	})
	return found
}

func isArgumentBuffer(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "arg") && (strings.Contains(l, "buf") || strings.Contains(l, "args"))
}

// wrappedIn reports whether call sits inside a fn(...) invocation somewhere in
// the file's AST.
func wrappedIn(file *ast.File, call *ast.CallExpr, fn string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		outer, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := outer.Fun.(*ast.Ident)
		if !ok || id.Name != fn {
			return true
		}
		ast.Inspect(outer, func(m ast.Node) bool {
			if m == call {
				found = true
			}
			return !found
		})
		return !found
	})
	return found
}

// The invariant the rest of the codebase now leans on: whatever a provider was
// handed, the block it emits is marshalable. A ToolCallBlock is serialised by
// the session writer, every provider's request builder, and the ctrlproto wire
// behind the TUI — one invalid RawMessage breaks all three at once.
func TestFinalizeAlwaysYieldsMarshalableArguments(t *testing.T) {
	for name, in := range map[string]string{
		"valid":          `{"path":"a.go"}`,
		"raw tab":        goEditCall,
		"truncated":      `{"path":"a.go",`,
		"lost prefix":    `command":"cd /tmp"}`,
		"empty":          "",
		"not an object":  `garbage`,
		"trailing bytes": `{"a":1} trailing`,
	} {
		args, unparsed := FinalizeToolArguments(in)
		if !json.Valid(args) {
			t.Errorf("%s: Arguments are not valid JSON: %s", name, args)
		}
		if _, err := json.Marshal(ToolCallBlock{ID: "x", Name: "edit", Arguments: args, RawArguments: unparsed}); err != nil {
			t.Errorf("%s: block does not marshal: %v", name, err)
		}
	}
}

// The unparseable text must be KEPT, not swallowed. Losing it is what made the
// original defect unreadable after the fact — the session recorded a result
// with no call in front of it, so neither the tool name nor the arguments nor
// the fact a call happened survived.
func TestFinalizeKeepsTheTextItCouldNotParse(t *testing.T) {
	broken := `{"path":"a.go",`
	args, unparsed := FinalizeToolArguments(broken)
	if unparsed != broken {
		t.Errorf("unparsed = %q, want the original %q", unparsed, broken)
	}
	if string(args) != "{}" {
		t.Errorf("args = %s, want {}", args)
	}
}

// A call that parsed must NOT be flagged as unparseable, or every healthy call
// would be refused at dispatch.
func TestFinalizeFlagsNothingWhenTheCallIsFine(t *testing.T) {
	for _, in := range []string{`{"path":"a.go"}`, goEditCall, ""} {
		if _, unparsed := FinalizeToolArguments(in); unparsed != "" {
			t.Errorf("FinalizeToolArguments(%q) flagged a usable call as unparseable: %q", in, unparsed)
		}
	}
}
