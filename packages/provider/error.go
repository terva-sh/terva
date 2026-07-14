package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProviderError is the typed error every in-tree client returns for a
// provider-side failure: a non-2xx HTTP response, an in-stream error
// event, or a connection that died before the protocol's terminal
// frame. Downstream classification — core's retry loop, the rescue
// dialog, 413 auto-compact — switches on these fields instead of
// substring-matching rendered prose, so a provider rewording an error
// can no longer silently change retry behavior.
//
// Clients construct it with their own protocol knowledge: only the
// anthropic client knows "overloaded_error" is transient, only the
// bedrock client knows "throttlingException" is. Policy lives where
// the information is; consumers just read Transient.
//
// Custom Client implementations (SDK embedders) that want retry and
// rescue classification should return this type too; untyped errors
// get a conservative transport-level fallback (IsTransportError) and
// are otherwise treated as permanent.
type ProviderError struct {
	// Provider is the client name as shown to users, e.g. "anthropic"
	// or "openai-compatible".
	Provider string
	// Status is the HTTP status code, or 0 when the failure was not
	// an HTTP response (stream death, in-stream error events).
	Status int
	// Transient marks failures worth retrying: rate limits, 5xx,
	// truncated streams, throttling. Set at construction by the code
	// that understands the wire protocol.
	Transient bool
	// RetryAfter is the server-requested wait parsed from a
	// Retry-After header; 0 when absent. Core's retry loop honors it
	// (capped) instead of its default backoff.
	RetryAfter time.Duration
	// Msg is the human-readable description: a body excerpt for HTTP
	// failures, the event message for in-stream errors.
	Msg string

	// err is the wrapped cause, surfaced via Unwrap so callers can
	// errors.Is against sentinels like io.ErrUnexpectedEOF.
	err error
}

func (e *ProviderError) Error() string {
	var b strings.Builder
	if e.Provider != "" {
		b.WriteString(e.Provider)
		b.WriteString(": ")
	}
	if e.Status != 0 {
		fmt.Fprintf(&b, "http %d: ", e.Status)
	}
	b.WriteString(e.Msg)
	if e.err != nil {
		fmt.Fprintf(&b, ": %v", e.err)
	}
	return b.String()
}

func (e *ProviderError) Unwrap() error { return e.err }

// NewHTTPError classifies a non-2xx HTTP response. retryAfter is the
// raw Retry-After header value ("" when absent); body is the response
// body (callers may pre-truncate).
func NewHTTPError(provider string, status int, retryAfter, body string) *ProviderError {
	return &ProviderError{
		Provider:   provider,
		Status:     status,
		Transient:  transientHTTPStatus(status),
		RetryAfter: ParseRetryAfter(retryAfter),
		Msg:        strings.TrimSpace(body),
	}
}

// NewStreamDeathError marks a connection that closed before the
// protocol's terminal frame (anthropic's message_stop, openai's
// [DONE], …) — a truncation, always worth retrying. Wraps
// io.ErrUnexpectedEOF so errors.Is keeps working.
func NewStreamDeathError(provider, terminal string) *ProviderError {
	return &ProviderError{
		Provider:  provider,
		Transient: true,
		Msg:       "stream ended before " + terminal,
		err:       io.ErrUnexpectedEOF,
	}
}

// ErrStreamLimit is the sentinel behind NewStreamLimitError, so callers
// can errors.Is a limit abort apart from a plain truncation.
var ErrStreamLimit = errors.New("event stream line exceeded its size limit")

// NewStreamLimitError marks a stream aborted because a single wire line
// exceeded the reader's ceiling.
//
// Unlike stream death this is *permanent*, and the distinction is the
// whole point. A truncated stream is a transport accident: retrying
// re-runs the request and probably succeeds. An over-limit line is
// deterministic — the server re-sends the identical oversized event on
// every attempt — so marking it transient burns the entire retry budget,
// paying full input tokens per attempt, to fail in exactly the same place
// and then blame the network. Transient stays false.
func NewStreamLimitError(provider string, max int) *ProviderError {
	return &ProviderError{
		Provider:  provider,
		Transient: false,
		Msg:       fmt.Sprintf("stream aborted: a single event exceeded the %d MiB line limit", max>>20),
		err:       ErrStreamLimit,
	}
}

