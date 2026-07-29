package agent

import (
	"regexp"
	"testing"
)

// A model swap has to reach four things — the usage snapshot carried onto the
// new client, the client and model themselves, terva_status's provider
// identity, and every host-routed dispatch tool — and missing one is silent in
// all four directions. The meters blank; the model reports the wrong provider
// and loses its context-window size; a sub-agent spawned next runs on the
// pre-swap model. Nothing errors and no test fails.
//
// Four hosts performed this sequence and each spelled it out. Two of them
// carried four separate comments naming the file they were copied from — which
// is what a codebase writes when it has no way to say "these are the same
// event". They had already drifted: only the daemon re-pointed the dispatch
// tools, which was harmless solely because all three such tools happen to be
// constructed in workspace_session.go, a fact nothing recorded.
//
// build.ModelSwap is that list, and build.ApplyModelSwap the order. This guard
// is the other half: a host must reach it through the event rather than
// performing the steps itself, because a host that spells them out is a host
// that can spell out three of four.
//
// It reads sources — see host_census_test.go, whose walk it reuses — because
// three of the four swaps are closures over locals inside functions that
// resolve credentials and open network clients before line one.

// Each step is matched only in CALL position (dot-prefixed), so the
// declarations in core, provider and tools are not mistaken for uses.
var modelSwapStepsOwnedByTheEvent = []struct {
	pattern *regexp.Regexp
	except  *regexp.Regexp
	why     string
}{
	{
		pattern: regexp.MustCompile(`\.SeedClientUsage\(`),
		why: "carrying the usage snapshot is step 1, and its ORDER is the one hard constraint in the event: " +
			"seed the new client before installing it, or Agent.Usage() reads the fresh client's empty snapshot " +
			"and the status meters blank until the next turn's headers arrive",
	},
	{
		pattern: regexp.MustCompile(`\.SetClientAndModel\(`),
		except:  regexp.MustCompile(`Swap:\s*func\(`),
		why: "installing the client is the step every host got right, which is exactly why doing it alone is " +
			"the dangerous one — it looks like the whole swap and is three quarters of it",
	},
	{
		pattern: regexp.MustCompile(`\.SetHost\(`),
		why: "re-pointing the host-routed dispatch tools is the step three hosts out of four omitted; a " +
			"sub-agent spawned after the swap otherwise inherits the pre-swap route",
	},
}

// swapStepHome is where these steps legitimately live: the event, and the one
// host whose assignment genuinely cannot go through it.
var swapStepHome = map[string]string{
	"packages/agent/build/modelswap.go": "build.ApplyModelSwap itself",
	"packages/agent/chat/loop.go": "bot mode's fan-out: one swap has to reach every per-chat agent, so the " +
		"Loop owns the assignment and receives it through ModelSwap.Swap. It performs ONLY that step — the " +
		"other three still run in the event, against loop.Agent",
}

func TestNoHostPerformsAModelSwapStepOutsideTheEvent(t *testing.T) {
	var checked int
	for _, f := range census(t) {
		if _, ok := swapStepHome[f.path]; ok {
			continue
		}
		for _, code := range f.code {
			for _, step := range modelSwapStepsOwnedByTheEvent {
				if !step.pattern.MatchString(code) {
					continue
				}
				if step.except != nil && step.except.MatchString(code) {
					checked++ // an excused host assignment still proves the pattern matches real code
					continue
				}
				t.Errorf("%s performs a model-swap step outside build.ApplyModelSwap:\n    %s\n  %s", f.path, code, step.why)
			}
		}
	}
	if checked == 0 {
		t.Error("no model-swap step matched anywhere, not even the one excused host assignment — the " +
			"patterns have gone stale and this guard is now checking nothing")
	}
}

// The other direction: every host that swaps a model must reach the event. A
// named list is the only thing that can fail the day one of them stops calling
// it, because to a census a host that never swapped and one that quietly
// stopped look identical.
var modelSwapHosts = map[string]string{
	"packages/agent/workspace/workspace.go": "the daemon's models.switch (the original this event was lifted from)",
	"packages/agent/acp_mode.go":            "the acp factory's SwitchModel, applied by the acp session through ModelSwitch.Apply",
	"packages/agent/botcmd.go":              "bot mode's RefreshCreds re-resolve",
	"packages/agent/cli.go":                 "resume onto a session's stored model",
}

func TestEveryModelSwapHostReachesTheEvent(t *testing.T) {
	seen := map[string]bool{}
	call := regexp.MustCompile(`ApplyModelSwap\(`)
	for _, f := range census(t) {
		if f.path == "packages/agent/build/modelswap.go" {
			continue
		}
		for _, code := range f.code {
			if call.MatchString(code) {
				seen[f.path] = true
			}
		}
	}
	for path, what := range modelSwapHosts {
		if !seen[path] {
			t.Errorf("%s no longer reaches build.ApplyModelSwap (%s) — either it stopped swapping models "+
				"(drop it from modelSwapHosts) or it grew its own sequence, which is how the four copies "+
				"came to disagree in the first place", path, what)
		}
	}
	for path := range seen {
		if _, listed := modelSwapHosts[path]; !listed {
			t.Errorf("%s swaps a model and is not in modelSwapHosts — add it, so that the day it stops, "+
				"something fails", path)
		}
	}
}
