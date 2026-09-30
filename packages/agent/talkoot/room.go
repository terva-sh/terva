package talkoot

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	// LineSeat binds a member to what runs it: Member, and in Ref the session
	// id of a native member or the swarm id of a worker member. An empty Ref
	// unbinds it. The workspace writes these; the router ignores them.
	LineSeat = "seat"
	// LineRoster records a change to the roster: By, the person who made or
	// approved it, and the sha256 of the new talkoot.md in Ref. Changes holds
	// each member before and after, and a change from a proposal names it in
	// Proposal and its proposer in Proposer. Edited says the person replaced
	// the proposal's operations with their own, so Changes are not what the
	// proposer asked for. A line from before those fields has only By and
	// Ref. The router ignores it.
	LineRoster = "roster"
	// LineAnswer records a person's answer to a member's question: Member,
	// the member that asked, the answer's own id in Ref, and each question
	// and its answer in Answers (answer.go). A member cites it as
	// answer:<id>.
	LineAnswer = "answer"
	// LineIntro is a member's plain introduction card: Member, and the card
	// in Text, which the workspace builds from the member's entry. The
	// coordinator reads it as a note (intro.go).
	LineIntro = "intro"
	// LineToolError, LineRetry, LineCardOpen, and LineCardClose are signals:
	// things that happened to a member, which a reader of the room replays,
	// such as the expression engine (signals.go). The router ignores them.
	LineToolError = "tool_error"
	LineRetry     = "retry"
	LineCardOpen  = "card_open"
	LineCardClose = "card_close"
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
	// about, or an answer line's own id. Notes counts the notes a delivery
	// carried.
	Ref   string `json:"ref,omitempty"`
	Notes int    `json:"notes,omitempty"`
	// Proposal, Proposer, Edited, and Changes are set on a roster line.
	Proposal string         `json:"proposal,omitempty"`
	Proposer string         `json:"proposer,omitempty"`
	Edited   bool           `json:"edited,omitempty"`
	Changes  []MemberChange `json:"changes,omitempty"`
	// ColorBefore and ColorAfter are set on a roster line that changed the
	// team colour: the whole colour before and after, with the default
	// filled in, so a reset still names the colour it shows.
	ColorBefore string `json:"color_before,omitempty"`
	ColorAfter  string `json:"color_after,omitempty"`
	// Answers is set on an answer line.
	Answers []Answered `json:"answers,omitempty"`
	// Text is set on an intro line.
	Text string `json:"text,omitempty"`
	// Tool, Attempt, Card, and Outcome are set on a signal line (signals.go).
	Tool    string `json:"tool,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Card    string `json:"card,omitempty"`
	Outcome string `json:"outcome,omitempty"`
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
	// The page index. The first scan builds it, and each append keeps it.
	// entries counts the entries the file's lines make, lineNo is the number
	// of the file's last line, and status holds the entries a scan adds after
	// them for the head. stale sends the next Page to a full scan, which
	// rebuilds the index: an append failed, or could not learn where its line
	// landed, or a checkpoint no longer starts a line.
	checkpoints []checkpoint
	entries     int
	lineNo      int
	status      []Line
	indexed     bool
	stale       bool
}

// roomCheckpointEvery is how many entries lie between two checkpoints. A page
// reads at most this many lines before its own.
const roomCheckpointEvery = 64

// checkpoint is a place a page can start to verify from. The entry at index
// entry is file line lineNo, starts at byte off, and seals to good. sum is the
// SHA-256 of the line, which tells a page that the line at off is still this
// one.
type checkpoint struct {
	entry, lineNo int
	off           int64
	good          string
	sum           [sha256.Size]byte
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
	// The line starts at the file's size, past the newline that ends a torn
	// line. os.File leaves Seek on an append-mode file unspecified, so the
	// size comes from Stat, under r.mu.
	lead, size := 0, int64(-1)
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
		last := make([]byte, 1)
		if size > 0 {
			if _, err := f.ReadAt(last, size-1); err == nil && last[0] != '\n' {
				b = append([]byte{'\n'}, b...)
				lead = 1
			}
		}
	}
	if _, err := f.Write(b); err != nil {
		// Some of the bytes may be on disk, so the index no longer knows where
		// the lines are.
		r.stale = true
		_ = f.Close()
		return fmt.Errorf("talkoot: room: %w", err)
	}
	if err := f.Close(); err != nil {
		r.stale = true
		return fmt.Errorf("talkoot: room: %w", err)
	}
	if size < 0 {
		// Nothing says where the line landed.
		r.stale = true
		size = 0
	}
	r.indexAppendLocked(size+int64(lead), r.last, b[lead:len(b)-1])
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
// at the end of the chain, where the next Append continues. The first scan
// also builds the page index.
func (r *Room) scanLocked() ([]Line, error) {
	k := *r.key
	h, headWhy := readHead(r.dir, k)
	var out []Line
	var cps []checkpoint
	good, count, lineNo := "", 0, 0
	sawHead := h.Count == 0
	f, err := os.Open(r.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("talkoot: room: %w", err)
	default:
		defer f.Close()
		err := roomLines(f, 0, 0, func(n int, start int64, raw []byte) bool {
			lineNo = n
			if len(raw) == 0 {
				return true
			}
			if len(out)%roomCheckpointEvery == 0 {
				cps = append(cps, checkpoint{entry: len(out), lineNo: n, off: start, good: good, sum: sha256.Sum256(raw)})
			}
			l, mac, valid := verifyRoomLine(k, good, raw, n)
			// The head names the last line written. A line that claims its
			// MAC is that line, even when its seal fails, so the end was not
			// cut: the failure is already reported for the line itself.
			if mac != "" && mac == h.Last {
				sawHead = true
			}
			if valid {
				good = mac
				count++
			}
			out = append(out, l)
			return true
		})
		if err != nil {
			return out, fmt.Errorf("talkoot: room: %w", err)
		}
	}
	lines := len(out)
	switch {
	case headWhy != "":
		out = append(out, damagedLine("%s", headWhy))
	case !sawHead:
		out = append(out, damagedLine("the room ends before the last line the router wrote, so lines were cut from its end"))
	case count < h.Count:
		out = append(out, damagedLine("%d of the %d lines the router wrote are missing or fail their seal", h.Count-count, h.Count))
	}
	// 🚨 Only the first scan sets where the next line goes. After that, the
	// router's own last line is the truth. Adopting the file as it reads now
	// would let the next append seal over a cut and make it permanent.
	if !r.scanned {
		r.scanned, r.last, r.count = true, good, count
	}
	// The index follows the file as it reads now. It only says where lines
	// are, so rebuilding it after the first scan moves no append position.
	if !r.indexed || r.stale {
		r.indexed, r.stale = true, false
		r.checkpoints, r.entries, r.lineNo = cps, lines, lineNo
		r.status = append([]Line(nil), out[lines:]...)
	}
	return out, nil
}

// indexAppendLocked records raw, the line an append wrote at off, sealed to
// good.
//
// 🔑 The entries a scan adds for the head go with the first append: that
// append rewrites the head to name its own line, and a scan from then on
// reports no cut and no missing line. So Page reports what Read would.
func (r *Room) indexAppendLocked(off int64, good string, raw []byte) {
	r.lineNo++
	if r.entries%roomCheckpointEvery == 0 {
		r.checkpoints = append(r.checkpoints, checkpoint{entry: r.entries, lineNo: r.lineNo, off: off, good: good, sum: sha256.Sum256(raw)})
	}
	r.entries++
	r.status = nil
}

// Page returns up to limit entries of what Read returns: the newest ones
// before entry before, or the newest of all when before is 0 or past the end.
// It also returns the index of the first entry, and how many entries Read
// would return.
//
// 🔑 A page reads the file from the checkpoint at or before its first entry,
// so it costs its own lines and fewer than roomCheckpointEvery more, not the
// whole room. Each line it returns is still verified against its seal.
func (r *Room) Page(before, limit int) ([]Line, int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLocked(); err != nil {
		return nil, 0, 0, err
	}
	if !r.indexed || r.stale {
		// The scan that builds the index has read every entry already.
		return r.scanPageLocked(before, limit)
	}
	total := r.entries + len(r.status)
	start, end := pageBounds(total, before, limit)
	var out []Line
	if fileEnd := min(end, r.entries); start < fileEnd {
		lines, err := r.readEntriesLocked(start, fileEnd)
		if errors.Is(err, errCheckpointMoved) {
			r.stale = true
			return r.scanPageLocked(before, limit)
		}
		if err != nil {
			return nil, 0, 0, err
		}
		out = lines
	}
	for i := max(start, r.entries); i < end; i++ {
		out = append(out, r.status[i-r.entries])
	}
	return out, start, total, nil
}

// scanPageLocked serves a page from a full scan, which also rebuilds the index.
func (r *Room) scanPageLocked(before, limit int) ([]Line, int, int, error) {
	all, err := r.scanLocked()
	if err != nil {
		return nil, 0, 0, err
	}
	start, end := pageBounds(len(all), before, limit)
	return all[start:end], start, len(all), nil
}

// errCheckpointMoved says a checkpoint's offset no longer starts its line: a
// line before it changed length, or lines were added or cut before it, since
// the index was built.
var errCheckpointMoved = errors.New("talkoot: room: a checkpoint no longer starts its line")

// pageBounds is the page of limit entries that ends before before, out of
// total.
func pageBounds(total, before, limit int) (int, int) {
	end := total
	if before > 0 && before < end {
		end = before
	}
	return max(0, end-max(limit, 0)), end
}

// readEntriesLocked verifies and returns the entries the file's lines make
// from index start up to end, from the checkpoint at or before start.
func (r *Room) readEntriesLocked(start, end int) ([]Line, error) {
	i := sort.Search(len(r.checkpoints), func(i int) bool { return r.checkpoints[i].entry > start }) - 1
	if i < 0 {
		return nil, fmt.Errorf("talkoot: room: no checkpoint before entry %d", start)
	}
	cp := r.checkpoints[i]
	f, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("talkoot: room: %w", err)
	}
	defer f.Close()
	// 🚨 A line before the checkpoint that changed length, or a line added or
	// cut there, moves every line after it. The old offset then starts inside
	// a line, or at the start of another one, so the page checks both and goes
	// back to a full scan instead.
	if cp.off > 0 {
		prev := make([]byte, 1)
		if _, err := f.ReadAt(prev, cp.off-1); err != nil || prev[0] != '\n' {
			return nil, errCheckpointMoved
		}
	}
	if _, err := f.Seek(cp.off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("talkoot: room: %w", err)
	}
	k := *r.key
	good, e := cp.good, cp.entry
	out := make([]Line, 0, end-start)
	moved := false
	err = roomLines(f, cp.off, cp.lineNo-1, func(n int, _ int64, raw []byte) bool {
		if len(raw) == 0 {
			return true
		}
		if e == cp.entry && sha256.Sum256(raw) != cp.sum {
			moved = true
			return false
		}
		l, mac, valid := verifyRoomLine(k, good, raw, n)
		if valid {
			good = mac
		}
		if e >= start {
			out = append(out, l)
		}
		e++
		return e < end
	})
	if moved {
		return nil, errCheckpointMoved
	}
	if err != nil {
		return out, fmt.Errorf("talkoot: room: %w", err)
	}
	// 🚨 The file lost lines this daemon read or wrote. A short page would
	// hide that, so the page says so.
	if len(out) < end-start {
		out = append(out, damagedLine("the room ends before entry %d, which this daemon read or wrote, so lines were cut from it", start+len(out)))
	}
	return out, nil
}

// damagedLine is the entry that stands for a line, or a part of the room,
// that does not verify.
func damagedLine(format string, a ...any) Line {
	return Line{Type: LineDamaged, Reason: fmt.Sprintf(format, a...)}
}

// verifyRoomLine checks one line of the room against good, the MAC of the last
// line that verified. It returns the line's entry, the MAC the line claims,
// and whether its seal verifies, which moves the chain on even when the body
// does not parse.
func verifyRoomLine(k roomKey, good string, raw []byte, n int) (Line, string, bool) {
	body, mac, ok := unseal(raw)
	if !ok {
		return damagedLine("line %d of the room carries no seal", n), "", false
	}
	// 🚨 A line verifies only against the last line that verified. Accepting
	// the MAC a failed line claimed would let a member replay any genuine pair
	// of lines: a copy of the line before a person's resume, then the resume.
	// After a failure, nothing later is trusted until the router writes again,
	// and it writes on from the last line that verified.
	if !hmac.Equal([]byte(mac), []byte(k.lineMAC(good, body))) {
		return damagedLine("line %d of the room fails its seal", n), mac, false
	}
	var l Line
	if json.Unmarshal(body, &l) != nil || l.Type == "" || l.Type == LineDamaged || l.Kid != k.id {
		return damagedLine("line %d of the room does not parse", n), mac, true
	}
	l.Kid, l.MAC = "", ""
	return l, mac, true
}

// roomLines calls fn with each line of f from byte off on, numbered on from
// n, with the byte it starts at, until fn returns false. It splits lines as
// bufio.ScanLines does, and refuses a line above 16 MiB.
func roomLines(f *os.File, off int64, n int, fn func(n int, start int64, raw []byte) bool) error {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	pos, start := off, off
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		adv, tok, err := bufio.ScanLines(data, atEOF)
		if adv > 0 {
			start, pos = pos, pos+int64(adv)
		}
		return adv, tok, err
	})
	for sc.Scan() {
		n++
		if !fn(n, start, sc.Bytes()) {
			return nil
		}
	}
	return sc.Err()
}
