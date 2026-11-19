package talkoot

// A routine is a recurring timer that a person approved (decision 0029 rules
// 4 and 5). A member asks for one, the ask waits in the inbox as a proposal,
// and only a person's approval stores it.
//
// This file holds what a routine is and where it is kept. The host owns the
// timer and the approval, and the room records each step in a routine line.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/privfs"
)

// RoutinesDir is the directory under a talkoot's directory that holds its
// routines, one JSON file each. The daemon names each file from an id it
// minted, as it does for a proposal.
const RoutinesDir = "routines"

// The kinds of a routine. The kind carries the defaults of the six limits.
const (
	// RoutineWatcher runs a command with no model, and wakes its member only
	// when the output changes.
	RoutineWatcher = "watcher"
	// RoutineBrief writes to the person at a time the person set.
	RoutineBrief = "brief"
	// RoutineTask does work.
	RoutineTask = "task"
)

// RoutineKinds lists every kind, in the order a person reads them.
var RoutineKinds = []string{RoutineWatcher, RoutineBrief, RoutineTask}

// The states of a routine.
const (
	RoutineActive = "active"
	RoutinePaused = "paused"
	// RoutineEnded is a routine that reached the end its approval gave it:
	// a count of runs, or a day. It does not resume.
	RoutineEnded = "ended"
)

// The actions of a routine line.
const (
	RoutineApproved = "approved"
	RoutineResumed  = "resumed"
	RoutineDeleted  = "deleted"
	// A paused routine line has the action "paused", which is ActionPaused.

	// RoutineSkipped records a time at which the routine started no run:
	// the daemon was away and the routine does not catch up, or its last
	// run was still at work.
	RoutineSkipped = "skipped"
	// A routine that reached its end has a line with the action "ended",
	// which is RoutineEnded.
)

// Limits on what a member writes into a routine.
const (
	// MaxRoutinePromptBytes bounds a routine's prompt. A run reads it at
	// every firing, so a long one costs at every firing.
	MaxRoutinePromptBytes = 4 * 1024
	// MaxRoutineCommandBytes bounds a watcher's command. The card shows the
	// command as written, and a person must be able to read all of it.
	MaxRoutineCommandBytes = 1024
	// MaxRoutineFollowUps and the other three bound the numbers a member may
	// ask for. The card would show a larger number, and a person could miss
	// its size. The configuration holds the values, because a default of a
	// kind has the same top.
	MaxRoutineFollowUps      = config.TalkootRoutineMaxFollowUps
	MaxRoutineReviewsPerDay  = config.TalkootRoutineMaxReviewsPerDay
	MaxRoutineSpendUSDPerDay = config.TalkootRoutineMaxSpendUSDPerDay
	MaxRoutineEnd            = config.TalkootRoutineMaxEnd
)

// RoutineFields are the six limits of a routine. A zero end means that the
// routine has no end of that kind.
type RoutineFields struct {
	// EndAfterRuns and EndAfterDays are the end condition: the routine stops
	// after that many runs, or that many days after its approval.
	EndAfterRuns int `json:"end_after_runs"`
	EndAfterDays int `json:"end_after_days"`
	// SpendUSDPerDay caps what the routine's runs spend in one day.
	SpendUSDPerDay float64 `json:"spend_usd_per_day"`
	// ReviewsPerDay caps the review requests of the routine's runs in one
	// day. The daemon cannot price a review that runs on the forge.
	ReviewsPerDay int `json:"reviews_per_day"`
	// FollowUps is how many wakes one run may set for itself.
	FollowUps int `json:"follow_ups"`
	// CatchUp says what a missed window does: one late run, or none.
	CatchUp bool `json:"catch_up"`
}

// The names of the six limits, as the tool, the configuration and the card
// write them.
const (
	FieldEndAfterRuns   = "end_after_runs"
	FieldEndAfterDays   = "end_after_days"
	FieldSpendUSDPerDay = "spend_usd_per_day"
	FieldReviewsPerDay  = "reviews_per_day"
	FieldFollowUps      = "follow_ups"
	FieldCatchUp        = "catch_up"
)

