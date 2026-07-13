package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Codex "banked rate-limit reset" support. OpenAI grants ChatGPT-subscription
// Codex accounts consumable credits that clear the 5h + weekly usage windows;
// the redeem button lives only in the desktop app and IDE extensions, not the
// public CLI. terva reaches the same undocumented account endpoints the
// extension uses, under /backend-api/wham (sibling to the /codex/responses
// stream path), with the same Bearer + ChatGPT-Account-Id auth every codex
// call carries. Endpoints reverse-engineered from the openai.chatgpt VS Code
// extension; see docs/proposals/usage-resets.md.
//
//	GET  /wham/rate-limit-reset-credits          list credits
//	POST /wham/rate-limit-reset-credits/consume  redeem one {credit_id, redeem_request_id}

// codexResetNamespace makes a redeem_request_id a pure function of the credit
// id (UUIDv5). Every attempt to redeem the same credit therefore sends an
// identical (credit_id, redeem_request_id) pair, which OpenAI's idempotency
// layer dedupes — so a retried or timed-out consume returns the original
// outcome instead of spending a second credit, with no local state to persist,
// lose, or reconcile. A fixed, terva-specific namespace keeps our ids from
// colliding with another client's for the same credit.
var codexResetNamespace = uuid.MustParse("d94b0f3a-1c7e-4b52-9a83-7f0e6d5c4b2a")

func codexRedeemRequestID(creditID string) string {
	return uuid.NewSHA1(codexResetNamespace, []byte(creditID)).String()
}

// codexWhamBase derives the /backend-api root (home of the /wham/* account
// endpoints) from the responses baseURL, which normally ends in
// /codex/responses. A base that doesn't match (an exotic proxy) is returned
// unchanged, so the wham call targets a plausible path and any mismatch
// surfaces as a clean HTTP error rather than a silent wrong-host request.
func codexWhamBase(baseURL string) string {
	b := strings.TrimRight(baseURL, "/")
	for _, suffix := range []string{"/codex/responses", "/responses"} {
		if strings.HasSuffix(b, suffix) {
			return strings.TrimSuffix(b, suffix)
		}
	}
	return b
}

// ---- wire types ----

type codexResetCredit struct {
	ID              string `json:"id"`
	ResetType       string `json:"reset_type"`
	Status          string `json:"status"`
	GrantedAt       string `json:"granted_at"`
	ExpiresAt       string `json:"expires_at"`
	RedeemStartedAt string `json:"redeem_started_at"`
	RedeemedAt      string `json:"redeemed_at"`
	Title           string `json:"title"`
	Description     string `json:"description"`
}

type codexResetCreditsResp struct {
	Credits        []codexResetCredit `json:"credits"`
	AvailableCount int                `json:"available_count"`
}

type codexConsumeResp struct {
	Code         string           `json:"code"`
	Credit       codexResetCredit `json:"credit"`
	WindowsReset int              `json:"windows_reset"`
}

// ---- mapping ----

// codexResetFromWire maps a wire credit to the generic form. It is clock-free
// (fully table-testable): "expired" is a display-time comparison of ExpiresAt
// to now, not a status the mapper invents, so a list result is deterministic.
func codexResetFromWire(c codexResetCredit) UsageReset {
	r := UsageReset{
		ID:          c.ID,
		Kind:        c.ResetType,
		Title:       c.Title,
		Description: c.Description,
		GrantedAt:   parseCodexResetTime(c.GrantedAt),
		ExpiresAt:   parseCodexResetTime(c.ExpiresAt),
		RedeemedAt:  parseCodexResetTime(c.RedeemedAt),
	}
	switch {
	case c.RedeemedAt != "" || c.Status == "redeemed":
		r.Status = ResetRedeemed
	case c.RedeemStartedAt != "":
		r.Status = ResetPending
	case c.Status == "available":
		r.Status = ResetAvailable
	default:
		// Forward-compat: surface an unknown status verbatim rather than
		// forcing it into one of ours.
		r.Status = ResetStatus(c.Status)
	}
	return r
}

func parseCodexResetTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// ---- UsageResetProvider ----

