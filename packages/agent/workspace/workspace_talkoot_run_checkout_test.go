package workspace

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

// 🚨 TKT-01M7Q6SX, through the daemon. A member with a worktree of its own
// has one turn at a time there. A second routine of the member that comes
// due while a run works starts no run, and the room says why. A post to the
// member waits for the run's end, and then it follows the run's report.
func TestARunAndItsMemberDoNotShareAWorktree(t *testing.T) {
	p := &runScript{reply: "Pull request 13 waits for a review."}
	cwd := talkootHome(t)
	routinesConfig(t, `{"max": 5}`)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, p.serve)
	release := p.held(t)
	w.talkootLeases = &fakeLeases{root: testsupport.TempDir(t)}
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: auto-edit\n    workspace: worktree\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(text)); err != nil {
		t.Fatal(err)
	}
	seat := helmAfterOneTurn(t, w)
	first := approved(t, w, seat, ciTask)
	second := approved(t, w, seat, talkoot.RoutineAsk{Kind: talkoot.RoutineTask, Schedule: "every 15m", Prompt: "List the open tickets."})

	// Both routines come due at one time. One starts its run, and the run
	// stays at work.
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no run made a request")
	}
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("two runs of one member work in its worktree: %d roots", n)
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "another run of helm is still at work in its checkout") {
		t.Fatalf("the room must say why the second routine started no run: %+v", skipped)
	}
	if a, b := routineOf(t, w, first), routineOf(t, w, second); a.Runs+b.Runs != 1 {
		t.Fatalf("a time that started no run must count none: %d and %d", a.Runs, b.Runs)
	}

	// A post to the member waits while the run works in its worktree.
	before := memberView(t, w, "crew", "helm").Status.Turns
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, "What is the state of the import?", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the line for the held delivery", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool {
			return l.Type == talkoot.LineGuard && l.Guard == talkoot.GuardWorking && l.Action == talkoot.ActionQueued &&
				l.Member == "helm" && strings.Contains(l.Reason, "a run of a routine of helm works in its checkout")
		})) == 1
	})
	if m := memberView(t, w, "crew", "helm"); m.Status.Working || m.Status.Turns != before {
		t.Fatalf("the member took a turn in the worktree of a run that is at work: %+v", m.Status)
	}

	// The run ends. Its report and the post both reach the member.
	release()
	waitTalkoot(t, "the report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	waitTalkoot(t, "the member's turns for the report and the post", func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns > before && !m.Status.Working
	})
	rest := strings.Join(p.rest(), "\n")
	if !strings.Contains(rest, "What is the state of the import?") {
		t.Fatal("the held post never reached the member")
	}
}
