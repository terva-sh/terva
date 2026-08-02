package tools

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/i18n"
)

// raatiLiveBoard renders a running deliberation as a few lines of text, for
// every surface whose live view of a tool call is its progress string.
//
// The web has had the whole picture since the board shipped — who is seated,
// on which model, holding which prior, how each has voted, what the panel
// asked. The TUI had one line, replaced on every event, so a convening that
// spends six sub-agent turns and minutes of wall clock showed as:
//
//	raati_convene {"class":"advisory","converge":true,"evidence":"Reposi…
//	  YATA-1 has cast its ballot: reject (confidence 0.88)
//
// — the question invisible behind the raw arguments, two of the three seats
// invisible entirely, and the previous line gone. That is the same structured
// feed the web renders (Config.OnEvent); nothing was missing but a reader.
//
// It is deliberately NOT a second model of the deliberation: it folds the same
// events the board hook folds, and both are fed from one place so they cannot
// disagree about what the panel is doing.
type raatiLiveBoard struct {
	// Fixed at convene time — what the caller asked for, before any seat
	// answers. Shown immediately so the box is informative from the first
	// frame rather than after the first ballot.
	level string // the rigor nickname: kaiku | kuoro | käräjät
	class string // advisory | gate | veto
	order string // fixed | convene | turn

	round  int
	seats  []raatiSeatState
	asked  int
	open   int
	status string // the latest narration line

	// emit is the surface's progress callback. Every fold ends in one call,
	// so a surface that only keeps the newest string still sees the whole
	// board rather than the newest fragment of it.
	emit func(string)
}

type raatiSeatState struct {
	name    string
	binding string
	// verdict/confidence once cast; why when absent. state is what the
	// column prints when there is no ballot yet.
	state      string
	verdict    string
	confidence float64
	voted      bool
	absent     bool
}

// newRaatiLiveBoard seeds the board with what is known before the panel is
// spawned: the shape of the convening, and one row per seat.
func newRaatiLiveBoard(level, class, order string, units []raati.Unit, emit func(string)) *raatiLiveBoard {
	b := &raatiLiveBoard{level: level, class: class, order: order, emit: emit}
	for _, u := range units {
		b.seats = append(b.seats, raatiSeatState{name: u.Name, state: i18n.T("waiting")})
	}
	return b
}

// Narrate takes the one-line narration Config.Progress has always carried and
// folds it into the board as its status line, rather than letting it replace
// the board. Both feeds say true things about the same deliberation; only one
// of them can be the whole of what a surface shows.
func (b *raatiLiveBoard) Narrate(msg string) {
	if b == nil {
		return
	}
	b.status = msg
	b.flush()
}

// Event folds one structured state change. Called synchronously from the
// convening goroutine in order (see raati.Config.OnEvent), so no locking.
func (b *raatiLiveBoard) Event(ev raati.Event) {
	if b == nil {
		return
	}
	switch ev.Kind {
	case raati.EventSeated:
		if s := b.seat(ev.Unit); s != nil {
			s.binding = ev.Binding
			// A reseat (seat_order "turn") re-seats an already-voted unit on
			// a new binding for the next round: its previous verdict is no
			// longer this seat's answer.
			s.voted, s.absent, s.verdict = false, false, ""
			s.state = i18n.T("deliberating…")
		}
	case raati.EventRound:
		b.round = ev.Round
	case raati.EventVoted:
		if s := b.seat(ev.Unit); s != nil && ev.Ballot != nil {
			s.voted, s.absent = true, false
			s.verdict, s.confidence = string(ev.Ballot.Verdict), ev.Ballot.Confidence
		}
	case raati.EventAbsent:
		if s := b.seat(ev.Unit); s != nil {
			s.absent, s.voted = true, false
			s.state = i18n.T("absent — %s", ev.Why)
		}
	case raati.EventInquiry:
		b.asked++
		if ev.Source == raati.SourceUnanswered {
			b.open++
		}
	}
	b.flush()
}

func (b *raatiLiveBoard) seat(name string) *raatiSeatState {
	for i := range b.seats {
		if b.seats[i].name == name {
			return &b.seats[i]
		}
	}
	return nil
}

func (b *raatiLiveBoard) flush() {
	if b.emit != nil {
		b.emit(b.Render())
	}
}

// Render draws the board. Kept under the TUI's tool-body collapse threshold
// (12 lines) for the shipped three-seat panel, so the whole thing stays
// visible without an expand — a live view folded behind "… 4 more lines" is
// not a live view.
func (b *raatiLiveBoard) Render() string {
	var out []string
	if head := b.header(); head != "" {
		out = append(out, head, "")
	}
	// One aligned column for the seat name and one for its binding, so three
	// seats read as a table rather than three sentences of different lengths.
	nameW, bindW := 0, 0
	for _, s := range b.seats {
		nameW = max(nameW, len(s.name))
		bindW = max(bindW, len(s.binding))
	}
	for _, s := range b.seats {
		row := fmt.Sprintf("  %-*s  %-*s  %s", nameW, s.name, bindW, s.binding, s.column())
		out = append(out, strings.TrimRight(row, " "))
	}
	if foot := b.footer(); foot != "" {
		out = append(out, "", foot)
	}
	return strings.Join(out, "\n")
}

func (b *raatiLiveBoard) header() string {
	var parts []string
	if b.level != "" {
		parts = append(parts, b.level)
	}
	if b.class != "" {
		parts = append(parts, b.class)
	}
	if b.round > 0 {
		parts = append(parts, i18n.T("round %d", b.round))
	}
	if b.order != "" {
		parts = append(parts, i18n.T("seats %s", b.order))
	}
	return strings.Join(parts, " · ")
}

func (b *raatiLiveBoard) footer() string {
	var parts []string
	if b.asked > 0 {
		asked := i18n.T("the panel asked %d", b.asked)
		if b.open > 0 {
			// An open question is unmet evidence, not a footnote — the tool's
			// own description tells the agent to treat it that way, so the
			// live view should not be the one place it goes unsaid.
			asked += i18n.T(" (%d open)", b.open)
		}
		parts = append(parts, asked)
	}
	if b.status != "" {
		parts = append(parts, b.status)
	}
	return strings.Join(parts, " · ")
}

// column is the seat's rightmost cell: its ballot once cast, else what it is
// doing. A confidence is part of a ballot's meaning, not decoration — a 0.51
// approve and a 0.95 approve are different answers.
func (s raatiSeatState) column() string {
	switch {
	case s.voted:
		// Padded to the longest verdict ("abstain") so the confidences line
		// up under each other — the column is there to be compared down, not
		// read across.
		return fmt.Sprintf("%-7s %.2f", s.verdict, s.confidence)
	case s.absent, s.state != "":
		return s.state
	}
	return ""
}
