package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const openaiDefaultBaseURL = "https://api.openai.com"

// versionSegmentSuffix matches a trailing API version segment such as
// "/v1" or Z.AI's "/v4".
var versionSegmentSuffix = regexp.MustCompile(`/v\d+$`)

// chatCompletionsURL builds the chat-completions endpoint for an
// OpenAI-compatible base URL. A base that already carries an API
// version segment gets "/chat/completions" appended directly; a bare
// host (e.g. api.openai.com) gets the conventional "/v1/chat/completions".
//
// Matching any "/vN" segment (not just "/v1") keeps Z.AI's coding-plan
// base, which ends in "/paas/v4", from getting a spurious "/v1" that
// yields ".../paas/v4/v1/chat/completions" and a 404.
func chatCompletionsURL(baseURL string) string {
	if versionSegmentSuffix.MatchString(baseURL) {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

type openaiClient struct {
	cred    CredentialSource
	baseURL string
	name    string
	headers map[string]string
	http    *http.Client

	// usage holds the rate-limit snapshot parsed from response headers in
	// Stream and read by UsageSnapshot from the TUI goroutine. Ephemeral
	// (WindowRateLimit) — deliberately NOT seeded across a client rebuild, so
	// no SeedUsage method is wired to it.
	usage usageObservation
}

// NewOpenAI creates an OpenAI client using an API key. baseURL may be empty.
func NewOpenAI(apiKey, baseURL string) Client {
	if baseURL == "" {
		baseURL = openaiDefaultBaseURL
	}
	return &openaiClient{
		cred:    StaticCredential(apiKey),
		baseURL: strings.TrimRight(baseURL, "/"),
		name:    "openai",
		http:    &http.Client{Timeout: 0},
	}
}

// NewKimi creates a Kimi/Moonshot client. Kimi's chat API is OpenAI-compatible.
func NewKimi(apiKey, baseURL string) Client {
	return NewKimiWithHeaders(apiKey, baseURL, nil)
}

// NewDeepSeek creates a DeepSeek client. DeepSeek's chat API is
// OpenAI-compatible at https://api.deepseek.com/v1.
func NewDeepSeek(apiKey, baseURL string) Client {
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
	}
	base := strings.TrimRight(baseURL, "/")
	inner := &openaiClient{
		cred:    StaticCredential(apiKey),
		baseURL: base,
		name:    "deepseek",
		http:    &http.Client{Timeout: 0},
	}
	// Usage (/usage): lazily fetch GET {host}/user/balance for the account
	// balance, rendered as `Credits`. No subscription windows.
	return newPollingUsageClient(inner, usagePollTTL, fetchDeepSeekBalance(&http.Client{Timeout: 0}, apiKey, base))
}

// NewKimiWithHeaders creates a Kimi/Moonshot client with extra headers.
// Subscription tokens from Kimi Code need the official CLI's X-Msh-* headers.
func NewKimiWithHeaders(apiKey, baseURL string, headers map[string]string) Client {
	if baseURL == "" {
		baseURL = "https://api.kimi.com/coding/v1"
	}
	return &openaiClient{
		cred:    StaticCredential(apiKey),
		baseURL: strings.TrimRight(baseURL, "/"),
		name:    "kimi",
		headers: headers,
		http:    &http.Client{Timeout: 0},
	}
}

func (c *openaiClient) Name() string {
	if c.name != "" {
		return c.name
	}
	return "openai"
}

// Capabilities declares that this client mirrors tool-result images.
// The chat-completions wire (which every openaiClient-backed provider
// speaks — openai, openai-compatible, ollama, groq, xai, kimi,
// azure-openai-responses, and the rest) only accepts text in a `tool`
// message (see buildOAIToolContent), so the agent loop mirrors images
// into a following user message. WIRE FORMAT only; whether the model
// can see images is the separate per-model capability the loop checks
// alongside this.
func (c *openaiClient) Capabilities() ClientCapabilities {
	return ClientCapabilities{MirrorsToolImages: true}
}

// ---- wire types ----

type oaiContentText struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type oaiContentImage struct {
	Type     string `json:"type"` // "image_url"
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type oaiToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type oaiToolCall struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"` // "function"
	Function oaiToolCallFn `json:"function"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content,omitempty"` // string or []block
	Name       string        `json:"name,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	// ReasoningContent carries the model's chain-of-thought summary
	// alongside an assistant tool-call message. Required by Kimi's
	// chat completions endpoint when thinking is enabled and the
	// assistant message contains a tool call; OpenAI ignores it.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type oaiTool struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiRequest struct {
	Model            string            `json:"model"`
	Messages         []oaiMessage      `json:"messages"`
	Tools            []oaiTool         `json:"tools,omitempty"`
	ToolChoice       string            `json:"tool_choice,omitempty"`
	Stream           bool              `json:"stream"`
	StreamOptions    *oaiStreamOptions `json:"stream_options,omitempty"`
	Temperature      *float32          `json:"temperature,omitempty"`
	MaxTokens        *int              `json:"max_tokens,omitempty"`
	MaxCompletionTok *int              `json:"max_completion_tokens,omitempty"`
	ReasoningEffort  string            `json:"reasoning_effort,omitempty"`
	// PromptCacheKey pins prefix-cache routing per conversation. Only sent
	// to the real OpenAI backend (see buildRequest) — this client also
	// serves OpenAI-compatible endpoints that may reject unknown fields.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

// reasoningMarker is one thinking-channel delimiter pair that a server may hand
// back inside reasoning_content when its parser never matched the closer.
//
// namesChannel marks an opener that is followed by the channel's name on the
// rest of its line ("<|channel>thought\n"). Removing only the marker would
// leave that name reading as the first word of the answer, so the opener
// consumes through the end of its line.
type reasoningMarker struct {
	opener       string
	closer       string
	namesChannel bool
}

// reasoningMarkers is deliberately short and literal: these are the pairs
// actually seen in the wild. A general "anything that looks like a control
// marker" rule would eat legitimate prose — this codebase discusses these very
// tags in its own comments.
var reasoningMarkers = []reasoningMarker{
	{opener: "<think>", closer: "</think>"},
	{opener: "<|channel>", closer: "<channel|>", namesChannel: true},
}

// stripReasoningMarkers removes thinking-channel delimiters from reasoning text
// that is about to be promoted into an assistant message's visible content.
//
// A server that splits a thinking channel out of the token stream can return
// the whole reply in reasoning_content, markers included, when the channel
// never closes. Promoting that verbatim replays the markers as if the model had
// written them in its answer, and the model then imitates the pattern — the
// contamination compounds with every promoted turn.
//
// When a closer is present, everything up to and including it is deliberation
// and the text after it is the answer the user was meant to see, so only the
// tail survives. When no closer is present the parser swallowed the entire
// reply: the words are all there is, so they are kept and only a stray opener
// is removed. Losing the turn is what the promotion exists to prevent.
//
// This runs on the promotion path only. Text the model emitted as ordinary
// content is never touched.
func stripReasoningMarkers(s string) string {
	for _, m := range reasoningMarkers {
		// The last closer wins: deliberation can mention an earlier one, and
		// what follows the final closer is the answer.
		if i := strings.LastIndex(s, m.closer); i >= 0 {
			s = s[i+len(m.closer):]
		}
		for {
			i := strings.Index(s, m.opener)
			if i < 0 {
				break
			}
			rest := s[i+len(m.opener):]
			if m.namesChannel {
				if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
					rest = rest[nl+1:]
				} else {
					// No newline: the whole remainder is the channel name.
					rest = ""
				}
			}
			s = s[:i] + rest
		}
	}
	return strings.TrimSpace(s)
}

// ---- request building ----

func (c *openaiClient) buildRequest(req Request) (*oaiRequest, error) {
	// Look up the model by id across all providers (not just openai)
	// because the OpenAI client is also used for ollama and other
	// OpenAI-compatible backends.
	m, err := FindModel("", req.Model)
	if err != nil {
		// Unknown model: use sensible defaults so local/custom
		// models still work without a catalog entry.
		m = Model{
			ID:            req.Model,
			ContextWindow: 32768,
			MaxOutput:     8192,
		}
	}
	out := &oaiRequest{
		Model:         req.Model,
		Stream:        true,
		StreamOptions: &oaiStreamOptions{IncludeUsage: true},
		Temperature:   req.Temperature,
	}
	// Forward the cache-routing key only to the real OpenAI backend. The
	// same client serves kimi/ollama/azure/copilot and other
	// OpenAI-compatible endpoints, where an unknown parameter risks a 400.
	if c.name == "openai" {
		out.PromptCacheKey = req.PromptCacheKey
	}

	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = m.MaxOutput
	}
	// Clamp max_tokens so output plus a minimum input reservation fits
	// within the context window. Some providers (OpenRouter) enforce
	// input + max_output <= context_length and reject requests where the
	// total exceeds it. Reserving headroom guarantees the system prompt,
	// first message, and tool definitions have room.
	//
	// The cap is derived from the context window, never from MaxOutput:
	// MaxOutput is already the output ceiling, so subtracting from it
	// would shrink every model's budget even when its output comfortably
	// fits the window. We only lower maxTok when ContextWindow - reserve
	// is actually tighter than the requested budget, which is exactly the
	// pathological case (e.g. a model whose MaxOutput equals its window).
	//
	// The reserve is proportional (window/8, capped at 4096) rather than a
	// flat 4096 so small-window models aren't over-penalized: a flat 4096
	// would halve gpt-4's 8192 budget, while window/8 reserves a sensible
	// 1024 there and still tops out at 4096 for large contexts.
	//
	// Some providers (OpenRouter) report inflated model-level context
	// windows (e.g. 1000000) while the serving provider enforces a much
	// tighter limit (e.g. 262144). Discovery already prefers the serving
	// provider's smaller context_length, so m.ContextWindow is the real
	// limit by the time we get here.
	if m.ContextWindow > 0 {
		reserve := m.ContextWindow / 8
		const maxReserve = 4096
		if reserve > maxReserve {
			reserve = maxReserve
		}
		clamped := m.ContextWindow - reserve
		if clamped < 1 {
			clamped = 1
		}
		if maxTok > clamped {
			maxTok = clamped
		}
	}
	if m.Reasoning {
		if maxTok > 0 {
			out.MaxCompletionTok = &maxTok
		}
		eff := EffectiveReasoning(req.Reasoning, req.ReasoningSet, m)
		effort := OpenAIReasoningEffort(eff)
		if usesAdaptiveThinking(m) {
			// Some gateways expose adaptive-thinking Anthropic models through
			// the OpenAI-compatible chat-completions wire. They accept the
			// same reasoning_effort knob, including the top "xhigh" tier;
			// don't clamp terva's "maximum" to "high" for those models.
			effort = OpenAICompatAnthropicEffort(eff)
		}
		if effort != "" {
			out.ReasoningEffort = effort
		}
	} else if maxTok > 0 {
		// Omit max_tokens when unknown (some discovered models don't
		// advertise an output cap) so the server applies its own default
		// instead of receiving an invalid max_tokens: 0.
		out.MaxTokens = &maxTok
	}

	if req.System != "" {
		out.Messages = append(out.Messages, oaiMessage{Role: "system", Content: req.System})
	}

	// Models without the image-input capability (a vision-less local
	// GGUF, a server that 400s on multimodal parts) get every
	// user/tool message forced to a plain string and image blocks
	// silently dropped, so historical sessions with screenshots still
	// replay instead of bricking every subsequent turn. Per-model via
	// the capability tag (docs/plans/model-capabilities.md); this
	// replaced a provider-wide `c.name == "deepseek"` check that had
	// gone stale against the V4 catalog entries.
	textOnly := !m.Has(CapImageInput)

	req.Messages = RepairOrphanedToolResults(req.Messages)
	// OpenAI proper tolerates a leading assistant turn (a card's seeded
	// greeting) and same-role adjacency, but this builder also serves every
	// OpenAI-compatible clone (Moonshot/Kimi, local templates with strict
	// alternation) via newOpenAICompat — merging any adjacency an edit/delete
	// left behind and the leading-user guard are no-cost safety nets there.
	req.Messages = EnsureLeadingUserTurn(MergeAdjacentSameRole(req.Messages))
	for _, msg := range req.Messages {
		switch msg.Role {
		case RoleUser:
			content := buildOAIUserContent(msg.Content, textOnly)
			out.Messages = append(out.Messages, oaiMessage{Role: "user", Content: content})
		case RoleAssistant:
			am := oaiMessage{Role: "assistant"}
			var text strings.Builder
			var reasoning strings.Builder
			for _, b := range msg.Content {
				switch v := b.(type) {
				case TextBlock:
					if strings.TrimSpace(v.Text) == "" {
						continue
					}
					if text.Len() > 0 {
						text.WriteString("\n")
					}
					text.WriteString(v.Text)
				case ToolCallBlock:
					args := v.Arguments
					if len(args) == 0 || !json.Valid(args) {
						args = json.RawMessage("{}")
					}
					am.ToolCalls = append(am.ToolCalls, oaiToolCall{
						ID:   v.ID,
						Type: "function",
						Function: oaiToolCallFn{
							Name:      v.Name,
							Arguments: string(args),
						},
					})
				case ReasoningBlock:
					if v.Summary != "" {
						if reasoning.Len() > 0 {
							reasoning.WriteString("\n")
						}
						reasoning.WriteString(v.Summary)
					}
				}
			}
			if text.Len() > 0 {
				am.Content = text.String()
			}
			if reasoning.Len() > 0 && len(am.ToolCalls) > 0 {
				am.ReasoningContent = reasoning.String()
			}
			// A turn whose only substance is reasoning. A server that splits a
			// thinking channel out of the token stream puts the whole reply in
			// reasoning_content and leaves content empty whenever that channel
			// never closes: every token after the opener stays classified as
			// reasoning. The marker pair is model-specific (<think>…</think>,
			// <|channel>thought…<channel|>, others), and a chat template can
			// force the condition by prefilling an opener it never closes. The
			// text is a real answer, so promote it to the visible content and
			// let the model see its own turn on the next request.
			//
			// It used to be dropped, which left a hole in the history exactly
			// where the answer had been: the model then apologised for a lapse
			// it had no record of, and every such turn silently cost its own
			// context. Promotion also keeps the prefix stable — a turn that
			// vanishes from replay invalidates the cached prefix behind it.
			//
			// It goes in content, not reasoning_content: the endpoints that
			// read that field only accept it beside a tool call, and Kimi
			// rejects a message whose sole substance is reasoning_content.
			//
			// The thinking markers are stripped on the way: promoting them
			// verbatim replays them as words the model appears to have written
			// in its own answer, and it then imitates the pattern, so the
			// contamination compounds with every promoted turn.
			if am.Content == nil && len(am.ToolCalls) == 0 && reasoning.Len() > 0 {
				if promoted := stripReasoningMarkers(reasoning.String()); promoted != "" {
					am.Content = promoted
				}
			}
			// Kimi rejects assistant messages with neither visible text nor
			// tool calls ("assistant must not be empty"). Nothing survives the
			// promotion above, so there is genuinely nothing to send.
			if am.Content == nil && len(am.ToolCalls) == 0 {
				continue
			}
			out.Messages = append(out.Messages, am)
		case RoleTool:
			// Each ToolResultBlock becomes its own tool message. Preserve
			// image blocks for vision-capable OpenAI models instead of
			// flattening the tool output to plain text.
			for _, b := range msg.Content {
				if tr, ok := b.(ToolResultBlock); ok {
					content := buildOAIToolContent(tr.Content, tr.IsError)
					out.Messages = append(out.Messages, oaiMessage{
						Role:       "tool",
						ToolCallID: tr.CallID,
						Content:    content,
					})
				}
			}
		}
	}

	// Ephemeral context: a trailing user message, request-scoped (never
	// in req.Messages, never persisted). OpenAI prefix-caches
	// automatically, so appending at the tail leaves the cached prefix
	// (system + tools + history) intact and only this block is fresh.
	if req.EphemeralContext != "" {
		out.Messages = append(out.Messages, oaiMessage{Role: "user", Content: req.EphemeralContext})
	}

	for _, t := range req.Tools {
		var tool oaiTool
		tool.Type = "function"
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		tool.Function.Parameters = t.Schema
		out.Tools = append(out.Tools, tool)
	}
	if len(out.Tools) > 0 {
		out.ToolChoice = "auto"
	}

	return out, nil
}

func buildOAIUserContent(blocks []Content, textOnly bool) interface{} {
	hasImage := false
	for _, b := range blocks {
		if _, ok := b.(ImageBlock); ok {
			hasImage = true
			break
		}
	}
	if textOnly || !hasImage {
		var sb strings.Builder
		for _, b := range blocks {
			if tb, ok := b.(TextBlock); ok {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(tb.Text)
			}
		}
		return sb.String()
	}
	return buildOAIContentBlocks(blocks, false)
}

// buildOAIToolContent renders a tool result for a chat-completions
// `tool` message. That role only accepts text: chat-completions cannot
// carry images in a tool message, and several OpenAI-compatible servers
// reject array / image_url content there outright (HTTP 400 "'content'
// field must be a string or an array of objects"). Image bytes are
// instead delivered by the agent loop, which mirrors tool-result images
// into the following user message (mirrorToolImagesAsUser in
// packages/core/agent.go) — that is where vision models actually receive
// them. This mirrors the text-only treatment the Responses path already
// uses (see buildRequest in openai_codex.go); a short note is emitted for
// an image-only result so the tool message is never empty and the model
// knows the image arrives in the next message.
func buildOAIToolContent(blocks []Content, isError bool) string {
	var sb strings.Builder
	imageCount := 0
	for _, b := range blocks {
		switch v := b.(type) {
		case TextBlock:
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(v.Text)
		case ImageBlock:
			imageCount++
		}
	}
	if sb.Len() == 0 && imageCount > 0 {
		if imageCount == 1 {
			sb.WriteString("[image returned; see the following message]")
		} else {
			fmt.Fprintf(&sb, "[%d images returned; see the following message]", imageCount)
		}
	}
	if isError && sb.Len() > 0 {
		sb.WriteString(" [error]")
	}
	return sb.String()
}

func buildOAIContentBlocks(blocks []Content, isError bool) []interface{} {
	var arr []interface{}
	for _, b := range blocks {
		switch v := b.(type) {
		case TextBlock:
			arr = append(arr, oaiContentText{Type: "text", Text: v.Text})
		case ImageBlock:
			var img oaiContentImage
			img.Type = "image_url"
			img.ImageURL.URL = "data:" + v.MimeType + ";base64," + base64.StdEncoding.EncodeToString(v.Data)
			arr = append(arr, img)
		}
	}
	if isError {
		arr = append(arr, oaiContentText{Type: "text", Text: "[error]"})
	}
	return arr
}

// ---- streaming ----

func (c *openaiClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	endpoint := chatCompletionsURL(c.baseURL)
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	key, err := c.cred(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	newReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("accept", "text/event-stream")
		httpReq.Header.Set("authorization", "Bearer "+key)
		for k, v := range c.headers {
			httpReq.Header.Set(k, v)
		}
		return httpReq, nil
	}

	resp, err := doStreamWithRetry(ctx, c.http, newReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Name(), err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := errorBodySnippet(resp.Body)
		resp.Body.Close()
		return nil, NewHTTPError(c.Name(), resp.StatusCode, resp.Header.Get("Retry-After"), snippet)
	}

	// Capture x-ratelimit-* off every successful response (free, passive —
	// the UsageReporter half of the merged /usage view).
	c.recordRateLimitHeaders(resp.Header)

	out := make(chan Event, 16)
	go c.runStream(ctx, resp, req, out)
	return out, nil
}

// recordRateLimitHeaders parses x-ratelimit-* from a successful response into
// the cached usage snapshot, when this provider has a (non-disabled) spec.
func (c *openaiClient) recordRateLimitHeaders(h http.Header) {
	spec, ok := rateLimitSpecFor(c.name)
	if !ok {
		return
	}
	snap, ok := parseRateLimitHeaders(h, spec)
	if ok {
		snap.Provider = c.name
	}
	c.usage.record(snap, ok)
}

// UsageSnapshot returns the rate-limit windows parsed from the most recent
// response (the UsageReporter contract). ok=false until the first response
// that carried usable x-ratelimit-* headers.
func (c *openaiClient) UsageSnapshot() (UsageSnapshot, bool) {
	return c.usage.snapshot()
}

func (c *openaiClient) runStream(ctx context.Context, resp *http.Response, req Request, out chan<- Event) {
	defer close(out)

	model, _ := FindModel("", req.Model)
	out <- EventStart{Model: req.Model, Provider: c.Name()}

	stream := newSSEStream(resp.Body, c.Name())
	defer stream.Close() // owns resp.Body: closes it, then unparks the reader
	raw := stream.Events()

	// Interleaved block tracking: text and tool_calls preserve their
	// emission order so the assistant message renders in the same order
	// the model produced it. The builder fires one kind of block at a
	// time — incoming text deltas after a tool_call split into a fresh
	// text block; subsequent tool_calls each get their own slot.
	type blockEntry struct {
		kind      string // "text" | "tool_use"
		textBuf   strings.Builder
		toolID    string
		toolName  string
		toolArgs  strings.Builder
		announced bool
	}
	var (
		blocks       []*blockEntry
		currentText  *blockEntry             // most-recent text block, nil if none
		toolByIdx    = map[int]*blockEntry{} // openai tool_call index -> block
		reasoningBuf strings.Builder
		usage        Usage
		stop         StopReason = StopEnd
		finalErr     error
		// sawDone tracks the [DONE] terminal frame. If the raw channel
		// closes before it (TCP drop mid-stream, including mid-tool-call),
		// the message is truncated and we must surface an error so the
		// agent retries or rescues instead of treating it as clean.
		sawDone bool
	)

	appendText := func(delta string) {
		if currentText == nil {
			currentText = &blockEntry{kind: "text"}
			blocks = append(blocks, currentText)
		}
		currentText.textBuf.WriteString(delta)
	}

	getOrCreateTool := func(idx int) *blockEntry {
		if t, ok := toolByIdx[idx]; ok {
			return t
		}
		t := &blockEntry{kind: "tool_use"}
		toolByIdx[idx] = t
		blocks = append(blocks, t)
		// A new tool block breaks the current text block. Subsequent text
		// deltas will start a fresh text block after this tool.
		currentText = nil
		return t
	}

	assembleMsg := func() Message {
		content := []Content{}
		for _, b := range blocks {
			switch b.kind {
			case "text":
				if b.textBuf.Len() > 0 {
					content = append(content, TextBlock{Text: b.textBuf.String()})
				}
			case "tool_use":
				args, unparsed := FinalizeToolArguments(b.toolArgs.String())
				content = append(content, ToolCallBlock{
					ID: b.toolID, Name: b.toolName, Arguments: args, RawArguments: unparsed,
				})
			}
		}
		if reasoningBuf.Len() > 0 {
			content = append(content, ReasoningBlock{Summary: reasoningBuf.String()})
		}
		return Message{Role: RoleAssistant, Content: content, Time: time.Now()}
	}

	sendDone := func() {
		ApplyCost(model, &usage)
		out <- EventUsage{Usage: usage}
		out <- EventDone{Stop: stop, Err: finalErr, Message: assembleMsg()}
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
				case sawDone:
					// Terminal frame already seen: the message is whole, so a
					// stumble on the trailing bytes is not worth failing over.
				case stream.Err() != nil:
					// Over-limit line (permanent) or a transport read error.
					stop = StopError
					finalErr = stream.Err()
				default:
					stop = StopError
					finalErr = NewStreamDeathError(c.Name(), "[DONE]")
				}
				sendDone()
				return
			}
			if ev.Data == "[DONE]" {
				sawDone = true
				sendDone()
				return
			}
			var chunk struct {
				Choices []struct {
					Index int `json:"index"`
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
						ToolCalls        []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens        int `json:"prompt_tokens"`
					CompletionTokens    int `json:"completion_tokens"`
					PromptTokensDetails struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
					// A pointer: most OpenAI-compatible gateways omit this
					// block entirely, and an absent block must stay "not
					// reported" rather than becoming a measured zero.
					CompletionTokensDetails *struct {
						ReasoningTokens int `json:"reasoning_tokens"`
					} `json:"completion_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					// An openai-wire error carries its specific reason in `code`
					// and only its family in `type`. This decoder did not read it
					// at all, so a code-only failure — which is how a gateway or a
					// local server usually reports one — was classified on a field
					// that was empty, and every one of them came out permanent.
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
				continue
			}
			if chunk.Error != nil {
				stop = StopError
				finalErr = NewAPIError(c.Name(), chunk.Error.Message,
					transientErrorCode(chunk.Error.Code, chunk.Error.Type, chunk.Error.Message))
				sendDone()
				return
			}
			if chunk.Usage != nil {
				usage.InputTokens = chunk.Usage.PromptTokens - chunk.Usage.PromptTokensDetails.CachedTokens
				if usage.InputTokens < 0 {
					usage.InputTokens = chunk.Usage.PromptTokens
				}
				usage.OutputTokens = chunk.Usage.CompletionTokens
				usage.CacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
				// Reasoning stays INSIDE OutputTokens — it is billed at the
				// output rate, and subtracting it here would change the bill.
				if d := chunk.Usage.CompletionTokensDetails; d != nil {
					usage.ReasoningTokens = d.ReasoningTokens
					usage.ReasoningTokensKnown = true
				}
			}
			for _, ch := range chunk.Choices {
				if ch.Delta.ReasoningContent != "" {
					reasoningBuf.WriteString(ch.Delta.ReasoningContent)
					out <- EventReasoningDelta{Delta: ch.Delta.ReasoningContent}
				}
				if ch.Delta.Content != "" {
					appendText(ch.Delta.Content)
					out <- EventTextDelta{Delta: ch.Delta.Content}
				}
				for _, tc := range ch.Delta.ToolCalls {
					t := getOrCreateTool(tc.Index)
					if tc.ID != "" {
						t.toolID = tc.ID
					}
					if tc.Function.Name != "" {
						t.toolName = tc.Function.Name
					}
					if !t.announced && t.toolID != "" && t.toolName != "" {
						t.announced = true
						out <- EventToolStart{ID: t.toolID, Name: t.toolName}
					}
					if tc.Function.Arguments != "" {
						t.toolArgs.WriteString(tc.Function.Arguments)
						if t.announced {
							out <- EventToolArgs{ID: t.toolID, Delta: tc.Function.Arguments}
						}
					}
				}
				switch ch.FinishReason {
				case "stop":
					stop = StopEnd
				case "length":
					stop = StopLength
				case "tool_calls", "function_call":
					stop = StopToolUse
					for _, b := range blocks {
						if b.kind == "tool_use" && b.announced {
							out <- EventToolEnd{ID: b.toolID}
						}
					}
				}
			}
		}
	}
}
