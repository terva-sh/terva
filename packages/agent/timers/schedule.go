package timers

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinEvery is the shortest interval a recurring timer may have. The service
// looks at the wall clock at most every pollMax, so a shorter interval would
// promise a precision the loop does not have.
const MinEvery = time.Minute

// MaxDaily is the most times of day one schedule holds. The service walks the
// windows of a gap one at a time with its lock held, so the count of windows
// in a day bounds that work.
const MaxDaily = 48

// Schedule says when a timer fires. Exactly one of At, Every, and Daily is
// set. At is one time. Every and Daily recur.
type Schedule struct {
	// At is the one time a one-time timer fires.
	At time.Time `json:"at,omitzero"`

	// Every is the interval of a recurring timer. Its windows are Anchor plus
	// a whole number of intervals, so a late firing does not move the ones
	// after it.
	Every  Duration  `json:"every,omitempty"`
	Anchor time.Time `json:"anchor,omitzero"`

	// Daily holds times of day, each as a count of minutes after midnight,
	// in ascending order. Days limits them to some days of the week, and an
	// empty Days means every day. Zone is the IANA name of the zone the
	// times are read in, and an empty Zone means the daemon's local zone.
	Daily []ClockTime    `json:"daily,omitempty"`
	Days  []time.Weekday `json:"days,omitempty"`
	Zone  string         `json:"zone,omitempty"`
}

// Recurs reports whether the schedule has more than one window.
func (s Schedule) Recurs() bool { return s.At.IsZero() }

// Duration is a time.Duration that JSON holds as text, such as "15m", so a
// person can read the file.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ClockTime is a time of day, as minutes after midnight. JSON holds it as
// "07:30".
type ClockTime int

func (c ClockTime) String() string { return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60) }

func (c ClockTime) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

func (c *ClockTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := parseClock(s)
	if err != nil {
		return err
	}
	*c = v
	return nil
}

func parseClock(s string) (ClockTime, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || len(m) != 2 || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("timers: %q is not a time of day such as 07:30", s)
	}
	return ClockTime(hh*60 + mm), nil
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

var dayOrder = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// ParseSchedule reads the text form of a schedule. now anchors the relative
// forms. The forms are:
//
//	in 20m                      one time, 20 minutes from now
//	at 2026-12-31T09:00:00Z     one time, at an RFC 3339 instant
//	every 15m                   each 15 minutes, counted from now
//	daily 07:30                 each day at 07:30
//	daily 07:30,19:00           each day at both times
//	weekdays 07:30              Monday to Friday
//	mon,thu 09:00               the named days
//
// A daily form may end with an IANA zone name, such as Europe/Helsinki.
// Without one, the times are in the daemon's local zone. A schedule holds at
// most MaxDaily times of day.
func ParseSchedule(text string, now time.Time) (Schedule, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	if len(f) < 2 {
		return Schedule{}, fmt.Errorf("timers: %q is not a schedule; write it as \"every 15m\" or \"daily 07:30\"", text)
	}
	raw := strings.Fields(strings.TrimSpace(text))
	var s Schedule
	switch f[0] {
	case "in":
		d, err := time.ParseDuration(f[1])
		if err != nil || d <= 0 || len(f) != 2 {
			return Schedule{}, fmt.Errorf("timers: %q needs a wait such as \"in 20m\"", text)
		}
		s.At = now.Add(d).Round(0)
	case "at":
		at, err := time.Parse(time.RFC3339, raw[1])
		if err != nil || len(f) != 2 {
			return Schedule{}, fmt.Errorf("timers: %q needs an instant such as \"at 2026-12-31T09:00:00Z\"", text)
		}
		s.At = at
	case "every":
		d, err := time.ParseDuration(f[1])
		if err != nil || len(f) != 2 {
			return Schedule{}, fmt.Errorf("timers: %q needs an interval such as \"every 15m\"", text)
		}
		s.Every, s.Anchor = Duration(d), now.Round(0)
	default:
		switch f[0] {
		case "daily":
		case "weekdays":
			s.Days = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
		default:
			for _, name := range strings.Split(f[0], ",") {
				d, ok := dayNames[name]
				if !ok {
					return Schedule{}, fmt.Errorf("timers: %q is not a schedule; write it as \"every 15m\" or \"daily 07:30\"", text)
				}
				if !slices.Contains(s.Days, d) {
					s.Days = append(s.Days, d)
				}
			}
			slices.Sort(s.Days)
		}
		for _, c := range strings.Split(f[1], ",") {
			t, err := parseClock(c)
			if err != nil {
				return Schedule{}, err
			}
			if !slices.Contains(s.Daily, t) {
				s.Daily = append(s.Daily, t)
			}
		}
		slices.Sort(s.Daily)
		switch len(f) {
		case 2:
		case 3:
			s.Zone = raw[2]
		default:
			return Schedule{}, fmt.Errorf("timers: %q has more after the zone than a schedule takes", text)
		}
	}
	if err := s.Validate(); err != nil {
		return Schedule{}, err
	}
	return s, nil
}

