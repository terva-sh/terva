package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Codex (ChatGPT subscription) client. Uses OpenAI's Responses API via
// chatgpt.com/backend-api with the chatgpt-account-id handshake.
//
// Wire protocol notes:
//   - Endpoint: POST https://chatgpt.com/backend-api/codex/responses
//   - Headers: Authorization: Bearer <access_token>, chatgpt-account-id: <id>,
//     OpenAI-Beta: responses=experimental, originator: terva
//   - Body: OpenAI Responses API shape (not chat/completions).
//     input: [{role, content: [{type: "input_text" | "input_image" | ... }]}]
//     instructions: <system prompt>
//     tools: [{type:"function", name, description, parameters, strict}]
//     stream: true
//   - SSE events: response.output_item.added, response.output_text.delta,
//     response.function_call_arguments.delta, response.output_item.done,
//     response.completed, response.failed, error
//
// The access_token comes from the OpenAI OAuth flow (auth.openai.com).
// The accountID is parsed from the id_token JWT's `chatgpt_account_id`
// claim; see auth/oauth.go.

const codexDefaultBaseURL = "https://chatgpt.com/backend-api/codex/responses"

type codexClient struct {
	cred      CredentialSource
	accountID string
	baseURL   string
	http      *http.Client

	// Latest usage snapshot parsed from response headers (see
	// recordUsageHeaders). Guarded by mu because Stream runs
	// concurrently with UsageSnapshot reads from the TUI.
	mu        sync.Mutex
	lastUsage UsageSnapshot
	hasUsage  bool

	// Reset credits, cached for resetsTTL (see ListResets). Also under mu.
	resets    []UsageReset
	hasResets bool
	resetsAt  time.Time
}

// NewOpenAICodex creates a client that talks to ChatGPT's Codex endpoint
// using a subscription OAuth access token and the user's ChatGPT
// account id. baseURL may be empty to use the default.
func NewOpenAICodex(token, accountID, baseURL string) Client {
	return NewOpenAICodexSource(StaticCredential(token), accountID, baseURL)
}

// NewOpenAICodexSource is NewOpenAICodex with a CredentialSource instead of a
// fixed token, so the OAuth access token can rotate (refresh) without
// rebuilding the client — the client resolves it once per Stream.
func NewOpenAICodexSource(cred CredentialSource, accountID, baseURL string) Client {
	if baseURL == "" {
		baseURL = codexDefaultBaseURL
	}
	return &codexClient{
		cred:      cred,
		accountID: accountID,
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: 0},
	}
}

func (c *codexClient) Name() string { return "openai-codex" }

// codexUserAgent is the UA sent on every chatgpt.com/backend-api call (the
// responses stream and the /wham account endpoints), so both paths present
// terva identically.
func codexUserAgent() string {
	return fmt.Sprintf("terva (%s %s)", runtime.GOOS, runtime.GOARCH)
}

// Capabilities declares that tool-result images must be mirrored into
// a following user message. The Responses API's function_call_output
// only carries a string (see buildRequest), so image bytes can't ride
// along with the tool result and are delivered via the mirror instead.
func (c *codexClient) Capabilities() ClientCapabilities {
	return ClientCapabilities{MirrorsToolImages: true}
}

// ---- Responses API wire types (subset needed for terva's surface) ----

type codexInputText struct {
	Type string `json:"type"` // "input_text"
	Text string `json:"text"`
}

type codexInputImage struct {
	Type     string `json:"type"` // "input_image"
	Detail   string `json:"detail"`
	ImageURL string `json:"image_url"`
}