// NewStreamReadError wraps a transport failure that killed an event
// stream mid-read. Transient: the read failed, not the payload.
func NewStreamReadError(provider string, err error) *ProviderError {
	return &ProviderError{
		Provider:  provider,
		Transient: true,
		Msg:       "event stream read failed",
		err:       err,
	}
}

// NewAPIError wraps an error event delivered inside an otherwise-OK
// stream. transient comes from the constructing client's knowledge of
// its protocol's error vocabulary.
func NewAPIError(provider, msg string, transient bool) *ProviderError {
	return &ProviderError{Provider: provider, Transient: transient, Msg: msg}
}

// transientHTTPStatus is the retry policy for plain HTTP failures:
// timeouts, rate limits, and server-side errors are transient; client
// errors are not. 529 is Anthropic's documented "overloaded", 522/524
// are Cloudflare origin-timeout codes seen through provider edges.
func transientHTTPStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
		522, 524, 529:
		return true
	}
	return false
}

// retryAdvice is the sentence OpenAI's backend puts in a generic server error
// when it carries no machine-readable code at all: "…You can retry your request,
// or contact support…". It is the server instructing the client to retry, which
// is a fact and not a guess — but it is the LAST thing consulted, only after both
// the transient and the permanent vocabularies below have declined to recognize
// the error, so a permanent failure whose prose happens to mention retrying can
// never reach it.
//
// This is the one prose match allowed anywhere in the retry path. Agent.
// canRetryError used to hold a list of them and it retried "prompt is too long:
// 208500 tokens" because "500" matched.
const retryAdvice = "you can retry your request"

// transientErrorCode is the retry policy for errors delivered INSIDE an
// otherwise-healthy stream — the sibling of [transientHTTPStatus], for the axis
// that has no status code. The wire has already returned 200; whatever went
// wrong is described only by the error frame's own vocabulary.
//
// It exists because every streaming decoder had grown its own copy of this
// judgement and they disagreed: codex knew about rate limits but called a
// server_error permanent, openai knew about server_error but never read the
// `code` field, anthropic knew about api_error and nothing else. The vocabulary
// is shared across the openai and anthropic wire formats, so the classifier is
// too, and a provider that learns a new retryable code teaches every other one.
//
// code and kind are the error object's "code" and "type" (either may be empty;
// providers populate them inconsistently, which is the whole problem). msg is the
// human-readable message, consulted only as a last resort.
func transientErrorCode(code, kind, msg string) bool {
	for _, v := range []string{code, kind} {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		// rate_limit_error, rate_limit_exceeded, rate_limited, …
		if strings.HasPrefix(v, "rate_limit") {
			return true
		}
		switch v {
		case "server_error", "internal_server_error", "service_unavailable",
			"overloaded_error", "timeout", "request_timeout",
			// anthropic's name for its own 500.
			"api_error":
			return true
		}
		// Recognized CLIENT errors are permanent, and must say so HERE — before
		// the prose fallback gets a chance to guess otherwise. Retrying one of
		// these just fails the same way N more times and spends the user's money
		// doing it.
		switch v {
		case "invalid_request_error", "invalid_api_key", "authentication_error",
			"permission_error", "not_found_error", "context_length_exceeded",
			"content_filter", "invalid_prompt":
			return false
		}
	}
	return strings.Contains(strings.ToLower(msg), retryAdvice)
}

// ParseRetryAfter parses a Retry-After header value: either delay
// seconds ("17") or an HTTP-date. Returns 0 for absent or
// unparseable values, never a negative duration.
func ParseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// IsTransportError reports whether err is a network-level failure
// worth retrying, classified by type rather than message prose:
// timeouts, abrupt EOFs, connection resets/refusals, broken pipes,
// and DNS blips. Context cancellation is never transient — it is the
// caller saying stop.
func IsTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
