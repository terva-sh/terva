package provider

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestProviderErrorFormats(t *testing.T) {
	cases := []struct {
		name string
		err  *ProviderError
		want string
	}{
		{"http", NewHTTPError("openai", 500, "", " boom \n"), "openai: http 500: boom"},
		{"stream death", NewStreamDeathError("anthropic", "message_stop"),
			"anthropic: stream ended before message_stop: unexpected EOF"},
		{"api event", NewAPIError("anthropic", "overloaded_error: Overloaded", true),
			"anthropic: overloaded_error: Overloaded"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s: Error() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProviderErrorTransience(t *testing.T) {
	transient := []*ProviderError{
		NewHTTPError("x", 408, "", ""),
		NewHTTPError("x", 429, "", ""),
		NewHTTPError("x", 500, "", ""),
		NewHTTPError("x", 502, "", ""),
		NewHTTPError("x", 503, "", ""),
		NewHTTPError("x", 504, "", ""),
		NewHTTPError("x", 529, "", ""),
		NewStreamDeathError("x", "[DONE]"),
	}
	for _, e := range transient {
		if !e.Transient {
			t.Errorf("status %d should be transient", e.Status)
		}
	}
	permanent := []*ProviderError{
		NewHTTPError("x", 400, "", ""),
		NewHTTPError("x", 401, "", ""),
		NewHTTPError("x", 403, "", ""),
		NewHTTPError("x", 404, "", ""),
		NewHTTPError("x", 413, "", ""),
		NewHTTPError("x", 418, "", ""), // the e2e fake's script-exhausted marker relies on this
		NewAPIError("x", "invalid_request_error: bad", false),
	}
	for _, e := range permanent {
		if e.Transient {
			t.Errorf("status %d (%s) should NOT be transient", e.Status, e.Msg)
		}
	}
}

func TestProviderErrorWrapping(t *testing.T) {
	death := NewStreamDeathError("openai", "[DONE]")
	if !errors.Is(death, io.ErrUnexpectedEOF) {
		t.Errorf("stream death should wrap io.ErrUnexpectedEOF")
	}

	// The type must survive fmt.Errorf %w wrapping — clients wrap with
	// their name prefix and core unwraps with errors.As.
	wrapped := fmt.Errorf("turn failed: %w", NewHTTPError("kimi", 429, "7", "slow down"))
	var pe *ProviderError
	if !errors.As(wrapped, &pe) {
		t.Fatalf("errors.As failed through fmt.Errorf wrap")
	}
	if pe.Status != 429 || pe.Provider != "kimi" || pe.RetryAfter != 7*time.Second {
		t.Errorf("unexpected fields after unwrap: %+v", pe)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"17", 17 * time.Second},
		{" 5 ", 5 * time.Second},
		{"-3", 0},
		{"soon", 0},
		{"0", 0},
	}
	for _, c := range cases {
		if got := ParseRetryAfter(c.in); got != c.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// HTTP-date form: a date in the future yields a positive wait, a
	// date in the past yields 0.
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got := ParseRetryAfter(future); got <= 0 || got > 91*time.Second {
		t.Errorf("future HTTP-date = %v, want ~90s", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := ParseRetryAfter(past); got != 0 {
		t.Errorf("past HTTP-date = %v, want 0", got)
	}
}
