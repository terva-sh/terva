package talkoot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/privfs"
)

// Introductions (TKT-01M39VWMT). A member introduces itself when it joins and
// when its team starts. A person's action asks for each introduction: an
// approved add, or a kickoff. So an introduction roots a chain the way a post
// does, and the room records who asked. A member that cannot take a turn gets
// a plain card, which the workspace builds from the member's entry.

// IntroMode says which introduction a member writes.
type IntroMode int

const (
	// IntroJoin is a member that joined a running team. It tells the room with
	// a note, and the coordinator with a message, so the coordinator can route
	// work to it.
	IntroJoin IntroMode = iota
	// IntroKickoff is a member at the start of a team. It tells the room with
	// a note and wakes nobody, so the coordinator writes last.
	IntroKickoff
	// IntroKickoffLead is the coordinator at the end of a kickoff. It reads
	// the other introductions, says who does what, and asks the person the
	// first question.
	IntroKickoffLead
)

// Introduce asks member to introduce itself, in the name of the person human.
// The envelope's body is the instruction IntroBody writes, so the room shows
// the request as the daemon's text and not as the person's words. It wakes the
// member and roots a chain, so the member's reply passes the human-root guard,
// and every cap and guard applies to the turn as to any other.
func (rt *Router) Introduce(human, member string, mode IntroMode) (Envelope, error) {
	if !tokenPattern.MatchString(human) {
		return Envelope{}, fmt.Errorf("talkoot: the person's name %q must be 1 to 64 letters, digits, and . _ @ -", human)
	}
	if _, ok := rt.roster.member(member); !ok {
		return Envelope{}, fmt.Errorf("talkoot: %q is not a member of %s", member, rt.roster.ID)
	}
	o := Outgoing{To: []string{member}, Kind: KindIntro, Body: IntroBody(rt.roster, member, mode)}
	rt.mu.Lock()
	now := rt.now()
	e := rt.envelope(HumanPrefix+human, o, now)
	e.Chain = Chain{Root: e.ID}
	rt.chain(e.ID)
	if err := rt.room.Append(Line{Type: LineEnvelope, At: now, Envelope: &e}); err != nil {
		rt.mu.Unlock()
		return Envelope{}, err
	}
	ds := rt.routeLocked(e, member, false)
	rt.mu.Unlock()
	rt.dispatch(ds)
	return e, nil
}

// Card writes a member's plain introduction card to the room: text, which the
// workspace builds from the member's entry. It wakes nobody. The coordinator
// reads it as a note with its next delivery, unless the card is its own.
func (rt *Router) Card(member, text string) error {
	if _, ok := rt.roster.member(member); !ok {
		return fmt.Errorf("talkoot: %q is not a member of %s", member, rt.roster.ID)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("talkoot: the introduction card is empty")
	}
	if len(text) > MaxBodyBytes {
		return fmt.Errorf("talkoot: the introduction card is %d bytes, above the %d limit", len(text), MaxBodyBytes)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	l := Line{Type: LineIntro, At: rt.now(), Member: member, Text: text}
	if err := rt.room.Append(l); err != nil {
		return err
	}
	rt.cardLocked(l)
	return nil
}

// cardLocked buffers a card for the coordinator. The live Card and replay both
// call it, so a restart keeps the note owed.
func (rt *Router) cardLocked(l Line) {
	lead := rt.roster.coordinator().ID
	if lead == "" || lead == l.Member {
		return
	}
	rt.notes[lead] = append(rt.notes[lead], renderCard(rt.roster, l))
}

func renderCard(r Roster, l Line) string {
	var b strings.Builder
	who := l.Member
	if m, ok := r.member(l.Member); ok {
		who += " (" + m.Role + ")"
	}
	fmt.Fprintf(&b, "[talkoot %s] introduction card for %s, written from the roster\n", r.ID, who)
	for _, line := range splitLines(strings.TrimSpace(l.Text)) {
		b.WriteString("> " + line + "\n")
	}
	return b.String()
}

// IntroBody is the instruction an introduction envelope carries.
func IntroBody(r Roster, member string, mode IntroMode) string {
	m, _ := r.member(member)
	lead := r.coordinator().ID
	var others, peers []string
	for _, o := range r.Members {
		if o.ID == member {
			continue
		}
		others = append(others, o.ID)
		if o.ID != lead {
			peers = append(peers, o.ID)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Introduce yourself to the talkoot %s. You are %s, the %s.\n", r.ID, member, m.Role)
	b.WriteString("Read the roster and your own entry with talkoot_roster first.\n")
	switch mode {
	case IntroKickoffLead:
		b.WriteString("Your team is starting. The other members introduced themselves, and their notes arrive with this envelope.\n")
		if len(others) > 0 {
			fmt.Fprintf(&b, "Write one short post that says who does what. Send it with talkoot_send as a note to %s.\n", strings.Join(others, ", "))
		}
		b.WriteString("Then ask the person the first question the team needs answered, with ask_user_question.\n")
	default:
		b.WriteString("Write one short post: who you are and your one job, what you will not do, and who you expect to trade work with.\n")
		switch {
		case mode == IntroJoin && member != lead && lead != "":
			if len(peers) > 0 {
				fmt.Fprintf(&b, "Send it with talkoot_send as a note to %s.\n", strings.Join(peers, ", "))
			}
			fmt.Fprintf(&b, "Send it again with talkoot_send as a message to %s, the coordinator, so it can route work to you.\n", lead)
		case len(others) > 0:
			fmt.Fprintf(&b, "Send it with talkoot_send as a note to %s. A note wakes nobody.\n", strings.Join(others, ", "))
		default:
			b.WriteString("No other member is on the roster yet, so send nothing.\n")
		}
	}
	b.WriteString("Then stop. Do not start other work in this turn.")
	return b.String()
}

// KickoffFile holds a talkoot's kickoff state, beside its roster.
const KickoffFile = "kickoff.json"

// Kickoff is the state of a team's introductions. A talkoot made before
// kickoffs shipped has no file, and may still run one.
type Kickoff struct {
	// State is waiting, running, done, or skipped.
	State string    `json:"state"`
	By    string    `json:"by,omitempty"`
	At    time.Time `json:"at"`
}

// LoadKickoff reads a talkoot's kickoff state. ok is false when it has none.
func LoadKickoff(dir string) (k Kickoff, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, KickoffFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Kickoff{}, false, nil
	}
	if err != nil {
		return Kickoff{}, false, fmt.Errorf("talkoot: read kickoff: %w", err)
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return Kickoff{}, false, fmt.Errorf("talkoot: kickoff %s does not parse: %w", filepath.Join(dir, KickoffFile), err)
	}
	return k, true, nil
}

// SaveKickoff writes a talkoot's kickoff state.
func SaveKickoff(dir string, k Kickoff) error {
	raw, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return fmt.Errorf("talkoot: %w", err)
	}
	if err := privfs.WriteFile(filepath.Join(dir, KickoffFile), raw); err != nil {
		return fmt.Errorf("talkoot: save kickoff: %w", err)
	}
	return nil
}
