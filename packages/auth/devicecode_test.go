package auth

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// The kimi copy consulted no deadline at all, so a login the user opened and
// then abandoned in the browser polled Kimi's token endpoint every five seconds
// for as long as the process lived. Nothing but process exit stopped it.
func TestAnAbandonedLoginStopsAtTheAuthorizationExpiry(t *testing.T) {
	polls := 0
	done := make(chan error, 1)
	go func() {
		_, err := pollDeviceToken(context.Background(), "test",
			time.Millisecond, 40*time.Millisecond,
			func(context.Context) (*OAuthToken, deviceRetry, error) {
				polls++
				return nil, devicePending, nil
			})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an expired device authorization returned no error")
		}
		if !strings.Contains(err.Error(), "expired") {
			t.Errorf("error does not name the cause: %v", err)
		}
		if polls == 0 {
			t.Error("the loop expired without ever polling")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poll never stopped: an abandoned login runs until the process dies")
	}
}

// The complement: a login that IS approved must return its token. Without
// this, "always expire" would pass the test above.
func TestAnApprovedLoginReturnsItsToken(t *testing.T) {
	calls := 0
	tok, err := pollDeviceToken(context.Background(), "test",
		time.Millisecond, time.Second,
		func(context.Context) (*OAuthToken, deviceRetry, error) {
			calls++
			if calls < 3 {
				return nil, devicePending, nil
			}
			return &OAuthToken{AccessToken: "granted"}, deviceStop, nil
		})
	if err != nil {
		t.Fatalf("an approved login failed: %v", err)
	}
	if tok == nil || tok.AccessToken != "granted" {
		t.Errorf("token = %+v", tok)
	}
	if calls != 3 {
		t.Errorf("polled %d times, want 3", calls)
	}
}

// An authorization with no expiry must not be treated as already expired —
// a zero deadline computed from time.Now() is in the past on the first tick.
func TestAnAuthorizationWithNoExpiryKeepsPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := pollDeviceToken(ctx, "test", time.Millisecond, 0,
		func(context.Context) (*OAuthToken, deviceRetry, error) {
			calls++
			if calls == 3 {
				cancel()
			}
			return nil, devicePending, nil
		})
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled — a missing expiry became an instant one", err)
	}
	if calls < 3 {
		t.Errorf("polled %d times before cancellation, want 3", calls)
	}
}

// slow_down must make the wait GROW and stay grown. The kimi copy folded
// slow_down into the pending branch and returned a fixed five seconds, so the
// server's "you are too fast" changed nothing at all. The copilot copy did back
// off, but computed the increase inside its once function and passed the
// ORIGINAL interval on the next iteration, so the growth lasted one tick.
//
// Asserted on the interval arithmetic rather than on wall-clock gaps: the real
// backoff is five seconds, and a timing test would either take fifteen seconds
// or measure something jitter can flip.
func TestSlowDownIncreasesTheIntervalAndKeepsIt(t *testing.T) {
	base := 5 * time.Second

	after := nextDeviceInterval(base, deviceSlowDown)
	if after != base+deviceBackoff {
		t.Errorf("slow_down gave %v, want %v", after, base+deviceBackoff)
	}
	// It must STICK: the next pending response keeps the grown interval.
	if kept := nextDeviceInterval(after, devicePending); kept != after {
		t.Errorf("the interval fell back to %v after one pending response — the back-off lasted a single tick", kept)
	}
	// And it compounds, as a server repeating slow_down expects.
	if twice := nextDeviceInterval(after, deviceSlowDown); twice != base+2*deviceBackoff {
		t.Errorf("a second slow_down gave %v, want %v", twice, base+2*deviceBackoff)
	}
	if got := nextDeviceInterval(base, devicePending); got != base {
		t.Errorf("a pending response changed the interval to %v", got)
	}
}

// nextDeviceInterval being correct proves nothing about the LOOP using what it
// returns. A mutation that computed the back-off and discarded it survived
// every other test in this file, which is the one-production-caller trap: the
// helper is pinned, its only caller is not.
//
// This reads the actual sequence of waits the loop asks for.
func TestTheLoopActuallyWaitsTheGrowingInterval(t *testing.T) {
	var waits []time.Duration
	real := deviceWait
	deviceWait = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { deviceWait = real })

	base := 2 * time.Second
	calls := 0
	_, _ = pollDeviceToken(context.Background(), "test", base, 0,
		func(context.Context) (*OAuthToken, deviceRetry, error) {
			calls++
			switch calls {
			case 1:
				return nil, devicePending, nil
			case 2:
				return nil, deviceSlowDown, nil
			case 3:
				return nil, devicePending, nil
			}
			return &OAuthToken{AccessToken: "t"}, deviceStop, nil
		})

	want := []time.Duration{base, base + deviceBackoff, base + deviceBackoff}
	if len(waits) != len(want) {
		t.Fatalf("the loop waited %d times, want %d: %v", len(waits), len(want), waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d was %v, want %v — the loop is not using the interval it computed", i, waits[i], want[i])
		}
	}
}

// slow_down and authorization_pending are BOTH delivered as HTTP 400. Both
// copies tested pending first, so the back-off arm was unreachable in each —
// the one ordering mistake that makes a slow_down handler look present and do
// nothing.
func TestSlowDownIsClassifiedBeforeTheBare400(t *testing.T) {
	if got := classifyDevicePoll("slow_down", http.StatusBadRequest); got != deviceSlowDown {
		t.Errorf("slow_down on a 400 classified as %v — the pending arm swallowed it", got)
	}
	if got := classifyDevicePoll("authorization_pending", http.StatusBadRequest); got != devicePending {
		t.Errorf("authorization_pending classified as %v", got)
	}
	if got := classifyDevicePoll("", http.StatusBadRequest); got != devicePending {
		t.Errorf("a bare 400 classified as %v", got)
	}
	if got := classifyDevicePoll("access_denied", http.StatusForbidden); got != deviceStop {
		t.Errorf("access_denied classified as %v — a refused grant must not be retried", got)
	}
	if got := classifyDevicePoll("", http.StatusOK); got != deviceStop {
		t.Errorf("a 200 with no token classified as %v", got)
	}
}

// Both providers must go through the shared loop. They were written twice and
// the copies disagreed on the interval, the back-off and the expiry; a third
// hand-rolled poll would disagree again.
func TestBothDeviceLoginsUseTheSharedPoll(t *testing.T) {
	for _, f := range []string{"kimi.go", "github_copilot.go"} {
		src := readSource(t, f)
		if !strings.Contains(src, "pollDeviceToken(") {
			t.Errorf("%s does not call pollDeviceToken — it has its own poll loop again", f)
		}
		if strings.Contains(src, "case <-time.After(") {
			t.Errorf("%s waits between attempts itself; the interval and the expiry belong to pollDeviceToken", f)
		}
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
