// Package jsengine embeds a JavaScript interpreter (grafana/sobek, the
// maintained goja fork) behind a small, capability-bound API. It is terva's
// first in-process interpreter — the shell has none (mvdan.cc/sh is used
// only as a parser for permission scoping; bash executes /bin/sh out of
// process) — so this package owns the safety discipline every consumer
// inherits: a fresh single-goroutine VM per run (sobek runtimes are not
// goroutine-safe), context-driven interruption, a call-stack bound, capped
// output, counted host calls, and a recover() boundary so an interpreter
// panic fails the run, never the process.
//
// A script's entire capability set is the bindings the caller passes: the
// VM has no filesystem, network, require, or process access unless a
// binding provides it. The interpreter is therefore NOT a security
// boundary against the model — a script can only do what its bindings can,
// and bindings are expected to re-enter the normal permission gate per
// call (reach, not authority; the same stance as ext host_tool_call).
//
// Consumers and profiles (docs/plans/jsengine-code-execution-and-workflows.md):
// the scripting profile (the code_execution tool) runs synchronous scripts
// with host-tool bindings and no determinism constraints. The workflow
// profile (the deterministic orchestration runner) arrives with the
// workflow engine and adds the promise/job pump and the clock/randomness
// bans; nothing here should grow workflow-only behavior ahead of that.
package jsengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/sobek"
)

const (
	defaultMaxHostCalls    = 50
	defaultMaxOutputBytes  = 32 * 1024
	defaultMaxCallStack    = 2048
	defaultMaxBindingBytes = 1 << 20
)

// reservedGlobals are the names the engine itself installs. A caller that
// binds one of these would silently replace the engine's own function and
// break output capture, so it is refused up front.
var reservedGlobals = map[string]bool{"print": true, "console": true}

// Binding is a host function exposed to a script by name. args are the
// call's arguments coerced to strings (a trailing undefined/null is
// dropped, so optional JS parameters map to a shorter args slice). An
// error return is thrown into the script as a catchable exception.
type Binding func(ctx context.Context, args []string) (string, error)

// TypedBinding is a host function that keeps JS types instead of
// flattening them. Its args arrive as exported Go values in call order
// (an undefined/null argument arrives as a nil element rather than being
// dropped, so a caller can tell f(undefined, 2) from f(2)), and its
// return is converted back with the runtime's usual mapping, so a map
// becomes an object and a slice an array — the script does not have to
// JSON.parse a string the host just serialized.
//
// It is the synchronous profile's counterpart to the async profile's
// RawBinding (loop.go), differing by the ctx it receives: this profile
// re-checks the context and charges the host-call budget on every call,
// which is exactly what a binding reaching a gated host tool needs.
// Prefer it for new bindings; Binding stays for the string-shaped ones.
type TypedBinding func(ctx context.Context, args []any) (any, error)

// Limits bounds one run. Zero values take the package defaults.
type Limits struct {
	// MaxHostCalls caps total binding invocations (default 50).
	MaxHostCalls int
	// MaxOutputBytes caps the print() buffer (default 32 KiB); output
	// past the cap is dropped and the result marked truncated.
	MaxOutputBytes int
	// MaxCallStack bounds JS recursion depth (default 2048).
	MaxCallStack int
	// MaxBindingBytes caps the string a single binding call may return
	// (default 1 MiB). A binding that would return more fails that call
	// with a catchable error instead of truncating, because a silently
	// shortened result is indistinguishable in-script from a genuinely
	// short one and would corrupt whatever the script computes from it.
	//
	// This is a deliberate memory bound, not tidiness. No pure-Go
	// interpreter can cap heap growth (docs/decisions/0008), so the only
	// bound available is on what a run may *import*: at most
	// MaxHostCalls x MaxBindingBytes (default 50 MiB) enters the VM
	// through bindings. It cannot bound what the script then allocates
	// itself — a script can still concatenate its way to trouble, and
	// only the context deadline stops that.
	//
	// Structured TypedBinding returns are measured only when they are
	// strings; a map or slice passes uncounted and stays bounded by
	// whatever produced it (for a host tool, its own output cap).
	MaxBindingBytes int
}

// Options configures one run.
type Options struct {
	// Bindings are string-shaped host functions.
	Bindings map[string]Binding
	// TypedBindings are host functions that keep JS types (preferred for
	// new bindings). A name may appear in Bindings or TypedBindings, not
	// both.
	TypedBindings map[string]TypedBinding
	// Globals are plain values set as globals before the script runs —
	// the synchronous profile's counterpart to AsyncOptions.Globals, and
	// the way to hand a script its input without encoding it into src.
	Globals map[string]any
	Limits  Limits
}

