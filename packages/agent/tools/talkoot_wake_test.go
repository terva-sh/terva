package tools

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
)

type fakeWaker struct {
	delay  time.Duration
	reason string
	calls  int
	err    error
}

func (w *fakeWaker) Wake(delay time.Duration, reason string) (TalkootWakeReceipt, error) {
	w.calls++
	w.delay, w.reason = delay, reason
	if w.err != nil {
		return TalkootWakeReceipt{}, w.err
	}
	due := time.Date(2026, 11, 17, 9, 15, 0, 0, time.UTC)
	return TalkootWakeReceipt{Wake: talkoot.Wake{ID: "01WAKE", Member: "helm", Reason: reason, Due: due}, Open: 2, Cap: 3}, nil
}

func TestTalkootWakeHandsTheDelayAndTheReasonToTheSeat(t *testing.T) {
	seat := &fakeWaker{}
	got, err := talkootText(t, &TalkootWakeTool{Seat: seat}, `{"delay_minutes":15,"reason":"Check the CI run for pull request 12."}`)
	if err != nil {
		t.Fatal(err)
	}
	if seat.delay != 15*time.Minute || seat.reason != "Check the CI run for pull request 12." {
		t.Fatalf("the seat got %s and %q", seat.delay, seat.reason)
	}
	for _, want := range []string{"01WAKE", "2026-11-17 09:15 UTC", "2 of 3 open wakes", "Check the CI run for pull request 12."} {
		if !strings.Contains(got, want) {
			t.Errorf("the result must say %q, got %q", want, got)
		}
	}
}

func TestTalkootWakeRefusesABadCallBeforeTheSeat(t *testing.T) {
	seat := &fakeWaker{}
	for _, args := range []string{
		`{"delay_minutes":0,"reason":"Check it."}`,
		`{"delay_minutes":-5,"reason":"Check it."}`,
		`{"reason":"Check it."}`,
		`{"delay_minutes":5,"reason":"  "}`,
		`{"delay_minutes":5}`,
		`{"delay_minutes":"soon","reason":"Check it."}`,
	} {
		if _, err := talkootText(t, &TalkootWakeTool{Seat: seat}, args); err == nil {
			t.Errorf("%s must be refused", args)
		}
	}
	if seat.calls != 0 {
		t.Errorf("a bad call must not reach the seat, got %d calls", seat.calls)
	}
	if _, err := talkootText(t, &TalkootWakeTool{}, `{"delay_minutes":5,"reason":"Check it."}`); err == nil {
		t.Error("a tool with no seat set a wake")
	}
}

// The seat names the person's cap in its refusal, and the model must read
// those words as the seat wrote them.
func TestTalkootWakeReturnsTheSeatsRefusal(t *testing.T) {
	seat := &fakeWaker{err: errors.New("you have 3 open wakes, and the limit is 3 (talkoot_wakes.per_member)")}
	_, err := talkootText(t, &TalkootWakeTool{Seat: seat}, `{"delay_minutes":5,"reason":"Check it."}`)
	if err == nil || !strings.Contains(err.Error(), "the limit is 3 (talkoot_wakes.per_member)") {
		t.Fatalf("want the seat's refusal, got %v", err)
	}
}

// A delay that no time.Duration holds must reach the seat as a long delay,
// which the person's cap refuses, and never as a short or negative one.
func TestTalkootWakeKeepsAHugeDelayLong(t *testing.T) {
	seat := &fakeWaker{}
	if _, err := talkootText(t, &TalkootWakeTool{Seat: seat}, `{"delay_minutes":9223372036854775807,"reason":"Check it."}`); err != nil {
		t.Fatal(err)
	}
	if seat.delay < 24*365*time.Hour {
		t.Errorf("a huge delay became %s", seat.delay)
	}
}

// 🚨 The MCP bridge serves, from TalkootToolDefs, the tools that
// talkoot.BridgeTools names, and decision 0023 lists what the bridge may
// serve. Its amendment of 2026-11-19 put the wake on that list. The wake is
// still no seat tool: a native session gets it only while the person's
// configuration turns wakes on, so it must not ride in with the seat's six.
func TestTalkootWakeIsBridgedAndIsNoSeatTool(t *testing.T) {
	wake := (&TalkootWakeTool{}).Name()
	if !slices.ContainsFunc(TalkootToolDefs(), func(d TalkootToolDef) bool { return d.Name == wake }) {
		t.Fatal("talkoot_wake is not in TalkootToolDefs, so the MCP bridge has no text to serve for it")
	}
	if !slices.Contains(talkoot.BridgeTools, wake) {
		t.Fatal("talkoot_wake is not in talkoot.BridgeTools, and decision 0023 lists it since 2026-11-19")
	}
	for _, tool := range TalkootTools(nil) {
		if tool.Name() == wake {
			t.Fatal("talkoot_wake is among the seat's tools, so a native member has it while wakes are off")
		}
	}
	// A roster's tools list narrows it, as it narrows any tool of a job.
	if slices.Contains(talkoot.SeatTools, "talkoot_wake") {
		t.Fatal("talkoot_wake is a seat tool, so a member's tools list cannot remove it")
	}
}
