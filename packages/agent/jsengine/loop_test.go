package jsengine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunAsyncAwaitBinding(t *testing.T) {
	double := func(_ context.Context, args []any) (any, error) {
		n, _ := args[0].(int64)
		return n * 2, nil
	}
	res, err := RunAsync(context.Background(), "t.js",
		`const a = await double(21); return a`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"double": double}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(res.Value) != "42" {
		t.Fatalf("value = %#v, want 42", res.Value)
	}
}

func TestRunAsyncParallelIsConcurrent(t *testing.T) {
	var inflight, peak atomic.Int64
	slow := func(_ context.Context, args []any) (any, error) {
		cur := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		return args[0], nil
	}
	start := time.Now()
	res, err := RunAsync(context.Background(), "t.js",
		`const out = await Promise.all([slow(1), slow(2), slow(3), slow(4)]); return out`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"slow": slow}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Promise.all took %v — host ops are serialized", elapsed)
	}
	if peak.Load() < 2 {
		t.Fatalf("peak in-flight = %d, want concurrency", peak.Load())
	}
	if arr, ok := res.Value.([]any); !ok || len(arr) != 4 {
		t.Fatalf("value = %#v, want 4 results", res.Value)
	}
}

func TestRunAsyncRejectionIsCatchable(t *testing.T) {
	fail := func(_ context.Context, _ []any) (any, error) {
		return nil, fmt.Errorf("boom")
	}
	res, err := RunAsync(context.Background(), "t.js",
		`try { await fail() } catch (e) { return "caught:" + String(e).includes("boom") }`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"fail": fail}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Value != "caught:true" {
		t.Fatalf("value = %#v", res.Value)
	}
}

func TestRunAsyncUncaughtRejectionFailsRun(t *testing.T) {
	fail := func(_ context.Context, _ []any) (any, error) {
		return nil, fmt.Errorf("boom")
	}
	_, err := RunAsync(context.Background(), "t.js", `await fail()`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"fail": fail}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

// A panicking AsyncBinding must fail the RUN, not the PROCESS. The binding
// runs off the VM goroutine, so RunAsync's own recover() cannot see it, and
// before callAsyncBinding existed this test did not fail — it killed the
// test binary outright, taking every other test in the package with it.
func TestRunAsyncBindingPanicFailsRunNotProcess(t *testing.T) {
	boom := func(_ context.Context, _ []any) (any, error) {
		panic("host binding exploded")
	}
	_, err := RunAsync(context.Background(), "t.js", `await boom()`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"boom": boom}})
	if err == nil {
		t.Fatal("err = nil, want the panic surfaced as a failed run")
	}
	if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "host binding exploded") {
		t.Fatalf("err = %v, want it to name the panic and its value", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to name the binding", err)
	}
}

// The same panic, caught in-script: a panicking host operation rejects its
// promise like any other failure, so a script may handle it.
func TestRunAsyncBindingPanicIsCatchable(t *testing.T) {
	boom := func(_ context.Context, _ []any) (any, error) {
		panic("host binding exploded")
	}
	res, err := RunAsync(context.Background(), "t.js",
		`try { await boom() } catch (e) { return "caught:" + String(e).includes("exploded") }`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"boom": boom}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Value != "caught:true" {
		t.Fatalf("value = %#v", res.Value)
	}
}

// A panic in ONE branch of a fan-out must not sink the siblings: the others
// still settle and the script can filter, which is how parallel() behaves
// for ordinary failures.
func TestRunAsyncBindingPanicDoesNotSinkSiblings(t *testing.T) {
	maybe := func(_ context.Context, args []any) (any, error) {
		if n, _ := args[0].(int64); n == 2 {
			panic("only the second one explodes")
		}
		return args[0], nil
	}
	res, err := RunAsync(context.Background(), "t.js",
		`const out = await Promise.all([1,2,3].map(n => maybe(n).catch(() => null))); return out`,
		AsyncOptions{AsyncBindings: map[string]AsyncBinding{"maybe": maybe}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	arr, ok := res.Value.([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("value = %#v, want 3 results", res.Value)
	}
	if arr[0] == nil || arr[2] == nil {
		t.Fatalf("value = %#v, want the surviving siblings to hold values", res.Value)
	}
	if arr[1] != nil {
		t.Fatalf("value = %#v, want the panicking branch to be null", res.Value)
	}
}

func TestRunAsyncDeadlockDetected(t *testing.T) {
	_, err := RunAsync(context.Background(), "t.js",
		`await new Promise(() => {}); return 1`, AsyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "deadlock") {
		t.Fatalf("err = %v, want deadlock detection", err)
	}
}

func TestRunAsyncSyncBindingsAndGlobals(t *testing.T) {
	var logs []string
	logFn := func(args []any) (any, error) {
		logs = append(logs, fmt.Sprint(args[0]))
		return nil, nil
	}
	res, err := RunAsync(context.Background(), "t.js",
		`log("seen " + args.n); return args.n + 1`,
		AsyncOptions{
			Bindings: map[string]RawBinding{"log": logFn},
			Globals:  map[string]any{"args": map[string]any{"n": int64(41)}},
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(res.Value) != "42" || len(logs) != 1 || logs[0] != "seen 41" {
		t.Fatalf("value = %#v logs = %v", res.Value, logs)
	}
}

func TestRunAsyncNondeterminismStubsThrow(t *testing.T) {
	res, err := RunAsync(context.Background(), "t.js",
		`try { Date.now() } catch (e) { return String(e) }`,
		AsyncOptions{WithholdNondeterminism: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s, _ := res.Value.(string); !strings.Contains(s, "unavailable") {
		t.Fatalf("value = %#v, want the unavailable error", res.Value)
	}
}

func TestRunAsyncTimeoutInterruptsSpin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := RunAsync(ctx, "t.js", `for (;;) {}`, AsyncOptions{})
	if err == nil || !res.TimedOut {
		t.Fatalf("err = %v timedOut = %v", err, res.TimedOut)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("interrupt too slow")
	}
}

func TestCheckDeterminism(t *testing.T) {
	if err := CheckDeterminism(`const t = Date.now()`); err == nil {
		t.Fatal("Date.now must be flagged")
	}
	if err := CheckDeterminism(`agent("count files")`); err != nil {
		t.Fatalf("clean script flagged: %v", err)
	}
}
