package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// TalkootWaker is a seat that can set a wake for its member (decision 0029
// rules 4 and 9). It is apart from TalkootSeat, because only a native member
// on a daemon whose owner turned wakes on has one.
type TalkootWaker interface {
	// Wake sets one follow-up for the seat's member, delay from now.
	Wake(delay time.Duration, reason string) (TalkootWakeReceipt, error)
}

// TalkootWakeReceipt is what a seat returns for a wake it set.
type TalkootWakeReceipt struct {
	Wake talkoot.Wake
	// Open counts the member's open wakes, this one included, and Cap is the
	// most the person allows.
	Open int
	Cap  int
	// FollowUps is the allowance of the routine whose run set this wake, and
	// zero for a member's wake. The wake's own Follow is its number.
	FollowUps int
}

// maxWakeMinutes keeps the delay inside a time.Duration. The person's cap is
// far below it, and the seat names that cap in its refusal.
const maxWakeMinutes = 10 * 366 * 24 * 60

// ⚠️ The language gate resolves a constant only inside its own file, so the
// text stays beside the Description method that returns it.
const (
	talkootWakeDesc = "Set a wake for yourself. A wake is one later turn in this chain. After the delay, the router sends you the reason that you gave, and a turn starts for you. Use a wake to look at a later result, such as a CI run or a deploy.\n\nGive the reason as one short line that says what to look at, and where. The wake brings no other text, so read what you need when the turn starts. Do not set a wake to wait for a member, because an envelope from that member starts your turn.\n\nA wake is possible only in a chain that a person started. The person sets a limit on your open wakes and on the delay, and the person can cancel a wake. If you work when the delay ends, you get the wake at the next step of your turn."

	talkootRunWakeDesc = "Set one follow-up for this run. A follow-up is one more run of this routine at a later time. After the delay, terva starts a new run in a new session. That run reads the reason that you gave. Use a follow-up to look at a later result, such as a CI run or a deploy.\n\nGive the reason as one short line that says what to look at, and where. The follow-up brings no other text, so write what the next run needs in a team note. This run still ends with its report.\n\nA run sets one follow-up. The card of the routine gives the number of follow-ups after one run at the time of the routine. The person sets a limit on the delay, and the person can cancel a follow-up."

	talkootWakeSchema = `{"type":"object","properties":{"delay_minutes":{"type":"integer","minimum":1,"description":"The number of minutes from now to the wake. The person sets the maximum."},"reason":{"type":"string","description":"One short line that says what to look at, and where. At most 240 bytes."}},"required":["delay_minutes","reason"]}`
)

// TalkootWakeTool sets one follow-up turn for the member that calls it.
type TalkootWakeTool struct{ Seat TalkootWaker }

func (t *TalkootWakeTool) Name() string { return "talkoot_wake" }
func (t *TalkootWakeTool) Description() string {
	return i18n.D("tool.talkoot_wake.description", talkootWakeDesc)
}
func (t *TalkootWakeTool) Schema() json.RawMessage { return json.RawMessage(talkootWakeSchema) }
func (t *TalkootWakeTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Seat == nil {
		return core.ToolResult{}, errors.New("this session is not a member of a talkoot")
	}
	var a struct {
		DelayMinutes int64  `json:"delay_minutes"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.DelayMinutes < 1 {
		return core.ToolResult{}, errors.New("give delay_minutes as a whole number of minutes, 1 or more")
	}
	if strings.TrimSpace(a.Reason) == "" {
		return core.ToolResult{}, errors.New("give the reason as one short line that says what to look at, and where")
	}
	r, err := t.Seat.Wake(time.Duration(min(a.DelayMinutes, maxWakeMinutes))*time.Minute, a.Reason)
	if err != nil {
		return core.ToolResult{}, err
	}
	text := fmt.Sprintf("Wake %s is set for %s, %d minute(s) from now. You have %d of %d open wakes. Finish this turn as usual. The wake starts a new turn for you with this reason: %s",
		r.Wake.ID, r.Wake.Due.UTC().Format("2006-01-02 15:04 UTC"), a.DelayMinutes, r.Open, r.Cap, oneLine(r.Wake.Reason))
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: text}}, Details: r.Wake}, nil
}

// TalkootRunWakeTool sets the one follow-up of a routine's run (decision
// 0029 rule 11). It has the name of the member's tool, because it sets the
// same kind of timer, and its own text, because what the wake starts is
// another run and no turn of this session.
type TalkootRunWakeTool struct{ Seat TalkootWaker }

func (t *TalkootRunWakeTool) Name() string { return "talkoot_wake" }
func (t *TalkootRunWakeTool) Description() string {
	return i18n.D("tool.talkoot_wake.run.description", talkootRunWakeDesc)
}
func (t *TalkootRunWakeTool) Schema() json.RawMessage { return json.RawMessage(talkootWakeSchema) }
func (t *TalkootRunWakeTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Seat == nil {
		return core.ToolResult{}, errors.New("this session is not a run of a routine")
	}
	var a struct {
		DelayMinutes int64  `json:"delay_minutes"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.DelayMinutes < 1 {
		return core.ToolResult{}, errors.New("give delay_minutes as a whole number of minutes, 1 or more")
	}
	if strings.TrimSpace(a.Reason) == "" {
		return core.ToolResult{}, errors.New("give the reason as one short line that says what to look at, and where")
	}
	r, err := t.Seat.Wake(time.Duration(min(a.DelayMinutes, maxWakeMinutes))*time.Minute, a.Reason)
	if err != nil {
		return core.ToolResult{}, err
	}
	text := fmt.Sprintf("Follow-up %s is set for %s, %d minute(s) from now. It is follow-up %d of the %d that the card of the routine allows. End this run with your report as usual. A new run of the routine starts then, with this reason: %s",
		r.Wake.ID, r.Wake.Due.UTC().Format("2006-01-02 15:04 UTC"), a.DelayMinutes, r.Wake.Follow, r.FollowUps, oneLine(r.Wake.Reason))
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: text}}, Details: r.Wake}, nil
}
