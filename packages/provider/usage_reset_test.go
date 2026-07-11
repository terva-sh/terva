package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Tests for the generic reset seam: capability probing and wrapper-walk
// routing, independent of any provider's wire format.

func TestClientSupportsResets(t *testing.T) {
	if !ClientSupportsResets(NewOpenAICodex("tok", "acct", "")) {
		t.Error("codex client should advertise reset support")
	}
	if ClientSupportsResets(NewOpenAI("tok", "")) {
		t.Error("plain openai client should not advertise reset support")
	}
}

func TestClientResetsThroughWrapper(t *testing.T) {
	// A wrapped codex client must still be reachable — same clientAs walk every
	// other capability probe uses.
	wrapped := newPollingUsageClient(NewOpenAICodex("tok", "acct", ""), time.Minute,
		func(ctx context.Context) (UsageSnapshot, error) { return UsageSnapshot{}, nil })
	if !ClientSupportsResets(wrapped) {
		t.Error("reset support did not survive the wrapper")
	}
}

func TestClientListResetsUnsupportedIsEmpty(t *testing.T) {
	got, err := ClientListResets(context.Background(), NewOpenAI("tok", ""))
	if err != nil || got != nil {
		t.Errorf("unsupported ListResets = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestClientConsumeResetUnsupportedErrors(t *testing.T) {
	// Consume must fail loudly on a client that can't spend — a silent success
	// would falsely imply a credit moved.
	_, err := ClientConsumeReset(context.Background(), NewOpenAI("tok", ""), "credit_x")
	if !errors.Is(err, ErrResetsUnsupported) {
		t.Errorf("unsupported ConsumeReset err = %v, want ErrResetsUnsupported", err)
	}
}

func TestUsageResetAvailable(t *testing.T) {
	if !(UsageReset{Status: ResetAvailable}).Available() {
		t.Error("ResetAvailable credit should be Available()")
	}
	for _, s := range []ResetStatus{ResetPending, ResetRedeemed, ResetExpired} {
		if (UsageReset{Status: s}).Available() {
			t.Errorf("%s credit should not be Available()", s)
		}
	}
}
