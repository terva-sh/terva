package modes

// A replay's answer to a prompt, played as keystrokes.
//
// A live daemon resolves a prompt by naming it, and this client drops the
// dialog: whoever answered already saw their own keys. A replay carrier also
// names the option that won (ctrlproto.Resolved.Option), and here that is
// played back through the dialog's own key handler: the cursor walks to the
// option, overshoots by one row and comes back the way a hand does, a note is
// typed, and Enter answers. The dialog resolves through the same channel a
// person's Enter resolves it, so nothing here knows how a dialog closes.
//
// The walk is only for a replay, checked at the carrier, and only for the
// request on screen: a queued one is dismissed as before, because a viewer
// could not have seen it answered.

import (
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/replay"
	"terva.sh/terva/packages/tui"
)

// The step lengths are the replay package's, which adds the same total to
// the frame after a resolution so the scene waits for the walk. Vars so a
// test can compress them.
var (
	dialogWalkStep     = replay.WalkStep
	dialogWalkTypeStep = replay.WalkTypeStep
)

// walkKeys is the key sequence that moves a cursor from row 0 to option
// (1-based) in a list of n rows: down to it, one row past when there is one,
// back, then Enter. A first-row answer still shows a glance at the second.
func walkKeys(option, n int) []tui.Key {
	down := tui.Key{Kind: tui.KeyDown}
	up := tui.Key{Kind: tui.KeyUp}
	var keys []tui.Key
	for range option - 1 {
		keys = append(keys, down)
	}
	if option < n {
		keys = append(keys, down, up)
	} else if option > 1 {
		keys = append(keys, up, down)
	}
	return keys
}

// walkCarrierPermission plays a resolved permission as keystrokes and reports
// whether it did; false means the caller should dismiss the dialog instead.
func (i *Interactive) walkCarrierPermission(r ctrlproto.Resolved) bool {
	if _, replay := i.replayController(); !replay || r.Option <= 0 {
		return false
	}
	i.mu.Lock()
	cr := i.carrierPerm[r.CallID]
	i.mu.Unlock()
	if cr == nil || !i.confirmDialog.IsShowing(cr) {
		return false
	}
	keys := append(walkKeys(r.Option, 5), tui.Key{Kind: tui.KeyEnter})
	i.playKeys(keys, nil, func(k tui.Key) { i.confirmDialog.HandleKey(k) })
	return true
}

// walkCarrierAsk is walkCarrierPermission for a question: the cursor walks to
// the option, a note is typed after n, and Enter answers.
func (i *Interactive) walkCarrierAsk(r ctrlproto.Resolved) bool {
	if _, replay := i.replayController(); !replay || r.Option <= 0 {
		return false
	}
	i.mu.Lock()
	qr := i.carrierAsk[r.AskID]
	i.mu.Unlock()
	if qr == nil || !i.questionDialog.IsShowing(qr) || len(qr.Questions) != 1 {
		return false
	}
	q := qr.Questions[0]
	if r.Option > len(q.Options) {
		return false
	}
	keys := walkKeys(r.Option, len(q.Options))
	var typed []rune
	if r.Note != "" {
		keys = append(keys, tui.Key{Kind: tui.KeyRune, Rune: 'n'})
		typed = []rune(r.Note)
	}
	i.playKeys(keys, typed, func(k tui.Key) { i.questionDialog.HandleKey(k) })
	return true
}

// playKeys feeds keys to a dialog on the main goroutine, one per step, then
// the typed runes one per type step, then Enter. Steps stretch with the
// replay's speed so a half-speed recording moves the cursor at half speed.
func (i *Interactive) playKeys(keys []tui.Key, typed []rune, feed func(tui.Key)) {
	i.mu.Lock()
	speed := i.replayState.Speed
	i.mu.Unlock()
	if speed <= 0 {
		speed = 1
	}
	step := time.Duration(float64(dialogWalkStep) / speed)
	typeStep := time.Duration(float64(dialogWalkTypeStep) / speed)
	go func() {
		press := func(k tui.Key) {
			i.runOnMain(func() { feed(k); i.invalidate() })
		}
		for _, k := range keys {
			time.Sleep(step)
			press(k)
		}
		for _, r := range typed {
			time.Sleep(typeStep)
			press(tui.Key{Kind: tui.KeyRune, Rune: r})
		}
		time.Sleep(step)
		press(tui.Key{Kind: tui.KeyEnter})
	}()
}