// resetsTTL bounds how often ListResets re-fetches. Credits are granted rarely
// and expire on multi-day horizons, so the list is all but static — the one
// thing that changes it within a TTL is a redeem, and ConsumeReset invalidates
// the cache explicitly rather than waiting this out.
//
// Without it every REMOUNT of a UI that lists credits is an uncached GET
// against an undocumented endpoint: the web panel fetches when its resets
// section mounts, which happens again on every pane close/reopen, surface
// tab-switch, and session change. Flipping the pane 30 times sent 30 requests.
// (Usage has had the same guard since it shipped — usagePollTTL.)
const resetsTTL = 5 * time.Minute

func (c *codexClient) ListResets(ctx context.Context) ([]UsageReset, error) {
	c.mu.Lock()
	if c.hasResets && time.Since(c.resetsAt) < resetsTTL {
		out := append([]UsageReset(nil), c.resets...) // copy: callers must not alias the cache
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	var resp codexResetCreditsResp
	if err := c.whamJSON(ctx, http.MethodGet, "/wham/rate-limit-reset-credits", nil, &resp); err != nil {
		// Deliberately NOT serving a stale list on error: this drives a
		// spend-a-credit button, and a failed refresh must not leave a
		// confidently-rendered list of credits that may no longer exist.
		return nil, err
	}
	out := make([]UsageReset, 0, len(resp.Credits))
	for _, cr := range resp.Credits {
		out = append(out, codexResetFromWire(cr))
	}

	c.mu.Lock()
	c.resets = append([]UsageReset(nil), out...)
	c.hasResets = true
	c.resetsAt = time.Now()
	c.mu.Unlock()
	return out, nil
}

// invalidateResets drops the cached credit list, so the next ListResets goes to
// the backend. Called after a redeem: the whole point of the next list is to
// show the credit gone, and serving the pre-redeem cache would show it still
// there — the UI refreshes immediately after consuming, so this is not
// theoretical.
func (c *codexClient) invalidateResets() {
	c.mu.Lock()
	c.resets, c.hasResets, c.resetsAt = nil, false, time.Time{}
	c.mu.Unlock()
}

func (c *codexClient) ConsumeReset(ctx context.Context, id string) (UsageResetResult, error) {
	if strings.TrimSpace(id) == "" {
		return UsageResetResult{}, fmt.Errorf("openai-codex: empty reset credit id")
	}
	body := map[string]string{
		"credit_id":         id,
		"redeem_request_id": codexRedeemRequestID(id),
	}
	var resp codexConsumeResp
	err := c.whamJSON(ctx, http.MethodPost, "/wham/rate-limit-reset-credits/consume", body, &resp)
	// Invalidate on failure too: a redeem that timed out may well have landed
	// (it is idempotent by construction, not by luck — see codexRedeemRequestID),
	// so the cached list is no longer trustworthy either way.
	c.invalidateResets()
	if err != nil {
		return UsageResetResult{}, err
	}
	return UsageResetResult{
		Reset:        codexResetFromWire(resp.Credit),
		WindowsReset: resp.WindowsReset,
	}, nil
}

// whamJSON performs an authenticated JSON request against a /wham account
// endpoint and decodes the response into out. It resolves the credential per
// call (like Stream), carries the same Bearer + account-id auth, and turns a
// non-2xx into an HTTPError so the caller sees the provider's own message.
func (c *codexClient) whamJSON(ctx context.Context, method, path string, body any, out any) error {
	url := codexWhamBase(c.baseURL) + path

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("openai-codex: encode %s body: %w", path, err)
		}
		reqBody = bytes.NewReader(b)
	}

	token, err := c.cred(ctx)
	if err != nil {
		return fmt.Errorf("openai-codex: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("openai-codex: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("chatgpt-account-id", c.accountID)
	req.Header.Set("accept", "application/json")
	req.Header.Set("originator", "terva")
	req.Header.Set("user-agent", codexUserAgent())
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openai-codex: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return NewHTTPError("openai-codex", resp.StatusCode, resp.Header.Get("Retry-After"), string(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("openai-codex: decode %s response: %w", path, err)
		}
	}
	return nil
}
