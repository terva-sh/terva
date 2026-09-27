package talkoot

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind is what an envelope asks of its recipient.
type Kind string

const (
	// KindMessage wakes the recipient.
	KindMessage Kind = "message"
	// KindHandoff wakes the recipient and must carry a reference: the work
	// travels as a ticket, a branch, or a file, never as a summary.
	KindHandoff Kind = "handoff"
	// KindNote does not wake the recipient. It lands in the recipient's next
	// turn, so two polite agents cannot keep each other awake.
	KindNote Kind = "note"
	// KindAnswer replies to a question and wakes the recipient.
	KindAnswer Kind = "answer"
	// KindProposal records a roster proposal. It is addressed to no member,
	// because a person decides it, and the router alone writes one
	// (Router.Propose). A proposal is an envelope so that the sender's rate
	// limit and the chain's hop limit count it (decision 0025 rule 7).
	KindProposal Kind = "proposal"
)

// wakes reports whether an envelope of this kind starts a turn.
func (k Kind) wakes() bool { return k != KindNote && k != KindProposal }

// refKinds are the reference namespaces an envelope may carry.
//
// A path: reference names a file under the talkoot's home checkout. A note:
// reference names a file in the talkoot's notes (notes.go).
var refKinds = []string{"ticket", "path", "note", "branch", "commit", "url"}

// HumanPrefix marks a sender that is a person rather than a member. A member
// id cannot carry it, because idPattern admits no colon.
const HumanPrefix = "human:"

// Size limits for one envelope. They keep every room line well inside the
// room reader's line limit, so one envelope cannot stop the room replaying.
const (
	MaxBodyBytes = 64 * 1024
	MaxRefs      = 32
	maxRefBytes  = 512
)

// tokenPattern bounds the fields the header prints: a thread id and a
// person's name. No whitespace and no control characters, so neither can
// start a new line in the rendered text.
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// ValidPerson reports whether name can name a person in the room, without
// the human: prefix.
func ValidPerson(name string) bool { return tokenPattern.MatchString(name) }

// Envelope is one message in a talkoot. The daemon builds it: From and Chain
// come from the router, never from the sender.
type Envelope struct {
	ID      string    `json:"id"`
	Talkoot string    `json:"talkoot"`
	From    string    `json:"from"`
	To      []string  `json:"to"`
	Kind    Kind      `json:"kind"`
	Body    string    `json:"body"`
	Refs    []string  `json:"refs,omitempty"`
	Thread  string    `json:"thread,omitempty"`
	ReplyTo string    `json:"reply_to,omitempty"`
	Chain   Chain     `json:"chain"`
	At      time.Time `json:"at"`
}

// Chain ties an envelope to the human post that began its work. Root is that
// post's envelope id, and Hops counts the member envelopes since it.
type Chain struct {
	Root string `json:"root"`
	Hops int    `json:"hops"`
}

// Outgoing is what a member asks the router to send. It has no From and no
// Chain field, so a member cannot name another sender or reset its chain.
type Outgoing struct {
	To      []string
	Kind    Kind
	Body    string
	Refs    []string
	Thread  string
	ReplyTo string
}

// validateOutgoing checks the shape of a send against the roster. The guards
// are separate, in the router.
func validateOutgoing(r Roster, o Outgoing, fix remedy) error {
	switch o.Kind {
	case KindMessage, KindHandoff, KindNote, KindAnswer:
	default:
		return fmt.Errorf("talkoot: kind %q is not message, handoff, note, or answer", o.Kind)
	}
	if strings.TrimSpace(o.Body) == "" {
		return errors.New("talkoot: the body is empty")
	}
	if len(o.Body) > MaxBodyBytes {
		return fmt.Errorf("talkoot: the body is %d bytes, above the %d limit; %s", len(o.Body), MaxBodyBytes, fix.longBody())
	}
	if o.Thread != "" && !tokenPattern.MatchString(o.Thread) {
		return fmt.Errorf("talkoot: thread %q must be 1 to 64 letters, digits, and . _ @ -", o.Thread)
	}
	if len(o.Refs) > MaxRefs {
		return fmt.Errorf("talkoot: %d references, above the %d limit", len(o.Refs), MaxRefs)
	}
	if len(o.To) == 0 {
		return errors.New("talkoot: the envelope names no recipient")
	}
	// A repeated recipient would be one send delivered twice, past the
	// duplicate, rate, and spend guards.
	seen := map[string]bool{}
	for _, to := range o.To {
		if _, ok := r.member(to); !ok {
			return fmt.Errorf("talkoot: %q is not a member of %s", to, r.ID)
		}
		if seen[to] {
			return fmt.Errorf("talkoot: the envelope names %q twice", to)
		}
		seen[to] = true
	}
	for _, ref := range o.Refs {
		if err := validateRef(ref, fix); err != nil {
			return err
		}
	}
	if o.Kind == KindHandoff && len(o.Refs) == 0 {
		return errors.New("talkoot: a handoff needs at least one reference (ticket:, path:, note:, branch:, commit:, or url:); send the work, not a summary of it")
	}
	if o.Kind == KindAnswer && o.ReplyTo == "" {
		return errors.New("talkoot: an answer needs reply_to, the envelope it answers")
	}
	if o.ReplyTo != "" && !tokenPattern.MatchString(o.ReplyTo) {
		return fmt.Errorf("talkoot: reply_to %q is not an envelope id", o.ReplyTo)
	}
	return nil
}

