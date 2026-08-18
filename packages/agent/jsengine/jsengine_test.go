package jsengine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunPrintAndBindings(t *testing.T) {
	echo := func(_ context.Context, args []string) (string, error) {
		return strings.Join(args, "|"), nil
	}
	res, err := Run(context.Background(), "t.js",
		`const a = echo("x", "y"); print("got", a); console.log("also", 42)`,
		Options{Bindings: map[string]Binding{"echo": echo}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "got x|y\nalso 42\n"; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
	if res.HostCalls != 1 {
		t.Fatalf("host calls = %d, want 1", res.HostCalls)
	}
}

func TestRunOptionalArgsDropped(t *testing.T) {
	var got []string
	rec := func(_ context.Context, args []string) (string, error) {
		got = args
		return "", nil
	}
	if _, err := Run(context.Background(), "t.js", `rec("only", undefined, null)`,
		Options{Bindings: map[string]Binding{"rec": rec}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 1 || got[0] != "only" {
		t.Fatalf("args = %#v, want [only]", got)
	}
}

func TestRunTimeoutInterruptsSpinLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := Run(ctx, "t.js", `print("before"); for (;;) {}`, Options{})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("interrupt took %v — watchdog not working", time.Since(start))
	}
	if res.Output != "before\n" {
		t.Fatalf("partial output lost: %q", res.Output)
	}
}

func TestRunOutputCap(t *testing.T) {
	res, err := Run(context.Background(), "t.js",
		`for (let i = 0; i < 100; i++) print("x".repeat(100))`,
		Options{Limits: Limits{MaxOutputBytes: 500}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Fatal("expected truncation")
	}
	if len(res.Output) > 500 {
		t.Fatalf("output %d bytes exceeds cap", len(res.Output))
	}
}

func TestRunHostCallLimit(t *testing.T) {
	noop := func(_ context.Context, _ []string) (string, error) { return "", nil }
	res, err := Run(context.Background(), "t.js",
		`for (let i = 0; i < 10; i++) noop()`,
		Options{Bindings: map[string]Binding{"noop": noop}, Limits: Limits{MaxHostCalls: 3}})
	if err == nil || !strings.Contains(err.Error(), "host-call limit") {
		t.Fatalf("err = %v, want host-call limit", err)
	}
	if res.HostCalls != 3 {
		t.Fatalf("host calls = %d, want 3", res.HostCalls)
	}
}

func TestRunBindingErrorIsCatchable(t *testing.T) {
	fail := func(_ context.Context, _ []string) (string, error) {
		return "", fmt.Errorf("boom")
	}
	res, err := Run(context.Background(), "t.js",
		`try { fail() } catch (e) { print("caught:", String(e).includes("boom")) }`,
		Options{Bindings: map[string]Binding{"fail": fail}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "caught: true\n"; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
}

func TestRunUncaughtBindingErrorFailsRun(t *testing.T) {
	fail := func(_ context.Context, _ []string) (string, error) {
		return "", fmt.Errorf("boom")
	}
	_, err := Run(context.Background(), "t.js", `fail()`,
		Options{Bindings: map[string]Binding{"fail": fail}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestRunStackOverflowIsAnError(t *testing.T) {
	_, err := Run(context.Background(), "t.js", `function f() { return f() }; f()`,
		Options{Limits: Limits{MaxCallStack: 64}})
	if err == nil {
		t.Fatal("expected a stack error, not a crash")
	}
}

// A rejection nothing handles must fail the run rather than passing for a
// clean one. Node treats this as fatal and the async profile already does;
// before this, the script "succeeded" and returned its partial output.
func TestRunUnhandledRejectionFailsRun(t *testing.T) {
	res, err := Run(context.Background(), "t.js",
		`print("before"); Promise.reject(new Error("boom"))`, Options{})
	if err == nil {
		t.Fatal("err = nil, want the unhandled rejection to fail the run")
	}
	if !strings.Contains(err.Error(), "unhandled promise rejection") {
		t.Fatalf("err = %v, want it to name the unhandled rejection", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to carry the rejection reason", err)
	}
	// Partial output still comes back, as it does for every other failure.
	if !strings.Contains(res.Output, "before") {
		t.Fatalf("output = %q, want the pre-failure output preserved", res.Output)
	}
}

// The mirror image: a handled rejection is ordinary control flow and must
// not fail the run. This is what keeps the check from breaking scripts that
// use promises correctly.
func TestRunHandledRejectionDoesNotFailRun(t *testing.T) {
	res, err := Run(context.Background(), "t.js",
		`Promise.reject(new Error("boom")).catch(e => print("caught", e.message))`,
		Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "caught boom\n"; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
}

// A handler attached after the rejection still counts as handled: sobek
// reports that late attachment as PromiseRejectionHandle, and the tracker
// has to retract its earlier suspicion rather than accumulating it.
func TestRunLateAttachedHandlerClearsRejection(t *testing.T) {
	_, err := Run(context.Background(), "t.js", `
		const p = Promise.reject(new Error("boom"));
		Promise.resolve().then(() => p.catch(() => print("handled late")));
	`, Options{})
	if err != nil {
		t.Fatalf("run: %v, want the late handler to clear the rejection", err)
	}
}

// A rejection thrown out of an async function is the shape a model is most
// likely to write by accident, and it must be reported like any other.
func TestRunUnhandledRejectionFromAsyncFunction(t *testing.T) {
	_, err := Run(context.Background(), "t.js",
		`(async () => { throw new Error("boom") })()`, Options{})
	if err == nil || !strings.Contains(err.Error(), "unhandled promise rejection") {
		t.Fatalf("err = %v, want the unhandled rejection reported", err)
	}
}

// An uncaught binding error already fails the run through the exception
// path; awaiting one must not now be reported as a *rejection* instead,
// which would change an existing error message out from under callers.
func TestRunUncaughtBindingErrorStillReportsAsScriptError(t *testing.T) {
	fail := func(_ context.Context, _ []string) (string, error) {
		return "", fmt.Errorf("binding blew up")
	}
	_, err := Run(context.Background(), "t.js", `fail()`,
		Options{Bindings: map[string]Binding{"fail": fail}})
	if err == nil || !strings.Contains(err.Error(), "binding blew up") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "unhandled promise rejection") {
		t.Fatalf("err = %v, want the synchronous exception path, not the rejection path", err)
	}
}

func TestCheckSyntaxError(t *testing.T) {
	if err := Check("t.js", `const = broken(`); err == nil {
		t.Fatal("expected a parse error")
	}
	if err := Check("t.js", `print("fine")`); err != nil {
		t.Fatalf("valid script rejected: %v", err)
	}
}

func TestRunTypedBindingKeepsTypes(t *testing.T) {
	var got []any
	rec := func(_ context.Context, args []any) (any, error) {
		got = args
		return map[string]any{"n": 7, "list": []any{"a", "b"}}, nil
	}
	res, err := Run(context.Background(), "t.js",
		`const r = rec(42, {k: "v"}, [1, 2]); print(r.n, r.list[1], r.list.length)`,
		Options{TypedBindings: map[string]TypedBinding{"rec": rec}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The return crossed back as a real object, not a string the script
	// had to parse.
	if want := "7 b 2\n"; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
	if len(got) != 3 {
		t.Fatalf("args = %#v, want 3", got)
	}
	if s := fmt.Sprintf("%v", got[0]); s != "42" {
		t.Fatalf("args[0] = %v (%T), want 42", got[0], got[0])
	}
	obj, ok := got[1].(map[string]any)
	if !ok || obj["k"] != "v" {
		t.Fatalf("args[1] = %#v, want map with k=v", got[1])
	}
	if _, ok := got[2].([]any); !ok {
		t.Fatalf("args[2] = %#v (%T), want a slice", got[2], got[2])
	}
}

// A typed binding keeps argument POSITIONS, where a string Binding drops a
// trailing undefined/null (TestRunOptionalArgsDropped). The two profiles
// differ deliberately: a typed binding can be told f(undefined, 2) from
// f(2), which is what makes optional middle parameters expressible.
func TestRunTypedBindingKeepsArgumentPositions(t *testing.T) {
	var got []any
	rec := func(_ context.Context, args []any) (any, error) {
		got = args
		return nil, nil
	}
	if _, err := Run(context.Background(), "t.js", `rec("only", undefined, null)`,
		Options{TypedBindings: map[string]TypedBinding{"rec": rec}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("args = %#v, want 3 (positions preserved)", got)
	}
	if got[0] != "only" || got[1] != nil || got[2] != nil {
		t.Fatalf("args = %#v, want [only nil nil]", got)
	}
}

func TestRunGlobalsAreVisibleToTheScript(t *testing.T) {
	res, err := Run(context.Background(), "t.js", `print(input.name, input.count + 1)`,
		Options{Globals: map[string]any{"input": map[string]any{"name": "x", "count": 2}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "x 3\n"; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
}

func TestRunTypedBindingChargesTheHostCallBudget(t *testing.T) {
	noop := func(_ context.Context, _ []any) (any, error) { return nil, nil }
	res, err := Run(context.Background(), "t.js",
		`try { for (let i = 0; i < 10; i++) noop() } catch (e) { print("stopped:", e.message) }`,
		Options{
			TypedBindings: map[string]TypedBinding{"noop": noop},
			Limits:        Limits{MaxHostCalls: 3},
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.HostCalls != 3 {
		t.Fatalf("host calls = %d, want 3", res.HostCalls)
	}
	if !strings.Contains(res.Output, "host-call limit reached") {
		t.Fatalf("output = %q, want the limit error", res.Output)
	}
}

func TestRunBindingReturnByteCap(t *testing.T) {
	big := func(_ context.Context, _ []string) (string, error) {
		return "0123456789", nil
	}
	res, err := Run(context.Background(), "t.js",
		`try { big() } catch (e) { print("caught:", e.message) }`,
		Options{
			Bindings: map[string]Binding{"big": big},
			Limits:   Limits{MaxBindingBytes: 8},
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Refused, not truncated, and catchable in-script.
	if !strings.Contains(res.Output, "returned 10 bytes") ||
		!strings.Contains(res.Output, "8-byte limit") {
		t.Fatalf("output = %q, want the byte-cap error", res.Output)
	}
}

func TestRunUncaughtByteCapFailsTheRun(t *testing.T) {
	big := func(_ context.Context, _ []string) (string, error) { return "0123456789", nil }
	_, err := Run(context.Background(), "t.js", `big()`,
		Options{
			Bindings: map[string]Binding{"big": big},
			Limits:   Limits{MaxBindingBytes: 4},
		})
	if err == nil {
		t.Fatal("want an error when the cap is not caught")
	}
	if !strings.Contains(err.Error(), "4-byte limit") {
		t.Fatalf("err = %v, want the byte-cap error", err)
	}
}

// The cap measures strings only. A structured return passes uncounted —
// a real gap, pinned here so it is a known behaviour rather than a
// surprise: such a value stays bounded by whatever produced it.
func TestRunByteCapDoesNotMeasureStructuredReturns(t *testing.T) {
	wide := func(_ context.Context, _ []any) (any, error) {
		return map[string]any{"a": "0123456789", "b": "0123456789"}, nil
	}
	res, err := Run(context.Background(), "t.js", `print(wide().a)`,
		Options{
			TypedBindings: map[string]TypedBinding{"wide": wide},
			Limits:        Limits{MaxBindingBytes: 4},
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "0123456789\n"; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
}

func TestRunRefusesCollidingNames(t *testing.T) {
	strBind := func(_ context.Context, _ []string) (string, error) { return "", nil }
	typBind := func(_ context.Context, _ []any) (any, error) { return nil, nil }
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"reserved binding", Options{Bindings: map[string]Binding{"print": strBind}}, "engine installs itself"},
		{"reserved global", Options{Globals: map[string]any{"console": 1}}, "engine installs itself"},
		{"binding declared twice", Options{
			Bindings:      map[string]Binding{"f": strBind},
			TypedBindings: map[string]TypedBinding{"f": typBind},
		}, "both a Binding and a TypedBinding"},
		{"global shadows a binding", Options{
			Bindings: map[string]Binding{"f": strBind},
			Globals:  map[string]any{"f": 1},
		}, "both a global and a binding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Run(context.Background(), "t.js", `print(1)`, tc.opts)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
