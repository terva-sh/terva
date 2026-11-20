package privfs

import (
	"errors"
	"testing"
	"time"
)

// The rule of replace, on a rename that the test controls. The real refusal
// is in replace_windows_test.go, which only a Windows machine runs.
func TestReplaceTriesAgainWhileTheFileIsBusy(t *testing.T) {
	errBusy, errGone := errors.New("another handle holds the file"), errors.New("no such directory")
	busy := func(err error) bool { return errors.Is(err, errBusy) }
	for name, c := range map[string]struct {
		errs  []error // what each rename returns, and the last one repeats
		wait  time.Duration
		want  error
		calls int // 0 means more than one
	}{
		"a rename that works runs one time":           {errs: []error{nil}, wait: time.Minute, calls: 1},
		"a busy file is tried until it is free":       {errs: []error{errBusy, errBusy, nil}, wait: time.Minute, calls: 3},
		"an error that time does not end is returned": {errs: []error{errGone}, wait: time.Minute, want: errGone, calls: 1},
		"a file that stays busy fails after the wait": {errs: []error{errBusy}, wait: 20 * time.Millisecond, want: errBusy},
		"an error after a busy one ends the tries":    {errs: []error{errBusy, errGone}, wait: time.Minute, want: errGone, calls: 2},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			rename := func(from, to string) error {
				if from != "tmp" || to != "path" {
					t.Errorf("rename(%q, %q), want the two names of replace", from, to)
				}
				err := c.errs[min(calls, len(c.errs)-1)]
				calls++
				return err
			}
			start := time.Now()
			if err := replaceWith(rename, busy, c.wait, "tmp", "path"); !errors.Is(err, c.want) {
				t.Fatalf("replaceWith returned %v, want %v", err, c.want)
			}
			if c.calls != 0 && calls != c.calls {
				t.Fatalf("rename ran %d times, want %d", calls, c.calls)
			}
			if c.calls == 0 && calls < 2 {
				t.Fatalf("rename ran %d times, want a second try in the wait", calls)
			}
			if took := time.Since(start); took > c.wait+5*time.Second {
				t.Fatalf("replaceWith took %s with a wait of %s", took, c.wait)
			}
		})
	}
}

// No rename of this system waits for a reader, except on Windows.
func TestOnlyWindowsHasABusyRename(t *testing.T) {
	if replaceBusy(errors.New("an error of no system call")) {
		t.Fatal("an error with no number of the system is not a busy file")
	}
}
