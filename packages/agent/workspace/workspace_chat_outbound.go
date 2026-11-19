package workspace

// When a team's message reaches the chat (decision 0029 rule 22).
//
// An entity that sends everything becomes a log the person stops reading. So
// the host decides what leaves and when. This file holds that decision and
// nothing else: each method takes what happened and returns the lines to send
// now. The pump in workspace_chat_talkoot.go sends them.
//
// 🔑 No reason here comes from a member. A member that could name its own
// reason would write "the work finished" on every message, and a prompt that
// asks it not to is not a control. Each reason is read from state the host
// already holds: a card in the inbox, the time of the person's last post, and
// the team's state.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
)

// chatPresence is how long after the person's last post a member's message
// to them counts as a reply. Rule 22 is about writing first, and an answer
// to someone who is waiting for it is not that.
var chatPresence = 10 * time.Minute

// chatNow is the bridge's clock. A test replaces it to move in and out of
// quiet hours.
var chatNow = time.Now

// chatHeldMax bounds the messages that wait. Past it the queue counts what it
// drops, and the brief says how many more are in the room.
const chatHeldMax = 200

// chatBriefMax bounds the updates one brief lists. A brief of every held
// message could be dozens of chat messages at once. Past the bound the brief
// lists the newest and says how many more are in the room.
const chatBriefMax = 30

// chatHeldBody bounds one held message. The brief is one chat message, and
// the room holds the whole text.
const chatHeldBody = 600

// The reasons a held message goes out before the brief. A message with none
// is an update, and it waits for the brief.
const (
	outFinished = "finished" // the team went quiet, and this is its last word
	outDecision = "decision" // a proposal or a kickoff waits in the app
	outWatch    = "watch"    // something the person asked to watch changed
)

// chatLine is one message to send to the chat now.
type chatLine struct{ speaker, text string }

