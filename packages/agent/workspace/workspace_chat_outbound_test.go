package workspace

// The outbound policy by itself: what goes to the chat now and what waits.
// Each test calls the policy with a time it chose, so none waits on a clock.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// outboundAt builds a policy with the given brief times and quiet range, in
// UTC, with its queue in memory.
func outboundAt(t *testing.T, brief []string, quiet string) *chatOutbound {
	t.Helper()
	rules, err := parseChatOutboundRules(brief, quiet)
	if err != nil {
		t.Fatal(err)
	}
	rules.loc = time.UTC
	return openChatOutbound("", rules, t.Logf, nil)
}

// noon is a time outside the quiet hours these tests use.
var noon = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func texts(lines []chatLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text
		if l.speaker != "" {
			out[i] = l.speaker + ": " + l.text
		}
	}
	return out
}

func heldTexts(o *chatOutbound) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, len(o.held))
	for i, h := range o.held {
		out[i] = h.Text
		if h.Reason != "" {
			out[i] = h.Reason + "/" + h.Text
		}
	}
	return out
}

func same(got []string, want ...string) bool {
	return fmt.Sprint(got) == fmt.Sprint(want)
}

// An answer to a person who is waiting for it is not the entity writing
// first. Ten minutes after their last post, it is.
func TestAReplyGoesOutAndALaterMessageWaits(t *testing.T) {
	o := outboundAt(t, []string{"07:30"}, "")
	o.personPosted(noon)
	if got := texts(o.memberSaid(noon.Add(9*time.Minute), "e1", "c1", "", "on it")); !same(got, "on it") {
		t.Fatalf("a reply inside the conversation = %v, want it sent", got)
	}
	if got := o.memberSaid(noon.Add(10*time.Minute), "e2", "c1", "", "halfway"); got != nil {
		t.Fatalf("a message after the conversation = %v, want it held", texts(got))
	}
	if got := heldTexts(o); !same(got, "halfway") {
		t.Fatalf("held = %v", got)
	}
}

// With no person in the conversation, a member's messages wait. When the
// team goes quiet, the last word on each piece of work goes out, and the
// rest waits for the brief.
func TestFinishedWorkSendsItsLastWordAndTheBriefHoldsTheRest(t *testing.T) {
	o := outboundAt(t, []string{"07:30"}, "")
	at := noon
	for _, m := range []struct{ id, chain, speaker, text string }{
		{"e1", "c1", "", "started the migration"},
		{"e2", "c2", "jev", "looking at the flaky test"},
		{"e3", "c1", "", "the migration is done, 3 tables moved"},
		{"e4", "c2", "jev", "the flaky test is a race, fixed in #12"},
	} {
		at = at.Add(time.Minute)
		if got := o.memberSaid(at, m.id, m.chain, m.speaker, m.text); got != nil {
			t.Fatalf("%s went out with no person in the conversation", m.id)
		}
	}
	got := texts(o.settled(at.Add(time.Minute)))
	if !same(got, "the migration is done, 3 tables moved", "jev: the flaky test is a race, fixed in #12") {
		t.Fatalf("when the team went quiet the chat got %v, want the last word of each chain", got)
	}
	if got := heldTexts(o); !same(got, "started the migration", "looking at the flaky test") {
		t.Fatalf("held after that = %v, want the two earlier updates", got)
	}
	// A second quiet moment has nothing new to report.
	if got := o.settled(at.Add(2 * time.Minute)); len(got) != 0 {
		t.Fatalf("a second quiet moment sent %v", texts(got))
	}
	if got := heldTexts(o); !same(got, "started the migration", "looking at the flaky test") {
		t.Fatalf("the second quiet moment sent an update that was not the last word: held = %v", got)
	}

	brief := texts(o.briefCame(at.Add(time.Hour)))
	if len(brief) != 1 || !strings.Contains(brief[0], "2 updates") ||
		!strings.Contains(brief[0], "12:01 the coordinator: started the migration") ||
		!strings.Contains(brief[0], "12:02 jev: looking at the flaky test") {
		t.Fatalf("the brief = %q, want one message that lists both updates with their times", brief)
	}
	if got := heldTexts(o); len(got) != 0 {
		t.Fatalf("held after the brief = %v", got)
	}
	if got := o.briefCame(at.Add(2 * time.Hour)); len(got) != 0 {
		t.Fatalf("a brief over an empty queue sent %v", texts(got))
	}
}

