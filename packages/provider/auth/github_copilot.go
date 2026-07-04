package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
)

const githubCopilotClientID = "Iv1.b507a08c87ecfe98"

// GitHubCopilotDeviceAuthorization is GitHub's OAuth 2 device-code
// response used for Copilot subscription login.
type GitHubCopilotDeviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// RequestGitHubCopilotDeviceAuthorization starts GitHub Copilot's
// device-code login. The resulting GitHub access token is later traded
// for short-lived Copilot inference tokens by the provider client.
func RequestGitHubCopilotDeviceAuthorization(ctx context.Context) (GitHubCopilotDeviceAuthorization, error) {
	form := url.Values{}
	form.Set("client_id", githubCopilotClientID)
	form.Set("scope", "read:user")
	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/device/code", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return GitHubCopilotDeviceAuthorization{}, err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", "GitHubCopilotChat/0.35.0")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return GitHubCopilotDeviceAuthorization{}, fmt.Errorf("github copilot device authorization: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GitHubCopilotDeviceAuthorization{}, fmt.Errorf("github copilot device authorization http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out GitHubCopilotDeviceAuthorization
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("parse github copilot device authorization: %w", err)
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return out, nil
}

// PollGitHubCopilotDeviceToken polls until GitHub's browser/device-code
// login completes and returns the GitHub access token.
func PollGitHubCopilotDeviceToken(ctx context.Context, auth GitHubCopilotDeviceAuthorization) (*OAuthToken, error) {
	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	for {
		if auth.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, i18n.Errorf("github copilot device login expired")
		}
		tok, retry, err := pollGitHubCopilotDeviceTokenOnce(ctx, auth.DeviceCode, interval)
		if err != nil {
			return nil, err
		}
		if tok != nil {
			return tok, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retry):
		}
	}
}

func pollGitHubCopilotDeviceTokenOnce(ctx context.Context, deviceCode string, interval time.Duration) (*OAuthToken, time.Duration, error) {
	form := url.Values{}
	form.Set("client_id", githubCopilotClientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", "GitHubCopilotChat/0.35.0")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("github copilot token poll: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var raw struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &raw)
	if resp.StatusCode == http.StatusOK && raw.AccessToken != "" {
		return &OAuthToken{
			AccessToken: raw.AccessToken,
			TokenType:   raw.TokenType,
			Scope:       raw.Scope,
			ClientID:    githubCopilotClientID,
		}, 0, nil
	}
	if raw.Error == "authorization_pending" || resp.StatusCode == http.StatusBadRequest {
		return nil, interval, nil
	}
	if raw.Error == "slow_down" {
		return nil, interval + 5*time.Second, nil
	}
	if raw.Error != "" {
		return nil, 0, fmt.Errorf("github copilot token poll: %s: %s", raw.Error, raw.ErrorDescription)
	}
	return nil, 0, fmt.Errorf("github copilot token poll http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
