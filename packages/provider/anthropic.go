package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"terva.sh/terva/packages/envcompat"
)

const anthropicDefaultBaseURL = "https://api.anthropic.com"
const anthropicAPIVersion = "2023-06-01"

// Stealth identity used when talking to Anthropic via subscription OAuth.
// These values mimic the official Claude Code CLI so Anthropic's edge
// accepts the request; diverging from them causes 429 rate_limit_error
// or 403 on the very first request.
const (
	claudeCodeVersion  = "2.1.75"
	claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."
)

// Claude Code's canonical tool casing. When running under OAuth we must
// advertise tool names that match this list (case-insensitive lookup),
// because Anthropic's backend cross-checks them.
var claudeCodeToolNames = map[string]string{
	"read":  "Read",
	"write": "Write",
	"edit":  "Edit",
	"bash":  "Bash",
	"grep":  "Grep",
	"glob":  "Glob",
}

func toClaudeCodeToolName(name string) string {
	if cc, ok := claudeCodeToolNames[strings.ToLower(name)]; ok {
		return cc
	}
	return name
}

func fromClaudeCodeToolName(name string, tools []Tool) string {
	lower := strings.ToLower(name)
	for _, t := range tools {
		if strings.ToLower(t.Name) == lower {
			return t.Name
		}
	}
	return name
}

// anthropicClient implements Client against the Anthropic Messages API.
type anthropicClient struct {
	cred    CredentialSource
	baseURL string
	// oauth selects the Claude-subscription auth MODE: Bearer auth plus the
	// Claude Code identity system prompt and tool renaming the subscription
	// endpoint requires. API-key clients (and Anthropic-compatible third
	// parties like kimi-coding) leave it false and send x-api-key. The mode
	// is fixed at construction; the credential VALUE it presents rotates
	// through cred (an OAuth refresh) without rebuilding the client.
	oauth bool
	http  *http.Client

	// name overrides the default "anthropic" identity. Anthropic-Messages-
	// compatible third-party endpoints (kimi-coding, fireworks, minimax,
	// vercel-ai-gateway, etc.) set this so cost lookup / logging / image-
	// stripping logic can route on a stable provider id.
	name string

	// headers carries extra request headers (e.g. Kimi Code's X-Msh-*).
	headers map[string]string

	// usage caches the subscription windows parsed off the most recent
	// successful response — the UsageReporter half of /usage. Shared store,
	// same as the openai client's.
	usage usageObservation
}

// NewAnthropic creates an Anthropic client using an API key. baseURL may be empty.
func NewAnthropic(apiKey, baseURL string) Client {
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &anthropicClient{
		cred:    StaticCredential(apiKey),
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 0},
	}
}

// NewAnthropicOAuth creates an Anthropic client using a subscription OAuth access token.
func NewAnthropicOAuth(accessToken, baseURL string) Client {
	return NewAnthropicOAuthSource(StaticCredential(accessToken), baseURL)
}

// NewAnthropicOAuthSource is NewAnthropicOAuth with a CredentialSource instead
// of a fixed token, so the subscription access token can rotate (refresh)
// without rebuilding the client — resolved once per Stream.
func NewAnthropicOAuthSource(cred CredentialSource, baseURL string) Client {
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &anthropicClient{
		cred:    cred,
		oauth:   true,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 0},
	}
}

func (c *anthropicClient) Name() string {
	if c.name != "" {
		return c.name
	}
	return "anthropic"
}

// Capabilities reports the Anthropic wire format's opt-in behaviors. Claude's
// Messages API extends a trailing assistant message as a prefill (the basis for
// the Stage "continue" interaction), so ContinuesAssistantPrefill is true.
// MirrorsToolImages stays false — Anthropic carries images inside tool results.
func (c *anthropicClient) Capabilities() ClientCapabilities {
	return ClientCapabilities{ContinuesAssistantPrefill: true, ReasoningWire: reasoningWireAnthropic}
}

// ---- wire types ----

type anthTextBlock struct {
	Type         string         `json:"type"` // "text"
	Text         string         `json:"text"`
	CacheControl *anthCacheCtrl `json:"cache_control,omitempty"`
}

