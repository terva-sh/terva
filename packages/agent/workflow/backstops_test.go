package workflow

// The two hard backstops in the runner: the host-side spend ceiling and the
// agent lifetime cap.
//
// Both are enforced in agentBinding under `mu`, before the concurrency semaphore
// is acquired, and neither had a test. The only budget coverage anywhere was the
// SCRIPT-side `budget.remaining()` helper in examples/workflows_test.go, which
// reads a number the host hands it and can pass with the enforcement gone.
//
// What that costs: move either check below the `sem <- struct{}{}` acquire, or
// drop the mu-guarded read while folding cost accounting into a helper, and a
// `--budget-usd 5` run happily spends $500 while a `while(true) agent(...)`
// script loses its runaway backstop — with a fully green suite either way.

import (
	"context"
	"strings"
	"testing"
)

// The ceiling is checked BEFORE the spawn, against what has already been
// billed, so a run bills up to the ceiling and then stops rather than
// overshooting by one agent: at $1 an agent under a $2 budget the first two
// spawn (spent 0, then 1 — both below 2) and the third is refused (spent 2).
//
// The refusal must also be an in-script exception, not a host abort. A workflow
// that fans out and tolerates individual failures catches it; if the host tore
// the run down instead, `budget.remaining()`-style guards in the wild would be
// unable to wind a run down gracefully.
func TestTheSpendCeilingStopsARunAtTheBudget(t *testing.T) {
	const script = `export const meta = {
  name: 'budget-backstop',
  description: 'spend past the ceiling',
  phases: [{ title: 'Spend' }],
}
const done = []
let caught = ''
for (let i = 0; i < 5; i++) {
  try {
    done.push(await agent('task ' + i))
  } catch (e) {
    caught = String(e)
    break
  }
}
return { count: done.length, caught }
`
	eng := &fakeEngine{cost: 1.0}
	opts := runOpts(t)
	opts.BudgetUSD = 2.0

	res, err := Run(context.Background(), eng, []byte(script), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	v, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", res.Value)
	}
	if got := v["count"]; got != int64(2) && got != 2.0 {
		t.Errorf("%v agents completed under a $2 budget at $1 each; want 2 — the ceiling did not stop the run", got)
	}
	if caught, _ := v["caught"].(string); !strings.Contains(caught, "budget exhausted") {
		t.Errorf("the 3rd agent did not throw a catchable budget error, got %q", caught)
	}
	if eng.spawns != 2 {
		t.Errorf("the engine was asked to spawn %d times; want 2 — the check must refuse BEFORE the spawn, "+
			"or the budget is enforced by paying for the agent that breaks it", eng.spawns)
	}
	if res.CostUSD != 2.0 {
		t.Errorf("Result.CostUSD = %v, want 2 — the run must report what it actually spent", res.CostUSD)
	}
}

// The lifetime cap is the runaway-loop backstop: a script whose termination
// condition never becomes true must stop somewhere, and that somewhere is a
// fixed count rather than a budget (a run with no BudgetUSD set skips the
// ceiling entirely — `opts.BudgetUSD > 0` guards it — so the cap is the ONLY
// thing standing between `while (true) agent(...)` and an unbounded spend).
//
// Deliberately drives the real constant rather than a test-only override: a cap
// that a test lowers is a cap whose production value nothing checks, and the
// interesting failure — someone raises agentLifetimeCap past what the host can
// survive, or moves the check where the loop can outrun it — is invisible
// against an injected one.
func TestTheAgentLifetimeCapStopsARunawayLoop(t *testing.T) {
	const script = `export const meta = {
  name: 'lifetime-backstop',
  description: 'loop forever',
  phases: [{ title: 'Loop' }],
}
let n = 0
let caught = ''
for (;;) {
  try {
    await agent('spin ' + n)
    n++
  } catch (e) {
    caught = String(e)
    break
  }
}
return { n, caught }
`
	eng := &fakeEngine{}
	opts := runOpts(t) // no BudgetUSD: the cap is the only backstop left

	res, err := Run(context.Background(), eng, []byte(script), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	v, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", res.Value)
	}
	n, _ := toInt(v["n"])
	if n != agentLifetimeCap {
		t.Errorf("a `for(;;)` script ran %d agents; want it stopped at agentLifetimeCap (%d)", n, agentLifetimeCap)
	}
	if caught, _ := v["caught"].(string); !strings.Contains(caught, "agent lifetime cap reached") {
		t.Errorf("the loop was not stopped by the cap; it ended with %q", caught)
	}
	if eng.spawns != agentLifetimeCap {
		t.Errorf("the engine spawned %d times against a cap of %d", eng.spawns, agentLifetimeCap)
	}
	if res.Agents != agentLifetimeCap {
		t.Errorf("Result.Agents = %d, want %d", res.Agents, agentLifetimeCap)
	}
}

// toInt normalises the script engine's number, which surfaces as int64 or
// float64 depending on how the value was produced.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}