// Result reports what a completed (or failed) run produced.
type Result struct {
	// Output is everything the script print()ed, up to the cap.
	Output string
	// Truncated reports output dropped past MaxOutputBytes.
	Truncated bool
	// HostCalls counts binding invocations that started.
	HostCalls int
	// TimedOut reports that the run was interrupted by the context
	// deadline (as opposed to an outer cancellation or a script error).
	TimedOut bool
	Elapsed  time.Duration
}

// Check compiles src without executing it, so a caller can reject a
// syntactically invalid script with a good error before spending a VM.
func Check(name, src string) error {
	_, err := sobek.Compile(name, src, false)
	if err != nil {
		return fmt.Errorf("script does not parse: %w", err)
	}
	return nil
}

// Run executes src in a fresh VM. print(...) (and console.log) append to
// the capped output buffer; each binding call counts against MaxHostCalls
// and re-checks ctx; ctx cancellation or deadline interrupts the VM
// mid-execution. The returned Result is meaningful even when err is
// non-nil (partial output survives a timeout or script error).
func Run(ctx context.Context, name, src string, opts Options) (res Result, err error) {
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()

	if verr := validateNames(opts); verr != nil {
		return res, verr
	}

	prog, cerr := sobek.Compile(name, src, false)
	if cerr != nil {
		return res, fmt.Errorf("script does not parse: %w", cerr)
	}

	lim := opts.Limits
	if lim.MaxHostCalls <= 0 {
		lim.MaxHostCalls = defaultMaxHostCalls
	}
	if lim.MaxOutputBytes <= 0 {
		lim.MaxOutputBytes = defaultMaxOutputBytes
	}
	if lim.MaxCallStack <= 0 {
		lim.MaxCallStack = defaultMaxCallStack
	}
	if lim.MaxBindingBytes <= 0 {
		lim.MaxBindingBytes = defaultMaxBindingBytes
	}

	vm := sobek.New()
	vm.SetMaxCallStackSize(lim.MaxCallStack)

	// An unhandled promise rejection must not vanish. This profile is
	// synchronous and has no host-driven async, so a script that rejects
	// without a handler used to finish "successfully" and return whatever it
	// had printed so far — the worst failure mode for a tool whose whole job
	// is returning a small answer derived from output the caller never sees,
	// because there is nothing left to check the answer against. Node treats
	// this as fatal and the async profile already fails the run; this makes
	// the two profiles agree.
	//
	// sobek reports PromiseRejectionReject when a promise is rejected with no
	// handler, and PromiseRejectionHandle when one is attached later, so what
	// survives after the job queue drains is the genuinely unhandled set.
	// Both callbacks run on the VM goroutine, so this needs no locking.
	var unhandled []*sobek.Promise
	vm.SetPromiseRejectionTracker(func(p *sobek.Promise, op sobek.PromiseRejectionOperation) {
		switch op {
		case sobek.PromiseRejectionReject:
			unhandled = append(unhandled, p)
		case sobek.PromiseRejectionHandle:
			for i, q := range unhandled {
				if q == p {
					unhandled = append(unhandled[:i], unhandled[i+1:]...)
					break
				}
			}
		}
	})

	var out strings.Builder
	printFn := func(call sobek.FunctionCall) sobek.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.String())
		}
		line := strings.Join(parts, " ") + "\n"
		if out.Len() >= lim.MaxOutputBytes {
			res.Truncated = true
			return sobek.Undefined()
		}
		if room := lim.MaxOutputBytes - out.Len(); len(line) > room {
			out.WriteString(line[:room])
			res.Truncated = true
			return sobek.Undefined()
		}
		out.WriteString(line)
		return sobek.Undefined()
	}
	if err := vm.Set("print", printFn); err != nil {
		return res, fmt.Errorf("bind print: %w", err)
	}
	console := vm.NewObject()
	if err := console.Set("log", printFn); err != nil {
		return res, fmt.Errorf("bind console.log: %w", err)
	}
	if err := vm.Set("console", console); err != nil {
		return res, fmt.Errorf("bind console: %w", err)
	}

	for k, v := range opts.Globals {
		if err := vm.Set(k, v); err != nil {
			return res, fmt.Errorf("bind global %s: %w", k, err)
		}
	}

	// checkCall applies the preconditions every binding kind shares: the
	// host-call budget, then a fresh context check. Panicking with a JS
	// error is sobek's idiom for throwing from a Go function, so a script
	// can catch these like any other exception.
	checkCall := func(bname string) {
		if res.HostCalls >= lim.MaxHostCalls {
			panic(vm.NewGoError(fmt.Errorf("%s: host-call limit reached (%d)", bname, lim.MaxHostCalls)))
		}
		res.HostCalls++
		if cerr := ctx.Err(); cerr != nil {
			panic(vm.NewGoError(fmt.Errorf("%s: %w", bname, cerr)))
		}
	}
	// capBindingBytes refuses an oversized return rather than truncating
	// it: a shortened result looks exactly like a short one to the script,
	// so silent truncation would corrupt whatever it computes next.
	capBindingBytes := func(bname string, n int) {
		if n > lim.MaxBindingBytes {
			panic(vm.NewGoError(fmt.Errorf("%s: returned %d bytes, over the %d-byte limit for one binding call; narrow the request", bname, n, lim.MaxBindingBytes)))
		}
	}

	for bname, b := range opts.Bindings {
		bname, b := bname, b
		fn := func(call sobek.FunctionCall) sobek.Value {
			checkCall(bname)
			args := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				if sobek.IsUndefined(a) || sobek.IsNull(a) {
					continue
				}
				args = append(args, a.String())
			}
			text, berr := b(ctx, args)
			if berr != nil {
				panic(vm.NewGoError(fmt.Errorf("%s: %w", bname, berr)))
			}
			capBindingBytes(bname, len(text))
			return vm.ToValue(text)
		}
		if err := vm.Set(bname, fn); err != nil {
			return res, fmt.Errorf("bind %s: %w", bname, err)
		}
	}

	for bname, b := range opts.TypedBindings {
		bname, b := bname, b
		fn := func(call sobek.FunctionCall) sobek.Value {
			checkCall(bname)
			out, berr := b(ctx, exportArgs(call))
			if berr != nil {
				panic(vm.NewGoError(fmt.Errorf("%s: %w", bname, berr)))
			}
			if s, ok := out.(string); ok {
				capBindingBytes(bname, len(s))
			}
			return vm.ToValue(out)
		}
		if err := vm.Set(bname, fn); err != nil {
			return res, fmt.Errorf("bind %s: %w", bname, err)
		}
	}

	// Interrupt the VM when the context ends. sobek checks the interrupt
	// flag on loop back-edges and calls, so a pure spin loop is bounded by
	// the context deadline rather than running forever (the enforcement
	// the retired code-execution extension left as a TODO).
	watch := make(chan struct{})
	defer close(watch)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-watch:
		}
	}()

	// recover() converts an interpreter panic into a failed run. Thrown JS
	// exceptions (including binding errors the script didn't catch) come
	// back as *sobek.Exception errors, not panics.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("script runtime panic: %v", r)
		}
	}()
	_, rerr := vm.RunProgram(prog)
	res.Output = out.String()
	if rerr != nil {
		var ierr *sobek.InterruptedError
		if errors.As(rerr, &ierr) {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				res.TimedOut = true
				return res, fmt.Errorf("script timed out")
			}
			return res, fmt.Errorf("script interrupted: %v", ctx.Err())
		}
		return res, fmt.Errorf("script error: %w", rerr)
	}
	// RunProgram has drained the job queue by now, so anything still here was
	// never handled.
	if len(unhandled) > 0 {
		return res, fmt.Errorf("script error: %s", unhandledRejectionMessage(unhandled))
	}
	return res, nil
}