// span writes an interval as a person writes one: 15m, and not 15m0s. The
// text still parses, so String and ParseSchedule stay a pair.
func span(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// String returns the text form that ParseSchedule reads. An interval loses
// its anchor, so the text of a recurring timer names its rhythm and not its
// phase.
func (s Schedule) String() string {
	switch {
	case !s.At.IsZero():
		return "at " + s.At.Format(time.RFC3339)
	case s.Every > 0:
		return "every " + span(time.Duration(s.Every))
	}
	times := make([]string, len(s.Daily))
	for i, c := range s.Daily {
		times[i] = c.String()
	}
	head := "daily"
	weekdays := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	switch {
	case slices.Equal(s.Days, weekdays):
		head = "weekdays"
	case len(s.Days) > 0:
		names := make([]string, len(s.Days))
		for i, d := range s.Days {
			names[i] = dayOrder[d]
		}
		head = strings.Join(names, ",")
	}
	out := head + " " + strings.Join(times, ",")
	if s.Zone != "" {
		out += " " + s.Zone
	}
	return out
}

// normalized returns the schedule with its times of day and its days in
// ascending order and with no repeats. next reads the times in order and
// returns the first one that fits, so a schedule out of order would skip a
// window. ParseSchedule writes them in order, and a caller that builds a
// Schedule by hand need not.
func (s Schedule) normalized() Schedule {
	if len(s.Daily) > 0 {
		s.Daily = slices.Compact(slices.Sorted(slices.Values(s.Daily)))
	}
	if len(s.Days) > 0 {
		s.Days = slices.Compact(slices.Sorted(slices.Values(s.Days)))
	}
	return s
}

// Validate reports whether the schedule can produce a window on this machine.
// Set uses it, so a new timer cannot name a zone this machine does not know.
func (s Schedule) Validate() error {
	if err := s.check(); err != nil {
		return err
	}
	if _, err := s.location(); err != nil {
		return err
	}
	return nil
}

// check is Validate without the zone. The file loader uses it. A zone can
// stop loading after a timer was set, when the daemon moves to a machine with
// less zone data, and that must cost one timer and not the whole file.
func (s Schedule) check() error {
	kinds := 0
	if !s.At.IsZero() {
		kinds++
	}
	if s.Every != 0 {
		kinds++
	}
	if len(s.Daily) > 0 {
		kinds++
	}
	if kinds != 1 {
		return errors.New("timers: a schedule sets exactly one of a time, an interval, and times of day")
	}
	if s.Every != 0 {
		if time.Duration(s.Every) < MinEvery {
			return fmt.Errorf("timers: an interval is at least %s", MinEvery)
		}
		if s.Anchor.IsZero() {
			return errors.New("timers: an interval needs the time it counts from")
		}
	}
	if len(s.Daily) > MaxDaily {
		return fmt.Errorf("timers: a schedule holds at most %d times of day", MaxDaily)
	}
	for _, c := range s.Daily {
		if c < 0 || c >= 24*60 {
			return fmt.Errorf("timers: %d is not a time of day", int(c))
		}
	}
	for _, d := range s.Days {
		if d < time.Sunday || d > time.Saturday {
			return fmt.Errorf("timers: %d is not a day of the week", int(d))
		}
	}
	if len(s.Daily) == 0 && (len(s.Days) > 0 || s.Zone != "") {
		return errors.New("timers: days and a zone belong to times of day")
	}
	return nil
}

// zones holds each zone that loaded. A load reads a file, and the service
// asks for a timer's zone at every poll. A zone that did not load is not
// kept, so it is found once the machine has it.
var zones sync.Map

func (s Schedule) location() (*time.Location, error) {
	if s.Zone == "" {
		return time.Local, nil
	}
	if loc, ok := zones.Load(s.Zone); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(s.Zone)
	if err != nil {
		return nil, fmt.Errorf("timers: %q is not a zone this machine knows", s.Zone)
	}
	zones.Store(s.Zone, loc)
	return loc, nil
}

// zoneProblem says why the schedule cannot be read on this machine, or
// nothing. A timer with one waits and does not fire.
//
// 🔑 The alternative was to read the times in the daemon's own zone. It lost
// because a brief set for 07:30 in Helsinki would then run at 07:30 somewhere
// else, and a firing at the wrong hour cannot be taken back. A timer that
// waits shows in the list, and it fires once the zone loads.
func (s Schedule) zoneProblem() string {
	if _, err := s.location(); err != nil {
		return fmt.Sprintf("this machine does not know the zone %q", s.Zone)
	}
	return ""
}

// first returns the first window of a schedule made at now.
func (s Schedule) first(now time.Time) time.Time {
	if !s.At.IsZero() {
		return s.At
	}
	return s.next(now)
}

// next returns the first window later than after. A one-time schedule has
// none, and returns the zero time.
func (s Schedule) next(after time.Time) time.Time {
	switch {
	case !s.At.IsZero():
		return time.Time{}
	case s.Every > 0:
		every := time.Duration(s.Every)
		if after.Before(s.Anchor) {
			return s.Anchor.Add(every)
		}
		return s.Anchor.Add((after.Sub(s.Anchor)/every + 1) * every)
	}
	return s.nextDaily(after, s.zone())
}

// zone is the zone the times of day are read in. The service does not ask a
// schedule with a zoneProblem for a window, so the fallback here only keeps
// a caller that skipped that check from a nil zone.
func (s Schedule) zone() *time.Location {
	loc, err := s.location()
	if err != nil {
		return time.Local
	}
	return loc
}

// window returns the instant of the time of day c on a date, in loc.
//
// 🔑 A clock change that goes forward skips an hour, and a time in that hour
// does not exist on that date. time.Date then returns an instant on one side
// of the change, and it does not promise which side. So the code decides: the
// window is the moment of the change, the first instant after the time that
// was skipped. That day's window still fires once, and never before the time
// a person wrote.
//
// A clock change that goes back gives one time twice. time.Date returns one
// of the two instants, and either is a correct reading of the time.
func window(y int, m time.Month, d int, c ClockTime, loc *time.Location) time.Time {
	w := time.Date(y, m, d, int(c)/60, int(c)%60, 0, 0, loc)
	// Compare what the wall shows at w with what was asked for, as plain
	// dates with no zone.
	wall := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, time.UTC)
	asked := time.Date(y, m, d, int(c)/60, int(c)%60, 0, 0, time.UTC)
	switch start, end := w.ZoneBounds(); {
	case wall.Equal(asked):
		return w
	case wall.Before(asked) && !end.IsZero():
		// w is before the change, so the change ends w's period.
		return end
	case wall.After(asked) && !start.IsZero():
		// w is after the change, so the change starts w's period.
		return start
	}
	return w
}