// RoutineAsk is the routine a member asks for. A nil limit asks for the
// default of the kind.
type RoutineAsk struct {
	Kind string `json:"kind"`
	// Schedule is the schedule in the timer service's text form, such as
	// "every 15m" or "weekdays 07:30".
	Schedule string `json:"schedule"`
	// Prompt is what a brief or a task run reads. Command is what a watcher
	// runs. A routine has the one that its kind takes.
	Prompt  string `json:"prompt,omitempty"`
	Command string `json:"command,omitempty"`

	EndAfterRuns   *int     `json:"end_after_runs,omitempty"`
	EndAfterDays   *int     `json:"end_after_days,omitempty"`
	SpendUSDPerDay *float64 `json:"spend_usd_per_day,omitempty"`
	ReviewsPerDay  *int     `json:"reviews_per_day,omitempty"`
	FollowUps      *int     `json:"follow_ups,omitempty"`
	CatchUp        *bool    `json:"catch_up,omitempty"`
}

// RoutineDiff is one limit that an ask sets to another value than the
// default of its kind. The card shows these and hides the rest.
type RoutineDiff struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Default string `json:"default"`
}

// ValidRoutineKind reports whether kind names a kind of routine.
func ValidRoutineKind(kind string) bool { return slices.Contains(RoutineKinds, kind) }

// Check refuses an ask that no approval could store. It does not read the
// schedule, which the host parses with its timer service.
func (a RoutineAsk) Check() error {
	if !ValidRoutineKind(a.Kind) {
		return fmt.Errorf("talkoot: the routine's kind %q must be one of %s", a.Kind, strings.Join(RoutineKinds, ", "))
	}
	if strings.TrimSpace(a.Schedule) == "" {
		return errors.New("talkoot: the routine needs a schedule, such as \"every 15m\" or \"weekdays 07:30\"")
	}
	// 🚨 The text that the kind does not take must be empty as written, and
	// not only after a trim. The record keeps the text as written, so a task
	// with a command of spaces would otherwise store both, and a reader that
	// prefers the command would show the spaces and hide the prompt.
	if a.Kind == RoutineWatcher {
		if strings.TrimSpace(a.Command) == "" || a.Prompt != "" {
			return errors.New("talkoot: a watcher takes a command and no prompt")
		}
		if len(a.Command) > MaxRoutineCommandBytes {
			return fmt.Errorf("talkoot: the watcher's command is %d bytes, above the %d limit", len(a.Command), MaxRoutineCommandBytes)
		}
		if err := shownAsWritten(a.Command, false); err != nil {
			return fmt.Errorf("talkoot: the watcher's command holds %w", err)
		}
	} else {
		if strings.TrimSpace(a.Prompt) == "" || a.Command != "" {
			return fmt.Errorf("talkoot: a %s takes a prompt and no command", a.Kind)
		}
		if len(a.Prompt) > MaxRoutinePromptBytes {
			return fmt.Errorf("talkoot: the routine's prompt is %d bytes, above the %d limit; keep a pointer, and let the run fetch the detail", len(a.Prompt), MaxRoutinePromptBytes)
		}
		if err := shownAsWritten(a.Prompt, true); err != nil {
			return fmt.Errorf("talkoot: the routine's prompt holds %w", err)
		}
	}
	for _, n := range []struct {
		field string
		v     *int
		max   int
	}{
		{FieldEndAfterRuns, a.EndAfterRuns, MaxRoutineEnd},
		{FieldEndAfterDays, a.EndAfterDays, MaxRoutineEnd},
		{FieldReviewsPerDay, a.ReviewsPerDay, MaxRoutineReviewsPerDay},
		{FieldFollowUps, a.FollowUps, MaxRoutineFollowUps},
	} {
		if n.v != nil && (*n.v < 0 || *n.v > n.max) {
			return fmt.Errorf("talkoot: %s is %d, and it must be from 0 to %d", n.field, *n.v, n.max)
		}
	}
	if v := a.SpendUSDPerDay; v != nil && (*v < 0 || *v > MaxRoutineSpendUSDPerDay || *v != *v) {
		return fmt.Errorf("talkoot: %s is %v, and it must be from 0 to %d", FieldSpendUSDPerDay, *v, MaxRoutineSpendUSDPerDay)
	}
	return nil
}