// chatHeld is one message that waits.
type chatHeld struct {
	ID      string    `json:"id"`
	Chain   string    `json:"chain,omitempty"`
	Speaker string    `json:"speaker,omitempty"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	// Reason is why the message goes out when quiet hours end. An update has
	// none and waits for the brief.
	Reason string `json:"reason,omitempty"`
	// Passed is set on an update that was in the queue when the team last
	// went quiet and was not its last word. It waits for the brief, and a
	// later quiet moment does not take it for a last word.
	Passed bool `json:"passed,omitempty"`
}

// chatOutboundRules is what the person set: when the brief goes out, and when
// the chat stays quiet. Both read in loc.
type chatOutboundRules struct {
	brief []timers.ClockTime
	// quietFrom and quietTo bound quiet hours, as minutes after midnight. The
	// range may pass midnight. Equal values mean no quiet hours.
	quietFrom, quietTo timers.ClockTime
	loc                *time.Location
}

// parseChatOutboundRules reads the brief times and the quiet range from the
// talkoot_chat key.
func parseChatOutboundRules(brief []string, quiet string) (chatOutboundRules, error) {
	r := chatOutboundRules{loc: time.Local}
	clock := func(text string) (timers.ClockTime, error) {
		s, err := timers.ParseSchedule("daily "+strings.TrimSpace(text), time.Now())
		if err != nil || len(s.Daily) != 1 {
			return 0, fmt.Errorf("%q is not a time of day such as 07:30", text)
		}
		return s.Daily[0], nil
	}
	for _, b := range brief {
		c, err := clock(b)
		if err != nil {
			return r, fmt.Errorf("talkoot_chat.brief: %w", err)
		}
		r.brief = append(r.brief, c)
	}
	if quiet = strings.TrimSpace(quiet); quiet != "" {
		from, to, ok := strings.Cut(quiet, "-")
		if !ok {
			return r, fmt.Errorf("talkoot_chat.quiet: %q is not a range such as 22:00-07:00", quiet)
		}
		var err error
		if r.quietFrom, err = clock(from); err != nil {
			return r, fmt.Errorf("talkoot_chat.quiet: %w", err)
		}
		if r.quietTo, err = clock(to); err != nil {
			return r, fmt.Errorf("talkoot_chat.quiet: %w", err)
		}
	}
	return r, nil
}

// quiet reports whether now is inside quiet hours.
func (r chatOutboundRules) quiet(now time.Time) bool {
	if r.quietFrom == r.quietTo {
		return false
	}
	local := now.In(r.loc)
	m := timers.ClockTime(local.Hour()*60 + local.Minute())
	if r.quietFrom < r.quietTo {
		return m >= r.quietFrom && m < r.quietTo
	}
	return m >= r.quietFrom || m < r.quietTo
}

// chatOutbound is the queue of one talkoot's chat bridge, and the rules that
// fill and empty it.
type chatOutbound struct {
	rules chatOutboundRules
	// path is the queue's file. Empty keeps the queue in memory.
	path string
	logf func(string, ...any)
	// changed runs after the queue changed, with no lock held, so the app
	// reads the team's view again.
	changed func()

	mu sync.Mutex
	// lastPerson is when the person last posted, from chat or from the app.
	lastPerson time.Time
	held       []chatHeld
	dropped    int
	// briefDue is set when a brief came due while it could not go out: in
	// quiet hours, or with no chat connected.
	briefDue bool
}

// chatOutboundFile is the queue on disk.
type chatOutboundFile struct {
	Version  int        `json:"version"`
	Held     []chatHeld `json:"held"`
	Dropped  int        `json:"dropped,omitempty"`
	BriefDue bool       `json:"brief_due,omitempty"`
}

// openChatOutbound reads the queue at path. A file that does not read starts
// an empty queue, and the daemon's log says so: the room still holds every
// message, and a bridge that refused to start over its own queue would cost
// more than the brief it lost.
func openChatOutbound(path string, rules chatOutboundRules, logf func(string, ...any), changed func()) *chatOutbound {
	o := &chatOutbound{rules: rules, path: path, logf: logf, changed: changed}
	if o.logf == nil {
		o.logf = func(string, ...any) {}
	}
	if o.changed == nil {
		o.changed = func() {}
	}
	if path == "" {
		return o
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return o
	}
	var f chatOutboundFile
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil || f.Version != 1 {
		o.logf("talkoot chat: the held messages in %s do not read, so the queue starts empty: %v", path, err)
		return o
	}
	o.held, o.dropped, o.briefDue = f.Held, f.Dropped, f.BriefDue
	if len(o.held) > chatHeldMax {
		o.dropped += len(o.held) - chatHeldMax
		o.held = o.held[len(o.held)-chatHeldMax:]
	}
	return o
}

// saveLocked writes the queue. A failure is logged and the queue stays in
// memory: the messages are still in the room.
func (o *chatOutbound) saveLocked() {
	if o.path == "" {
		return
	}
	b, err := json.Marshal(chatOutboundFile{Version: 1, Held: o.held, Dropped: o.dropped, BriefDue: o.briefDue})
	if err == nil {
		err = privfs.WriteFile(o.path, b)
	}
	if err != nil {
		o.logf("talkoot chat: the held messages did not save: %v", err)
	}
}

// done ends a method that may have changed the queue: it saves, unlocks, and
// tells the app. It compares what the file would hold before and after, so
// no method has to remember to say that it changed something.
func (o *chatOutbound) done(was string) {
	moved := o.stamp() != was
	if moved {
		o.saveLocked()
	}
	o.mu.Unlock()
	if moved {
		o.changed()
	}
}

// stamp is the queue's state as one comparable value.
func (o *chatOutbound) stamp() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %v", o.dropped, o.briefDue)
	for _, h := range o.held {
		fmt.Fprintf(&b, " %s:%s:%v", h.ID, h.Reason, h.Passed)
	}
	return b.String()
}

func (o *chatOutbound) holdLocked(h chatHeld) {
	if utf8.RuneCountInString(h.Text) > chatHeldBody {
		h.Text = string([]rune(h.Text)[:chatHeldBody]) + "…"
	}
	if len(o.held) >= chatHeldMax {
		// The oldest update goes first: a message held for a reason is one
		// the person should still get. With no update left to drop, a new
		// update is the one that goes. Only a queue that holds nothing but
		// messages with a reason loses one of those, the oldest.
		drop := -1
		for i, old := range o.held {
			if old.Reason == "" {
				drop = i
				break
			}
		}
		o.dropped++
		switch {
		case drop >= 0:
		case h.Reason == "":
			return
		default:
			drop = 0
		}
		o.held = append(o.held[:drop], o.held[drop+1:]...)
	}
	o.held = append(o.held, h)
}

// present reports whether the person is in the conversation.
func (o *chatOutbound) presentLocked(now time.Time) bool {
	return !o.lastPerson.IsZero() && now.Sub(o.lastPerson) < chatPresence
}

// personPosted records that the person wrote. They are here now, so what
// quiet hours held for a reason goes to them. With no brief time set, their
// message is also what empties the queue, so the brief follows.
func (o *chatOutbound) personPosted(now time.Time) []chatLine {
	o.mu.Lock()
	defer o.done(o.stamp())
	o.lastPerson = now
	out := o.releaseReasonsLocked()
	if len(o.rules.brief) == 0 || o.briefDue {
		out = append(out, o.briefLocked()...)
	}
	return out
}

// releaseReasonsLocked takes every message held for a reason out of the
// queue, oldest first.
func (o *chatOutbound) releaseReasonsLocked() []chatLine {
	var out []chatLine
	keep := o.held[:0]
	for _, h := range o.held {
		if h.Reason != "" {
			out = append(out, chatLine{h.Speaker, h.Text})
			continue
		}
		keep = append(keep, h)
	}
	o.held = keep
	return out
}

// memberSaid takes a member's message to the person. It goes out now when the
// person is in the conversation. Otherwise it waits as an update.
func (o *chatOutbound) memberSaid(now time.Time, id, chain, speaker, text string) []chatLine {
	o.mu.Lock()
	defer o.done(o.stamp())
	if o.presentLocked(now) {
		// The person has this chain's newest word now. An older update of
		// the chain that is still held must not go out after it as the last
		// word, so it waits for the brief.
		for i := range o.held {
			if o.held[i].Chain == chain && o.held[i].Reason == "" {
				o.held[i].Passed = true
			}
		}
		return []chatLine{{speaker, text}}
	}
	o.holdLocked(chatHeld{ID: id, Chain: chain, Speaker: speaker, Text: text, At: now})
	return nil
}

// notice takes a message the host itself writes for a reason: a decision
// that waits in the app, or a watched thing that changed. It goes out now,
// and quiet hours hold it until they end.
func (o *chatOutbound) notice(now time.Time, id, reason, text string) []chatLine {
	o.mu.Lock()
	defer o.done(o.stamp())
	if o.rules.quiet(now) && !o.presentLocked(now) {
		o.holdLocked(chatHeld{ID: id, Text: text, At: now, Reason: reason})
		return nil
	}
	return []chatLine{{"", text}}
}

// forBrief takes a text that the host writes for the next brief. It never
// goes out before the brief, and a quiet moment of the team does not take it
// for a last word.
func (o *chatOutbound) forBrief(now time.Time, id, text string) {
	o.mu.Lock()
	defer o.done(o.stamp())
	o.holdLocked(chatHeld{ID: id, Text: text, At: now, Passed: true})
}

// settled is called when the team stopped working with nobody waiting on a
// person. The newest update of each chain is the team's last word on that
// work, so it goes out now. The older updates stay for the brief.
func (o *chatOutbound) settled(now time.Time) []chatLine {
	o.mu.Lock()
	defer o.done(o.stamp())
	newest := map[string]int{}
	for i, h := range o.held {
		if h.Reason == "" && !h.Passed {
			newest[h.Chain] = i
		}
	}
	if len(newest) == 0 {
		return nil
	}
	last := map[int]bool{}
	for _, i := range newest {
		last[i] = true
	}
	for i := range o.held {
		if o.held[i].Reason == "" && !last[i] {
			o.held[i].Passed = true
		}
	}
	if o.rules.quiet(now) {
		for i := range o.held {
			if last[i] {
				o.held[i].Reason = outFinished
			}
		}
		return nil
	}
	var out []chatLine
	keep := o.held[:0]
	for i, h := range o.held {
		if last[i] {
			out = append(out, chatLine{h.Speaker, h.Text})
			continue
		}
		keep = append(keep, h)
	}
	o.held = keep
	return out
}

// briefCame is called at a brief time. In quiet hours the brief waits for
// them to end.
func (o *chatOutbound) briefCame(now time.Time) []chatLine {
	o.mu.Lock()
	defer o.done(o.stamp())
	if o.rules.quiet(now) {
		o.briefDue = true
		return nil
	}
	return o.briefLocked()
}

// briefMissed records a brief that came due with no chat to send it to. The
// next thing that can send it does.
func (o *chatOutbound) briefMissed() {
	o.mu.Lock()
	defer o.done(o.stamp())
	o.briefDue = true
}

// quietEnded sends what quiet hours held for a reason, oldest first, and
// then a brief that came due inside them.
func (o *chatOutbound) quietEnded(now time.Time) []chatLine {
	o.mu.Lock()
	defer o.done(o.stamp())
	if o.rules.quiet(now) {
		return nil
	}
	out := o.releaseReasonsLocked()
	if o.briefDue {
		out = append(out, o.briefLocked()...)
	}
	return out
}

// briefLocked empties the queue into one message. A message held for a
// reason goes out as its own line first, because the person must not have to
// find it in a list.
func (o *chatOutbound) briefLocked() []chatLine {
	o.briefDue = false
	if len(o.held) == 0 && o.dropped == 0 {
		return nil
	}
	var out []chatLine
	var updates []chatHeld
	for _, h := range o.held {
		if h.Reason != "" {
			out = append(out, chatLine{h.Speaker, h.Text})
			continue
		}
		updates = append(updates, h)
	}
	older := o.dropped
	if len(updates) > chatBriefMax {
		older += len(updates) - chatBriefMax
		updates = updates[len(updates)-chatBriefMax:]
	}
	var b strings.Builder
	for _, h := range updates {
		who := h.Speaker
		if who == "" {
			who = i18n.T("the coordinator")
		}
		fmt.Fprintf(&b, "\n%s %s: %s", h.At.In(o.rules.loc).Format("15:04"), who, h.Text)
	}
	if len(updates) > 0 || older > 0 {
		head := i18n.T("%d updates from the team while you were away:", len(updates))
		if len(updates) == 1 {
			head = i18n.T("1 update from the team while you were away:")
		}
		switch {
		case older == 1:
			b.WriteString("\n" + i18n.T("1 older update is in the room, in the Talkoot app."))
		case older > 1:
			b.WriteString("\n" + i18n.T("%d older updates are in the room, in the Talkoot app.", older))
		}
		out = append(out, chatLine{"", head + b.String()})
	}
	o.held, o.dropped = nil, 0
	return out
}

// view is the queue as the app shows it. nextBrief and quietEnds come from
// the daemon's timers, and are zero when it has none.
func (o *chatOutbound) view(now, nextBrief, quietEnds time.Time) *ctrlproto.ChatOutbound {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := &ctrlproto.ChatOutbound{
		More:         o.dropped,
		Quiet:        o.rules.quiet(now),
		NextBrief:    nextBrief,
		BriefOnReply: len(o.rules.brief) == 0,
	}
	if v.Quiet {
		v.QuietEnds = quietEnds
	}
	for _, h := range o.held {
		text := h.Text
		if utf8.RuneCountInString(text) > 160 {
			text = string([]rune(text)[:160]) + "…"
		}
		v.Held = append(v.Held, ctrlproto.ChatHeld{From: h.Speaker, Text: text, At: h.At, Reason: h.Reason})
	}
	return v
}

// --- the workspace's hold on the policy ------------------------------------

// chatTimerOwner names the brief and the end of quiet hours in the daemon's
// timer service.
const chatTimerOwner = "talkoot-chat"

func chatBriefTimer(id string) string    { return id + ".brief" }
func chatQuietEndTimer(id string) string { return id + ".quiet-end" }

// chatOutboundSet makes the policy of the talkoot id with these rules, and
// sets its two timers. It replaces a policy made with other rules, and the
// queue's file carries the held messages over.
func (w *Workspace) chatOutboundSet(id string, rules chatOutboundRules) {
	svc := w.Timers()
	if svc == nil && len(rules.brief) > 0 {
		// With no timer nothing would ever send the brief, so the person's
		// next message does.
		w.diagf("talkoot %s: this host runs no timers, so the brief goes out when you next write and not at its times", id)
		rules.brief = nil
	}
	out := openChatOutbound(filepath.Join(talkoot.Dir(), id, "chat-outbound.json"), rules, w.diagf, w.announceTalkoots)
	w.chat.mu.Lock()
	if w.chat.outbound == nil {
		w.chat.outbound = map[string]*chatOutbound{}
	}
	w.chat.outbound[id] = out
	w.chat.mu.Unlock()
	if svc == nil {
		return
	}
	svc.Handle(chatTimerOwner, w.chatTimerFired)
	keep := func(timer, label string, times []timers.ClockTime) {
		if len(times) == 0 {
			if _, err := svc.Cancel(chatTimerOwner, timer); err != nil {
				w.diagf("talkoot %s: %v", id, err)
			}
			return
		}
		want := timers.Schedule{Daily: times}
		// 🔑 Set starts a schedule again from now. A daemon that set its
		// timers again at every start would lose the window that passed
		// while it was down, which is the morning's brief.
		if cur, ok := svc.Get(chatTimerOwner, timer); ok && cur.Schedule.String() == want.String() {
			return
		}
		if _, err := svc.Set(timers.Timer{Owner: chatTimerOwner, ID: timer, Label: label, Schedule: want}); err != nil {
			w.diagf("talkoot %s: %v", id, err)
		}
	}
	keep(chatBriefTimer(id), i18n.T("The chat brief of the team %s", id), rules.brief)
	var end []timers.ClockTime
	if rules.quietFrom != rules.quietTo {
		end = []timers.ClockTime{rules.quietTo}
	}
	keep(chatQuietEndTimer(id), i18n.T("The end of quiet hours for the team %s", id), end)
}

// chatOutboundFor returns the policy of the talkoot id. A bridge that was
// connected with no rules gets the default ones.
func (w *Workspace) chatOutboundFor(id string) *chatOutbound {
	w.chat.mu.Lock()
	out := w.chat.outbound[id]
	w.chat.mu.Unlock()
	if out != nil {
		return out
	}
	w.chatOutboundSet(id, chatOutboundRules{loc: time.Local})
	return w.chatOutboundFor(id)
}

// chatTimerFired is the timer service's handler. It tells the talkoot's pump,
// which does the sending.
func (w *Workspace) chatTimerFired(f timers.Firing) {
	id, brief := strings.CutSuffix(f.Timer.ID, ".brief")
	if !brief {
		id = strings.TrimSuffix(f.Timer.ID, ".quiet-end")
	}
	w.chat.mu.Lock()
	out := w.chat.outbound[id]
	var pump *chatTeamPump
	for _, b := range w.chat.bridges {
		if b.talkoot == id && b.pump != nil {
			pump = b.pump
		}
	}
	w.chat.mu.Unlock()
	switch {
	case pump != nil:
		pump.timerFired(brief)
	case brief && out != nil:
		// No chat is connected. The brief goes out with the next thing that
		// can send it.
		out.briefMissed()
	}
}

// chatOutboundView is the policy's state for the team's view.
func (w *Workspace) chatOutboundView(id string) *ctrlproto.ChatOutbound {
	w.chat.mu.Lock()
	out := w.chat.outbound[id]
	w.chat.mu.Unlock()
	if out == nil {
		return nil
	}
	var nextBrief, quietEnds time.Time
	if svc := w.Timers(); svc != nil {
		if t, ok := svc.Get(chatTimerOwner, chatBriefTimer(id)); ok {
			nextBrief = t.Next
		}
		if t, ok := svc.Get(chatTimerOwner, chatQuietEndTimer(id)); ok {
			quietEnds = t.Next
		}
	}
	return out.view(chatNow(), nextBrief, quietEnds)
}
