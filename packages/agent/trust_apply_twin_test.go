package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A trust flip has to reach four surfaces — the hook engine, the extension
// gate, the model's tool set, and its per-turn lore — and forgetting one is
// silent in both directions: nothing errors, the confirmation still prints, and
// the session simply keeps running the launch answer for whichever half was
// missed. ACP shipped a /trust verb that moved only the extension manager, and
// each of the other three read as working code.
//
// build.TrustSurfaces is that list, so the list has one home. This guard is the
// other half: a host must reach it through ApplyTrust rather than performing the
// steps itself, because a host that spells them out is a host that can spell out
// three of four.
//
// It reads sources rather than calling anything, which is the same trade
// tool_rebuild_twin_test.go makes and for the same reason — the applies are
// closures over locals of functions that spawn subprocesses before line one.

// trustHosts are the files that apply a trust flip. Adding a host means adding
// it here; the emptiness check below is what makes that unavoidable.
var trustHosts = []string{"acp_mode.go", "workspace/workspace.go", "workspace/workspace_session.go"}

func TestEveryTrustFlipGoesThroughTheSharedEvent(t *testing.T) {
	for _, file := range trustHosts {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(b)
		if !strings.Contains(src, "build.ApplyTrust(") && !strings.Contains(src, "ApplyTrust(ctx") {
			t.Errorf("%s is listed as a host that applies a trust flip but never calls ApplyTrust — either it "+
				"stopped applying trust (drop it from trustHosts) or it grew its own sequence, which is how ACP "+
				"came to move one surface out of four", file)
		}
	}
}

// The daemon is the one host whose surfaces are split across two scopes: one
// hook engine behind every session, and an extension manager per session. So it
// makes two calls, and the workspace-scoped one has to come FIRST — otherwise
// the withdrawal ordering ApplyTrust encodes is undone by the caller, with the
// hooks landing after a per-session reload that can take seconds.
//
// This is a source-order check because the thing it protects is a race window:
// asserting it by running the flip and timing a concurrent tool call would be
// exactly the flaky test that teaches people to re-run CI.
func TestTheDaemonMovesItsWorkspaceScopedSurfaceFirst(t *testing.T) {
	b, err := os.ReadFile("workspace/workspace.go")
	if err != nil {
		t.Fatalf("read workspace.go: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "func (w *Workspace) applyTrust(")
	if start < 0 {
		t.Fatal("applyTrust not found — the function this guard reads has moved or been renamed")
	}
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	shared := strings.Index(body, "build.ApplyTrust(")
	perSession := strings.Index(body, "setTrusted(")
	switch {
	case shared < 0:
		t.Error("applyTrust no longer calls build.ApplyTrust for the workspace-scoped surfaces (the hook engine)")
	case perSession < 0:
		t.Error("applyTrust no longer fans out to its sessions — if that moved, this ordering guard needs rewriting")
	case shared > perSession:
		t.Error("applyTrust flips its sessions BEFORE the workspace-scoped hook engine, which puts the hooks last " +
			"again: for the length of a session's extension reload, an untrusted repo's pre-tool-use hook is still " +
			"in the chain for any tool call a running turn makes")
	}
}

// The steps ApplyTrust performs must not ALSO be performed by a host. Each of
// these is a step that, done outside the shared event, is done in isolation from
// the other three — and its ordering relative to them becomes accidental.
//
// `except` is what separates a FLIP from a launch SEED. Every host tells its
// freshly built manager the verdict it resolved with, which is not the event
// this guard is about; the seed is recognisable because its argument is the
// Resolved's own field. Nothing else is excused.
var trustStepsOwnedByApplyTrust = []struct {
	pattern *regexp.Regexp
	except  *regexp.Regexp
	why     string
}{
	{
		pattern: regexp.MustCompile(`HookSpecsFor\(`),
		why:     "swapping hook specs is step 1 of the flip; a host doing it alone decides for itself whether the repo stops executing before or after it stops being visible, which is the ordering bug the daemon had",
	},
	{
		pattern: regexp.MustCompile(`\.SetProjectTrusted\(`),
		except:  regexp.MustCompile(`\.SetProjectTrusted\(r\.Trusted\)`),
		why:     "flipping the extension gate has to be paired with the reload that acts on it, which is what ApplyTrust does — a bare flip changes nothing until something else reloads, so it reads as applied when it is not (the TUI carried exactly this for three verbs)",
	},
}

func TestNoHostPerformsATrustStepOutsideTheSharedEvent(t *testing.T) {
	files := append([]string{"rpc.go", "cli.go", "swarm_agent.go", "modes/slash_registry.go", "modes/interactive_settings.go"}, trustHosts...)
	var checked int
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if code == "" || strings.HasPrefix(code, "//") {
				continue
			}
			for _, step := range trustStepsOwnedByApplyTrust {
				if !step.pattern.MatchString(code) {
					continue
				}
				if step.except != nil && step.except.MatchString(code) {
					checked++ // an excused launch seed still proves the pattern matches real code
					continue
				}
				t.Errorf("%s performs a trust-flip step outside build.ApplyTrust:\n    %s\n  %s", file, code, step.why)
			}
		}
	}
	// Without this the whole guard passes vacuously the day a spelling changes:
	// zero matches and zero failures look identical from the outside.
	if checked == 0 {
		t.Error("no trust-step spelling matched anywhere, not even the launch seeds every host has — the patterns " +
			"have gone stale and this guard is now checking nothing")
	}
}