// shownAsWritten refuses text that a card cannot show as it is stored.
//
// 🚨 The card shows a routine's command or prompt as written, and a person
// approves what they read. So every character must be one that a reader
// sees: a letter, a mark, a number, a punctuation mark, a symbol, or a
// space. That leaves out four kinds.
//
//   - A control character can hide a part of the text from a terminal or a
//     browser.
//   - A format character does the same with no escape sequence. A
//     bidirectional override puts the text in another order, and a tag
//     character holds text that no reader sees and a model still reads.
//   - A code point that Unicode has not assigned, or keeps for private use,
//     shows as nothing or as a box.
//   - Some letters and marks show as nothing by their own definition: the
//     variation selectors, the fillers, and the vowels and selectors that
//     isBlank lists.
//
// A line break and a tab pass. prose also passes three characters that
// Persian, the Indic scripts and many emoji need, and only where a script
// puts them:
//
//   - a joiner, U+200C or U+200D, between two characters that a reader sees,
//   - a text or emoji selector, U+FE0E or U+FE0F, after one such character.
//
// A run of them, or one that stands alone, is refused. Such a run shows
// nothing on the card, and it can hold text in a code that a model reads. A
// command takes none of them, and no space but the plain one: another space
// looks the same and does not split a shell's words.
//
// ⚠️ This is no proof that a card shows all of a text. A joiner between each
// pair of letters still passes. The check takes away the cheap channels, and
// the person's reading of the card stays the control.
func shownAsWritten(text string, prose bool) error {
	if !utf8.ValidString(text) {
		return errors.New("bytes that are not UTF-8")
	}
	unseen := func(r rune) error { return fmt.Errorf("a character that a reader does not see, U+%04X", r) }
	rs := []rune(text)
	// seen reports whether the character at i is one that a reader sees, so
	// a joiner or a selector may stand beside it.
	seen := func(i int) bool {
		if i < 0 || i >= len(rs) {
			return false
		}
		r := rs[i]
		return r != ' ' && unicode.IsPrint(r) && !isBlank(r)
	}
	for i, r := range rs {
		switch {
		case r == '\n' || r == '\t' || r == ' ':
		case isJoiner(r):
			// A selector may stand between the character and the joiner,
			// as in an emoji sequence.
			before := i - 1
			if before >= 0 && (rs[before] == '\ufe0e' || rs[before] == '\ufe0f') {
				before--
			}
			if !prose || !seen(before) || !seen(i+1) {
				return unseen(r)
			}
		case r == '\ufe0e' || r == '\ufe0f':
			if !prose || !seen(i-1) {
				return unseen(r)
			}
		case isBlank(r):
			return unseen(r)
		case unicode.IsControl(r):
			return fmt.Errorf("a control character, U+%04X", r)
		case unicode.Is(unicode.Zs, r):
			if !prose {
				return fmt.Errorf("a space that is not the plain space, U+%04X", r)
			}
		case !unicode.IsPrint(r):
			// The format characters, the separators of lines and
			// paragraphs, the private use, and what is not assigned.
			return unseen(r)
		}
	}
	return nil
}

func isJoiner(r rune) bool { return r == '\u200c' || r == '\u200d' }

// isBlank reports a character that is a letter, a mark or a symbol by its
// category and shows as nothing. The category test does not find these, so
// they are listed: the variation selectors of both blocks and the Mongolian
// ones, the combining grapheme joiner, the Khmer inherent vowels, the Hangul
// fillers, and the Braille blank. Unicode names most of them in its
// Default_Ignorable_Code_Point property.
func isBlank(r rune) bool {
	switch {
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return true
	case r >= 0x180b && r <= 0x180f:
		return true
	case r == 0x17b4 || r == 0x17b5:
		return true
	}
	switch r {
	case 0x034f, 0x115f, 0x1160, 0x3164, 0xffa0, 0x2800:
		return true
	}
	return false
}