// nextDaily is next for times of day, in a zone the caller loaded. The zone
// is a parameter because due asks for many windows.
func (s Schedule) nextDaily(after time.Time, loc *time.Location) time.Time {
	local := after.In(loc)
	// Eight days covers a schedule of one day of the week, from any start.
	for day := 0; day <= 8; day++ {
		d := local.AddDate(0, 0, day)
		if len(s.Days) > 0 && !slices.Contains(s.Days, d.Weekday()) {
			continue
		}
		for _, c := range s.Daily {
			if w := window(d.Year(), d.Month(), d.Day(), c, loc); w.After(after) {
				return w
			}
		}
	}
	return time.Time{}
}

// lastDaily returns the latest window at or before now, or the zero time.
func (s Schedule) lastDaily(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	for day := 0; day >= -8; day-- {
		d := local.AddDate(0, 0, day)
		if len(s.Days) > 0 && !slices.Contains(s.Days, d.Weekday()) {
			continue
		}
		for i := len(s.Daily) - 1; i >= 0; i-- {
			if w := window(d.Year(), d.Month(), d.Day(), s.Daily[i], loc); !w.After(now) {
				return w
			}
		}
	}
	return time.Time{}
}

// dueCap bounds the count of windows that due walks, with the service's lock
// held. A daily schedule left for longer reports the cap as its count, which
// already says "a very long time". The window it reports is still the latest.
const dueCap = 10_000

// due returns the latest window at or before now, starting from the window
// next, and how many windows that span holds. The caller knows next is at or
// before now.
func (s Schedule) due(next, now time.Time) (latest time.Time, n int) {
	switch {
	case !s.At.IsZero():
		return next, 1
	case s.Every > 0:
		every := time.Duration(s.Every)
		extra := int(now.Sub(next) / every)
		return next.Add(time.Duration(extra) * every), extra + 1
	}
	latest, n = next, 1
	loc := s.zone()
	for n < dueCap {
		w := s.nextDaily(latest, loc)
		if w.IsZero() || w.After(now) {
			break
		}
		latest, n = w, n+1
	}
	if n == dueCap {
		// The walk stopped at the cap and not at now. The count stays at the
		// cap, and the window is found from the other end.
		if w := s.lastDaily(now, loc); w.After(latest) {
			latest = w
		}
	}
	return latest, n
}
