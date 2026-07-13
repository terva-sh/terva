package swarm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/privfs"
)

// Event is one structured datum in an agent's durable event log.
//
// The shape is intentionally a thin map+timestamp wrapper rather
// than a strict struct so the swarm package doesn't have to track
// every field added to core.AgentEvent. The child writes
// `modes.EventToJSON(ev)` plus a few swarm-internal types
// (lifecycle, user_input echo, error). The supervisor doesn't
// interpret most fields; it just keeps them for replay and lifts
// a few well-known ones into AgentSnapshot (Activity, Status,
// Tail) for the dashboard.
//
// The on-disk file (events.jsonl) is append-only, one event per
// line, newline-terminated JSON. Reading is forward-only; readers
// stat the file size and read from their last offset on every
// poll.
type Event struct {
	Time time.Time      `json:"time"`
	Type string         `json:"type"`
	Data map[string]any `json:"-"`
	Raw  map[string]any `json:"-"` // includes type+time+data for replay
}

// MarshalJSON flattens Data into the top-level object so consumers
// see {type, time, ...fields} rather than {type, time, data:{...}}.
func (e Event) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(e.Data)+2)
	for k, v := range e.Data {
		out[k] = v
	}
	out["type"] = e.Type
	out["time"] = e.Time
	return json.Marshal(out)
}

// UnmarshalJSON accepts a flat object with at least type+time.
// All other fields land in both Data and Raw.
func (e *Event) UnmarshalJSON(b []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	e.Raw = m
	e.Data = map[string]any{}
	for k, v := range m {
		if k == "type" || k == "time" {
			continue
		}
		e.Data[k] = v
	}
	if t, ok := m["type"].(string); ok {
		e.Type = t
	} else {
		return errors.New("swarm event: missing type")
	}
	if ts, ok := m["time"].(string); ok {
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err == nil {
			e.Time = parsed
		}
	}
	return nil
}

// EventLog is an append-only writer for events.jsonl. Safe for
// concurrent writers (the child only has one writer, but tests
// occasionally fan out).
type EventLog struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// OpenEventLog opens (or creates) the events.jsonl file at path.
// Parent directories are created as needed.
func OpenEventLog(path string) (*EventLog, error) {
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("event log dir: %w", err)
	}
	// The event log carries full tool output — session-transcript-grade
	// data — so it is user-private like the child's session file.
	f, err := privfs.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return nil, fmt.Errorf("event log open: %w", err)
	}
	return &EventLog{path: path, f: f}, nil
}

// Append writes one event. The encoding is `<json>\n`. Concurrent
// callers are serialised; small enough events never need partial
// writes since unix guarantees atomicity for writes ≤ PIPE_BUF on
// regular files, and we hold a per-process mutex on top.
func (l *EventLog) Append(ev Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = l.f.Write(b)
	return err
}

// Close flushes and closes the underlying file.
func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// NewEvent is a convenience constructor that fills the timestamp.
func NewEvent(typ string, data map[string]any) Event {
	if data == nil {
		data = map[string]any{}
	}
	return Event{Time: time.Now(), Type: typ, Data: data}
}

// ReadEventLog parses every event currently in the file. Used by
// the dashboard on first open and by tests; live tailing uses
// FollowEventLog below.
func ReadEventLog(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return readEvents(f)
}

func readEvents(r io.Reader) ([]Event, error) {
	br := bufio.NewReader(r)
	var out []Event
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var ev Event
			if jerr := json.Unmarshal(line, &ev); jerr == nil {
				if !isLikelyDoubleWrite(ev, out) {
					out = append(out, ev)
				}
			}
			// Malformed lines are skipped silently; the dashboard
			// renders only well-formed events and the child is the
			// only writer.
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
	}
}

// isLikelyDoubleWrite reports whether ev is a back-to-back duplicate
// of the most recent event in `tail` — same type and identical Data
// payload, with timestamps within a small window (the supervisor and
// the child's mirror used to BOTH write each event to disk, landing
// the same content twice in quick succession in events.jsonl). The
// historical behaviour was fixed at write time (the child's mirror
// is now dormant unless the supervisor is gone), but on-disk files
// from before the fix are still polluted, so we dedupe defensively
// at read time too.
//
// We deliberately bound by time (250ms) so two genuinely identical
// adjacent events that happen seconds apart — e.g. an agent that
// runs the same tool twice in a row — still both render.
func isLikelyDoubleWrite(ev Event, tail []Event) bool {
	if len(tail) == 0 {
		return false
	}
	prev := tail[len(tail)-1]
	if prev.Type != ev.Type {
		return false
	}
	if !ev.Time.IsZero() && !prev.Time.IsZero() {
		dt := ev.Time.Sub(prev.Time)
		if dt < 0 {
			dt = -dt
		}
		if dt > 250*time.Millisecond {
			return false
		}
	}
	return sameEventData(prev.Data, ev.Data)
}

// sameEventData deep-compares two event payloads. Cheap because the
// payloads are small map[string]any trees built from JSON, and only
// called for adjacent same-type pairs.
func sameEventData(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// EventFollower polls an events.jsonl file and emits new events as
// they're appended. The supervisor uses one per agent so dashboard
// snapshots stay current without reparsing the whole file on every
// frame. Close stops the goroutine; the channel returned by Events
// is closed when Close is called or the file is removed.
type EventFollower struct {
	path     string
	interval time.Duration
	out      chan Event
	done     chan struct{}
	closed   chan struct{}
	once     sync.Once
}

// FollowEventLog starts polling path every interval. interval ≤ 0
// defaults to 50ms which is invisible to the user and cheap on
// disk (one stat + one short read per agent per tick).
func FollowEventLog(path string, interval time.Duration) *EventFollower {
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	f := &EventFollower{
		path:     path,
		interval: interval,
		out:      make(chan Event, 256),
		done:     make(chan struct{}),
		closed:   make(chan struct{}),
	}
	go f.loop()
	return f
}

// Events returns the receive end of the event stream.
func (f *EventFollower) Events() <-chan Event { return f.out }

// Close stops polling and closes the events channel. It waits for
// the polling goroutine to exit so tests on Windows can remove the
// temp directory without racing an open file handle.
func (f *EventFollower) Close() {
	f.once.Do(func() {
		close(f.done)
		<-f.closed
	})
}

func (f *EventFollower) loop() {
	defer close(f.closed)
	defer close(f.out)
	var offset int64
	tick := time.NewTicker(f.interval)
	defer tick.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-tick.C:
		}
		fi, err := os.Stat(f.path)
		if err != nil {
			continue
		}
		if fi.Size() <= offset {
			continue
		}
		fh, err := os.Open(f.path)
		if err != nil {
			continue
		}
		if _, err := fh.Seek(offset, io.SeekStart); err != nil {
			_ = fh.Close()
			continue
		}
		evs, _ := readEvents(fh)
		newOffset, _ := fh.Seek(0, io.SeekCurrent)
		_ = fh.Close()
		offset = newOffset
		for _, ev := range evs {
			select {
			case f.out <- ev:
			case <-f.done:
				return
			}
		}
	}
}
