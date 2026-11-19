package timers

import (
	"testing"
	"time"
)

func TestParseScheduleReadsEachFormAndWritesItBack(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	for text, want := range map[string]string{
		"in 20m":                         "at 2026-11-17T09:20:00Z",
		"at 2026-12-31T09:00:00Z":        "at 2026-12-31T09:00:00Z",
		"every 15m":                      "every 15m",
		"daily 07:30":                    "daily 07:30",
		"daily 19:00,07:30,07:30":        "daily 07:30,19:00",
		"weekdays 07:30":                 "weekdays 07:30",
		"thu,mon 09:00":                  "mon,thu 09:00",
		"  Daily 07:30 Europe/Helsinki ": "daily 07:30 Europe/Helsinki",
		"mon,tue,wed,thu,fri 08:00":      "weekdays 08:00",
	} {
		s, err := ParseSchedule(text, now)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", text, err)
			continue
		}
		if got := s.String(); got != want {
			t.Errorf("ParseSchedule(%q) writes back %q, want %q", text, got, want)
		}
		if err := s.Validate(); err != nil {
			t.Errorf("ParseSchedule(%q) made a schedule that does not validate: %v", text, err)
		}
	}
}

func TestParseScheduleRefusesWhatItCannotRead(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	for _, text := range []string{
		"", "daily", "every", "every 10s", "every soon", "in -5m", "in 5m 10m",
		"at tomorrow", "daily 7:3", "daily 24:00", "daily 07:60", "someday 07:30",
		"mon,funday 07:30", "daily 07:30 Mars/Olympus", "daily 07:30 UTC extra",
	} {
		if s, err := ParseSchedule(text, now); err == nil {
			t.Errorf("ParseSchedule(%q) accepted it as %q", text, s)
		}
	}
}

func TestADailyScheduleFindsItsNextWindow(t *testing.T) {
	helsinki, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		t.Skipf("this machine has no zone data: %v", err)
	}
	at := func(y int, m time.Month, d, hh, mm int) time.Time { return time.Date(y, m, d, hh, mm, 0, 0, helsinki) }
	parse := func(text string) Schedule {
		t.Helper()
		s, err := ParseSchedule(text, at(2026, 11, 17, 9, 0))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, c := range []struct {
		name  string
		sched Schedule
		after time.Time
		want  time.Time
	}{
		{"later the same day", parse("daily 07:30,19:00 Europe/Helsinki"), at(2026, 11, 17, 9, 0), at(2026, 11, 17, 19, 0)},
		{"the next day", parse("daily 07:30,19:00 Europe/Helsinki"), at(2026, 11, 17, 19, 0), at(2026, 11, 18, 7, 30)},
		{"a window is not after itself", parse("daily 07:30 Europe/Helsinki"), at(2026, 11, 17, 7, 30), at(2026, 11, 18, 7, 30)},
		// 2026-11-20 is a Friday.
		{"over the weekend", parse("weekdays 07:30 Europe/Helsinki"), at(2026, 11, 20, 8, 0), at(2026, 11, 23, 7, 30)},
		{"one day of the week", parse("tue 07:30 Europe/Helsinki"), at(2026, 11, 17, 7, 30), at(2026, 11, 24, 7, 30)},
		// The clocks go forward on 2027-03-28 at 03:00, so the day is 23
		// hours long and the window stays at 07:30 on the wall.
		{"across a clock change", parse("daily 07:30 Europe/Helsinki"), at(2027, 3, 27, 7, 30), at(2027, 3, 28, 7, 30)},
	} {
		if got := c.sched.next(c.after); !got.Equal(c.want) {
			t.Errorf("%s: next is %s, want %s", c.name, got.In(helsinki), c.want)
		}
	}
	// 03:30 does not exist on the day the clocks go forward. The window is
	// the moment of the change, 04:00 on the new clock, and it fires once.
	skipped := parse("daily 03:30 Europe/Helsinki")
	change := time.Date(2027, 3, 28, 1, 0, 0, 0, time.UTC)
	w := skipped.next(at(2027, 3, 27, 12, 0))
	if !w.Equal(change) || !skipped.next(w).Equal(at(2027, 3, 29, 3, 30)) {
		t.Errorf("the skipped hour: the window is %s and the one after is %s", w.In(helsinki), skipped.next(w).In(helsinki))
	}
}