type codexOutputText struct {
	Type        string `json:"type"` // "output_text"
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type codexInputMessage struct {
	Type    string `json:"type,omitempty"` // "message" (optional for input)
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type codexOutputMessage struct {
	Type    string            `json:"type"` // "message"
	Role    string            `json:"role"`
	Status  string            `json:"status,omitempty"`
	ID      string            `json:"id,omitempty"`
	Content []codexOutputText `json:"content"`
}

type codexFunctionCall struct {
	Type      string `json:"type"` // "function_call"
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type codexFunctionCallOutput struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"` // string (or ResponseFunctionCallOutputItemList for images; v1 only uses string)
}

// codexReasoningItem mirrors the Responses API "reasoning" output item.
// We capture it on incoming streams and replay it verbatim on follow-up
// requests: the API rejects assistant tool-call replays without it when
// thinking is enabled.
type codexReasoningItem struct {
	Type             string `json:"type"` // "reasoning"
	ID               string `json:"id,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	// Summary is required by the Responses API even when no summary text
	// was streamed; encode an empty array rather than omitting the field.
	Summary []codexReasoningSummary `json:"summary"`
}

type codexReasoningSummary struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text"`
}

type codexTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type codexReasoningConfig struct {
	Effort string `json:"effort,omitempty"`
}

type codexRequest struct {
	Model             string                `json:"model"`
	Store             bool                  `json:"store"`
	Stream            bool                  `json:"stream"`
	Instructions      string                `json:"instructions,omitempty"`
	Input             []any                 `json:"input"`
	Tools             []codexTool           `json:"tools,omitempty"`
	ToolChoice        string                `json:"tool_choice,omitempty"`
	ParallelToolCalls bool                  `json:"parallel_tool_calls"`
	Include           []string              `json:"include,omitempty"`
	Reasoning         *codexReasoningConfig `json:"reasoning,omitempty"`
	// PromptCacheKey pins this conversation's prefix-cache routing so
	// concurrent sessions on one account stop evicting each other
	// (Codex CLI sends the same field to this endpoint).
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

// ---- Request building ----

func (c *codexClient) buildRequest(req Request) (*codexRequest, error) {
	m, err := FindModel("openai-codex", req.Model)
	if err != nil {
		m, err = FindModel("openai", req.Model)
	}
	if err != nil {
		return nil, err
	}
	_ = m

	body := &codexRequest{
		Model:             req.Model,
		Store:             false,
		Stream:            true,
		Instructions:      req.System,
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
		PromptCacheKey:    req.PromptCacheKey,
	}
	if m.Reasoning {
		if effort := OpenAICodexReasoningEffort(req.Reasoning, req.Model); effort != "" {
			body.Reasoning = &codexReasoningConfig{Effort: effort}
		}
	}
	if len(req.Tools) > 0 {
		body.ToolChoice = "auto"
		for _, t := range req.Tools {
			params := t.Schema
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			body.Tools = append(body.Tools, codexTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			})
		}
	}

	msgIdx := 0
	req.Messages = RepairOrphanedToolResults(req.Messages)
	// Same guard as the chat-completions builder: keep a card's seeded
	// leading-assistant greeting valid for backends that require user-first.
	req.Messages = EnsureLeadingUserTurn(req.Messages)
	for _, msg := range req.Messages {
		switch msg.Role {
		case RoleUser:
			content := []any{}
			for _, c := range msg.Content {
				switch v := c.(type) {
				case TextBlock:
					if v.Text != "" {
						content = append(content, codexInputText{Type: "input_text", Text: v.Text})
					}
				case ImageBlock:
					url := "data:" + v.MimeType + ";base64," + base64.StdEncoding.EncodeToString(v.Data)
					content = append(content, codexInputImage{Type: "input_image", Detail: "auto", ImageURL: url})
				}
			}
			if len(content) == 0 {
				continue
			}
			body.Input = append(body.Input, codexInputMessage{Role: "user", Content: content})
		case RoleAssistant:
			// Emit one output_message per text block, one function_call per
			// tool call, and one reasoning item per ReasoningBlock,
			// preserving the order so the model sees the same interleaving
			// we captured. The reasoning replay is what keeps OpenAI
			// Codex from rejecting follow-up tool calls with
			// "thinking is enabled but reasoning_content is missing".
			for _, c := range msg.Content {
				switch v := c.(type) {
				case ReasoningBlock:
					item := codexReasoningItem{
						Type:             "reasoning",
						ID:               v.ID,
						EncryptedContent: v.Encrypted,
						Summary:          []codexReasoningSummary{},
					}
					if v.Summary != "" {
						item.Summary = []codexReasoningSummary{{Type: "summary_text", Text: v.Summary}}
					}
					body.Input = append(body.Input, item)
				case TextBlock:
					if v.Text == "" {
						continue
					}
					msgIdx++
					body.Input = append(body.Input, codexOutputMessage{
						Type:   "message",
						Role:   "assistant",
						Status: "completed",
						ID:     fmt.Sprintf("msg_%d", msgIdx),
						Content: []codexOutputText{
							{Type: "output_text", Text: v.Text, Annotations: []any{}},
						},
					})
				case ToolCallBlock:
					args := string(v.Arguments)
					if args == "" || !json.Valid([]byte(args)) {
						args = "{}"
					}
					callID, _ := splitCallID(v.ID)
					body.Input = append(body.Input, codexFunctionCall{
						Type:      "function_call",
						CallID:    callID,
						Name:      v.Name,
						Arguments: args,
					})
				}
			}
		case RoleTool:
			for _, c := range msg.Content {
				if tr, ok := c.(ToolResultBlock); ok {
					var text strings.Builder
					imageCount := 0
					for _, inner := range tr.Content {
						switch v := inner.(type) {
						case TextBlock:
							if text.Len() > 0 {
								text.WriteString("\n")
							}
							text.WriteString(v.Text)
						case ImageBlock:
							imageCount++
						}
					}
					// The Responses API function_call_output only carries a
					// string, so image bytes cannot ride along here. The agent
					// loop mirrors any tool-result images into the following
					// user message (which this client does serialize as
					// input_image). Leave a short text note so an image-only
					// result is not an empty output the API may reject, and so
					// the model knows the image arrives next.
					out := text.String()
					if out == "" && imageCount > 0 {
						if imageCount == 1 {
							out = "[image returned; see the following message]"
						} else {
							out = fmt.Sprintf("[%d images returned; see the following message]", imageCount)
						}
					}
					callID, _ := splitCallID(tr.CallID)
					body.Input = append(body.Input, codexFunctionCallOutput{
						Type:   "function_call_output",
						CallID: callID,
						Output: out,
					})
				}
			}
		}
	}

	// The host-assembled ephemeral tail (extension context cards, the
	// context-pressure note) rides as a trailing user input message,
	// mirroring the chat-completions and Anthropic builders. Trailing
	// placement keeps the cached transcript prefix stable — only this
	// block re-processes each step.
	if req.EphemeralContext != "" {
		body.Input = append(body.Input, codexInputMessage{
			Role:    "user",
			Content: []any{codexInputText{Type: "input_text", Text: req.EphemeralContext}},
		})
	}

	return body, nil
}

func splitCallID(id string) (string, string) {
	if i := strings.Index(id, "|"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, ""
}

// ---- Streaming ----

func (c *codexClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}

	// Resolve the credential once per turn (may refresh an expired OAuth
	// token); retries within this Stream reuse it.
	token, err := c.cred(ctx)
	if err != nil {
		return nil, fmt.Errorf("openai-codex: %w", err)
	}

	newReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("accept", "text/event-stream")
		httpReq.Header.Set("authorization", "Bearer "+token)
		httpReq.Header.Set("chatgpt-account-id", c.accountID)
		httpReq.Header.Set("openai-beta", "responses=experimental")
		httpReq.Header.Set("originator", "terva")
		httpReq.Header.Set("user-agent", codexUserAgent())
		return httpReq, nil
	}

	resp, err := doStreamWithRetry(ctx, c.http, newReq)
	if err != nil {
		return nil, fmt.Errorf("openai-codex: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := errorBodySnippet(resp.Body)
		resp.Body.Close()
		return nil, NewHTTPError("openai-codex", resp.StatusCode, resp.Header.Get("Retry-After"), snippet)
	}

	// Codex returns the subscription usage windows as response headers
	// on every successful call — capture them for /usage at no extra
	// request cost.
	c.recordUsageHeaders(resp.Header)

	out := make(chan Event, 16)
	go c.runStream(ctx, resp, req, out)
	return out, nil
}

// UsageSnapshot returns the most recent usage picture parsed from a
// Codex response. It is empty (ok=false) until the first turn populates
// it, and resets when a token refresh rebuilds the client (the next
// turn repopulates from fresh headers).
func (c *codexClient) UsageSnapshot() (UsageSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastUsage, c.hasUsage
}

// SeedUsage primes the snapshot from a predecessor client, so a rebuild
// (re-login, endpoint change) doesn't blank the usage meters until the next
// turn. Foreign snapshots are rejected — another provider's windows say
// nothing about this subscription — and a live observation always wins over
// a seed, since the seed is by definition at least one rebuild old.
func (c *codexClient) SeedUsage(snap UsageSnapshot) {
	if snap.Provider != "openai-codex" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasUsage {
		return
	}
	c.lastUsage = snap
	c.hasUsage = true
}

func (c *codexClient) recordUsageHeaders(h http.Header) {
	snap, ok := parseCodexUsageHeaders(h)
	if !ok {
		return
	}
	snap.Provider = "openai-codex"
	snap.CapturedAt = time.Now()
	c.mu.Lock()
	c.lastUsage = snap
	c.hasUsage = true
	c.mu.Unlock()
}

// parseCodexUsageHeaders reads the x-codex-* usage headers Codex returns
// on every /responses call into a snapshot. It is pure (no clock, no
// Provider/CapturedAt) so it is fully table-testable; recordUsageHeaders
// stamps those. ok is false when no window and no credits are present.
//
// The header set mirrors the Codex CLI's parser
// (codex-rs/codex-api/src/rate_limits.rs):
//
//	x-codex-{primary,secondary}-{used-percent,window-minutes,reset-at}
//	x-codex-credits-{has-credits,unlimited,balance}
//
// The slots are NOT fixed durations. "primary" was the short (~5h) rolling
// window and "secondary" the ~weekly one right up until OpenAI lifted the
// 5h limit (2026-07-12) and started reporting the weekly limit in the
// primary slot with an empty secondary. So every label is derived from
// window-minutes; the fallbacks below are a last resort for a window that
// reports usage without a duration, not a claim about which slot is which.
func parseCodexUsageHeaders(h http.Header) (UsageSnapshot, bool) {
	var snap UsageSnapshot
	if w, ok := parseCodexWindow(h, "primary", "5h"); ok {
		snap.Windows = append(snap.Windows, w)
	}
	if w, ok := parseCodexWindow(h, "secondary", "weekly"); ok {
		snap.Windows = append(snap.Windows, w)
	}
	snap.Credits = parseCodexCredits(h)
	return snap, len(snap.Windows) > 0 || snap.Credits != nil
}

func parseCodexWindow(h http.Header, key, fallbackLabel string) (UsageWindow, bool) {
	pct := strings.TrimSpace(h.Get("x-codex-" + key + "-used-percent"))
	mins := strings.TrimSpace(h.Get("x-codex-" + key + "-window-minutes"))
	reset := h.Get("x-codex-" + key + "-reset-at")
	if pct == "" && mins == "" && strings.TrimSpace(reset) == "" {
		return UsageWindow{}, false
	}
	// UsedPercent starts negative: "reported but unparseable" reads as
	// unknown, not a misleading 0%.
	w := UsageWindow{UsedPercent: -1}
	if v, err := strconv.ParseFloat(pct, 64); err == nil {
		w.UsedPercent = v
	}
	if v, err := strconv.Atoi(mins); err == nil {
		w.WindowMinutes = v
	}
	w.ResetsAt = parseCodexResetAt(reset)
	// An all-zero window is the backend saying "this limit is not in force",
	// not "you have used none of it" — when OpenAI lifted the 5h limit it kept
	// sending a bare `secondary-used-percent: 0` with no duration and no reset,
	// and a window emitted from that renders as an empty second bar carrying
	// its fallback label. A real window at 0% still has a duration to report,
	// so it survives on window-minutes. (Same has_data rule as the Codex CLI.)
	if w.UsedPercent <= 0 && w.WindowMinutes == 0 && w.ResetsAt.IsZero() {
		return UsageWindow{}, false
	}
	w.Label = windowLabel(w.WindowMinutes, fallbackLabel)
	return w, true
}

func parseCodexCredits(h http.Header) *Credits {
	has := strings.TrimSpace(h.Get("x-codex-credits-has-credits"))
	unl := strings.TrimSpace(h.Get("x-codex-credits-unlimited"))
	bal := strings.TrimSpace(h.Get("x-codex-credits-balance"))
	if has == "" && unl == "" && bal == "" {
		return nil
	}
	cr := &Credits{
		HasCredits: parseCodexBool(has),
		Unlimited:  parseCodexBool(unl),
	}
	if v, err := strconv.ParseFloat(bal, 64); err == nil {
		cr.Balance = v
	}
	return cr
}

func parseCodexBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// parseCodexResetAt interprets the x-codex-*-reset-at header. The header
// carries an absolute unix timestamp in seconds (the Codex CLI shows an
// absolute reset time); an RFC3339 string is accepted as a fallback.
// Anything else — including a small int that would be a relative offset
// rather than an epoch — yields the zero time (reset shown as unknown)
// rather than a wrong absolute time. (Encoding to be reconfirmed against
// a live response; see docs/plans/usage-windows.md §7.)
func parseCodexResetAt(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 1_000_000_000 { // < 2001-09; not a usable epoch
			return time.Time{}
		}
		return time.Unix(n, 0).UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// approxWindow reports whether minutes is within ±5% of want. The backend does
// not send round durations — 299 and 10079 both turn up in the wild for what it
// calls the 5-hour and weekly windows — so an exact-multiple test renders them
// as "299m" and "10079m". This is the tolerance the Codex CLI itself uses.
func approxWindow(minutes, want int) bool {
	return float64(minutes) >= float64(want)*0.95 && float64(minutes) <= float64(want)*1.05
}

// windowLabel humanizes a window length for display, falling back to the
// provided label when the duration is unknown. A duration is named in the
// largest unit it lands within tolerance of, so 299 and 300 both read "5h"
// while a genuine 90-minute window still reads "90m" rather than rounding
// itself up to a nonexistent 2h one.
func windowLabel(minutes int, fallback string) string {
	if minutes <= 0 {
		return fallback
	}
	if n := (minutes + 10080/2) / 10080; n > 0 && approxWindow(minutes, n*10080) {
		if n == 1 {
			return "weekly"
		}
		return fmt.Sprintf("%dw", n)
	}
	if n := (minutes + 1440/2) / 1440; n > 0 && approxWindow(minutes, n*1440) {
		if n == 1 {
			return "daily"
		}
		return fmt.Sprintf("%dd", n)
	}
	if n := (minutes + 60/2) / 60; n > 0 && approxWindow(minutes, n*60) {
		return fmt.Sprintf("%dh", n)
	}
	return fmt.Sprintf("%dm", minutes)
}

func (c *codexClient) runStream(ctx context.Context, resp *http.Response, req Request, out chan<- Event) {
	defer close(out)

	model, _ := FindModel("openai-codex", req.Model)
	if model.ID == "" {
		model, _ = FindModel("openai", req.Model)
	}
	out <- EventStart{Model: req.Model, Provider: "openai-codex"}

	stream := newSSEStream(resp.Body, "openai-codex")
	defer stream.Close() // owns resp.Body: closes it, then unparks the reader
	raw := stream.Events()

	// Accumulators. The Responses API emits output_items in order; each
	// item is either a "message" (text) or a "function_call". We track
	// the in-flight item by its index.
	type itemState struct {
		kind      string // "message" | "function_call" | "reasoning"
		callID    string
		name      string
		argsBuf   strings.Builder
		textBuf   strings.Builder
		summary   strings.Builder
		rawID     string
		encrypted string
		announced bool
	}
	var (
		items    = map[int]*itemState{}
		order    []int
		usage    Usage
		stop     StopReason = StopEnd
		finalErr error
		// sawTerminal tracks the wire's terminal frame
		// (response.completed/.done, or the .failed/error variants). If
		// the raw channel closes before one of these (connection death
		// mid-stream, including mid-tool-call), the response is truncated
		// and we surface an error instead of a clean StopEnd.
		sawTerminal bool
	)

	assemble := func() Message {
		content := []Content{}
		for _, idx := range order {
			it := items[idx]
			switch it.kind {
			case "message":
				if it.textBuf.Len() > 0 {
					content = append(content, TextBlock{Text: it.textBuf.String()})
				}
			case "function_call":
				args := it.argsBuf.String()
				if args == "" || !json.Valid([]byte(args)) {
					args = "{}"
				}
				content = append(content, ToolCallBlock{
					ID: it.callID, Name: it.name, Arguments: json.RawMessage(args),
				})
			case "reasoning":
				if it.encrypted == "" && it.summary.Len() == 0 && it.rawID == "" {
					continue
				}
				content = append(content, ReasoningBlock{
					ID:        it.rawID,
					Summary:   it.summary.String(),
					Encrypted: it.encrypted,
				})
			}
		}
		return Message{Role: RoleAssistant, Content: content, Time: time.Now()}
	}

	sendDone := func() {
		usage.CostUSD = ComputeCost(model, usage)
		out <- EventUsage{Usage: usage}
		out <- EventDone{Stop: stop, Err: finalErr, Message: assemble()}
	}

	for {
		select {
		case <-ctx.Done():
			stop = StopAborted
			finalErr = ctx.Err()
			sendDone()
			return
		case ev, ok := <-raw:
			if !ok {
				switch {
				case sawTerminal:
					// Terminal frame already seen: the message is whole, so a
					// stumble on the trailing bytes is not worth failing over.
				case stream.Err() != nil:
					// Over-limit line (permanent) or a transport read error.
					stop = StopError
					finalErr = stream.Err()
				default:
					stop = StopError
					finalErr = NewStreamDeathError("openai-codex", "response.completed")
				}
				sendDone()
				return
			}
			var head struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &head); err != nil {
				continue
			}

			switch head.Type {
			case "response.output_item.added":
				var p struct {
					OutputIndex int `json:"output_index"`
					Item        struct {
						Type             string `json:"type"` // "message" | "function_call" | "reasoning"
						ID               string `json:"id"`
						CallID           string `json:"call_id"`
						Name             string `json:"name"`
						EncryptedContent string `json:"encrypted_content"`
					} `json:"item"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				it := &itemState{}
				switch p.Item.Type {
				case "message":
					it.kind = "message"
				case "function_call":
					it.kind = "function_call"
					it.callID = p.Item.CallID
					it.name = p.Item.Name
					if !it.announced {
						it.announced = true
						out <- EventToolStart{ID: it.callID, Name: it.name}
					}
				case "reasoning":
					it.kind = "reasoning"
					it.rawID = p.Item.ID
					it.encrypted = p.Item.EncryptedContent
				default:
					continue
				}
				items[p.OutputIndex] = it
				order = append(order, p.OutputIndex)
			case "response.output_text.delta":
				var p struct {
					OutputIndex int    `json:"output_index"`
					Delta       string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				if it, ok := items[p.OutputIndex]; ok && it.kind == "message" {
					it.textBuf.WriteString(p.Delta)
					out <- EventTextDelta{Delta: p.Delta}
				}
			case "response.reasoning_summary_text.delta":
				var p struct {
					OutputIndex int    `json:"output_index"`
					Delta       string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				if it, ok := items[p.OutputIndex]; ok && it.kind == "reasoning" {
					it.summary.WriteString(p.Delta)
				}
			case "response.reasoning_summary_text.done":
				// summary text already accumulated via deltas
			case "response.function_call_arguments.delta":
				var p struct {
					OutputIndex int    `json:"output_index"`
					Delta       string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				if it, ok := items[p.OutputIndex]; ok && it.kind == "function_call" {
					it.argsBuf.WriteString(p.Delta)
					out <- EventToolArgs{ID: it.callID, Delta: p.Delta}
				}
			case "response.output_item.done":
				var p struct {
					OutputIndex int `json:"output_index"`
					Item        struct {
						Type             string `json:"type"`
						ID               string `json:"id"`
						EncryptedContent string `json:"encrypted_content"`
						Summary          []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"summary"`
					} `json:"item"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				if it, ok := items[p.OutputIndex]; ok {
					switch it.kind {
					case "function_call":
						out <- EventToolEnd{ID: it.callID}
					case "reasoning":
						if p.Item.EncryptedContent != "" {
							it.encrypted = p.Item.EncryptedContent
						}
						if it.rawID == "" && p.Item.ID != "" {
							it.rawID = p.Item.ID
						}
						for _, s := range p.Item.Summary {
							if s.Text == "" {
								continue
							}
							if it.summary.Len() > 0 {
								it.summary.WriteString("\n")
							}
							it.summary.WriteString(s.Text)
						}
					}
				}
			case "response.completed", "response.done":
				var p struct {
					Response struct {
						Usage struct {
							InputTokens        int `json:"input_tokens"`
							OutputTokens       int `json:"output_tokens"`
							InputTokensDetails struct {
								CachedTokens int `json:"cached_tokens"`
							} `json:"input_tokens_details"`
						} `json:"usage"`
						Status string `json:"status"`
					} `json:"response"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				usage.InputTokens = p.Response.Usage.InputTokens - p.Response.Usage.InputTokensDetails.CachedTokens
				if usage.InputTokens < 0 {
					usage.InputTokens = p.Response.Usage.InputTokens
				}
				usage.OutputTokens = p.Response.Usage.OutputTokens
				usage.CacheReadTokens = p.Response.Usage.InputTokensDetails.CachedTokens

				hadTool := false
				for _, it := range items {
					if it.kind == "function_call" {
						hadTool = true
						break
					}
				}
				if hadTool {
					stop = StopToolUse
				} else {
					stop = StopEnd
				}
				sawTerminal = true
				sendDone()
				return
			case "response.failed":
				var p struct {
					Response struct {
						Error struct {
							Message string `json:"message"`
						} `json:"error"`
					} `json:"response"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				stop = StopError
				finalErr = NewAPIError("openai-codex", p.Response.Error.Message, false)
				sawTerminal = true
				sendDone()
				return
			case "error":
				// The ChatGPT backend delivers errors in two shapes: a flat
				// {message,code} and a nested {error:{message,code}} — the
				// GPT-5.6 preview backend uses the nested form. Read both and
				// fall back to the raw payload so the user never sees a blank
				// error (the flat-only reader surfaced "" for nested errors).
				var p struct {
					Message string `json:"message"`
					Code    string `json:"code"`
					Error   struct {
						Message string `json:"message"`
						Code    string `json:"code"`
					} `json:"error"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &p)
				msg := p.Message
				if msg == "" {
					msg = p.Error.Message
				}
				if msg == "" {
					msg = p.Code
				}
				if msg == "" {
					msg = p.Error.Code
				}
				if msg == "" {
					msg = strings.TrimSpace(ev.Data)
				}
				code := p.Code
				if code == "" {
					code = p.Error.Code
				}
				stop = StopError
				// The gateway uses rate_limit_* codes for transient
				// throttling on the ChatGPT backend.
				finalErr = NewAPIError("openai-codex", msg, strings.HasPrefix(code, "rate_limit"))
				sawTerminal = true
				sendDone()
				return
			}
		}
	}
}
