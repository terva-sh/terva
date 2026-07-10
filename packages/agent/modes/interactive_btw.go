package modes

// /btw — the side-chat overlay, as a client of the daemon's sidechat surface.
//
// The dialog used to read the live *core.Agent for its transcript, system
// prompt and provider client (the last AgentFor reader, plan 4.1). Those are
// all daemon-side now: openBtwDialog asks the carrier to freeze a snapshot and
// hands the dialog an asker bound to it. The TUI holds no agent, builds no
// request, and streams no completion — it opens, asks, and closes.

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// carrierSideChat adapts the carrier's sidechat verbs to dialogs.SideChatAsker.
// It holds the frozen snapshot's id; the dialog carries the conversation on top
// of it and replays its prior exchanges on every ask.
type carrierSideChat struct {
	carrier Carrier
	sess    string
	id      string
}

func (c *carrierSideChat) Ask(ctx context.Context, prior []dialogs.SideChatExchange, question string) (string, error) {
	wire := make([]ctrlproto.SideChatTurn, 0, len(prior))
	for _, p := range prior {
		wire = append(wire, ctrlproto.SideChatTurn{User: p.User, Assistant: p.Assistant})
	}
	return c.carrier.SideChatAsk(ctx, c.sess, c.id, wire, question)
}

// Close releases the frozen snapshot. Best-effort on its own context: the
// dialog is already gone, and an unknown id is a daemon-side no-op.
func (c *carrierSideChat) Close() {
	_ = c.carrier.SideChatClose(context.Background(), c.sess, c.id)
}

// openBtwDialog freezes a snapshot of the current session daemon-side and opens
// the side-chat overlay against it. The optional argument is auto-submitted as
// the first question, so `/btw does X work?` fires the completion immediately.
func (i *Interactive) openBtwDialog(args []string) {
	if !i.ready() {
		i.setStatusErr(i18n.T("not logged in. type /login first."))
		return
	}
	c := i.cfg.Carrier
	if c == nil {
		i.setStatusErr(i18n.T("not running on a ctrlproto carrier"))
		return
	}
	sess := i.carrierSession()
	// The freeze is a session resolve plus a transcript copy — no network on the
	// in-process carrier — so opening it inline keeps the "not logged in" and
	// error paths synchronous. The blocking model call happens later, per ask,
	// on the dialog's own goroutine.
	id, err := c.SideChatOpen(context.Background(), sess)
	if err != nil {
		i.setStatusErr(err.Error())
		i.invalidate()
		return
	}
	asker := &carrierSideChat{carrier: c, sess: sess, id: id}
	seed := strings.TrimSpace(strings.Join(args, " "))
	i.btwDialog.Open(i.cfg.Theme, asker, i.cfg.CWD, seed, i.invalidate)
	i.invalidate()
}
