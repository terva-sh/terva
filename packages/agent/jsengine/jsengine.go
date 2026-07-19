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
	defaultMaxHostCalls   = 50
	defaultMaxOutputBytes = 32 * 1024
	defaultMaxCallStack   = 2048
)

// Binding is a host function exposed to a script by name. args are the
// call's arguments coerced to strings (a trailing undefined/null is
// dropped, so optional JS parameters map to a shorter args slice). An
// error return is thrown into the script as a catchable exception.
type Binding func(ctx context.Context, args []string) (string, error)

// Limits bounds one run. Zero values take the package defaults.
type Limits struct {
	// MaxHostCalls caps total binding invocations (default 50).
	MaxHostCalls int
	// MaxOutputBytes caps the print() buffer (default 32 KiB); output
	// past the cap is dropped and the result marked truncated.
	MaxOutputBytes int
	// MaxCallStack bounds JS recursion depth (default 2048).
	MaxCallStack int
}

// Options configures one run.
type Options struct {
	Bindings map[string]Binding
	Limits   Limits
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

	vm := sobek.New()
	vm.SetMaxCallStackSize(lim.MaxCallStack)

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

	for bname, b := range opts.Bindings {
		bname, b := bname, b
		fn := func(call sobek.FunctionCall) sobek.Value {
			if res.HostCalls >= lim.MaxHostCalls {
				panic(vm.NewGoError(fmt.Errorf("%s: host-call limit reached (%d)", bname, lim.MaxHostCalls)))
			}
			res.HostCalls++
			if cerr := ctx.Err(); cerr != nil {
				panic(vm.NewGoError(fmt.Errorf("%s: %w", bname, cerr)))
			}
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
			return vm.ToValue(text)
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
	return res, nil
}
