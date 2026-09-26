package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// TalkootSeat is a session's place in a talkoot: the member it speaks as and
// the router it speaks through. The workspace binds it, so the model never
// names the sender.
type TalkootSeat interface {
	// Send routes an envelope from the seat's member.
	Send(o talkoot.Outgoing) (talkoot.Envelope, error)
	// Roster lists every member in roster order, with its status.
	Roster() ([]TalkootRosterEntry, error)
	// WriteNote writes or replaces one of the seat's member's notes, and
	// returns the reference that cites it.
	WriteNote(name, text string) (string, error)
	// ReadNote returns the text of the note a note: reference cites.
	ReadNote(ref string) (string, error)
	// ListNotes lists every note in the talkoot, newest first.
	ListNotes() ([]talkoot.Note, error)
}

// TalkootRosterEntry is one member as talkoot_roster shows it.
type TalkootRosterEntry struct {
	Member talkoot.Member
	Status talkoot.Status
	// Self marks the seat's own member.
	Self bool
}

// TalkootToolDef is one Talkoot tool as a model sees it.
type TalkootToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// 🔑 The text below is the one copy. The native tools serve it, and the MCP
// bridge for external members (decision 0023) serves the same definitions
// through TalkootToolDefs, so a foreign model reads what a native one reads.
// ⚠️ The language gate reads a description only where a Description method
// returns the i18n.D call, and it resolves a constant only inside its own
// file. Keep both shapes, or the gate passes text it never read.
const (
	talkootSendDesc = "Send an envelope to one or more members of your talkoot. The router records it in the room and delivers it. A message or an answer starts a turn for each recipient. A note does not start a turn. The recipient reads a note at its next turn. To pass work to a member, use talkoot_handoff."

	talkootHandoffDesc = "Pass work to one or more members of your talkoot. A handoff must carry at least one reference: a ticket, a branch, a commit, a path, or a note. The work travels by reference and not as a summary, so give the reference that holds it. The handoff starts a turn for each recipient."

	talkootRosterDesc = "List the members of your talkoot. The result gives the id, title, role, driver, and status of each member. Call this tool to find the ids that talkoot_send and talkoot_handoff accept."

	talkootNoteWriteDesc = "Write a note for your talkoot. Every member of the talkoot can read it with talkoot_note_read. A second write with the same name replaces your note. The tool returns a note reference. Put that reference in the refs of an envelope, so the recipients can read the note. Use a note for a report that is too long for an envelope."

	talkootNoteReadDesc = "Read a note of your talkoot. Give the note reference from an envelope, as in note:atlas/plan.md. With no note, the tool lists every note, the newest first. One result holds at most 2000 lines and 50 KiB. If the note is larger, the tool cuts the result and gives the offset of the next line."

	talkootToProp     = `"to":{"type":"array","items":{"type":"string"},"minItems":1,"description":"The ids of the members that get the envelope. Call talkoot_roster for the ids."}`
	talkootBodyProp   = `"body":{"type":"string","description":"The text of the envelope."}`
	talkootThreadProp = `"thread":{"type":"string","description":"An optional thread id. Give the same id again to keep a conversation together."}`
	talkootReplyProp  = `"reply_to":{"type":"string","description":"The id of the envelope that this one replies to."}`

	talkootSendSchema = `{"type":"object","properties":{` + talkootToProp + `,"kind":{"type":"string","enum":["message","note","answer"],"description":"A message starts a turn. An answer replies to a question and starts a turn. A note waits for the next turn of the recipient."},` + talkootBodyProp + `,"refs":{"type":"array","items":{"type":"string"},"description":"Optional references. Start each reference with its kind: ticket, path, note, branch, commit, or url. Put a colon before the value, as in path:src/main.go. A path is relative to the home checkout of the talkoot. A note reference comes from talkoot_note_write."},` + talkootThreadProp + `,` + talkootReplyProp + `},"required":["to","kind","body"]}`

	talkootHandoffSchema = `{"type":"object","properties":{` + talkootToProp + `,` + talkootBodyProp + `,"refs":{"type":"array","items":{"type":"string"},"minItems":1,"description":"The references that hold the work. Give at least one. Start each reference with its kind: ticket, path, note, branch, or commit. Put a colon before the value, as in branch:feat/login. A path is relative to the home checkout of the talkoot."},` + talkootThreadProp + `,` + talkootReplyProp + `},"required":["to","body","refs"]}`

	talkootRosterSchema = `{"type":"object","properties":{}}`

	talkootNoteWriteSchema = `{"type":"object","properties":{"name":{"type":"string","description":"The name of the note, as in plan.md. Use only letters, digits, and . _ -. The name cannot start with a dot."},"text":{"type":"string","description":"The text of the note. A note holds at most 256 KiB."}},"required":["name","text"]}`

	talkootNoteReadSchema = `{"type":"object","properties":{"note":{"type":"string","description":"The note reference, as in note:atlas/plan.md. Leave it out to list every note."},"offset":{"type":"integer","description":"The first line to read. The first line of the note is 1."},"limit":{"type":"integer","description":"The maximum number of lines to read. One result holds at most 2000 lines and 50 KiB."}}}`
)