// validateNames refuses a binding or global set that would collide, before
// a run spends a VM on it. Every case here is a host wiring error, not
// anything a script can provoke — and each one fails silently if left
// alone, which is why it is worth refusing rather than resolving. A name
// bound twice makes one of the two unreachable with no error, and a
// caller who rebinds print or console breaks output capture while every
// run still reports success.
func validateNames(opts Options) error {
	for name := range opts.Bindings {
		if reservedGlobals[name] {
			return fmt.Errorf("binding %q is a name the engine installs itself", name)
		}
	}
	for name := range opts.TypedBindings {
		if reservedGlobals[name] {
			return fmt.Errorf("binding %q is a name the engine installs itself", name)
		}
		if _, dup := opts.Bindings[name]; dup {
			return fmt.Errorf("%q is declared as both a Binding and a TypedBinding", name)
		}
	}
	for name := range opts.Globals {
		if reservedGlobals[name] {
			return fmt.Errorf("global %q is a name the engine installs itself", name)
		}
		if _, dup := opts.Bindings[name]; dup {
			return fmt.Errorf("%q is declared as both a global and a binding", name)
		}
		if _, dup := opts.TypedBindings[name]; dup {
			return fmt.Errorf("%q is declared as both a global and a binding", name)
		}
	}
	return nil
}

// unhandledRejectionMessage describes the unhandled rejections a run left
// behind, naming the first reason and counting the rest.
func unhandledRejectionMessage(unhandled []*sobek.Promise) string {
	reason := "(no reason given)"
	if v := unhandled[0].Result(); v != nil {
		if s := v.String(); s != "" {
			reason = s
		}
	}
	if n := len(unhandled) - 1; n > 0 {
		return fmt.Sprintf("unhandled promise rejection: %s (and %d more)", reason, n)
	}
	return fmt.Sprintf("unhandled promise rejection: %s", reason)
}
