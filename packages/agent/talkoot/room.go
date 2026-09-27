package talkoot

import (
	"bufio"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RoomFile is the room log inside a talkoot's directory.
const RoomFile = "room.jsonl"

// Line types in the room.
const (
	LineEnvelope = "envelope" // an envelope or a human post
	LineTurn     = "turn"     // a member's turn ended, with its cost
	LineGuard    = "guard"    // a guard tripped
	LineResume   = "resume"   // a person resumed a member, a chain, or the talkoot
	LineDelivery = "delivery" // an envelope reached a member's driver
	// LineRead records that a member's turn read a delivery: Member, the
	// chain the member works in from then on, and the envelope in Ref. A
	// delivery line names the chain itself when its driver reports no reads.
	LineRead = "read"
	// LineSeat binds a native member to its session: Member, and the session
	// id in Ref. An empty Ref unbinds it. The workspace writes these; the
	// router ignores them.
	LineSeat = "seat"
	// LineRoster records a change to the roster: By, the person who made or
	// approved it, and the sha256 of the new talkoot.md in Ref. Changes holds
	// each member before and after, and a change from a proposal names it in
	// Proposal and its proposer in Proposer. Edited says the person replaced
	// the proposal's operations with their own, so Changes are not what the
	// proposer asked for. A line from before those fields has only By and
	// Ref. The router ignores it.
	LineRoster = "roster"
	// LineDamaged is never written. Read returns it in place of a line that
	// does not parse.
	LineDamaged = "damaged"
)

// Guard actions, which say what a tripped guard did.
const (
	ActionPaused  = "paused"  // a member, a chain, or the talkoot stopped taking deliveries
	ActionRefused = "refused" // the send failed and the sender was told why
	ActionDropped = "dropped" // a duplicate was not delivered
	ActionQueued  = "queued"  // the delivery waits for a working slot
)

// Line is one entry in the room. The room holds every envelope, and also the
// accounting and the guard trips, so a restarted router rebuilds the day's
// spend, the chains, and the pauses from the room alone.
type Line struct {
	Type     string    `json:"type"`
	At       time.Time `json:"at"`
	Envelope *Envelope `json:"envelope,omitempty"`
	Member   string    `json:"member,omitempty"`
	Chain    string    `json:"chain,omitempty"`
	CostUSD  float64   `json:"cost_usd,omitempty"`
	Guard    string    `json:"guard,omitempty"`
	Action   string    `json:"action,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	By       string    `json:"by,omitempty"`
	SpendUSD float64   `json:"spend_usd,omitempty"`
	// Ref is the envelope a delivery, read, queued, or failed-delivery line is
	// about, and Notes counts the notes a delivery carried.
	Ref   string `json:"ref,omitempty"`
	Notes int    `json:"notes,omitempty"`
	// Proposal, Proposer, Edited, and Changes are set on a roster line.
	Proposal string         `json:"proposal,omitempty"`
	Proposer string         `json:"proposer,omitempty"`
	Edited   bool           `json:"edited,omitempty"`
	Changes  []MemberChange `json:"changes,omitempty"`
	// Kid names the key that sealed the line, and MAC is the seal. MAC must
	// stay the last field: the seal covers every byte before it (seal.go).
	Kid string `json:"kid,omitempty"`
	MAC string `json:"mac,omitempty"`
}

// Room appends lines to room.jsonl. It never rewrites or truncates a line,
// the way a session transcript is kept (decision 0007). Every line is sealed
// to the one before it (seal.go).
type Room struct {
	dir  string
	path string
	mu   sync.Mutex
	// key is loaded on first use. last and count describe the end of the
	// chain: the MAC the next line seals to, and how many lines verify.
	key     *roomKey
	scanned bool
	last    string
	count   int
	// observers see each line after it is sealed and written.
	observers []func(Line)
}

// OpenRoom returns the room of the talkoot in dir. The room file, its key,
// and its head record are created on first use.
func OpenRoom(dir string) *Room {
	return &Room{dir: dir, path: filepath.Join(dir, RoomFile)}
}

// Path is the room file.
func (r *Room) Path() string { return r.path }

// loadLocked loads the key. A room with no key gets one, and its first sealed
// line pauses the talkoot.
//
// 🚨 A missing key is either a new talkoot that CreateRoom never saw, or a room
// deleted with its key and its head to clear the day's spend and every pause.
// The two look the same from here, so neither passes in silence. The pause is
// a sealed line, so it holds across restarts until a person resumes.
func (r *Room) loadLocked() error {
	if r.key != nil {
		return nil
	}
	k, created, err := loadKey(r.dir)
	if err != nil {
		return err
	}
	if !created {
		r.key = &k
		return nil
	}
	had := false
	if fi, err := os.Stat(r.path); err == nil && fi.Size() > 0 {
		had = true
	}
	if err := writeHead(r.dir, k, 0, ""); err != nil {
		return fmt.Errorf("talkoot: room head: %w", err)
	}
	r.key = &k
	if _, err := r.scanLocked(); err != nil {
		return err
	}
	reason := "the room had no key, so a new one was made; a new talkoot starts here, but a talkoot that ran before has lost its spend and its pauses; check, then resume the talkoot"
	if had {
		reason = "the room's key was missing, so no line before this one can be verified; check, then resume the talkoot"
	}
	return r.appendLocked(Line{Type: LineGuard, At: time.Now(), Guard: GuardRoom, Action: ActionPaused, Reason: reason})
}

// Observe calls fn with each line after it is sealed and written, in order.
// fn runs with the room locked, so it must not block and must not call back
// into the room or into a router over it.
func (r *Room) Observe(fn func(Line)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observers = append(r.observers, fn)
}

// Append seals and writes one line. The file opens in append mode for each
// write, so no handle stays open and no write can land anywhere but the end.
//
// 🚨 A crash can leave a last line with no newline. The next line would then
// join it, and the reader would lose both. Append starts a fresh line first
// when the file does not end in one.
func (r *Room) Append(l Line) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLocked(); err != nil {
		return err
	}
	return r.appendLocked(l)
}

// appendLocked writes the head first, naming the line about to land, and
// then the line.
//
// 🚨 The head is the only record a cut cannot take with it. A line written
// without its head could be cut together with every line after it, and the
// room would still match the older head. So a head that cannot be written
// means the line is not written either, and the caller handles a failed
// append. A line that fails after its head leaves the head ahead of the
// room, which reads as a cut: a crash there pauses the talkoot, which is the
// safe side.
func (r *Room) appendLocked(l Line) error {
	if !r.scanned {
		if _, err := r.scanLocked(); err != nil {
			return err
		}
	}
	l.Kid, l.MAC = r.key.id, ""
	body, err := json.Marshal(l)
	if err != nil {
		return err
	}
	b, mac := r.key.seal(r.last, body)
	b = append(b, '\n')
	if err := writeHead(r.dir, *r.key, r.count+1, mac); err != nil {
		return fmt.Errorf("talkoot: room head: %w", err)
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("talkoot: room: %w", err)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			b = append([]byte{'\n'}, b...)
		}
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("talkoot: room: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("talkoot: room: %w", err)
	}
	r.last = mac
	r.count++
	l.Kid = ""
	for _, fn := range r.observers {
		fn(l)
	}
	return nil
}

// Read returns every line in the room that verifies. A line that does not
// comes back as a LineDamaged entry in its place, with the reason: it does not
// parse, it carries no seal, or its seal fails. A room whose end was cut off
// gets one more LineDamaged entry after its last line.
//
// 🚨 A crash can tear a line, and the rest of the room is still the record,
// so a damaged line does not stop the read. It cannot vanish either: a torn
// turn line is spend that a cap would otherwise forget.
func (r *Room) Read() ([]Line, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLocked(); err != nil {
		return nil, err
	}
	return r.scanLocked()
}

// scanLocked verifies the room from its first line and leaves last and count
// at the end of the chain, where the next Append continues.
func (r *Room) scanLocked() ([]Line, error) {
	k := *r.key
	h, headWhy := readHead(r.dir, k)
	damaged := func(format string, a ...any) Line {
		return Line{Type: LineDamaged, Reason: fmt.Sprintf(format, a...)}
	}
	var out []Line
	good, count := "", 0
	sawHead := h.Count == 0
	f, err := os.Open(r.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("talkoot: room: %w", err)
	default:
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for n := 1; sc.Scan(); n++ {
			raw := sc.Bytes()
			if len(raw) == 0 {
				continue
			}
			body, mac, ok := unseal(raw)
			if !ok {
				out = append(out, damaged("line %d of the room carries no seal", n))
				continue
			}
			// 🚨 A line verifies only against the last line that verified.
			// Accepting the MAC a failed line claimed would let a member
			// replay any genuine pair of lines: a copy of the line before a
			// person's resume, then the resume. After a failure, nothing
			// later is trusted until the router writes again, and it writes
			// on from the last line that verified.
			valid := hmac.Equal([]byte(mac), []byte(k.lineMAC(good, body)))
			// The head names the last line written. A line that claims its
			// MAC is that line, even when its seal fails, so the end was not
			// cut: the failure is already reported for the line itself.
			if mac == h.Last {
				sawHead = true
			}
			if !valid {
				out = append(out, damaged("line %d of the room fails its seal", n))
				continue
			}
			good = mac
			count++
			var l Line
			if json.Unmarshal(body, &l) != nil || l.Type == "" || l.Type == LineDamaged || l.Kid != k.id {
				out = append(out, damaged("line %d of the room does not parse", n))
				continue
			}
			l.Kid, l.MAC = "", ""
			out = append(out, l)
		}
		if err := sc.Err(); err != nil {
			return out, fmt.Errorf("talkoot: room: %w", err)
		}
	}
	switch {
	case headWhy != "":
		out = append(out, damaged("%s", headWhy))
	case !sawHead:
		out = append(out, damaged("the room ends before the last line the router wrote, so lines were cut from its end"))
	case count < h.Count:
		out = append(out, damaged("%d of the %d lines the router wrote are missing or fail their seal", h.Count-count, h.Count))
	}
	// 🚨 Only the first scan sets where the next line goes. After that, the
	// router's own last line is the truth. Adopting the file as it reads now
	// would let the next append seal over a cut and make it permanent.
	if !r.scanned {
		r.scanned, r.last, r.count = true, good, count
	}
	return out, nil
}