// Resolve gives every limit its value: what the ask set, and the default of
// the kind for the rest. diffs names each limit that the ask set to another
// value than the default, in the order a card lists them.
func (a RoutineAsk) Resolve(def RoutineFields) (f RoutineFields, diffs []RoutineDiff) {
	f = def
	diff := func(field, value, was string) {
		if value != was {
			diffs = append(diffs, RoutineDiff{Field: field, Value: value, Default: was})
		}
	}
	if a.EndAfterRuns != nil {
		f.EndAfterRuns = *a.EndAfterRuns
	}
	if a.EndAfterDays != nil {
		f.EndAfterDays = *a.EndAfterDays
	}
	if a.SpendUSDPerDay != nil {
		f.SpendUSDPerDay = *a.SpendUSDPerDay
	}
	if a.ReviewsPerDay != nil {
		f.ReviewsPerDay = *a.ReviewsPerDay
	}
	if a.FollowUps != nil {
		f.FollowUps = *a.FollowUps
	}
	if a.CatchUp != nil {
		f.CatchUp = *a.CatchUp
	}
	diff(FieldEndAfterRuns, fmt.Sprint(f.EndAfterRuns), fmt.Sprint(def.EndAfterRuns))
	diff(FieldEndAfterDays, fmt.Sprint(f.EndAfterDays), fmt.Sprint(def.EndAfterDays))
	// 🚨 The spend is compared as a number. Text of two decimals would show
	// 5.004 as the default 5.00, and the card would then hide a limit that
	// the member changed.
	if f.SpendUSDPerDay != def.SpendUSDPerDay {
		diffs = append(diffs, RoutineDiff{Field: FieldSpendUSDPerDay, Value: usd(f.SpendUSDPerDay), Default: usd(def.SpendUSDPerDay)})
	}
	diff(FieldReviewsPerDay, fmt.Sprint(f.ReviewsPerDay), fmt.Sprint(def.ReviewsPerDay))
	diff(FieldFollowUps, fmt.Sprint(f.FollowUps), fmt.Sprint(def.FollowUps))
	diff(FieldCatchUp, fmt.Sprint(f.CatchUp), fmt.Sprint(def.CatchUp))
	return f, diffs
}

// Set lists each limit that the ask names, with its value and no default. A
// card shows these when no default is known to compare against.
func (a RoutineAsk) Set() []RoutineDiff {
	var out []RoutineDiff
	add := func(field, value string) { out = append(out, RoutineDiff{Field: field, Value: value}) }
	if a.EndAfterRuns != nil {
		add(FieldEndAfterRuns, fmt.Sprint(*a.EndAfterRuns))
	}
	if a.EndAfterDays != nil {
		add(FieldEndAfterDays, fmt.Sprint(*a.EndAfterDays))
	}
	if a.SpendUSDPerDay != nil {
		add(FieldSpendUSDPerDay, usd(*a.SpendUSDPerDay))
	}
	if a.ReviewsPerDay != nil {
		add(FieldReviewsPerDay, fmt.Sprint(*a.ReviewsPerDay))
	}
	if a.FollowUps != nil {
		add(FieldFollowUps, fmt.Sprint(*a.FollowUps))
	}
	if a.CatchUp != nil {
		add(FieldCatchUp, fmt.Sprint(*a.CatchUp))
	}
	return out
}

