package modes

// Host glue for the /resets dialog. Listing is a read-only carrier round-trip;
// consuming spends an irreversible credit, so it flows only from an explicit
// in-dialog confirmation (ResetsDialog gates that) and never from anywhere
// else. Both calls run off the UI goroutine and marshal their result back onto
// it, like the usage mirror.

import (
	"context"
	"time"
)

// resetsConsumeTimeout bounds a redeem round-trip. More generous than the list
// timeout: a consume mutates account state and we would rather wait than orphan
// a spend whose result we failed to observe.
const resetsConsumeTimeout = 20 * time.Second

// openResetsDialog shows the /resets modal and kicks the background list fetch.
// The dialog opens in a loading state and fills in when fetchResetsList lands.
func (i *Interactive) openResetsDialog() {
	i.resetsDialog.Open(i.cfg.Provider)
	go i.fetchResetsList()
	i.invalidate()
}

// fetchResetsList pulls the provider's reset credits and hands them to the open
// dialog. Read-only.
func (i *Interactive) fetchResetsList() {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil || sess == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageFetchTimeout)
	defer cancel()
	res, err := c.ListResets(ctx, sess)
	i.runOnMain(func() {
		i.resetsDialog.SetList(res, err)
		i.invalidate()
	})
}

// consumeReset redeems the credit the user confirmed in the dialog. It is the
// ONLY caller of the carrier consume verb; the dialog's two-step confirm is the
// gate, so this performs the spend directly. Marks the dialog busy, then
// reports the outcome.
func (i *Interactive) consumeReset(id string) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil || sess == "" {
		return
	}
	i.resetsDialog.SetBusy()
	i.invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetsConsumeTimeout)
		defer cancel()
		res, err := c.ConsumeReset(ctx, sess, id)
		i.runOnMain(func() {
			i.resetsDialog.Finish(res, err)
			// A redemption cleared the provider's windows; refresh the usage
			// mirror so the status-bar meters reflect it promptly.
			i.refreshUsageAsync(false)
			i.invalidate()
		})
	}()
}