// TalkootToolDefs returns the Talkoot tools as a model sees them, for a
// surface that serves them without a seat, such as the MCP bridge.
func TalkootToolDefs() []TalkootToolDef {
	var out []TalkootToolDef
	for _, t := range TalkootTools(nil) {
		out = append(out, TalkootToolDef{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
	}
	return out
}

// TalkootTools returns the five tools for one seat.
func TalkootTools(seat TalkootSeat) []core.Tool {
	return []core.Tool{
		&TalkootSendTool{Seat: seat},
		&TalkootHandoffTool{Seat: seat},
		&TalkootRosterTool{Seat: seat},
		&TalkootNoteWriteTool{Seat: seat},
		&TalkootNoteReadTool{Seat: seat},
	}
}

type talkootSendArgs struct {
	To      []string `json:"to"`
	Kind    string   `json:"kind"`
	Body    string   `json:"body"`
	Refs    []string `json:"refs"`
	Thread  string   `json:"thread"`
	ReplyTo string   `json:"reply_to"`
}

// TalkootSendTool sends a message, a note, or an answer.
type TalkootSendTool struct{ Seat TalkootSeat }

func (t *TalkootSendTool) Name() string { return "talkoot_send" }
func (t *TalkootSendTool) Description() string {
	return i18n.D("tool.talkoot_send.description", talkootSendDesc)
}
func (t *TalkootSendTool) Schema() json.RawMessage { return json.RawMessage(talkootSendSchema) }
func (t *TalkootSendTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a talkootSendArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	// A handoff has its own tool, which asks for the references up front.
	switch talkoot.Kind(a.Kind) {
	case talkoot.KindMessage, talkoot.KindNote, talkoot.KindAnswer:
	case talkoot.KindHandoff:
		return core.ToolResult{}, errors.New("to pass work, call talkoot_handoff")
	default:
		return core.ToolResult{}, fmt.Errorf("kind %q is not message, note, or answer", a.Kind)
	}
	return talkootSend(t.Seat, talkoot.Outgoing{
		To: a.To, Kind: talkoot.Kind(a.Kind), Body: a.Body, Refs: a.Refs, Thread: a.Thread, ReplyTo: a.ReplyTo,
	})
}

// TalkootHandoffTool passes work by reference.
type TalkootHandoffTool struct{ Seat TalkootSeat }

func (t *TalkootHandoffTool) Name() string { return "talkoot_handoff" }
func (t *TalkootHandoffTool) Description() string {
	return i18n.D("tool.talkoot_handoff.description", talkootHandoffDesc)
}
func (t *TalkootHandoffTool) Schema() json.RawMessage { return json.RawMessage(talkootHandoffSchema) }
func (t *TalkootHandoffTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a talkootSendArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	// The router refuses this too. The tool says it first, in words that
	// name the fix.
	if len(a.Refs) == 0 {
		return core.ToolResult{}, errors.New("a handoff needs at least one reference, for example branch:feat/login, commit:4a2e907, or path:src/main.go; put the work there and send the reference, not a summary")
	}
	return talkootSend(t.Seat, talkoot.Outgoing{
		To: a.To, Kind: talkoot.KindHandoff, Body: a.Body, Refs: a.Refs, Thread: a.Thread, ReplyTo: a.ReplyTo,
	})
}

func talkootSend(seat TalkootSeat, o talkoot.Outgoing) (core.ToolResult, error) {
	if seat == nil {
		return core.ToolResult{}, errors.New("this session is not a member of a talkoot")
	}
	e, err := seat.Send(o)
	if err != nil {
		return core.ToolResult{}, err
	}
	text := fmt.Sprintf("Sent %s %s to %s.", e.Kind, e.ID, strings.Join(e.To, ", "))
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: text}}, Details: e}, nil
}

// TalkootRosterTool lists the members and their status.
type TalkootRosterTool struct{ Seat TalkootSeat }

func (t *TalkootRosterTool) Name() string { return "talkoot_roster" }
func (t *TalkootRosterTool) Description() string {
	return i18n.D("tool.talkoot_roster.description", talkootRosterDesc)
}
func (t *TalkootRosterTool) Schema() json.RawMessage { return json.RawMessage(talkootRosterSchema) }
func (t *TalkootRosterTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Seat == nil {
		return core.ToolResult{}, errors.New("this session is not a member of a talkoot")
	}
	entries, err := t.Seat.Roster()
	if err != nil {
		return core.ToolResult{}, err
	}
	var b strings.Builder
	for _, e := range entries {
		m := e.Member
		fmt.Fprintf(&b, "- %s", oneLine(m.ID))
		if e.Self {
			b.WriteString(" (you)")
		}
		if m.Title != "" {
			fmt.Fprintf(&b, ": %s", oneLine(m.Title))
		}
		fmt.Fprintf(&b, "; role %s; driver %s; %s\n", oneLine(m.Role), oneLine(m.Driver), oneLine(talkootStatusText(e.Status)))
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: b.String()}}}, nil
}

