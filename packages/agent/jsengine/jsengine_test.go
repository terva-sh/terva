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

func TestCheckSyntaxError(t *testing.T) {
	if err := Check("t.js", `const = broken(`); err == nil {
		t.Fatal("expected a parse error")
	}
	if err := Check("t.js", `print("fine")`); err != nil {
		t.Fatalf("valid script rejected: %v", err)
	}
}