// time.Date does not promise which side of a clock change it puts a time
// that the change skipped. For New York it has returned the hour before, so
// a window there fired early, or not at all for a lookup after that instant.
// The service decides the answer: the moment of the change.
func TestATimeThatAClockChangeSkippedFiresAtTheChange(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("this machine has no zone data: %v", err)
	}
	// The clocks go from 02:00 to 03:00 on 2027-03-14, at 07:00 UTC.
	change := time.Date(2027, 3, 14, 7, 0, 0, 0, time.UTC)
	sched := Schedule{Daily: []ClockTime{2*60 + 30}, Zone: "America/New_York"}
	for _, c := range []struct {
		name  string
		after time.Time
	}{
		{"from the day before", time.Date(2027, 3, 13, 12, 0, 0, 0, ny)},
		{"from 01:15, before either reading of 02:30", change.Add(-45 * time.Minute)},
		{"from 01:45, after the early reading of 02:30", change.Add(-15 * time.Minute)},
	} {
		if got := sched.next(c.after); !got.Equal(change) {
			t.Errorf("%s: the window is %s, want the change at %s", c.name, got.In(ny), change.In(ny))
		}
	}
	if got, want := sched.next(change), time.Date(2027, 3, 15, 2, 30, 0, 0, ny); !got.Equal(want) {
		t.Errorf("after the change the window is %s, want %s", got.In(ny), want)
	}
	// Two times in the skipped hour are one instant, so they fire once.
	both := Schedule{Daily: []ClockTime{2*60 + 15, 2*60 + 30}, Zone: "America/New_York"}
	latest, n := both.due(both.next(change.Add(-time.Hour)), change.Add(time.Hour))
	if !latest.Equal(change) || n != 1 {
		t.Errorf("two times in the skipped hour: latest %s, %d windows, want the change and 1", latest.In(ny), n)
	}
	// The window before a gap of days is still found from the far end.
	if got := sched.lastDaily(change.Add(-time.Minute), ny); !got.Equal(time.Date(2027, 3, 13, 2, 30, 0, 0, ny)) {
		t.Errorf("the window before the change is %s", got.In(ny))
	}
}

// The count of a very long gap stops at a cap. The window must not: it is
// the latest one, or an owner would take the firing for an old one.
func TestAGapPastTheCapStillReportsTheLatestWindow(t *testing.T) {
	var times []ClockTime
	for i := range MaxDaily {
		times = append(times, ClockTime(i*30))
	}
	sched := Schedule{Daily: times, Zone: "UTC"}
	if err := sched.Validate(); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 400 days of 48 windows is 19,200, past the cap.
	now := start.AddDate(0, 0, 400).Add(17 * time.Minute)
	latest, n := sched.due(start, now)
	if n != dueCap {
		t.Fatalf("the count is %d, want the cap %d", n, dueCap)
	}
	if want := start.AddDate(0, 0, 400); !latest.Equal(want) {
		t.Fatalf("the latest window is %s, want %s", latest, want)
	}
	if err := (Schedule{Daily: append(times, 23*60+59), Zone: "UTC"}).Validate(); err == nil {
		t.Fatalf("a schedule with %d times of day passed", MaxDaily+1)
	}
}

func TestDueCountsTheWindowsOfAGap(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	daily, err := ParseSchedule("daily 07:30,19:00 UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	next := daily.next(now) // 19:00 today
	latest, n := daily.due(next, now.Add(14*24*time.Hour))
	// 19:00 on the first day, then two a day up to 07:30 on the last.
	if want := time.Date(2026, 12, 1, 7, 30, 0, 0, time.UTC); !latest.Equal(want) || n != 28 {
		t.Fatalf("a fortnight away: latest %s with %d windows, want %s with 28", latest, n, want)
	}

	interval := Schedule{Every: Duration(15 * time.Minute), Anchor: now}
	latest, n = interval.due(now.Add(15*time.Minute), now.Add(15*time.Minute))
	if !latest.Equal(now.Add(15*time.Minute)) || n != 1 {
		t.Fatalf("on time: latest %s with %d windows, want the window itself and 1", latest, n)
	}
}