// anthThinkingBlock replays a thinking block exactly as it was received.
//
// 🪤 Signature seals Thinking. Anthropic verifies the pair, so neither half may
// be edited, truncated or re-wrapped on the way back out — a "tidied" thinking
// text fails the check as surely as a corrupted signature.
type anthThinkingBlock struct {
	Type      string `json:"type"` // "thinking"
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// anthRedactedThinkingBlock replays reasoning Anthropic itself encrypted. There
// is no readable half: Data is the entire block.
type anthRedactedThinkingBlock struct {
	Type string `json:"type"` // "redacted_thinking"
	Data string `json:"data"`
}

type anthImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthImageBlock struct {
	Type         string          `json:"type"` // "image"
	Source       anthImageSource `json:"source"`
	CacheControl *anthCacheCtrl  `json:"cache_control,omitempty"`
}

type anthToolUseBlock struct {
	Type         string          `json:"type"` // "tool_use"
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	CacheControl *anthCacheCtrl  `json:"cache_control,omitempty"`
}

type anthToolResultBlock struct {
	Type         string          `json:"type"` // "tool_result"
	ToolUseID    string          `json:"tool_use_id"`
	Content      json.RawMessage `json:"content"` // string or array of blocks
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *anthCacheCtrl  `json:"cache_control,omitempty"`
}

type anthCacheCtrl struct {
	Type string `json:"type"` // "ephemeral"
	TTL  string `json:"ttl,omitempty"`
}

type anthMessage struct {
	Role    string        `json:"role"`
	Content []interface{} `json:"content"`
}

type anthSystemBlock struct {
	Type         string         `json:"type"` // "text"
	Text         string         `json:"text"`
	CacheControl *anthCacheCtrl `json:"cache_control,omitempty"`
}

type anthTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *anthCacheCtrl  `json:"cache_control,omitempty"`
}

type anthThinking struct {
	// Type is "enabled" for budget-based thinking (Opus 4.6 and
	// earlier) or "adaptive" for adaptive-thinking models (Opus 4.7+).
	Type string `json:"type"`
	// BudgetTokens is only sent for Type=="enabled". Adaptive models
	// reject it (400), so it is omitted for them.
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

// anthOutputConfig carries the effort knob used by adaptive-thinking
// models to control reasoning depth ("low"|"medium"|"high"|"xhigh").
type anthOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       []anthSystemBlock `json:"system,omitempty"`
	Messages     []anthMessage     `json:"messages"`
	Tools        []anthTool        `json:"tools,omitempty"`
	Temperature  *float32          `json:"temperature,omitempty"`
	Thinking     *anthThinking     `json:"thinking,omitempty"`
	OutputConfig *anthOutputConfig `json:"output_config,omitempty"`
	Stream       bool              `json:"stream"`
}