// Quiet hours hold what the host would send for a reason, and send it when
// they end. An update still waits for the brief.
func TestQuietHoursHoldAndThenSend(t *testing.T) {
	o := outboundAt(t, []string{"09:00"}, "22:00-07:00")
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	if got := o.notice(night, "p1", outDecision, "jev proposed a change"); got != nil {
		t.Fatalf("a decision went out in quiet hours: %v", texts(got))
	}
	o.memberSaid(night.Add(time.Minute), "e1", "c1", "", "tests are running")
	o.memberSaid(night.Add(2*time.Minute), "e2", "c1", "", "all green, merged")
	if got := o.settled(night.Add(3 * time.Minute)); got != nil {
		t.Fatalf("finished work went out in quiet hours: %v", texts(got))
	}
	if got := o.notice(night.Add(4*time.Minute), "w1", outWatch, "the deploy finished"); got != nil {
		t.Fatalf("a watched change went out in quiet hours: %v", texts(got))
	}
	// Still inside quiet hours, at 02:00 the next day.
	if got := o.quietEnded(night.Add(3 * time.Hour)); got != nil {
		t.Fatalf("the end of quiet hours fired inside them and sent %v", texts(got))
	}

	morning := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)
	got := texts(o.quietEnded(morning))
	if !same(got, "jev proposed a change", "all green, merged", "the deploy finished") {
		t.Fatalf("at the end of quiet hours the chat got %v, want the three held for a reason, oldest first", got)
	}
	if got := heldTexts(o); !same(got, "tests are running") {
		t.Fatalf("held after quiet hours = %v, want the one update", got)
	}
	// Outside quiet hours a decision goes out at once.
	if got := texts(o.notice(morning, "p2", outDecision, "the team waits for its kickoff")); !same(got, "the team waits for its kickoff") {
		t.Fatalf("a decision outside quiet hours = %v", got)
	}
}

// A person who writes at night is awake, and the answer goes to them.
func TestAReplyGoesOutInQuietHours(t *testing.T) {
	o := outboundAt(t, nil, "22:00-07:00")
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	o.personPosted(night)
	if got := texts(o.memberSaid(night.Add(time.Minute), "e1", "c1", "", "yes, it merged")); !same(got, "yes, it merged") {
		t.Fatalf("a reply in quiet hours = %v, want it sent", got)
	}
	if got := texts(o.notice(night.Add(2*time.Minute), "p1", outDecision, "jev proposed a change")); !same(got, "jev proposed a change") {
		t.Fatalf("a decision while the person is in the conversation = %v, want it sent", got)
	}
}

// A brief that comes due in quiet hours waits for them to end.
func TestABriefInsideQuietHoursWaitsForTheirEnd(t *testing.T) {
	o := outboundAt(t, []string{"06:00"}, "22:00-07:00")
	o.memberSaid(time.Date(2026, 11, 17, 21, 0, 0, 0, time.UTC), "e1", "c1", "", "indexing the repo")
	if got := o.briefCame(time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)); got != nil {
		t.Fatalf("a brief went out in quiet hours: %v", texts(got))
	}
	got := texts(o.quietEnded(time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)))
	if len(got) != 1 || !strings.Contains(got[0], "indexing the repo") {
		t.Fatalf("at the end of quiet hours the chat got %v, want the brief", got)
	}
}

// With no brief time, the person's next message is what empties the queue.
func TestWithNoBriefTimeThePersonsNextMessageBringsTheBrief(t *testing.T) {
	o := outboundAt(t, nil, "")
	o.memberSaid(noon, "e1", "c1", "", "indexing the repo")
	got := texts(o.personPosted(noon.Add(time.Hour)))
	if len(got) != 1 || !strings.HasPrefix(got[0], "1 update from the team") || !strings.Contains(got[0], "indexing the repo") {
		t.Fatalf("the person's next message brought %v, want the brief", got)
	}
	// With a brief time set, it does not.
	timed := outboundAt(t, []string{"07:30"}, "")
	timed.memberSaid(noon, "e1", "c1", "", "indexing the repo")
	if got := timed.personPosted(noon.Add(time.Hour)); got != nil {
		t.Fatalf("with a brief time set, the person's message brought %v", texts(got))
	}
	// What quiet hours held for a reason does go to a person who writes:
	// they are here.
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	quiet := outboundAt(t, []string{"07:30"}, "22:00-07:00")
	quiet.notice(night, "p1", outDecision, "jev proposed a change")
	quiet.memberSaid(night, "e1", "c1", "", "indexing the repo")
	if got := texts(quiet.personPosted(night.Add(time.Hour))); !same(got, "jev proposed a change") {
		t.Fatalf("a person who wrote in quiet hours got %v, want the held decision and not the updates", got)
	}
	// A brief that came due with no chat connected goes out with it.
	timed.briefMissed()
	if got := texts(timed.personPosted(noon.Add(2 * time.Hour))); len(got) != 1 {
		t.Fatalf("a missed brief did not go out with the person's next message: %v", got)
	}
}

