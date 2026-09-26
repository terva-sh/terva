package auth

import (
	"context"
	"net/http"
	"time"

	"terva.sh/terva/packages/i18n"
)

// The RFC 8628 device-code poll, written once.
//
// It was written twice, and the copies disagreed on every part that matters.
// The github copilot one honoured the server's interval, backed off on
// slow_down, and stopped at the authorization's expiry. The kimi one threw all
// three away: it hard-coded a five second wait, folded slow_down into the
// pending branch, and consulted no deadline at all — so a login the user
// abandoned in the browser polled Kimi's token endpoint every five seconds
// until the process died.
//
// Both also had the same ordering defect, which is why this is one function
// rather than one function copied carefully: they tested authorization_pending
// (or a bare 400) BEFORE slow_down, and a slow_down arrives as a 400. The
// back-off arm could never be reached in either.

// deviceRetry is what one poll response says about the next attempt.
type deviceRetry int

const (
	deviceStop     deviceRetry = iota // a token, or a terminal error
	devicePending                     // not yet approved; wait the current interval
	deviceSlowDown                    // the server says we are too fast
)

// classifyDevicePoll maps one token-endpoint response to what to do next.
//
// slow_down is tested FIRST because it arrives as HTTP 400, which the pending
// arm also matches. Reversing these two silently disables the back-off.
func classifyDevicePoll(errCode string, status int) deviceRetry {
	switch {
	case errCode == "slow_down":
		return deviceSlowDown
	case errCode == "authorization_pending", status == http.StatusBadRequest:
		return devicePending
	default:
		return deviceStop
	}
}

// deviceBackoff is the increase RFC 8628 §3.5 prescribes per slow_down.
const deviceBackoff = 5 * time.Second

// nextDeviceInterval is the wait before the attempt after this one.
//
// Separate from the loop so the property can be tested without waiting five
// real seconds: the interval GROWS on slow_down and keeps the growth, because a
// back-off that reverts to the server's original interval on the next pending
// response is not a back-off.
func nextDeviceInterval(cur time.Duration, retry deviceRetry) time.Duration {
	if retry == deviceSlowDown {
		return cur + deviceBackoff
	}
	return cur
}

// pollDeviceToken drives the poll loop until the grant is approved, refused, or
// expires.
//
// once performs a single poll and returns the token (when approved), the retry
// class, and any terminal error. The wait between attempts is this loop's job,
// not its caller's: it grows with each slow_down and never shrinks, because a
// back-off that resets to the server's original interval on the next tick is
// not a back-off.
//
// expiresIn of zero means the authorization carried no expiry; the loop then
// runs until ctx is cancelled, which is the caller's own timeout.
func pollDeviceToken(
	ctx context.Context,
	label string,
	interval, expiresIn time.Duration,
	once func(ctx context.Context) (*OAuthToken, deviceRetry, error),
) (*OAuthToken, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(expiresIn)
	for {
		if expiresIn > 0 && time.Now().After(deadline) {
			return nil, i18n.Errorf("%s device login expired before it was approved", label)
		}
		tok, retry, err := once(ctx)
		if err != nil {
			return nil, err
		}
		if tok != nil {
			return tok, nil
		}
		interval = nextDeviceInterval(interval, retry)
		if err := deviceWait(ctx, interval); err != nil {
			return nil, err
		}
	}
}

// deviceWait is the sleep between attempts.
//
// A seam, not a style: nextDeviceInterval being right proves nothing about the
// loop USING what it returns, and a mutation that computed the back-off and
// threw it away survived every other test here. Swapping this lets a test read
// the actual sequence of waits without spending five real seconds per tick.
var deviceWait = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