// usesAdaptiveThinking reports whether a model only supports the
// adaptive thinking mode (Opus 4.7 and later, and the whole Fable
// family). These models reject explicit thinking budgets and non-default
// sampling parameters. The catalog flag is authoritative; the
// id-substring fallback catches the same family when reached through an
// Anthropic-Messages-compatible proxy whose catalog row predates the flag.
//
// "fable" is deliberately unversioned. Every Claude Fable model shipped
// so far is adaptive-only, so a bare marker covers 5, 5.1, and whatever
// comes next without another edit here. Add "mythos" when a Mythos row
// lands in the catalog and its thinking mode is confirmed.
func usesAdaptiveThinking(m Model) bool {
	if m.AdaptiveThinking {
		return true
	}
	id := strings.ToLower(m.ID)
	for _, marker := range []string{"opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "fable"} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

// ---- request building ----

func (c *anthropicClient) buildRequest(req Request) (*anthRequest, error) {
	// Look up under the client's actual provider id, which may be "anthropic"
	// (default) or a third party that speaks the Anthropic Messages API
	// (kimi, fireworks, minimax, vercel-ai-gateway, ...). Falling back to a
	// provider-agnostic lookup keeps things working for catalog-less
	// configurations (e.g. user passes --model on an obscure third party).
	m, err := FindModel(c.Name(), req.Model)
	if err != nil {
		if m2, err2 := FindModel("", req.Model); err2 == nil {
			m = m2
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	req.Messages = enforceImageInput(m, req.Messages)
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = m.MaxOutput
	}
	// Clamp to the model's advertised output cap. A stale per-turn budget
	// can outlive the model it was sized for: Agent.SetModel swaps the model
	// id in place without refreshing Agent.MaxTokens, so a /model switch from
	// a high-cap model (e.g. 128000) to a lower-cap one (sonnet's 64000)
	// would otherwise send the old budget and earn a hard 400
	// ("max_tokens: N > CAP"). The OpenAI builder clamps for the same reason.
	if m.MaxOutput > 0 && maxTok > m.MaxOutput {
		maxTok = m.MaxOutput
	}

	adaptive := usesAdaptiveThinking(m)

	out := &anthRequest{
		Model:     req.Model,
		MaxTokens: maxTok,
		Stream:    true,
	}
	// Adaptive-thinking models reject non-default sampling params
	// (temperature/top_p/top_k -> 400). Only forward temperature for
	// models that accept it.
	if !adaptive {
		out.Temperature = req.Temperature
	}

	// System prompt assembly differs between api-key and OAuth modes.
	// OAuth requests MUST begin with the Claude Code identity line or
	// Anthropic rejects them (429 rate_limit_error with zero tokens used).
	//
	// Cache budget: anthropic caps cache_control to 4 breakpoints per
	// request. We spend them on:
	//   1. claude-code identity (OAuth only; stable forever)
	//   2. user system prompt   (changes per-session at most)
	//   3. last tool definition (tools change rarely)
	//   4. last message block   (advances every turn)
	//
	// The identity line gets its OWN cache_control so the prefix
	// [identity] is cacheable independently of the user system
	// prompt. Without that, the cache prefix starts after block 2
	// and any drift in the user prompt (e.g. the Current date
	// line flipping at midnight) invalidates everything, including
	// the 17 identity tokens we have to re-send every request
	// forever.
	if c.oauth {
		out.System = []anthSystemBlock{{
			Type:         "text",
			Text:         claudeCodeIdentity,
			CacheControl: &anthCacheCtrl{Type: "ephemeral"},
		}}
		if req.System != "" {
			out.System = append(out.System, anthSystemBlock{
				Type:         "text",
				Text:         req.System,
				CacheControl: &anthCacheCtrl{Type: "ephemeral"},
			})
		}
	} else if req.System != "" {
		out.System = []anthSystemBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: &anthCacheCtrl{Type: "ephemeral"},
		}}
	}

	eff := EffectiveReasoning(req.Reasoning, req.ReasoningSet, m)
	if eff != "" && m.Reasoning {
		if adaptive {
			// Adaptive-thinking models (Opus 4.7+): the model decides when
			// and how much to think; depth is steered by output_config.effort.
			// Explicit budgets are rejected with a 400, so none is sent.
			out.Thinking = &anthThinking{Type: "adaptive"}
			if effort := AnthropicAdaptiveEffort(eff); effort != "" {
				out.OutputConfig = &anthOutputConfig{Effort: effort}
			}
		} else {
			budget := anthropicThinkingBudget(m, eff)
			if budget > 0 {
				const minAnswerTokens = anthropicMinAnswerTokens
				out.Thinking = &anthThinking{Type: "enabled", BudgetTokens: budget}
				if out.MaxTokens <= budget {
					out.MaxTokens = budget + minAnswerTokens
					if m.MaxOutput > 0 && out.MaxTokens > m.MaxOutput {
						out.MaxTokens = m.MaxOutput
					}
				}
			}
		}
	}

	for _, t := range req.Tools {
		name := t.Name
		if c.oauth {
			name = toClaudeCodeToolName(name)
		}
		out.Tools = append(out.Tools, anthTool{
			Name:        name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	// Cache the last tool definition (applies cache breakpoint to the whole tools array).
	if n := len(out.Tools); n > 0 {
		out.Tools[n-1].CacheControl = &anthCacheCtrl{Type: "ephemeral"}
	}

	// Convert messages. Anthropic's wire format has only "user" and
	// "assistant" roles; tool_result blocks live inside user messages.
	// Some image-capable providers (for example Gemini image generation)
	// can emit assistant image blocks. Anthropic only accepts image blocks
	// in user messages, so strip assistant-side images when switching
	// providers mid-session. The saved-path TextBlock emitted beside the
	// image keeps the artifact reachable in chat.
	//
	// CRITICAL: tool_result blocks go into their OWN new user
	// message, they are NOT merged into the preceding user message.
	// Merging would mutate the prior user message's content array
	// between turn N and turn N+1: turn N caches the prefix ending at
	// [user: "read sample.ts"], turn N+1 sends
	// [user: "read sample.ts" + tool_result=...] which is a
	// different block sequence, busting the cache prefix match.
	// Anthropic's API happily accepts consecutive user messages, and
	// emitting them separately keeps each message bit-stable across
	// turns, so the cache prefix matches for the entire history up
	// to the newest block.
	// Repair orphaned tool results, merge any same-role adjacency an edit/delete
	// left behind (Anthropic requires strict alternation), then make a leading
	// assistant turn (a card's seeded greeting) valid by prepending a
	// request-scoped user turn. All operate on the generic message list and never
	// mutate history.
	req.Messages = EnsureLeadingUserTurn(MergeAdjacentSameRole(RepairOrphanedToolResults(req.Messages)))
	for _, msg := range req.Messages {
		renameTools := c.oauth
		switch msg.Role {
		case RoleUser:
			out.Messages = append(out.Messages, anthMessage{
				Role:    "user",
				Content: convertAnthContent(msg.Content, renameTools),
			})
		case RoleTool:
			out.Messages = append(out.Messages, anthMessage{
				Role:    "user",
				Content: convertAnthContent(msg.Content, renameTools),
			})
		case RoleAssistant:
			out.Messages = append(out.Messages, anthMessage{
				Role:    "assistant",
				Content: convertAnthContent(filterAnthAssistantContent(msg.Content), renameTools),
			})
		}
	}

	// Tag the LAST user message with cache_control. Spends the 4th
	// breakpoint. For prefixes under ~1024 tokens (Anthropic's
	// minimum cacheable block size for Opus), no cache is written.
	tagLastUserCache(out.Messages)

	// Ephemeral context goes in a trailing user message AFTER the cache
	// breakpoint and carries NO cache_control: the cached prefix (system
	// + tools + history through the marked message) still hits, and only
	// this small block is re-processed. It is request-scoped — never part
	// of req.Messages, so it's never cached as a prefix that the next
	// turn (with different/absent context) would fail to match.
	if req.EphemeralContext != "" {
		out.Messages = append(out.Messages, anthMessage{
			Role:    "user",
			Content: []interface{}{anthTextBlock{Type: "text", Text: req.EphemeralContext}},
		})
	}

	// A trailing assistant message is a PREFILL — the continue interaction, where
	// the model extends the last response. Anthropic 400s on an assistant message
	// ending in whitespace, so right-trim its final text block. This only fires for
	// a prefill request: a normal turn ends in a user/tool message (or the
	// ephemeral user block just appended), never a bare trailing assistant.
	trimTrailingAssistantPrefill(out.Messages)

	return out, nil
}

// trimTrailingAssistantPrefill right-trims whitespace from the last text block of
// a trailing assistant message so an assistant-prefill continue request is
// accepted. No-op unless the final message is an assistant one.
func trimTrailingAssistantPrefill(msgs []anthMessage) {
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "assistant" {
		return
	}
	content := msgs[len(msgs)-1].Content
	for i := len(content) - 1; i >= 0; i-- {
		if tb, ok := content[i].(anthTextBlock); ok {
			tb.Text = strings.TrimRight(tb.Text, " \t\n\r")
			content[i] = tb
			return
		}
	}
}

// tagLastUserCache marks the last block of the most recent user
// message. One marker; combined with identity + systemPrompt +
// last-tool, spends Anthropic's 4-breakpoint budget.
func tagLastUserCache(msgs []anthMessage) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			markLastBlockEphemeral(msgs[i].Content)
			return
		}
	}
}

// markLastBlockEphemeral sets CacheControl on the last entry in blocks
// regardless of whether it's a text, image, tool_use, or tool_result.
// Each block type carries its own CacheControl pointer so we type-
// switch + reassign the slice element.
func markLastBlockEphemeral(blocks []interface{}) {
	if len(blocks) == 0 {
		return
	}
	i := len(blocks) - 1
	cc := &anthCacheCtrl{Type: "ephemeral"}
	switch v := blocks[i].(type) {
	case anthTextBlock:
		v.CacheControl = cc
		blocks[i] = v
	case anthImageBlock:
		v.CacheControl = cc
		blocks[i] = v
	case anthToolUseBlock:
		v.CacheControl = cc
		blocks[i] = v
	case anthToolResultBlock:
		v.CacheControl = cc
		blocks[i] = v
	}
}

func anthropicReasoningBudget(level string) int {
	return ReasoningBudget(level)
}

// anthropicMinAnswerTokens is the answer headroom kept below max_tokens when a
// thinking budget is in play: reasoning requires max_tokens > budget, so the
// budget cannot consume the model's whole output cap.
const anthropicMinAnswerTokens = 1024

// anthropicThinkingBudget is the thinking budget actually sent for m at this
// level — the ladder value clamped to what the model's output cap leaves room
// for. Returns 0 when no budget is sent.
//
// Extracted from buildRequest so the surface that EXPLAINS a rung
// (ReasoningEffectFor, and through it the /reasoning dialog) and the code that
// ENFORCES it are the same code. Duplicating the clamp is how a dialog ends up
// promising a budget the request never carries — the exact drift MaxIsNative
// was written to prevent for the top rung.
func anthropicThinkingBudget(m Model, level string) int {
	budget := anthropicReasoningBudget(level)
	if budget <= 0 {
		return 0
	}
	if m.MaxOutput > anthropicMinAnswerTokens && budget >= m.MaxOutput {
		budget = m.MaxOutput - anthropicMinAnswerTokens
	}
	return budget
}

func filterAnthAssistantContent(blocks []Content) []Content {
	filtered := make([]Content, 0, len(blocks))
	for _, b := range blocks {
		if _, ok := b.(ImageBlock); ok {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func convertAnthContent(blocks []Content, renameTools bool) []interface{} {
	out := make([]interface{}, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.(type) {
		case TextBlock:
			if v.Text == "" {
				continue
			}
			out = append(out, anthTextBlock{Type: "text", Text: v.Text})
		case ImageBlock:
			data, mime := anthShrinkImageBytesIfTooBig(v.Data, v.MimeType)
			out = append(out, anthImageBlock{
				Type: "image",
				Source: anthImageSource{
					Type:      "base64",
					MediaType: mime,
					Data:      base64.StdEncoding.EncodeToString(data),
				},
			})
		case ToolCallBlock:
			args := v.Arguments
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			name := v.Name
			if renameTools {
				name = toClaudeCodeToolName(name)
			}
			out = append(out, anthToolUseBlock{
				Type: "tool_use", ID: v.ID, Name: name, Input: args,
			})
		case ReasoningBlock:
			// Replayed only where it came from. A transcript outlives a
			// /model switch, so this is reached with Codex and Gemini
			// reasoning in hand too — untagged blocks are dropped, which is
			// what this function has always done with every ReasoningBlock.
			switch v.Shape {
			case ReasoningShapeAnthropicThinking:
				// Half a pair is worse than none: the signature seals this
				// exact text, so a block stripped for display-only recording
				// is dropped here rather than sent to fail the whole request.
				if v.Summary == "" || v.Encrypted == "" {
					continue
				}
				out = append(out, anthThinkingBlock{
					Type: "thinking", Thinking: v.Summary, Signature: v.Encrypted,
				})
			case ReasoningShapeAnthropicThinkingOpaque:
				// Anthropic withheld the text itself, so an empty Thinking is
				// what it signed and what it expects back — verified live: a
				// captured {thinking:"", signature} replays and is accepted.
				// The emptiness is native rather than the result of stripping,
				// which is precisely what the separate shape records.
				if v.Encrypted == "" {
					continue
				}
				out = append(out, anthThinkingBlock{
					Type: "thinking", Thinking: "", Signature: v.Encrypted,
				})
			case ReasoningShapeAnthropicRedacted:
				if v.Encrypted == "" {
					continue
				}
				out = append(out, anthRedactedThinkingBlock{
					Type: "redacted_thinking", Data: v.Encrypted,
				})
			}
		case ToolResultBlock:
			// Flatten content to a string if all text; else array of blocks.
			content, _ := anthBuildToolResultContent(v.Content)
			out = append(out, anthToolResultBlock{
				Type:      "tool_result",
				ToolUseID: v.CallID,
				Content:   content,
				IsError:   v.IsError,
			})
		}
	}
	return out
}

func anthBuildToolResultContent(blocks []Content) (json.RawMessage, error) {
	onlyText := true
	var sb strings.Builder
	for _, b := range blocks {
		if tb, ok := b.(TextBlock); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tb.Text)
		} else {
			onlyText = false
			break
		}
	}
	if onlyText {
		if sb.Len() == 0 {
			return json.Marshal("")
		}
		return json.Marshal(sb.String())
	}
	// Array form: text + image blocks.
	arr := make([]interface{}, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.(type) {
		case TextBlock:
			arr = append(arr, anthTextBlock{Type: "text", Text: v.Text})
		case ImageBlock:
			data, mime := anthShrinkImageBytesIfTooBig(v.Data, v.MimeType)
			arr = append(arr, anthImageBlock{
				Type: "image",
				Source: anthImageSource{
					Type:      "base64",
					MediaType: mime,
					Data:      base64.StdEncoding.EncodeToString(data),
				},
			})
		}
	}
	return json.Marshal(arr)
}

// ---- streaming ----

func (c *anthropicClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}

	// Optional debug dump: when $TERVA_DEBUG_ANTHROPIC is a file path
	// we append every outgoing request body to it, one JSON object
	// per line. Useful for diffing turn N vs turn N+1 to understand
	// why the cache prefix isn't matching.
	if dump := envcompat.Get("DEBUG_ANTHROPIC"); dump != "" {
		if f, derr := os.OpenFile(dump, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); derr == nil {
			_, _ = f.Write(body)
			_, _ = f.Write([]byte{'\n'})
			_ = f.Close()
		}
	}

	// Resolve the credential once per turn (may refresh an expired OAuth
	// token); retries within this Stream reuse it.
	cred, err := c.cred(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Name(), err)
	}

	newReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
		if c.oauth {
			// Claude-Code-shaped request: identical headers and values as the
			// official CLI. Any drift triggers Anthropic's anti-abuse check and
			// rate-limits (or outright blocks) the request.
			httpReq.Header.Set("accept", "application/json")
			httpReq.Header.Set("authorization", "Bearer "+cred)
			httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14")
			httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
			httpReq.Header.Set("user-agent", "claude-cli/"+claudeCodeVersion)
			httpReq.Header.Set("x-app", "cli")
			// Remove x-api-key entirely by NOT setting it.
		} else {
			httpReq.Header.Set("accept", "text/event-stream")
			httpReq.Header.Set("x-api-key", cred)
		}
		// Extra headers (set by anthropic-messages-compatible third parties
		// — kimi-coding's X-Msh-*, copilot's Editor-Plugin-Version, etc.).
		// Applied last so callers can override defaults if they really need to.
		for k, v := range c.headers {
			httpReq.Header.Set(k, v)
		}
		return httpReq, nil
	}

	// c.Name(), not "anthropic": this client also drives every
	// Anthropic-Messages-compatible third party (kimi-coding, minimax,
	// fireworks, vercel-ai-gateway). The wire format is Anthropic's; the
	// PROVIDER is whoever answered, and that is what the reader needs. A
	// hardcoded label sent kimi's expired-subscription 401 to the rescue
	// picker as "anthropic/k3-256k" — naming a vendor the request never
	// reached, and one whose login the user would then go and check.
	// ExtractFailedProvider reads ProviderError.Provider, so this is also
	// what decides whether the picker can drop the pair that just failed.
	resp, err := doStreamWithRetry(ctx, c.http, newReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Name(), err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := errorBodySnippet(resp.Body)
		resp.Body.Close()
		return nil, NewHTTPError(c.Name(), resp.StatusCode, resp.Header.Get("Retry-After"), snippet)
	}

	// Capture the unified subscription windows off every successful response
	// (free, passive — the same trade the openai client makes). Until this,
	// /usage told an Anthropic subscriber their provider reported nothing while
	// the numbers rode every response they had ever made.
	c.recordUsageHeaders(resp.Header)

	out := make(chan Event, 16)
	go c.runStream(ctx, resp, req, out)
	return out, nil
}