// An always-on daemon restarts at every update. The night's updates must
// still be in the morning's brief.
func TestTheQueueSurvivesARestart(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "chat-outbound.json")
	rules, err := parseChatOutboundRules([]string{"07:30"}, "22:00-07:00")
	if err != nil {
		t.Fatal(err)
	}
	rules.loc = time.UTC
	changes := 0
	o := openChatOutbound(path, rules, t.Logf, func() { changes++ })
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	o.memberSaid(night, "e1", "c1", "jev", "the build is green")
	o.notice(night, "p1", outDecision, "jev proposed a change")
	o.briefCame(night)
	if changes != 3 {
		t.Fatalf("the app was told of %d changes, want 3", changes)
	}

	again := openChatOutbound(path, rules, t.Logf, nil)
	if got := heldTexts(again); !same(got, "the build is green", "decision/jev proposed a change") {
		t.Fatalf("held after a restart = %v", got)
	}
	got := texts(again.quietEnded(time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)))
	if len(got) != 2 || got[0] != "jev proposed a change" || !strings.Contains(got[1], "jev: the build is green") {
		t.Fatalf("after a restart the end of quiet hours sent %v, want the decision and then the brief that came due", got)
	}
	// A file of Windows has no mode for a group or for others.
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("the queue's file: %v, %v, want a private file", info, err)
	}

	// A file that does not read starts an empty queue and stops nothing.
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := heldTexts(openChatOutbound(path, rules, t.Logf, nil)); len(got) != 0 {
		t.Fatalf("held from a file that does not read = %v", got)
	}
}

func TestTheQueueIsBounded(t *testing.T) {
	o := outboundAt(t, []string{"07:30"}, "22:00-07:00")
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	o.notice(night, "p1", outDecision, "jev proposed a change")
	for i := range chatHeldMax + 5 {
		o.memberSaid(night, fmt.Sprintf("e%d", i), "c1", "", fmt.Sprintf("update %d", i))
	}
	held := heldTexts(o)
	if len(held) != chatHeldMax || held[0] != "decision/jev proposed a change" || held[1] != "update 6" {
		t.Fatalf("the full queue holds %d, first %q then %q; want %d, the decision kept, and the oldest updates gone", len(held), held[0], held[1], chatHeldMax)
	}
	long := strings.Repeat("ä", chatHeldBody+50)
	o.memberSaid(night, "long", "c2", "", long)
	held = heldTexts(o)
	if got := []rune(held[len(held)-1]); len(got) != chatHeldBody+1 {
		t.Fatalf("a long message is held as %d runes, want %d and a mark", len(got), chatHeldBody)
	}
	got := texts(o.quietEnded(time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)))
	if !same(got, "jev proposed a change") {
		t.Fatalf("the end of quiet hours sent %v", got)
	}
	brief := texts(o.briefCame(time.Date(2026, 11, 18, 7, 30, 0, 0, time.UTC)))
	// The queue dropped 7, and the brief lists the newest of the 199 it held.
	want := fmt.Sprintf("%d older updates are in the room", 7+chatHeldMax-1-chatBriefMax)
	if len(brief) != 1 || !strings.Contains(brief[0], want) {
		t.Fatalf("the brief does not say %q: it ends %q", want, brief[0][max(0, len(brief[0])-120):])
	}
}

// A person who is in the conversation has a chain's newest word. An older
// update of that chain must not arrive after it as the last word.
func TestAnUpdateOlderThanADirectReplyIsNotTheLastWord(t *testing.T) {
	o := outboundAt(t, []string{"07:30"}, "")
	o.memberSaid(noon, "e1", "c1", "", "tests are running")
	o.memberSaid(noon, "e2", "c2", "jev", "reading the logs")
	// The person comes back. With a brief time set, the queue stays.
	if got := o.personPosted(noon.Add(time.Hour)); got != nil {
		t.Fatalf("the person's message brought %v", texts(got))
	}
	if got := texts(o.memberSaid(noon.Add(61*time.Minute), "e3", "c1", "", "tests passed")); !same(got, "tests passed") {
		t.Fatalf("the reply = %v", got)
	}
	// The team goes quiet. c1 has nothing new to say. c2 still does.
	if got := texts(o.settled(noon.Add(62 * time.Minute))); !same(got, "jev: reading the logs") {
		t.Fatalf("when the team went quiet the chat got %v, want only the chain the person has not heard from", got)
	}
	if got := heldTexts(o); !same(got, "tests are running") {
		t.Fatalf("held = %v, want the older update kept for the brief", got)
	}
}

