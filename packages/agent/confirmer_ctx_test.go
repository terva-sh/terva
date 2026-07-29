package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// core.Confirmer.Confirm takes a context, and the interface's contract is that
// an implementation which PARKS must unpark on it. That contract is load-bearing
// in a way most are not: the caller blocks inside ConfirmGate.Check, which blocks
// the turn goroutine, so a confirmer that ignores its context holds a cancelled
// turn open for as long as its own timeout allows — and, where asks are
// serialised, holds the NEXT turn's approval behind it.
//
// Before the parameter existed, four hosts each supplied that guarantee
// privately: rpc swept every parked ask on abort, the web daemon and ACP read a
// turn context back off the session, the worker runner wrapped Confirm in a
// goroutine it then raced. Threading the context let all four go — which is only
// safe if the thing that replaced them is actually observed. This is that check.
//
// It reads source rather than calling anything, because the failure it guards
// against is a confirmer that BLOCKS FOREVER: a runtime test for it either hangs
// the suite or passes on a timeout long enough to be useless.
//
// The walk is the host census's (host_census_test.go), same package, same
// skip predicate — a second walk here would be the drift that file argues
// against.

// confirmMethod matches an implementation of any of the three Confirmer shapes
// with a NAMED context. An unnamed one (`Confirm(context.Context, ...)`) cannot
// use it by definition and is reported the same way an ignored one is.
var confirmMethod = regexp.MustCompile(`func \([^)]*\) (Confirm|ConfirmWithCall|ConfirmWithRequest)\(`)

// usesCtx: the body waits on the PASSED context, checks it, or hands it to
// something that will. Passing it on is enough — that is what delegation looks
// like, and what the acp and web confirmers do.
//
// The leading [^.\w] is the whole point and cost a probe to find. Written as the
// obvious `ctx\.Done\(\)`, it also matches `s.ctx.Done()` — a context STASHED on
// the receiver, which is precisely the pattern this guard rejects. Every host
// that had this bug had it in that exact spelling, so the first draft certified
// all of them. RE2 has no lookbehind, hence the explicit character class rather
// than \b.
var usesCtx = regexp.MustCompile(`(?m)(^|[^.\w])ctx(\.Done\(\)|\.Err\(\)|\s*[,)])`)

// confirmersThatCannotPark are implementations that return without ever waiting,
// so there is nothing for a context to interrupt. Each needs a reason, because
// "this one cannot block" is a claim about the whole call path underneath it,
// and that is exactly the claim that stops being true quietly.
var confirmersThatCannotPark = map[string]string{}

func TestEveryConfirmerHonoursItsContext(t *testing.T) {
	var checked int
	for _, f := range census(t) {
		var hasDecl bool
		for _, code := range f.code {
			if confirmMethod.MatchString(code) {
				hasDecl = true
				break
			}
		}
		if !hasDecl {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoRoot, f.path))
		if err != nil {
			t.Fatalf("read %s: %v", f.path, err)
		}
		for _, decl := range confirmMethod.FindAllString(string(b), -1) {
			body := funcBody(t, string(b), decl)
			name := f.path + " " + strings.TrimPrefix(decl, "func ")
			reason, excused := confirmersThatCannotPark[name]
			switch {
			case usesCtx.MatchString(body) && excused:
				t.Errorf("%s is recorded as unable to park (%q) but does use its context — delete the entry", name, reason)
			case !usesCtx.MatchString(body) && !excused:
				t.Errorf("%s never touches the context it is given.\n"+
					"  A Confirmer that parks must select on it: the caller is blocking the turn goroutine inside "+
					"ConfirmGate.Check, so ignoring cancellation keeps a turn the user already stopped alive until "+
					"this implementation's own timeout — and, where asks are serialised, keeps the next turn's "+
					"approval queued behind a turn that no longer exists.\n"+
					"  Wait on it, pass it down, or record here why this one cannot block at all.", name)
			default:
				checked++
			}
		}
	}
	if checked == 0 {
		t.Error("no Confirmer implementation matched anywhere — the pattern has gone stale and this guard is " +
			"checking nothing")
	}
}