// Digest names what a card of the ask shows: the proposal, its proposer, the
// ask as written, and every limit as an approval would store it. A person
// approves a card by this value, so an approval is for the card that the
// person read and for no later one.
func (a RoutineAsk) Digest(proposal, proposer string, f RoutineFields) string {
	raw, err := json.Marshal(struct {
		Proposal string        `json:"proposal"`
		Proposer string        `json:"proposer"`
		Ask      RoutineAsk    `json:"ask"`
		Fields   RoutineFields `json:"fields"`
	}{proposal, proposer, a, f})
	if err != nil {
		// A value that does not marshal has no digest, and none matches it.
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// usd writes an amount with two decimals, or with every digit it has when two
// do not hold it. Two amounts that differ then never read the same.
func usd(v float64) string {
	short := strconv.FormatFloat(v, 'f', 2, 64)
	if back, err := strconv.ParseFloat(short, 64); err == nil && back == v {
		return short
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Summary is the one line that the room's envelope carries for the ask.
func (a RoutineAsk) Summary() string {
	what := a.Prompt
	if a.Kind == RoutineWatcher {
		what = a.Command
	}
	what = strings.Join(strings.Fields(what), " ")
	if len(what) > 120 {
		what = strings.ToValidUTF8(what[:120], "") + "..."
	}
	return fmt.Sprintf("routine: a %s, %s: %s", a.Kind, strings.Join(strings.Fields(a.Schedule), " "), what)
}

// Routine is a routine that a person approved. Its id is the id of the
// proposal that asked for it, and the host's timer has the same id.
type Routine struct {
	ID string `json:"id"`
	// Member owns the routine. A run reports to it (decision 0029 rule 9).
	Member   string `json:"member"`
	Kind     string `json:"kind"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt,omitempty"`
	Command  string `json:"command,omitempty"`
	// Fields holds every limit as the approval froze it, the ones the card
	// hid included.
	Fields RoutineFields `json:"fields"`
	// EndsAt is the end that EndAfterDays gives, counted from the approval.
	EndsAt time.Time `json:"ends_at,omitzero"`
	Status string    `json:"status"`
	// ApprovedBy is human: and the name of the person who approved it.
	ApprovedBy string    `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
	// ChangedBy, ChangedAt and Reason describe the last pause or resume.
	ChangedBy string    `json:"changed_by,omitempty"`
	ChangedAt time.Time `json:"changed_at,omitzero"`
	Reason    string    `json:"reason,omitempty"`
	// Runs counts the runs that began. EndAfterRuns reads it.
	Runs int `json:"runs,omitempty"`
}

// NewRoutine builds the routine that an approval of ask stores. def holds the
// defaults of the ask's kind, read when the person approves.
func NewRoutine(id, member string, ask RoutineAsk, schedule string, def RoutineFields, by string, now time.Time) Routine {
	f, _ := ask.Resolve(def)
	r := Routine{
		ID: id, Member: member, Kind: ask.Kind, Schedule: schedule, Prompt: ask.Prompt, Command: ask.Command,
		Fields: f, Status: RoutineActive, ApprovedBy: by, ApprovedAt: now,
	}
	if f.EndAfterDays > 0 {
		r.EndsAt = now.AddDate(0, 0, f.EndAfterDays)
	}
	return r
}

// Check refuses a stored routine that no approval could have stored: a text
// its kind does not take, a character that a card cannot show, or a limit
// outside its range. It runs the checks of an ask on what the record holds.
//
// 🚨 The record is a file, and a hand can edit it. Every step that gives a
// routine a timer reads it through LoadRoutine, which calls this.
func (r Routine) Check() error {
	f := r.Fields
	return RoutineAsk{
		Kind: r.Kind, Schedule: r.Schedule, Prompt: r.Prompt, Command: r.Command,
		EndAfterRuns: &f.EndAfterRuns, EndAfterDays: &f.EndAfterDays, SpendUSDPerDay: &f.SpendUSDPerDay,
		ReviewsPerDay: &f.ReviewsPerDay, FollowUps: &f.FollowUps, CatchUp: &f.CatchUp,
	}.Check()
}

// ErrRoutineDamaged reports a routine's record that exists and does not read.
var ErrRoutineDamaged = errors.New("talkoot: the record of the routine does not read")

// ErrNoRoutine reports an id that names no routine of the talkoot.
var ErrNoRoutine = errors.New("talkoot: no such routine")

func routinePath(dir, id string) (string, error) {
	// 🚨 The id is joined onto a path. A routine has the id of its proposal,
	// so the same pattern keeps it inside the directory.
	if !ValidProposalID(id) {
		return "", fmt.Errorf("%w: %q is not a routine id", ErrNoRoutine, id)
	}
	return filepath.Join(dir, RoutinesDir, id+".json"), nil
}

// SaveRoutine writes a routine, replacing the one with its id.
func SaveRoutine(dir string, r Routine) error {
	path, err := routinePath(dir, r.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("talkoot: %w", err)
	}
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("talkoot: routines: %w", err)
	}
	if err := privfs.WriteFile(path, raw); err != nil {
		return fmt.Errorf("talkoot: save routine: %w", err)
	}
	return nil
}

// LoadRoutine reads one routine.
func LoadRoutine(dir, id string) (Routine, error) {
	path, err := routinePath(dir, id)
	if err != nil {
		return Routine{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Routine{}, fmt.Errorf("%w: %s", ErrNoRoutine, id)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("talkoot: read routine: %w", err)
	}
	var r Routine
	if err := json.Unmarshal(raw, &r); err != nil {
		return Routine{}, fmt.Errorf("%w: %s: %v", ErrRoutineDamaged, id, err)
	}
	// 🚨 A record that a hand edited must not start a timer for a routine
	// that no approval could have stored. The id and the state are checked
	// here, and Check reads the rest as an approval reads an ask.
	switch {
	case r.ID != id:
		return Routine{}, fmt.Errorf("%w: %s names itself %q", ErrRoutineDamaged, id, r.ID)
	case !ValidRoutineKind(r.Kind):
		return Routine{}, fmt.Errorf("%w: %s has the kind %q", ErrRoutineDamaged, id, r.Kind)
	case r.Status != RoutineActive && r.Status != RoutinePaused && r.Status != RoutineEnded:
		return Routine{}, fmt.Errorf("%w: %s has the status %q", ErrRoutineDamaged, id, r.Status)
	case r.Member == "" || r.ApprovedBy == "":
		return Routine{}, fmt.Errorf("%w: %s names no member or no person who approved it", ErrRoutineDamaged, id)
	}
	if err := r.Check(); err != nil {
		return Routine{}, fmt.Errorf("%w: %s: %v", ErrRoutineDamaged, id, err)
	}
	if r.Runs < 0 {
		return Routine{}, fmt.Errorf("%w: %s counts %d runs", ErrRoutineDamaged, id, r.Runs)
	}
	return r, nil
}

// RemoveRoutine deletes a routine's record. A record that is already gone is
// no error.
func RemoveRoutine(dir, id string) error {
	path, err := routinePath(dir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("talkoot: remove routine: %w", err)
	}
	return nil
}

// ListRoutines returns every routine of the talkoot at dir, oldest first. A
// file that does not read is left out and named in damaged.
func ListRoutines(dir string) (list []Routine, damaged []string, err error) {
	entries, err := os.ReadDir(filepath.Join(dir, RoutinesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("talkoot: routines: %w", err)
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidProposalID(id) || !e.Type().IsRegular() {
			continue
		}
		r, err := LoadRoutine(dir, id)
		if err != nil {
			damaged = append(damaged, id)
			continue
		}
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, damaged, nil
}

// RoutineLine is the room's record of one step of a routine: approved,
// paused, resumed or deleted, by the person by. An approved line holds the
// whole routine, so the room has every limit's value, the ones the card hid
// included (decision 0029, "a default set once is authority nobody reads
// again").
func RoutineLine(r Routine, action, by, reason string, at time.Time) Line {
	l := Line{Type: LineRoutine, At: at, Action: action, Member: r.Member, Ref: r.ID, By: by, Reason: reason}
	if action == RoutineApproved {
		l.Proposal, l.Proposer, l.Routine = r.ID, r.Member, &r
	}
	return l
}
