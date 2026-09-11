package build

import (
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
)

// TicketEditWarnResetObserver is the turn boundary the ticket direct-edit
// warning has no other way to see. The tool context carries no step, and the
// ticket card refreshes on a timer, so the boundary has to arrive as an event.

const warnTicketPath = ".tickets/tickets/TKT-01M242D2VB60JWGR6ETSY9QM01.md"

// registryWithWarner returns a registry shaped like the built one: write and
// edit sharing a single warner behind a pointer.
func registryWithWarner() (core.Registry, *tools.TicketEditWarner) {
	w := &tools.TicketEditWarner{}
	w.Enable()
	return core.Registry{
		"edit":  &tools.EditTool{Tickets: w},
		"write": &tools.WriteTool{Tickets: w},
	}, w
}

// Criterion 2, and the whole point of the ticket. Step 1 is the start of a RUN,
// not of a step, so an observer gated on it would reset once per user prompt.
// A model working autonomously for thirty steps would then be warned once,
// which is the once-per-session behaviour this replaced.
func TestTicketWarnResetFiresOnEveryTurnNotOnlyStepOne(t *testing.T) {
	reg, w := registryWithWarner()
	observe := TicketEditWarnResetObserver(func() core.Registry { return reg })

	// Step 1 of a run: the warning is armed already, so it fires.
	if got := w.Notice(warnTicketPath); got == "" {
		t.Fatal("the first ticket write of a session must warn")
	}
	if got := w.Notice(warnTicketPath); got != "" {
		t.Fatalf("a second write in the same step must stay quiet, got %q", got)
	}

	// Steps 2 and up are exactly what an autonomous stretch is made of.
	for step := 2; step <= 6; step++ {
		observe(core.EvTurnStart{Step: step})
		if got := w.Notice(warnTicketPath); got == "" {
			t.Fatalf("step %d: the warning did not come back.\n"+
				"An observer gated on Step == 1 passes every test that only runs one "+
				"step and fails the model exactly where the warning is worth having.",
				step)
		}
		if got := w.Notice(warnTicketPath); got != "" {
			t.Fatalf("step %d: warned twice in one step, got %q", step, got)
		}
	}
}

// The observer must ignore everything else. EvTurnEnd is the sharp one: it is
// EvTurnStart's sibling and carries the same step, so a type switch that fell
// through to it would double every reset and warn twice in one step.
func TestTicketWarnResetIgnoresOtherEvents(t *testing.T) {
	reg, w := registryWithWarner()
	observe := TicketEditWarnResetObserver(func() core.Registry { return reg })

	if got := w.Notice(warnTicketPath); got == "" {
		t.Fatal("the first write must warn")
	}

	for _, ev := range []core.AgentEvent{
		core.EvTurnEnd{},
		core.EvDone{},
		core.EvToolUseStart{},
		core.EvAssistantStart{},
	} {
		observe(ev)
		if got := w.Notice(warnTicketPath); got != "" {
			t.Errorf("%T reset the warning, got %q. Only EvTurnStart is a turn boundary.", ev, got)
		}
	}

	observe(core.EvTurnStart{Step: 2})
	if got := w.Notice(warnTicketPath); got == "" {
		t.Error("EvTurnStart must still reset after the others were ignored")
	}
}

// The observer walks the registry on each turn rather than holding the warner,
// because /reload-ext swaps the registry mid-session and the new tools carry a
// fresh warner. A captured handle would reset an instance no tool reads, and
// the model would fall back to one warning per session with nothing to show it.
func TestTicketWarnResetFollowsARegistryRebuild(t *testing.T) {
	reg, first := registryWithWarner()
	live := reg
	observe := TicketEditWarnResetObserver(func() core.Registry { return live })

	if got := first.Notice(warnTicketPath); got == "" {
		t.Fatal("the first write must warn")
	}

	// A rebuild: a whole new registry, with a warner of its own.
	rebuilt, second := registryWithWarner()
	live = rebuilt

	if got := second.Notice(warnTicketPath); got == "" {
		t.Fatal("the rebuilt warner warns on its own first write")
	}
	if got := second.Notice(warnTicketPath); got != "" {
		t.Fatalf("and stays quiet after it, got %q", got)
	}

	observe(core.EvTurnStart{Step: 2})

	if got := second.Notice(warnTicketPath); got == "" {
		t.Error("the reset did not reach the warner the CURRENT tools hold.\n" +
			"This is what a captured handle gets wrong, and it fails silently: the " +
			"old warner is reset forever and nobody reads it.")
	}
}

// A host that passes nothing must not panic. The observer is composed
// unconditionally at three sites, so this is a real path rather than defensive
// habit.
func TestTicketWarnResetToleratesNoRegistry(t *testing.T) {
	TicketEditWarnResetObserver(nil)(core.EvTurnStart{Step: 1})

	empty := TicketEditWarnResetObserver(func() core.Registry { return nil })
	empty(core.EvTurnStart{Step: 1})

	// A registry with nothing that implements the binder, which is what --tools
	// pruning or plan mode leaves behind.
	bare := TicketEditWarnResetObserver(func() core.Registry {
		return core.Registry{"read": &tools.ReadTool{}}
	})
	bare(core.EvTurnStart{Step: 1})
}
