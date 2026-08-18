package jsengine

import (
	"strings"
	"testing"
)

var hostNames = []string{"read", "grep", "glob", "write"}

func analyze(t *testing.T, src string) BindingRefs {
	t.Helper()
	refs, err := AnalyzeBindings("t.js", src, hostNames)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return refs
}

func wantComplete(t *testing.T, refs BindingRefs) {
	t.Helper()
	if !refs.Complete {
		t.Fatalf("Complete = false, reasons = %v", refs.Reasons)
	}
}

func wantIncomplete(t *testing.T, refs BindingRefs, substr string) {
	t.Helper()
	if refs.Complete {
		t.Fatal("Complete = true, want false")
	}
	for _, r := range refs.Reasons {
		if strings.Contains(r, substr) {
			return
		}
	}
	t.Fatalf("reasons = %v, want one containing %q", refs.Reasons, substr)
}

func TestAnalyzeCountsCallsPerBinding(t *testing.T) {
	refs := analyze(t, `read("a"); read("b"); grep("x", "y"); print(glob("*.go"))`)
	wantComplete(t, refs)
	if refs.Calls["read"] != 2 {
		t.Fatalf("read = %d, want 2", refs.Calls["read"])
	}
	if refs.Calls["grep"] != 1 || refs.Calls["glob"] != 1 {
		t.Fatalf("calls = %v", refs.Calls)
	}
	// A binding that never appears is reported as zero, not absent, so a
	// caller can render the full set.
	if n, ok := refs.Calls["write"]; !ok || n != 0 {
		t.Fatalf("write = %d (present %v), want 0/true", n, ok)
	}
	if refs.Total() != 4 {
		t.Fatalf("total = %d, want 4", refs.Total())
	}
}

// Calls hide in every kind of nesting; the walker has to reach all of it.
func TestAnalyzeFindsCallsInNestedPositions(t *testing.T) {
	refs := analyze(t, `
		function a() { return read("1") }
		const b = () => read("2");
		const c = { m() { return read("3") }, p: read("4") };
		class D { static s = read("5"); m() { return read("6") } }
		for (const f of glob("*")) { read("7") }
		try { read("8") } catch (e) { read("9") } finally { read("10") }
		switch (read("11")) { case read("12"): read("13"); break }
		const t2 = `+"`x ${read(\"14\")}`"+`;
		const u = true ? read("15") : read("16");
		do { read("17") } while (false);
		label: { read("18") }
		const [p = read("19")] = [];
		const {q = read("20")} = {};
		new Thing(read("21"));
		[...read("22")];
	`)
	wantComplete(t, refs)
	if refs.Calls["read"] != 22 {
		t.Fatalf("read = %d, want 22 (a nesting position is unreachable)", refs.Calls["read"])
	}
}

// A property named like a binding is not the binding. Miscounting here
// would make the pre-check cry wolf on ordinary object code.
func TestAnalyzePropertyAccessIsNotABindingReference(t *testing.T) {
	refs := analyze(t, `const o = {read: 1, grep: 2}; o.read; o.grep; print(o.read)`)
	wantComplete(t, refs)
	if refs.Calls["read"] != 0 || refs.Mentions["read"] != 0 {
		t.Fatalf("read counted: calls=%d mentions=%d", refs.Calls["read"], refs.Mentions["read"])
	}
}

// Shorthand IS a reference: {read} means {read: read}.
func TestAnalyzeShorthandPropertyIsAReference(t *testing.T) {
	refs := analyze(t, `const o = {read}`)
	wantIncomplete(t, refs, "referenced without being called")
	if refs.Mentions["read"] != 1 {
		t.Fatalf("mentions = %v, want read=1", refs.Mentions)
	}
}

func TestAnalyzeOptionalCallIsAttributed(t *testing.T) {
	refs := analyze(t, `read?.("a")`)
	wantComplete(t, refs)
	if refs.Calls["read"] != 1 {
		t.Fatalf("read = %d, want 1", refs.Calls["read"])
	}
}

func TestAnalyzeAliasingIsALowerBound(t *testing.T) {
	refs := analyze(t, `const f = read; f("a"); f("b")`)
	// Statically there are zero read() call sites, but read runs twice.
	// Reporting "read x0" with Complete would be a lie.
	wantIncomplete(t, refs, "referenced without being called")
	if refs.Calls["read"] != 0 || refs.Mentions["read"] != 1 {
		t.Fatalf("calls=%v mentions=%v", refs.Calls, refs.Mentions)
	}
}

func TestAnalyzeEvasionDefeatsTheAnalysis(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"eval", `eval("read('x')")`, "reaches the global object"},
		{"Function constructor", `new Function("return read('x')")()`, "reaches the global object"},
		{"globalThis computed", `globalThis["re" + "ad"]("x")`, "reaches the global object"},
		{"globalThis dotted", `globalThis.read("x")`, "reaches the global object"},
		{"window", `window.read("x")`, "reaches the global object"},
		{"self", `self["read"]("x")`, "reaches the global object"},
		{"with", `with (Math) { read("x") }`, "rebinds names at runtime"},
		{"dynamic import", `import("./x.js")`, "dynamic `import()`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := AnalyzeBindings("t.js", tc.src, hostNames)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			wantIncomplete(t, refs, tc.want)
		})
	}
}