// TalkootNoteWriteTool writes one of the member's notes.
type TalkootNoteWriteTool struct{ Seat TalkootSeat }

func (t *TalkootNoteWriteTool) Name() string { return "talkoot_note_write" }
func (t *TalkootNoteWriteTool) Description() string {
	return i18n.D("tool.talkoot_note_write.description", talkootNoteWriteDesc)
}
func (t *TalkootNoteWriteTool) Schema() json.RawMessage {
	return json.RawMessage(talkootNoteWriteSchema)
}
func (t *TalkootNoteWriteTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Seat == nil {
		return core.ToolResult{}, errors.New("this session is not a member of a talkoot")
	}
	var a struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	ref, err := t.Seat.WriteNote(a.Name, a.Text)
	if err != nil {
		return core.ToolResult{}, err
	}
	text := fmt.Sprintf("Wrote %s (%d bytes). Cite it as %s in the refs of an envelope.", ref, len(a.Text), ref)
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: text}}}, nil
}

// Caps on one talkoot_note_read result, the same as read's.
const (
	talkootNoteMaxLines = 2000
	talkootNoteMaxBytes = 50 * 1024
	talkootNoteMaxList  = 200
)

// TalkootNoteReadTool reads a note, or lists every note.
type TalkootNoteReadTool struct{ Seat TalkootSeat }

func (t *TalkootNoteReadTool) Name() string { return "talkoot_note_read" }
func (t *TalkootNoteReadTool) Description() string {
	return i18n.D("tool.talkoot_note_read.description", talkootNoteReadDesc)
}
func (t *TalkootNoteReadTool) Schema() json.RawMessage {
	return json.RawMessage(talkootNoteReadSchema)
}
func (t *TalkootNoteReadTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Seat == nil {
		return core.ToolResult{}, errors.New("this session is not a member of a talkoot")
	}
	var a struct {
		Note   string `json:"note"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Note == "" {
		return t.list()
	}
	text, err := t.Seat.ReadNote(a.Note)
	if err != nil {
		return core.ToolResult{}, err
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: pageNote(a.Note, text, a.Offset, a.Limit)}}}, nil
}

func (t *TalkootNoteReadTool) list() (core.ToolResult, error) {
	notes, err := t.Seat.ListNotes()
	if err != nil {
		return core.ToolResult{}, err
	}
	if len(notes) == 0 {
		return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "The talkoot has no notes yet. Write one with talkoot_note_write."}}}, nil
	}
	var b strings.Builder
	for i, n := range notes {
		if i == talkootNoteMaxList {
			fmt.Fprintf(&b, "... and %d more, older.\n", len(notes)-i)
			break
		}
		fmt.Fprintf(&b, "- %s: %d bytes, written %s\n", n.Ref, n.Size, n.ModTime.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: b.String()}}}, nil
}

// pageNote returns the lines of text from offset, within the result caps. A
// cut result ends with the offset to continue from.
func pageNote(ref, text string, offset, limit int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return fmt.Sprintf("%s has %d line(s); offset %d is past the end.", ref, len(lines), offset)
	}
	if limit < 1 || limit > talkootNoteMaxLines {
		limit = talkootNoteMaxLines
	}
	var b strings.Builder
	next := 0
	for i := offset - 1; i < len(lines); i++ {
		line := lines[i]
		if i-(offset-1) == limit || (i > offset-1 && b.Len()+len(line)+1 > talkootNoteMaxBytes) {
			next = i + 1
			break
		}
		// ⚠️ One line above the cap would otherwise stop the paging at the
		// same offset for good. Show its start, and page past it.
		if len(line)+1 > talkootNoteMaxBytes {
			n := talkootNoteMaxBytes - 64
			for n > 0 && !utf8.RuneStart(line[n]) {
				n--
			}
			line = line[:n] + " [line cut]"
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if next > 0 {
		fmt.Fprintf(&b, "[%s continues: read it again with offset %d]\n", ref, next)
	}
	return b.String()
}

// oneLine keeps a field on its own line of the roster. A title with a newline
// could otherwise add a line that reads as another member, or as "(you)".
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

func talkootStatusText(s talkoot.Status) string {
	switch {
	case s.Paused != "":
		return "paused: " + s.Paused
	case s.Working:
		return "working"
	default:
		return "idle"
	}
}
