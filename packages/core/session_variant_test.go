package core

import (
	"os"
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func variantMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

// walkTail walks a session file and returns its effective transcript plus the
// tail-span variant state reported via onTail.
func walkTail(t *testing.T, path string) (eff []provider.Message, tailStart int, takes [][]provider.Message, active int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	eff, err = walkSession(f, &loadReport{}, sessionWalkHooks{
		onTail: func(ts int, tk [][]provider.Message, ac int) { tailStart, takes, active = ts, tk, ac },
	})
	if err != nil {
		t.Fatal(err)
	}
	return eff, tailStart, takes, active
}

// TestRetractKeepsVariants proves regenerate-with-preservation: retracting the
// current response sets it aside as a swipeable take (reconstructed from the
// original message rows — no byte duplication) while a new take becomes active.
func TestRetractKeepsVariants(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(variantMsg(provider.RoleUser, "u0")))
	must(s.AppendMessage(variantMsg(provider.RoleAssistant, "a0")))
	// Regenerate: set the response at index 1 aside, generate a new take.
	must(s.AppendAmend(AmendRetract, 1, nil, "retry"))
	must(s.AppendMessage(variantMsg(provider.RoleAssistant, "a1")))
	path := s.Path
	_ = s.Close()

	eff, tailStart, takes, active := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"u0", "a1"}) {
		t.Errorf("effective = %v, want [u0 a1]", got)
	}
	if tailStart != 1 || active != 1 || len(takes) != 2 {
		t.Fatalf("tail state: start=%d active=%d takes=%d, want 1/1/2", tailStart, active, len(takes))
	}
	if walkMsgTexts(takes[0])[0] != "a0" || walkMsgTexts(takes[1])[0] != "a1" {
		t.Errorf("takes = [%v %v], want [[a0] [a1]]", walkMsgTexts(takes[0]), walkMsgTexts(takes[1]))
	}
}

// TestSelectSwipesVariants proves swipe: selecting a stored take restores it as
// the active response while KEEPING the other take — and the choice survives a
// reload (OpenSession, which does not track tails, still replays select for a
// correct transcript).
func TestSelectSwipesVariants(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(variantMsg(provider.RoleUser, "u0")))
	must(s.AppendMessage(variantMsg(provider.RoleAssistant, "a0")))
	must(s.AppendAmend(AmendRetract, 1, nil, "retry"))
	must(s.AppendMessage(variantMsg(provider.RoleAssistant, "a1")))
	// Swipe back to take 0 (the original a0).
	must(s.AppendSelect(1, 0, "swipe"))
	path := s.Path
	_ = s.Close()

	eff, tailStart, takes, active := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("effective after swipe = %v, want [u0 a0]", got)
	}
	if tailStart != 1 || active != 0 || len(takes) != 2 {
		t.Fatalf("tail state: start=%d active=%d takes=%d, want 1/0/2", tailStart, active, len(takes))
	}
	// The reload path (OpenSession) must reconstruct the same swiped transcript.
	_, reloaded, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := walkMsgTexts(reloaded); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("OpenSession reload = %v, want [u0 a0]", got)
	}
}

// TestSeedGreetingVariants proves a card's opening set pre-seeds as message-0
// swipe variants: all N greetings become takes, the selected one is active in the
// effective transcript, and the whole state survives a reload.
func TestSeedGreetingVariants(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	greetings := []provider.Message{
		variantMsg(provider.RoleAssistant, "g0"),
		variantMsg(provider.RoleAssistant, "g1"),
		variantMsg(provider.RoleAssistant, "g2"),
	}
	active, err := s.SeedGreetingVariants(greetings, 1)
	if err != nil {
		t.Fatal(err)
	}
	if walkMsgTexts([]provider.Message{active})[0] != "g1" {
		t.Errorf("returned active greeting = %v, want g1", walkMsgTexts([]provider.Message{active}))
	}
	path := s.Path
	_ = s.Close()

	eff, tailStart, takes, act := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"g1"}) {
		t.Errorf("effective = %v, want [g1] (the selected greeting)", got)
	}
	if tailStart != 0 || act != 1 || len(takes) != 3 {
		t.Fatalf("tail: start=%d active=%d takes=%d, want 0/1/3", tailStart, act, len(takes))
	}
	if walkMsgTexts(takes[0])[0] != "g0" || walkMsgTexts(takes[1])[0] != "g1" || walkMsgTexts(takes[2])[0] != "g2" {
		t.Errorf("takes = [%v %v %v], want [[g0][g1][g2]]", walkMsgTexts(takes[0]), walkMsgTexts(takes[1]), walkMsgTexts(takes[2]))
	}
	if _, reloaded, err := OpenSession(path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else if got := walkMsgTexts(reloaded); !reflect.DeepEqual(got, []string{"g1"}) {
		t.Errorf("reload = %v, want [g1]", got)
	}
}

// TestSeedGreetingVariantsEdges: the last greeting active needs no select amend
// (already live), and a single greeting seeds one message with no swipe.
func TestSeedGreetingVariantsEdges(t *testing.T) {
	dir := testsupport.TempDir(t)
	// active == last: the loop leaves it live, so no select is written, and it is
	// still the active take.
	s, _ := NewSession(dir, dir, "fake", "model", "v")
	if _, err := s.SeedGreetingVariants([]provider.Message{
		variantMsg(provider.RoleAssistant, "g0"),
		variantMsg(provider.RoleAssistant, "g1"),
	}, 1); err != nil {
		t.Fatal(err)
	}
	p := s.Path
	_ = s.Close()
	eff, _, takes, act := walkTail(t, p)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"g1"}) || act != 1 || len(takes) != 2 {
		t.Errorf("active-last: eff=%v active=%d takes=%d, want [g1]/1/2", got, act, len(takes))
	}

	// A single greeting: one message, nothing to swipe.
	s2, _ := NewSession(dir, dir, "fake", "model", "v")
	if _, err := s2.SeedGreetingVariants([]provider.Message{variantMsg(provider.RoleAssistant, "only")}, 0); err != nil {
		t.Fatal(err)
	}
	p2 := s2.Path
	_ = s2.Close()
	eff2, _, takes2, _ := walkTail(t, p2)
	if got := walkMsgTexts(eff2); !reflect.DeepEqual(got, []string{"only"}) {
		t.Errorf("single greeting eff = %v, want [only]", got)
	}
	if len(takes2) >= 2 {
		t.Errorf("single greeting should have no swipe takes, got %d", len(takes2))
	}
}

// TestTailResetsOnNewUserTurn proves a new user message commits the response:
// its takes stop being the swipeable tail.
func TestTailResetsOnNewUserTurn(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(variantMsg(provider.RoleUser, "u0")))
	must(s.AppendMessage(variantMsg(provider.RoleAssistant, "a0")))
	must(s.AppendAmend(AmendRetract, 1, nil, "retry"))
	must(s.AppendMessage(variantMsg(provider.RoleAssistant, "a1")))
	// A new exchange begins.
	must(s.AppendMessage(variantMsg(provider.RoleUser, "u1")))
	path := s.Path
	_ = s.Close()

	eff, tailStart, takes, _ := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"u0", "a1", "u1"}) {
		t.Errorf("effective = %v, want [u0 a1 u1]", got)
	}
	if tailStart != -1 || len(takes) != 0 {
		t.Errorf("tail should reset on a new user turn: start=%d takes=%d", tailStart, len(takes))
	}
}