// A brief of every held message could be dozens of chat messages at once.
func TestTheBriefListsTheNewestAndCountsTheRest(t *testing.T) {
	o := outboundAt(t, []string{"07:30"}, "")
	for i := range chatBriefMax + 12 {
		o.memberSaid(noon, fmt.Sprintf("e%d", i), "c1", "", fmt.Sprintf("update %d", i))
	}
	brief := texts(o.briefCame(noon.Add(time.Hour)))
	if len(brief) != 1 {
		t.Fatalf("the brief is %d messages, want 1", len(brief))
	}
	lines := strings.Split(brief[0], "\n")
	if len(lines) != chatBriefMax+2 || !strings.HasPrefix(lines[0], fmt.Sprintf("%d updates", chatBriefMax)) ||
		!strings.HasSuffix(lines[1], "update 12") || !strings.HasPrefix(lines[len(lines)-1], "12 older updates are in the room") {
		t.Fatalf("the brief has %d lines, first %q, second %q, last %q", len(lines), lines[0], lines[1], lines[len(lines)-1])
	}
	if got := heldTexts(o); len(got) != 0 {
		t.Fatalf("%d messages are held after the brief", len(got))
	}
}

// A full queue keeps the messages held for a reason for as long as it can.
func TestAFullQueueOfReasonsDropsANewUpdate(t *testing.T) {
	o := outboundAt(t, []string{"07:30"}, "22:00-07:00")
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	for i := range chatHeldMax {
		o.notice(night, fmt.Sprintf("p%d", i), outDecision, fmt.Sprintf("decision %d", i))
	}
	o.memberSaid(night, "e1", "c1", "", "an update")
	held := heldTexts(o)
	if len(held) != chatHeldMax || held[0] != "decision/decision 0" || held[len(held)-1] != fmt.Sprintf("decision/decision %d", chatHeldMax-1) {
		t.Fatalf("a full queue of decisions took an update: %d held, first %q, last %q", len(held), held[0], held[len(held)-1])
	}
	// One more decision has nothing to push out but the oldest decision.
	o.notice(night, "p-new", outDecision, "the newest decision")
	held = heldTexts(o)
	if len(held) != chatHeldMax || held[0] != "decision/decision 1" || held[len(held)-1] != "decision/the newest decision" {
		t.Fatalf("after one more decision: %d held, first %q, last %q", len(held), held[0], held[len(held)-1])
	}
	o.mu.Lock()
	dropped := o.dropped
	o.mu.Unlock()
	if dropped != 2 {
		t.Fatalf("the queue counts %d dropped, want 2", dropped)
	}
}

func TestTheRulesReadTheirTimes(t *testing.T) {
	r, err := parseChatOutboundRules([]string{"07:30", " 19:00 "}, "22:00-07:00")
	if err != nil {
		t.Fatal(err)
	}
	r.loc = time.UTC
	if len(r.brief) != 2 || r.brief[0] != 7*60+30 || r.brief[1] != 19*60 {
		t.Fatalf("brief = %v", r.brief)
	}
	day := func(h, m int) time.Time { return time.Date(2026, 11, 17, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{day(21, 59), false}, {day(22, 0), true}, {day(23, 59), true},
		{day(0, 0), true}, {day(6, 59), true}, {day(7, 0), false}, {day(12, 0), false},
	} {
		if got := r.quiet(c.at); got != c.want {
			t.Errorf("quiet at %s = %v, want %v", c.at.Format("15:04"), got, c.want)
		}
	}
	// A range inside one day.
	siesta, err := parseChatOutboundRules(nil, "13:00-15:00")
	if err != nil {
		t.Fatal(err)
	}
	siesta.loc = time.UTC
	if siesta.quiet(day(12, 59)) || !siesta.quiet(day(13, 0)) || siesta.quiet(day(15, 0)) {
		t.Error("a range inside one day reads wrong at its edges")
	}
	for _, bad := range []struct {
		brief []string
		quiet string
	}{
		{[]string{"7.30"}, ""}, {[]string{"25:00"}, ""}, {nil, "22:00"}, {nil, "22:00-7am"}, {[]string{"07:30,19:00"}, ""},
	} {
		if _, err := parseChatOutboundRules(bad.brief, bad.quiet); err == nil {
			t.Errorf("brief %v, quiet %q passed", bad.brief, bad.quiet)
		}
	}
}
