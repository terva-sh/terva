package jsengine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/grafana/sobek"
)

// This file is the ASYNC (workflow) profile of the engine: a script runs
// on one goroutine that owns the VM, host operations run on their own
// goroutines and settle promises by posting callbacks back to the VM
// goroutine, and the run ends when the script's top-level promise
// settles. That single-owner discipline is what makes `Promise.all` over
// N agent spawns genuinely concurrent without ever touching the VM from
// two goroutines (sobek runtimes are not goroutine-safe).
//
// The scripting profile (Run, jsengine.go) stays synchronous — nothing
// here changes it.

// AsyncBinding is a host operation exposed to a script as a
// promise-returning function. It runs OFF the VM goroutine; its args are
// the call's arguments exported to plain Go values on the VM goroutine
// before handoff (never sobek.Values — those are runtime-bound). An error
// return rejects the promise (catchable in-script).
type AsyncBinding func(ctx context.Context, args []any) (any, error)

// RawBinding is a synchronous host function for the async profile,
// receiving exported Go values (unlike Binding's string coercion).
type RawBinding func(args []any) (any, error)

// AsyncOptions configures one async run.
type AsyncOptions struct {
	// Bindings are synchronous host functions (log, phase, budget reads).
	Bindings map[string]RawBinding
	// AsyncBindings are promise-returning host operations (agent).
	AsyncBindings map[string]AsyncBinding
	// Globals are plain values set as globals (args).
	Globals map[string]any
	// Prelude is JS evaluated before the body — pure-JS helpers
	// (pipeline/parallel) that need no host round-trip.
	Prelude string
	// WithholdNondeterminism replaces Date and Math.random with throwing
	// stubs (and CheckDeterminism should have run first): a resumable
	// script must re-derive identical host calls on replay.
	WithholdNondeterminism bool
	Limits                 Limits
}

// AsyncResult reports a completed (or failed) async run.
type AsyncResult struct {
	// Value is the script's return value, exported to plain Go values.
	Value    any
	TimedOut bool
	Elapsed  time.Duration
}

var nondetPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bDate\s*\.\s*now\s*\(`),
	regexp.MustCompile(`\bMath\s*\.\s*random\s*\(`),
	regexp.MustCompile(`\bnew\s+Date\s*\(\s*\)`),
}

// CheckDeterminism flags wall-clock/randomness use before a run — a
// pattern scan, not an AST walk, so a string literal spelling
// "Date.now()" false-positives; workflow scripts have no business
// containing that spelling either way. The runtime stubs are the real
// enforcement; this exists for a good error before any agent spawns.
func CheckDeterminism(src string) error {
	for _, p := range nondetPatterns {
		if loc := p.FindString(src); loc != "" {
			return fmt.Errorf("script uses %q — Date.now()/Math.random()/new Date() are unavailable in workflow scripts (they break resume); pass timestamps via args and vary prompts by index for diversity", strings.TrimSpace(loc))
		}
	}
	return nil
}

const nondetStubs = `
Date = function () { throw new Error("Date is unavailable in workflow scripts (breaks resume); pass timestamps via args") };
Date.now = Date;
Math.random = function () { throw new Error("Math.random is unavailable in workflow scripts (breaks resume); vary prompts by index instead") };
`

// RunAsync executes src (wrapped in an async function, so top-level await
// and top-level return both work) and pumps host-operation completions
// until the script's promise settles. ctx cancellation interrupts
// running JS and aborts the pump; outstanding host operations see the
// same ctx.
func RunAsync(ctx context.Context, name, src string, opts AsyncOptions) (res AsyncResult, err error) {
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()

	prog, cerr := sobek.Compile(name, "(async () => {\n"+src+"\n})()", false)
	if cerr != nil {
		return res, fmt.Errorf("script does not parse: %w", cerr)
	}

	lim := opts.Limits
	if lim.MaxCallStack <= 0 {
		lim.MaxCallStack = defaultMaxCallStack
	}
	vm := sobek.New()
	vm.SetMaxCallStackSize(lim.MaxCallStack)

	for k, v := range opts.Globals {
		if err := vm.Set(k, v); err != nil {
			return res, fmt.Errorf("bind global %s: %w", k, err)
		}
	}
	for bname, b := range opts.Bindings {
		bname, b := bname, b
		fn := func(call sobek.FunctionCall) sobek.Value {
			args := exportArgs(call)
			out, berr := b(args)
			if berr != nil {
				panic(vm.NewGoError(fmt.Errorf("%s: %w", bname, berr)))
			}
			return vm.ToValue(out)
		}
		if err := vm.Set(bname, fn); err != nil {
			return res, fmt.Errorf("bind %s: %w", bname, err)
		}
	}

	// The pump: host completions post VM-goroutine callbacks here.
	ops := make(chan func(), 256)
	pending := 0
	for bname, ab := range opts.AsyncBindings {
		bname, ab := bname, ab
		fn := func(call sobek.FunctionCall) sobek.Value {
			p, resolve, reject := vm.NewPromise()
			args := exportArgs(call)
			pending++
			go func() {
				out, berr := ab(ctx, args)
				ops <- func() {
					pending--
					if berr != nil {
						reject(vm.ToValue(fmt.Sprintf("%s: %v", bname, berr)))
						return
					}
					resolve(vm.ToValue(out))
				}
			}()
			return vm.ToValue(p)
		}
		if err := vm.Set(bname, fn); err != nil {
			return res, fmt.Errorf("bind %s: %w", bname, err)
		}
	}

	if opts.Prelude != "" {
		if _, perr := vm.RunString(opts.Prelude); perr != nil {
			return res, fmt.Errorf("prelude: %w", perr)
		}
	}
	if opts.WithholdNondeterminism {
		if _, serr := vm.RunString(nondetStubs); serr != nil {
			return res, fmt.Errorf("nondeterminism stubs: %w", serr)
		}
	}

	// Interrupt running JS when ctx ends (a spin loop dies at the
	// deadline); the pump select below handles the not-running-JS case.
	watch := make(chan struct{})
	defer close(watch)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-watch:
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("script runtime panic: %v", r)
		}
	}()

	v, rerr := vm.RunProgram(prog)
	if rerr != nil {
		return res, classifyAsyncErr(ctx, rerr, &res)
	}
	p, ok := v.Export().(*sobek.Promise)
	if !ok {
		res.Value = v.Export()
		return res, nil
	}
	for p.State() == sobek.PromiseStatePending {
		if pending == 0 {
			return res, errors.New("workflow deadlock: the script is awaiting but no host operation is outstanding (a promise that never settles?)")
		}
		select {
		case fn := <-ops:
			fn()
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				res.TimedOut = true
				return res, errors.New("workflow timed out awaiting host operations")
			}
			return res, fmt.Errorf("workflow interrupted: %v", ctx.Err())
		}
	}
	if p.State() == sobek.PromiseStateRejected {
		return res, fmt.Errorf("script error: %s", p.Result().String())
	}
	res.Value = p.Result().Export()
	return res, nil
}

func exportArgs(call sobek.FunctionCall) []any {
	args := make([]any, 0, len(call.Arguments))
	for _, a := range call.Arguments {
		if sobek.IsUndefined(a) || sobek.IsNull(a) {
			args = append(args, nil)
			continue
		}
		args = append(args, a.Export())
	}
	return args
}

func classifyAsyncErr(ctx context.Context, rerr error, res *AsyncResult) error {
	var ierr *sobek.InterruptedError
	if errors.As(rerr, &ierr) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
			return errors.New("script timed out")
		}
		return fmt.Errorf("script interrupted: %v", ctx.Err())
	}
	return fmt.Errorf("script error: %w", rerr)
}