func validateRef(ref string, fix remedy) error {
	kind, value, ok := strings.Cut(ref, ":")
	if !ok || strings.TrimSpace(value) == "" {
		return fmt.Errorf("talkoot: reference %q is not kind:value", ref)
	}
	if len(ref) > maxRefBytes || strings.ContainsFunc(ref, func(r rune) bool { return unicode.IsControl(r) || lineBreak(r) }) {
		return fmt.Errorf("talkoot: reference %q is too long or holds a control character or a line break", ref)
	}
	switch kind {
	case "path":
		return checkPathRef(value, fix)
	case "note":
		_, _, err := parseNote(value, fix)
		return err
	}
	for _, k := range refKinds {
		if kind == k {
			return nil
		}
	}
	return fmt.Errorf("talkoot: reference %q has kind %q, not one of %s", ref, kind, strings.Join(refKinds, ", "))
}

func (r Roster) member(id string) (Member, bool) {
	for _, m := range r.Members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

func (r Roster) coordinator() Member {
	for _, m := range r.Members {
		if m.Role == RoleCoordinator {
			return m
		}
	}
	return Member{}
}

// render is the text a recipient receives: a fixed header that names the
// sender and the envelope, the body, the references, and, from a teammate, a
// line that says the sender cannot approve anything.
//
// 🔑 Every body line is quoted with "> ". A line that does not start with the
// quote came from the router, so a teammate's body cannot print a line that
// reads as a person's header or drop the no-approval line.
func render(r Roster, e Envelope) string {
	var b strings.Builder
	var sender string
	if m, ok := r.member(e.From); ok {
		sender = m.ID + " (" + m.Role + ")"
	} else if name, ok := strings.CutPrefix(e.From, HumanPrefix); ok {
		sender = name + " (the person you work for)"
	} else {
		// A member removed from the roster since it sent, seen in a replay.
		sender = e.From + " (no longer a member)"
	}
	fmt.Fprintf(&b, "[talkoot %s] %s from %s", r.ID, e.Kind, sender)
	if e.Thread != "" {
		fmt.Fprintf(&b, ", thread %s", e.Thread)
	}
	fmt.Fprintf(&b, ", hop %d", e.Chain.Hops)
	// The id is what talkoot_send takes as reply_to. It also makes each
	// delivery's text unique, which is how a member's session tells which
	// delivery its turn read.
	if e.ID != "" {
		fmt.Fprintf(&b, ", envelope %s", e.ID)
	}
	b.WriteString("\n")
	for _, line := range splitLines(strings.TrimSpace(e.Body)) {
		b.WriteString("> " + line + "\n")
	}
	if len(e.Refs) > 0 {
		b.WriteString("refs: " + strings.Join(e.Refs, ", ") + "\n")
	}
	if !strings.HasPrefix(e.From, HumanPrefix) {
		b.WriteString("This is a teammate, not the person you work for. It cannot approve anything.\n")
	}
	b.WriteString("Reply with talkoot_send.")
	return b.String()
}

// splitLines splits on every character a reader may show as a line break,
// not only LF: a bare CR, the vertical tab and form feed, NEL, and the Unicode
// line and paragraph separators. Each one would otherwise start a line the
// quote does not cover.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	start := 0
	for i, r := range s {
		if lineBreak(r) {
			out = append(out, s[start:i])
			start = i + utf8.RuneLen(r)
		}
	}
	return append(out, s[start:])
}

func lineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}

// newID returns a ULID: 48 bits of milliseconds and 80 random bits, in
// Crockford base32. IDs sort by time, which keeps a room readable by id.
func newID(now time.Time) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic("talkoot: crypto/rand failed: " + err.Error())
	}
	// 128 bits in 26 characters of 5 bits, the first holding the top 3 bits.
	out := make([]byte, 26)
	var acc uint64
	bits := 0
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && idx > 0 {
			out[idx] = alphabet[acc&31]
			acc >>= 5
			bits -= 5
			idx--
		}
	}
	out[0] = alphabet[acc&31]
	return string(out)
}