// recordUsageHeaders parses anthropic-ratelimit-unified-* into the cached
// snapshot. Nothing is recorded when the headers are absent, so an API-key
// account (no subscription, no windows) keeps reporting nothing rather than
// reporting zeros it would be wrong to draw.
func (c *anthropicClient) recordUsageHeaders(h http.Header) {
	snap, ok := parseAnthropicUsageHeaders(h)
	if ok {
		snap.Provider = c.Name()
	}
	c.usage.record(snap, ok)
}

// UsageSnapshot returns the windows parsed from the most recent response (the
// UsageReporter contract). ok=false until one carried usable headers.
func (c *anthropicClient) UsageSnapshot() (UsageSnapshot, bool) {
	return c.usage.snapshot()
}

func (c *anthropicClient) runStream(ctx context.Context, resp *http.Response, req Request, out chan<- Event) {
	defer close(out)

	// Same lookup-by-actual-provider-id pattern as buildRequest, so cost
	// calculation works for third-party Anthropic-Messages endpoints.
	model, _ := FindModel(c.Name(), req.Model)
	if model.ID == "" {
		model, _ = FindModel("", req.Model)
	}
	out <- EventStart{Model: req.Model, Provider: c.Name()}

	stream := newSSEStream(resp.Body, c.Name())
	defer stream.Close() // owns resp.Body: closes it, then unparks the reader
	raw := stream.Events()

	// State for assembling the assistant message. Blocks are indexed
	// by their `index` field from Anthropic so we can preserve the
	// interleaved order the model emitted them in (text may come
	// before OR after tool_use; mixing both happens frequently).
	type blockEntry struct {
		kind     string // "text" | "tool_use" | "thinking" | "redacted_thinking"
		textBuf  strings.Builder
		toolCall ToolCallBlock
		toolArgs strings.Builder
		// sigBuf accumulates signature_delta for a thinking block. The
		// signature is Anthropic's seal over the thinking TEXT, so the two
		// only mean anything together — see assembleMsg.
		sigBuf strings.Builder
		// redacted is the opaque payload of a redacted_thinking block, which
		// arrives whole on content_block_start and never deltas.
		redacted string
	}
	var (
		blocks     = map[int]*blockEntry{}
		blockOrder []int // insertion order of indexes
		activeIdx  = -1
		usage      Usage
		stop       StopReason = StopEnd
		finalErr   error
		// sawStop tracks the message_stop terminal frame. If the raw
		// channel closes before it (connection death mid-stream,
		// including mid-tool-call), the message is truncated and we must
		// surface an error rather than report a clean StopEnd.
		sawStop bool
	)
	_ = activeIdx // read-only indicator used for legacy parity

	registerBlock := func(idx int, kind string) *blockEntry {
		if be, ok := blocks[idx]; ok {
			return be
		}
		be := &blockEntry{kind: kind}
		blocks[idx] = be
		blockOrder = append(blockOrder, idx)
		return be
	}

	assembleMsg := func() Message {
		content := []Content{}
		for _, idx := range blockOrder {
			be := blocks[idx]
			switch be.kind {
			case "text":
				if be.textBuf.Len() > 0 {
					content = append(content, TextBlock{Text: be.textBuf.String()})
				}
			case "tool_use":
				tc := be.toolCall
				tc.Arguments, tc.RawArguments = FinalizeToolArguments(be.toolArgs.String())
				content = append(content, tc)
			case "thinking":
				// The SIGNATURE is what makes the block replayable, and it is
				// the half that must be here. A stream cut mid-thinking is the
				// ordinary way to lose it — it is the last thing Anthropic
				// sends — and such a block is dropped rather than carried as
				// something that will 400 on every following turn.
				//
				// 🪤 The TEXT is legitimately empty on adaptive-thinking models
				// (Opus 4.7+, Sonnet 5): they withhold the readable
				// chain-of-thought and send one empty thinking_delta followed by
				// a real signature. Requiring text here threw those blocks away
				// — measured live, sonnet-5 spent 254 thinking tokens and terva
				// kept none of it, then had nothing to hand back.
				//
				// Which shape it is has to be decided HERE, while "no text ever
				// arrived" is still knowable. Downstream the two are identical.
				if be.sigBuf.Len() == 0 {
					continue
				}
				shape := ReasoningShapeAnthropicThinking
				if be.textBuf.Len() == 0 {
					shape = ReasoningShapeAnthropicThinkingOpaque
				}
				content = append(content, ReasoningBlock{
					Summary:   be.textBuf.String(),
					Encrypted: be.sigBuf.String(),
					Shape:     shape,
				})
			case "redacted_thinking":
				if be.redacted == "" {
					continue
				}
				content = append(content, ReasoningBlock{
					Encrypted: be.redacted,
					Shape:     ReasoningShapeAnthropicRedacted,
				})
			}
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
				case sawStop:
					// Terminal frame already seen: the message is whole, so a
					// stumble on the trailing bytes is not worth failing over.
				case stream.Err() != nil:
					// Over-limit line (permanent) or a transport read error.
					stop = StopError
					finalErr = stream.Err()
				default:
					stop = StopError
					finalErr = NewStreamDeathError(c.Name(), "message_stop")
				}
				sendDone()
				return
			}
			// Parse event payload based on event: type.
			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				continue
			}
			switch ev.Event {
			case "content_block_start":
				var idx int
				if b, ok := payload["index"]; ok {
					_ = json.Unmarshal(b, &idx)
				}
				var block struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
					Text string `json:"text"`
					// Data is the whole payload of a redacted_thinking block.
					Data  string          `json:"data"`
					Input json.RawMessage `json:"input"`
				}
				if b, ok := payload["content_block"]; ok {
					_ = json.Unmarshal(b, &block)
				}
				activeIdx = idx
				switch block.Type {
				case "tool_use":
					name := block.Name
					if c.oauth {
						name = fromClaudeCodeToolName(name, req.Tools)
					}
					be := registerBlock(idx, "tool_use")
					be.toolCall = ToolCallBlock{ID: block.ID, Name: name}
					out <- EventToolStart{ID: block.ID, Name: name}
				case "text":
					registerBlock(idx, "text")
				case "thinking":
					registerBlock(idx, "thinking")
				case "redacted_thinking":
					// Arrives whole: Anthropic encrypted the reasoning, so
					// there is nothing to stream and no text to show. Kept
					// only so the turn can be replayed intact.
					registerBlock(idx, "redacted_thinking").redacted = block.Data
				}
			case "content_block_delta":
				var idx int
				if b, ok := payload["index"]; ok {
					_ = json.Unmarshal(b, &idx)
				}
				var d struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					Thinking    string `json:"thinking"`
					Signature   string `json:"signature"`
				}
				if b, ok := payload["delta"]; ok {
					_ = json.Unmarshal(b, &d)
				}
				switch d.Type {
				case "text_delta":
					if be, ok := blocks[idx]; ok && be.kind == "text" {
						be.textBuf.WriteString(d.Text)
					}
					out <- EventTextDelta{Delta: d.Text}
				case "input_json_delta":
					if be, ok := blocks[idx]; ok && be.kind == "tool_use" {
						be.toolArgs.WriteString(d.PartialJSON)
						out <- EventToolArgs{ID: be.toolCall.ID, Delta: d.PartialJSON}
					}
				case "thinking_delta":
					if be, ok := blocks[idx]; ok && be.kind == "thinking" {
						be.textBuf.WriteString(d.Thinking)
					}
					out <- EventReasoningDelta{Delta: d.Thinking}
				case "signature_delta":
					// The seal over the text above. It is not reasoning to
					// show, so it is accumulated and never emitted.
					if be, ok := blocks[idx]; ok && be.kind == "thinking" {
						be.sigBuf.WriteString(d.Signature)
					}
				}
			case "content_block_stop":
				var idx int
				if b, ok := payload["index"]; ok {
					_ = json.Unmarshal(b, &idx)
				}
				if be, ok := blocks[idx]; ok && be.kind == "tool_use" {
					out <- EventToolEnd{ID: be.toolCall.ID}
				}
				activeIdx = -1
			case "message_start":
				var m struct {
					Message struct {
						Usage struct {
							InputTokens              int `json:"input_tokens"`
							OutputTokens             int `json:"output_tokens"`
							CacheReadInputTokens     int `json:"cache_read_input_tokens"`
							CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						} `json:"usage"`
					} `json:"message"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &m)
				// Anthropic sends cumulative values on message_start and
				// again on message_delta (refreshed), so assign, don't
				// accumulate. Accumulating doubles cache_creation_input
				// which can be 50-70% of cost.
				usage.InputTokens = m.Message.Usage.InputTokens
				usage.OutputTokens = m.Message.Usage.OutputTokens
				usage.CacheReadTokens = m.Message.Usage.CacheReadInputTokens
				usage.CacheWriteTokens = m.Message.Usage.CacheCreationInputTokens
			case "message_delta":
				var m struct {
					Delta struct {
						StopReason string `json:"stop_reason"`
					} `json:"delta"`
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						OutputTokens             int `json:"output_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						// Adaptive-thinking models break thinking out here.
						// A POINTER because the flag below means "this
						// provider reported a number", and a plain int cannot
						// tell an absent field from a measured zero — the
						// distinction ReasoningTokensKnown exists to carry.
						OutputTokensDetails *struct {
							ThinkingTokens int `json:"thinking_tokens"`
						} `json:"output_tokens_details"`
					} `json:"usage"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &m)
				// Refresh usage from the latest cumulative totals
				// Anthropic provides. Only apply non-zero fields in case
				// a given delta only carries output tokens.
				if m.Usage.InputTokens > 0 {
					usage.InputTokens = m.Usage.InputTokens
				}
				if m.Usage.OutputTokens > 0 {
					usage.OutputTokens = m.Usage.OutputTokens
				}
				if m.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadTokens = m.Usage.CacheReadInputTokens
				}
				if m.Usage.CacheCreationInputTokens > 0 {
					usage.CacheWriteTokens = m.Usage.CacheCreationInputTokens
				}
				// 🪤 Anthropic DOES break thinking out, on adaptive models.
				// It was documented as the provider that never does, so a
				// turn that spent 254 of its 258 output tokens thinking
				// reported "not measured" and the one number that explains
				// the bill was invisible.
				if d := m.Usage.OutputTokensDetails; d != nil {
					usage.ReasoningTokens = d.ThinkingTokens
					usage.ReasoningTokensKnown = true
				}
				switch m.Delta.StopReason {
				case "end_turn", "stop_sequence":
					stop = StopEnd
				case "max_tokens":
					stop = StopLength
				case "tool_use":
					stop = StopToolUse
				}
			case "message_stop":
				sawStop = true
				sendDone()
				return
			case "error":
				var e struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				_ = json.Unmarshal([]byte(ev.Data), &e)
				stop = StopError
				// Anthropic's documented transient error types — 529 overloaded,
				// 500 api_error, 429 rate_limit_error — are all in the shared
				// vocabulary. Its error frame has no `code`, only a `type`.
				finalErr = NewAPIError(c.Name(), e.Error.Type+": "+e.Error.Message,
					transientErrorCode("", e.Error.Type, e.Error.Message))
				sendDone()
				return
			}
			_ = activeIdx
		}
	}
}