func TestAnalyzeShadowingDefeatsTheAnalysis(t *testing.T) {
	cases := []struct{ name, src string }{
		{"const", `const read = () => 1; read("x")`},
		{"let", `let read = 1`},
		{"var", `var read = 1`},
		{"function declaration", `function read() {}`},
		{"function parameter", `function f(read) { read("x") }`},
		{"arrow parameter", `const f = (read) => read("x")`},
		{"catch parameter", `try {} catch (read) { read("x") }`},
		{"for-of declaration", `for (const read of []) { read("x") }`},
		{"class name", `class read {}`},
		{"destructured", `const {read} = obj`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := analyze(t, tc.src)
			wantIncomplete(t, refs, "redeclared")
		})
	}
}

func TestAnalyzeParseErrorIsReported(t *testing.T) {
	if _, err := AnalyzeBindings("t.js", `read(`, hostNames); err == nil {
		t.Fatal("want a parse error")
	}
}

func TestAnalyzeReasonsAreDeduplicatedAndSorted(t *testing.T) {
	refs := analyze(t, `eval("a"); eval("b"); eval("c")`)
	if len(refs.Reasons) != 1 {
		t.Fatalf("reasons = %v, want 1 deduplicated", refs.Reasons)
	}
	refs = analyze(t, `with (o) {} ; eval("a")`)
	if len(refs.Reasons) != 2 {
		t.Fatalf("reasons = %v, want 2", refs.Reasons)
	}
	if !sortedStrings(refs.Reasons) {
		t.Fatalf("reasons not sorted: %v", refs.Reasons)
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// unknown() is the walker's fail-closed default arm. sobek's marker
// methods (_expressionNode and friends) are unexported, so a synthetic
// node type cannot be built from this package to drive a default arm
// directly. The mechanism is therefore tested here, and the WIRING is
// covered by TestAnalyzeBroadSyntaxStaysComplete below: if a node type
// this walker does not handle ever reaches it, that corpus stops being
// Complete and the test fails.
func TestUnknownNodeMarksAnalysisIncomplete(t *testing.T) {
	refs := BindingRefs{Calls: map[string]int{}, Mentions: map[string]int{}}
	w := &refWalker{names: map[string]bool{}, refs: &refs, reason: map[string]bool{}}
	w.unknown("expression", struct{ x int }{})
	if len(w.reasons) != 1 || !strings.Contains(w.reasons[0], "unrecognized") {
		t.Fatalf("reasons = %v, want an unrecognized-node reason", w.reasons)
	}
}

// The regression guard for walker coverage. Every construct here is
// ordinary JavaScript that must NOT defeat the analysis. If sobek grows a
// node type, or the walker loses an arm, some construct below lands in a
// default arm and Complete goes false — naming the node type in the
// failure. Deliberately excluded: anything that legitimately defeats
// analysis (eval, with, import, shadowing), which the tests above cover.
func TestAnalyzeBroadSyntaxStaysComplete(t *testing.T) {
	src := `
		const {a, b: {c}, ...rest} = obj;
		const [x, , y = 2, ...more] = arr;
		let n = 1; n += 2; n **= 2; n ??= 3; n ||= 4; n &&= 5;
		const s = ` + "`t ${a} ${b ?? c}`" + `;
		const tagged = String.raw` + "`raw ${a}`" + `;
		const re = /ab+c/gi;
		const seq = (1, 2, 3);
		const tern = a ? b : c;
		const opt = obj?.deep?.[a]?.(1);
		const spread = [...arr, ...more];
		const obj2 = {[a]: 1, m() {}, get g() { return 1 }, set g(v) {}, ...rest};
		function* gen() { yield 1; yield* gen() }
		async function af() { return await Promise.resolve(1) }
		const arrow = async (p = 1, ...q) => { await af() };
		const arrowExpr = v => v * 2;
		class Base {}
		class C extends Base {
			#priv = 1;
			static st = 2;
			static { const inStatic = 3 }
			constructor() { super() }
			get val() { return this.#priv }
			static sm() { return new.target }
			[a]() { return 1 }
		}
		label: for (const k in obj) { if (k) continue label; else break label }
		for (let i = 0, j = 1; i < 10; i++, j--) { void i }
		for (const v of arr) { delete obj.p; typeof v }
		while (false) {}
		do {} while (false);
		switch (a) { case 1: break; default: break }
		try { throw new Error("x") } catch { } finally { }
		try { null } catch (e) { }
		if (a) {} else if (b) {} else {}
		debugger;
		;
		read("real call");
	`
	refs, err := AnalyzeBindings("t.js", src, hostNames)
	if err != nil {
		t.Fatalf("corpus does not parse: %v", err)
	}
	if !refs.Complete {
		t.Fatalf("broad-syntax corpus went incomplete — a node type is unhandled: %v", refs.Reasons)
	}
	if refs.Calls["read"] != 1 {
		t.Fatalf("read = %d, want 1", refs.Calls["read"])
	}
}
