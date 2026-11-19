package timers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// fileVersion is the format this build writes. A file with a later version
// came from a newer terva, and this build refuses it and leaves it alone.
const fileVersion = 1

type fileForm struct {
	Version int     `json:"version"`
	Timers  []Timer `json:"timers"`
}

func load(path string) ([]Timer, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timers: %w", err)
	}
	var f fileForm
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("timers: %s does not read: %w", path, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("timers: %s has version %d, and this terva reads version %d", path, f.Version, fileVersion)
	}
	seen := map[string]bool{}
	for i := range f.Timers {
		// A person may have written the file by hand.
		f.Timers[i].Schedule = f.Timers[i].Schedule.normalized()
		t := f.Timers[i]
		if !nameRE.MatchString(t.Owner) || !nameRE.MatchString(t.ID) {
			return nil, fmt.Errorf("timers: %s holds a timer with the owner %q and the id %q", path, t.Owner, t.ID)
		}
		if err := t.Schedule.check(); err != nil {
			return nil, fmt.Errorf("timers: %s, timer %s: %w", path, key(t.Owner, t.ID), err)
		}
		if seen[key(t.Owner, t.ID)] {
			return nil, fmt.Errorf("timers: %s holds the timer %s twice", path, key(t.Owner, t.ID))
		}
		seen[key(t.Owner, t.ID)] = true
	}
	return f.Timers, nil
}

// save replaces the file in one step. A reader, or a daemon that starts while
// this one writes, sees the old file or the new one and never a part of one.
// The file is private, because an owner's data can name what a person asked
// the agent to watch.
func save(path string, list []Timer) error {
	b, err := json.MarshalIndent(fileForm{Version: fileVersion, Timers: list}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return steady("create a file beside", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return steady("write", path, err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return steady("write", path, err)
	}
	// The mark that a firing is in flight has to outlive a crash, so it goes
	// to the disk before the rename makes it the record.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return steady("write", path, err)
	}
	if err := tmp.Close(); err != nil {
		return steady("write", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return steady("replace", path, err)
	}
	return nil
}

// steady names a failed step by the timers file and not by the temporary file
// beside it. That file has a new random name at every save. The service shows
// this text as its problem and reports a change of it, so a name that changed
// each time would make one lasting problem look like a new one at every poll.
func steady(step, path string, err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &le):
		err = le.Err
	case errors.As(err, &pe):
		err = pe.Err
	}
	return fmt.Errorf("%s %s: %w", step, path, err)
}
