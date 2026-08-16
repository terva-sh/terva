package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Google Gemini provider, talking directly to the Generative Language
// REST API at generativelanguage.googleapis.com. We hand-roll the wire
// format instead of pulling in @google/genai (the SDK is large and we
// only need streamGenerateContent + the models list).
//
// Auth model: API key only. Google does NOT issue OAuth tokens for
// consumer Gemini Advanced / Google One AI subscriptions; programmatic
// access requires either an AI Studio API key (this client) or Vertex
// AI / GCP service-account credentials (separate provider, not yet
// implemented in terva).

const geminiDefaultBaseURL = "https://generativelanguage.googleapis.com"

// geminiVersionSuffix matches an API version segment already present at the end
// of a base URL: "/v1", "/v1beta", "/v1alpha", "/v1beta2".
var geminiVersionSuffix = regexp.MustCompile(`/v\d+(alpha|beta)?\d*$`)

// geminiAPIURL joins a base URL with a version-relative path such as
// "models/gemini-3.5-flash:streamGenerateContent". A base that already carries
// a version segment is used as-is; a bare host gets the default "/v1beta".
//
// 🪤 This is not defensive tidying. Every `google` row in the built-in catalog
// carries a BaseURL ending in "/v1beta", and that value reaches this client, so
// appending the prefix unconditionally produced "/v1beta/v1beta/models/..." and
// EVERY catalogued Gemini model 404'd before it ever reached Google. The failure
// is near-invisible from the outside: that path answers 404 with an EMPTY body,
// so terva printed a bare "http 404:" with nothing after the colon, while a
// genuine upstream 404 always carries a JSON error object. Measured 2026-08-14
// on one key and model: the correct path returned 200, the doubled path 404.
// Google's own docs quote the versioned URL, so a user pasting it into
// --base-url walked into the same wall.
func geminiAPIURL(baseURL, relPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = geminiDefaultBaseURL
	}
	if geminiVersionSuffix.MatchString(base) {
		return base + "/" + relPath
	}
	return base + "/v1beta/" + relPath
}

// geminiClient implements Client against the Gemini Generative Language API.
type geminiClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewGemini creates a Gemini client using an AI Studio API key.
// baseURL may be empty; defaults to https://generativelanguage.googleapis.com.
func NewGemini(apiKey, baseURL string) Client {
	if baseURL == "" {
		baseURL = geminiDefaultBaseURL
	}
	return &geminiClient{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 0},
	}
}

func (c *geminiClient) Name() string { return "google" }

// Capabilities declares that Gemini needs the tool-image mirror.
//
// 🪤 The agent loop's comment used to claim Gemini "carries tool-result images
// natively" and left it untouched. It does not: convertGemToolResultParts
// flattens a ToolResultBlock to TEXT ONLY and silently drops every ImageBlock,
// and because no Capabilities() existed here, MirrorsToolImages defaulted to
// false and the loop skipped the mirror too. Both halves failed, so a
// screenshot tool's output reached the model as nothing at all — no error, no
// warning, just a model answering about an image it never saw.
//
// Measured live 2026-08-14 on gemini-3.5-flash with a solid-blue PNG and the
// question "what colour did the tool return?":
//
//	tool result text only (today)          -> "Black", twice     (never saw it)
//	inlineData beside the functionResponse -> "0blue", ""        (garbage)
//	inlineData nested in response.parts[]  -> "White", raw zlib  (garbage)
//	following user message (this mirror)   -> "Blue", "blue"     (correct)
//
// A plain user turn with the same image answers "Red"/"Blue" correctly, so the
// model and the encoding were never in doubt — only the tool-result path was.
// The mirror is therefore the one shape that works, which is exactly what this
// capability turns on.
func (c *geminiClient) Capabilities() ClientCapabilities {
	return ClientCapabilities{MirrorsToolImages: true}
}

// ---- wire types ----
//
// Subset of Gemini's Content / Part / GenerateContentRequest schema.
// Only the fields terva actually emits or consumes are declared here.

type gemInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type gemFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gemFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// gemPart is one element of a Content. Exactly one of the optional
// fields is populated per part. We use pointers so empty values are
// omitted from the wire, which matters for tool responses (Gemini
// rejects parts with both `text` and `functionResponse` set).
type gemPart struct {
	Text             string               `json:"text,omitempty"`
	InlineData       *gemInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *gemFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *gemFunctionResponse `json:"functionResponse,omitempty"`
	// Thought: true marks a thought-summary part. Outgoing parts
	// from terva never set this; incoming chunks might.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature is the opaque token Gemini 3 attaches to the part
	// that carries a functionCall. It MUST be echoed on that same part when
	// the call is replayed in history, or the API answers HTTP 400
	// "Function call is missing a thought_signature in functionCall parts".
	// See https://ai.google.dev/gemini-api/docs/thought-signatures.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type gemContent struct {
	Role  string    `json:"role"` // "user" | "model"
	Parts []gemPart `json:"parts"`
}

type gemFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type gemTool struct {
	FunctionDeclarations []gemFunctionDecl `json:"functionDeclarations,omitempty"`
}

type gemThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

type gemGenerationConfig struct {
	Temperature     *float32           `json:"temperature,omitempty"`
	MaxOutputTokens *int               `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *gemThinkingConfig `json:"thinkingConfig,omitempty"`
}

type gemSystemInstruction struct {
	Parts []gemPart `json:"parts"`
}

type gemRequest struct {
	Contents          []gemContent          `json:"contents"`
	SystemInstruction *gemSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []gemTool             `json:"tools,omitempty"`
	GenerationConfig  *gemGenerationConfig  `json:"generationConfig,omitempty"`
}

// ---- request building ----

func (c *geminiClient) buildRequest(req Request) (*gemRequest, string, error) {
	m, err := FindModel("google", req.Model)
	if err != nil {
		// Not in the catalog — still allow custom ids by falling back
		// to defaults so users can point at unreleased models or
		// alternate base URLs.
		m = Model{
			Provider:      "google",
			ID:            req.Model,
			ContextWindow: 1_000_000,
			MaxOutput:     8192,
			Reasoning:     strings.Contains(req.Model, "2.5") || strings.Contains(req.Model, "3"),
		}
	}

	out := &gemRequest{}

	// System prompt → systemInstruction.parts[0].text.
	if strings.TrimSpace(req.System) != "" {
		out.SystemInstruction = &gemSystemInstruction{
			Parts: []gemPart{{Text: req.System}},
		}
	}

	functionsEnabled := geminiSupportsFunctionCalling(m.ID)

	// Convert tool defs. Gemini image-generation models reject function
	// declarations with "Function calling is not enabled for this model";
	// for those models, send a direct multimodal prompt instead.
	if functionsEnabled && len(req.Tools) > 0 {
		decls := make([]gemFunctionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := sanitizeGeminiToolSchema(t.Schema)
			decls = append(decls, gemFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			})
		}
		out.Tools = []gemTool{{FunctionDeclarations: decls}}
	}

	// Generation config.
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = m.MaxOutput
	}
	gc := &gemGenerationConfig{Temperature: req.Temperature}
	if maxTok > 0 {
		gc.MaxOutputTokens = &maxTok
	}
	eff := EffectiveReasoning(req.Reasoning, req.ReasoningSet, m)
	if eff != "" && m.Reasoning {
		tc := geminiThinkingConfig(m.ID, eff)
		if tc != nil {
			gc.ThinkingConfig = tc
		}
	}
	out.GenerationConfig = gc

	// Convert messages. When function calling is disabled for the target
	// model, also remove historical functionCall/functionResponse parts;
	// image models should receive only text/image content.
	msgs := req.Messages
	if functionsEnabled {
		msgs = RepairOrphanedToolResults(req.Messages)
		if geminiRequiresThoughtSignature(m.ID) {
			// After the orphan repair, so a call and its result are already
			// paired up and the flattening cannot create a new orphan.
			msgs = geminiFlattenUnsignedToolCalls(msgs)
		}
	}
	// Gemini enforces strict alternation: merge any same-role adjacency an
	// edit/delete left behind. A card's seeded greeting is a leading assistant
	// turn, which it also rejects, so prepend a request-scoped user turn.
	msgs = EnsureLeadingUserTurn(MergeAdjacentSameRole(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case RoleUser:
			parts := convertGemUserParts(msg.Content)
			if len(parts) == 0 {
				continue
			}
			out.Contents = append(out.Contents, gemContent{Role: "user", Parts: parts})
		case RoleAssistant:
			parts := convertGemAssistantParts(msg.Content, functionsEnabled)
			if len(parts) == 0 {
				continue
			}
			out.Contents = append(out.Contents, gemContent{Role: "model", Parts: parts})
		case RoleTool:
			if !functionsEnabled {
				continue
			}
			// Each tool_result becomes a user-role message with a
			// functionResponse part. Gemini's protocol uses
			// "user" role for tool replies.
			parts := convertGemToolResultParts(msg.Content)
			if len(parts) == 0 {
				continue
			}
			out.Contents = append(out.Contents, gemContent{Role: "user", Parts: parts})
		}
	}

	return out, m.ID, nil
}

func sanitizeGeminiToolSchema(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 || !json.Valid(schema) {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var v any
	if err := json.Unmarshal(schema, &v); err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	v = stripGeminiUnsupportedSchemaFields(v)
	out, err := json.Marshal(v)
	if err != nil || len(out) == 0 || !json.Valid(out) {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return out
}

func stripGeminiUnsupportedSchemaFields(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch k {
			case "additionalProperties", "$schema":
				continue
			default:
				out[k] = stripGeminiUnsupportedSchemaFields(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = stripGeminiUnsupportedSchemaFields(val)
		}
		return out
	default:
		return v
	}
}

func convertGemUserParts(blocks []Content) []gemPart {
	var parts []gemPart
	for _, b := range blocks {
		switch v := b.(type) {
		case TextBlock:
			if v.Text == "" {
				continue
			}
			parts = append(parts, gemPart{Text: v.Text})
		case ImageBlock:
			parts = append(parts, gemPart{
				InlineData: &gemInlineData{
					MimeType: v.MimeType,
					Data:     base64.StdEncoding.EncodeToString(v.Data),
				},
			})
		}
	}
	return parts
}

func convertGemAssistantParts(blocks []Content, functionsEnabled bool) []gemPart {
	var parts []gemPart
	for _, b := range blocks {
		switch v := b.(type) {
		case TextBlock:
			if strings.TrimSpace(v.Text) == "" {
				continue
			}
			parts = append(parts, gemPart{Text: v.Text})
		case ToolCallBlock:
			if !functionsEnabled {
				continue
			}
			args := v.Arguments
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			parts = append(parts, gemPart{
				FunctionCall: &gemFunctionCall{
					Name: v.Name,
					Args: args,
				},
				// Replayed verbatim: Gemini 3 rejects a functionCall in
				// history whose signature is missing. Empty for models that
				// never issued one (2.5 and older), where the field is
				// omitted from the wire entirely.
				ThoughtSignature: v.Signature,
			})
		}
	}
	return parts
}

// geminiSupportsFunctionCalling reports whether tools may be sent to a model.
//
// 🪤 This used to match the substring "flash-image", which is wrong in BOTH
// directions once the nano-banana family arrived. Measured against the live
// API on 2026-08-14, one identical prompt per model, with and without a
// function declaration:
//
//	gemini-2.5-flash-image          200 IMAGE   400 "Function calling is not
//	                                            enabled for this model"
//	gemini-3-pro-image              200 IMAGE   200 IMAGE
//	gemini-3-pro-image-preview      200 IMAGE   200 IMAGE
//	gemini-3.1-flash-image          200 IMAGE   200 IMAGE
//	gemini-3.1-flash-image-preview  200 IMAGE   200 IMAGE
//	gemini-3.1-flash-lite-image     200 IMAGE   200 IMAGE
//
// Of the six, the substring matched three: gemini-2.5-flash-image (correctly)
// and gemini-3.1-flash-image plus its -preview twin (wrongly). So two models
// that accept tools had every tool stripped from every request. An agent on
// gemini-3.1-flash-image could not read a file or run a command; it had no
// tools and no way to say so, which reads as a model that refuses to work
// rather than a client that disarmed it.
//
// The other three (gemini-3-pro-image, its -preview, and
// gemini-3.1-flash-lite-image) never matched the substring at all, so they
// were already correct — by luck of spelling rather than by design, which is
// why the rule below is a measurement and not a pattern.
//
// Suppression is now an explicit list of what was MEASURED to reject tools.
// A new image model therefore keeps its tools by default: the cost of being
// wrong that way is one legible 400 naming the reason, while the cost of the
// old default was a silently crippled agent.
func geminiSupportsFunctionCalling(modelID string) bool {
	id := strings.ToLower(modelID)
	// The whole 2.5 image family predates function calling on these models.
	// A prefix, so a dated or -preview variant of the same generation is
	// covered without another live probe.
	if strings.HasPrefix(id, "gemini-2.5-flash-image") {
		return false
	}
	// The retired 2.0-era ids (gemini-2.0-flash-exp-image-generation and
	// friends). No id on the live list matches this today, so it is inert
	// for a current key; it stays for a grandfathered one, where it cannot
	// be re-probed and a wrong guess costs a 400 on every turn.
	if strings.Contains(id, "image-generation") {
		return false
	}
	return true
}

// saveGeminiImageToWorkingDir writes a generated image into dir.
//
// An empty dir means the process working directory, which is what this did
// unconditionally before Request.WorkingDir existed — and terva never chdirs,
// so that was the launch directory rather than the session's workspace.
func saveGeminiImageToWorkingDir(dir, mimeType string, data []byte) (string, error) {
	ext := ".png"
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	name := "terva-gemini-image-" + uuid.NewString() + ext
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func convertGemToolResultParts(blocks []Content) []gemPart {
	var parts []gemPart
	for _, b := range blocks {
		if tb, ok := b.(TextBlock); ok {
			// A flattened tool result (see geminiFlattenUnsignedToolCalls).
			// Nothing else puts text in a tool message, so this arm is
			// reachable only through that path.
			//
			// The narration rides the SAME content as any functionResponse
			// that survived beside it, so flattening changes block types and
			// nothing else: same message count, same roles, same order. A
			// separate message would have to be role user, which is what a
			// tool message already becomes here, and then the merge and
			// leading-turn passes would be reasoning about a shape the agent
			// never produced.
			if strings.TrimSpace(tb.Text) != "" {
				parts = append(parts, gemPart{Text: tb.Text})
			}
			continue
		}
		tr, ok := b.(ToolResultBlock)
		if !ok {
			continue
		}
		// Flatten text content. Image content in tool results is dropped
		// for Gemini < 3 (multimodal function responses are 3+); for the
		// common path (text output) we wrap as {"output": "..."} or
		// {"error": "..."} per the SDK's convention.
		var sb strings.Builder
		for _, c := range tr.Content {
			if tb, ok := c.(TextBlock); ok {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(tb.Text)
			}
		}
		key := "output"
		if tr.IsError {
			key = "error"
		}
		// Gemini wants response to be an object, not a string.
		respObj := map[string]string{key: sb.String()}
		respBytes, err := json.Marshal(respObj)
		if err != nil {
			respBytes = []byte(`{"output":""}`)
		}
		// Tool name is required on functionResponse; the original
		// ToolCallBlock has it but ToolResultBlock only carries the
		// call id. We thread the name back via the call id by using
		// the id as the name fallback — Gemini ignores the name on
		// the response side as long as it's non-empty.
		name := tr.CallID
		parts = append(parts, gemPart{
			FunctionResponse: &gemFunctionResponse{
				Name:     name,
				Response: respBytes,
			},
		})
	}
	return parts
}

// geminiUsesThinkingLevel reports whether a model takes Gemini 3's enum
// `thinkingLevel` knob rather than the 2.5 family's token `thinkingBudget`.
//
// 🪤 A plain `strings.Contains(id, "gemini-3")` test is not enough, because
// Google's rolling aliases name no generation at all. "gemini-flash-latest"
// and "gemini-flash-lite-latest" missed that test, fell through the 2.5
// switch to its `default: return nil`, and terva sent them NO thinkingConfig
// whatsoever — so `--reasoning` was a silent no-op on two catalogued models
// that advertise Reasoning: true. Nothing failed and nothing was logged; the
// thinking simply never happened. Measured live 2026-08-14 on
// gemini-flash-latest: no config → 107 thought tokens (the model's own
// default), thinkingLevel LOW → 134, HIGH → 345. On gemini-flash-lite-latest
// the default was 0 thought tokens and HIGH produced 407, so the knob is the
// only thing that turns thinking on there at all.
//
// An alias that names an OLDER generation explicitly still routes to the
// budget knob; "latest" only ever moves forward, so an unnumbered alias is
// treated as current.
func geminiUsesThinkingLevel(id string) bool { return geminiIsGeneration3(id) }

// geminiRequiresThoughtSignature reports whether a model rejects a replayed
// functionCall that arrives without its thoughtSignature.
//
// The same generation test as the thinking knob today, and a separate name on
// purpose: "which thinking parameter does this take" and "does this reject an
// unsigned call" are two questions about one model, and Google is free to
// answer them differently. One shared predicate, two named callers, so the day
// they diverge the change is a body rather than an archaeology exercise.
func geminiRequiresThoughtSignature(id string) bool { return geminiIsGeneration3(id) }

// geminiIsGeneration3 reports whether an id names the Gemini 3 generation.
func geminiIsGeneration3(id string) bool {
	if strings.Contains(id, "gemini-3") {
		return true
	}
	if strings.HasPrefix(id, "gemini-") && strings.HasSuffix(id, "-latest") {
		for _, older := range []string{"1.0", "1.5", "2.0", "2.5"} {
			if strings.Contains(id, older) {
				return false
			}
		}
		return true
	}
	return false
}

// Tags marking a flattened call or result as transcript history rather than a
// live one. The model reads these in a role it cannot tell apart from ordinary
// content, so they have to say what they are — the same reason every other
// block terva inserts on its own account carries a tag.
const (
	geminiFlattenedCallTag   = "[earlier tool call]"
	geminiFlattenedResultTag = "[earlier tool result]"
	geminiFlattenedErrorTag  = "[earlier tool error]"
)

// geminiFlattenUnsignedToolCalls rewrites function calls that carry no thought
// signature — and the results answering them — into plain text, for the models
// that reject an unsigned call.
//
// 🪤 A signature is the model's sealed reasoning and cannot be reconstructed,
// so a call that arrives without one can NEVER be replayed as a functionCall
// part: the API answers 400 "Function call is missing a thought_signature in
// functionCall parts". That is not hypothetical. Agent.SetModel exists to swap
// the model id inside a live session and keeps the transcript, so
// gemini-2.5-pro → gemini-3.x after any tool loop walked into exactly the 400
// this file's signature round-trip was written to prevent. History assembled by
// another provider, or handed back by an SDK embedder, arrives the same way.
//
// Flattened to text rather than dropped. Dropping is what the !functionsEnabled
// path does and it is the wrong trade here: the model would see a turn where it
// said nothing, having in fact read a file. The text keeps the narrative and
// gives up only the structured form, which was unusable on this model anyway.
//
// Inert on the ordinary path — a Gemini 3 session's own calls carry their
// signatures, nothing matches, and the messages are returned untouched.
func geminiFlattenUnsignedToolCalls(msgs []Message) []Message {
	unsigned := make(map[string]bool)
	for _, m := range msgs {
		if m.Role != RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if tc, ok := b.(ToolCallBlock); ok && tc.Signature == "" {
				unsigned[tc.ID] = true
			}
		}
	}
	if len(unsigned) == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleAssistant:
			m.Content = geminiFlattenCalls(m.Content, unsigned)
		case RoleTool:
			m.Content = geminiFlattenResults(m.Content, unsigned)
		}
		out = append(out, m)
	}
	return out
}

// geminiFlattenCalls replaces each unsigned ToolCallBlock with its text form.
// Builds a new slice: the caller's messages belong to the agent, not to this
// request.
func geminiFlattenCalls(blocks []Content, unsigned map[string]bool) []Content {
	out := make([]Content, 0, len(blocks))
	for _, b := range blocks {
		tc, ok := b.(ToolCallBlock)
		if !ok || !unsigned[tc.ID] {
			out = append(out, b)
			continue
		}
		args := strings.TrimSpace(string(tc.Arguments))
		if args == "" || !json.Valid(tc.Arguments) {
			args = "{}"
		}
		out = append(out, TextBlock{Text: geminiFlattenedCallTag + " " + tc.Name + " " + args})
	}
	return out
}

// geminiFlattenResults replaces the result answering an unsigned call with its
// text form. A functionResponse whose functionCall is gone is an orphan, and
// the API rejects those too.
func geminiFlattenResults(blocks []Content, unsigned map[string]bool) []Content {
	out := make([]Content, 0, len(blocks))
	for _, b := range blocks {
		tr, ok := b.(ToolResultBlock)
		if !ok || !unsigned[tr.CallID] {
			out = append(out, b)
			continue
		}
		var sb strings.Builder
		for _, c := range tr.Content {
			if tb, ok := c.(TextBlock); ok {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(tb.Text)
			}
		}
		tag := geminiFlattenedResultTag
		if tr.IsError {
			tag = geminiFlattenedErrorTag
		}
		out = append(out, TextBlock{Text: tag + " " + sb.String()})
	}
	return out
}

// geminiThinkingConfig maps terva's reasoning level ("low"/"medium"/"high")
// to Gemini's thinkingConfig. The right knob depends on the model
// generation: 2.5 family uses thinkingBudget (tokens), 3.x uses
// thinkingLevel (enum). Returns nil when the level is unrecognised.
func geminiThinkingConfig(modelID, level string) *gemThinkingConfig {
	level = NormalizeReasoning(level)
	id := strings.ToLower(modelID)

	// Gemini 3.x: enum-based thinkingLevel. Pro can't go below LOW.
	if geminiUsesThinkingLevel(id) {
		isPro := strings.Contains(id, "-pro")
		var lvl string
		switch level {
		case "minimum":
			if isPro {
				lvl = "LOW"
			} else {
				lvl = "MINIMAL"
			}
		case "low":
			lvl = "LOW"
		case "medium":
			if isPro {
				lvl = "HIGH"
			} else {
				lvl = "MEDIUM"
			}
		case "high", "maximum", "max":
			lvl = "HIGH"
		default:
			return nil
		}
		return &gemThinkingConfig{IncludeThoughts: true, ThinkingLevel: lvl}
	}

	// Gemini 2.5 family: token-budget per-model.
	budget := ReasoningBudget(level)
	switch {
	case strings.Contains(id, "2.5-pro"):
		if budget > 32768 {
			budget = 32768
		}
	case strings.Contains(id, "2.5-flash-lite"):
		if budget > 24576 {
			budget = 24576
		}
	case strings.Contains(id, "2.5-flash"):
		if budget > 24576 {
			budget = 24576
		}
	default:
		return nil
	}
	if budget <= 0 {
		return nil
	}
	return &gemThinkingConfig{IncludeThoughts: true, ThinkingBudget: &budget}
}

// ---- streaming ----

func (c *geminiClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	wire, modelID, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}

	// Gemini's streaming endpoint is a POST with ?alt=sse to get
	// an EventSource-compatible response. Without alt=sse the
	// server returns a JSON array (one element per chunk), which
	// we'd need a different parser for.
	url := geminiAPIURL(c.baseURL, fmt.Sprintf("models/%s:streamGenerateContent", modelID)) + "?alt=sse"

	newReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("accept", "text/event-stream")
		// Two ways to authenticate against the Generative Language
		// API: x-goog-api-key header or ?key= query param. We use
		// the header so the key never lands in proxy access logs.
		httpReq.Header.Set("x-goog-api-key", c.apiKey)
		return httpReq, nil
	}

	resp, err := doStreamWithRetry(ctx, c.http, newReq)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := errorBodySnippet(resp.Body)
		resp.Body.Close()
		return nil, NewHTTPError("google", resp.StatusCode, resp.Header.Get("Retry-After"), snippet)
	}

	out := make(chan Event, 16)
	go c.runStream(ctx, resp, req, out)
	return out, nil
}

func (c *geminiClient) runStream(ctx context.Context, resp *http.Response, req Request, out chan<- Event) {
	defer close(out)

	model, _ := FindModel("google", req.Model)
	out <- EventStart{Model: req.Model, Provider: "google"}

	stream := newSSEStream(resp.Body, "google")
	defer stream.Close() // owns resp.Body: closes it, then unparks the reader
	raw := stream.Events()

	// Gemini's SSE stream is a sequence of complete JSON
	// GenerateContentResponse objects, one per data: line. Each
	// candidate carries a list of parts (text or functionCall),
	// possibly accumulating across chunks.
	type blockEntry struct {
		kind      string // "text" | "image" | "tool_use"
		textBuf   strings.Builder
		image     *ImageBlock
		imagePath string
		toolID    string
		toolName  string
		toolArgs  strings.Builder
		// toolSig is the part's thoughtSignature, kept so the next request
		// can replay this call with it. Gemini 3 makes it mandatory.
		toolSig string
	}
	var (
		blocks      []*blockEntry
		currentText *blockEntry
		usage       Usage
		stop        StopReason = StopEnd
		finalErr    error
		toolCounter int
		// reasoningBuf collects the thought-summary parts (thought=true) that
		// arrive interleaved with the answer. They are held apart from the
		// text blocks rather than appended to them: a thought summary is not
		// something the model said, and folding it into a TextBlock would put
		// it back into the reply on the next turn.
		reasoningBuf strings.Builder
		// sawFinish tracks whether any candidate carried an explicit
		// terminal finishReason. The Gemini SSE wire has no [DONE]
		// sentinel — a clean stream always ends with a non-empty
		// finishReason. If the raw channel closes without one (connection
		// death mid-stream, including mid-tool-call), the response is
		// truncated and we surface an error instead of a clean StopEnd.
		sawFinish bool
	)

	appendText := func(delta string) {
		if currentText == nil {
			currentText = &blockEntry{kind: "text"}
			blocks = append(blocks, currentText)
		}
		currentText.textBuf.WriteString(delta)
	}

	appendImage := func(mimeType, dataB64 string) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil || len(data) == 0 {
			return
		}
		img := ImageBlock{MimeType: mimeType, Data: data}
		path, _ := saveGeminiImageToWorkingDir(req.WorkingDir, mimeType, data)
		blocks = append(blocks, &blockEntry{kind: "image", image: &img, imagePath: path})
		// Image blocks break the current text run.
		currentText = nil
	}

	startTool := func(name string, providedID string, args json.RawMessage, sig string) *blockEntry {
		toolCounter++
		id := providedID
		if id == "" {
			id = fmt.Sprintf("%s_%d_%d", name, time.Now().UnixNano(), toolCounter)
		}
		t := &blockEntry{
			kind:     "tool_use",
			toolID:   id,
			toolName: name,
			toolSig:  sig,
		}
		if len(args) > 0 && json.Valid(args) {
			t.toolArgs.Write(args)
		}
		blocks = append(blocks, t)
		// New tool block breaks the current text run.
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
			case "image":
				if b.image != nil && len(b.image.Data) > 0 {
					content = append(content, *b.image)
					if b.imagePath != "" {
						content = append(content, TextBlock{Text: fmt.Sprintf("Saved image: `%s`", b.imagePath)})
					}
				}
			case "tool_use":
				args, unparsed := FinalizeToolArguments(b.toolArgs.String())
				content = append(content, ToolCallBlock{
					ID: b.toolID, Name: b.toolName, Arguments: args, RawArguments: unparsed,
					Signature: b.toolSig,
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
				case sawFinish:
					// Terminal frame already seen: the message is whole, so a
					// stumble on the trailing bytes is not worth failing over.
				case stream.Err() != nil:
					// Over-limit line (permanent) or a transport read error.
					// Gemini streams whole responses per line, inline image
					// bytes included, so this is the client most likely to
					// meet the ceiling.
					stop = StopError
					finalErr = stream.Err()
				default:
					stop = StopError
					finalErr = NewStreamDeathError("google", "a finishReason")
				}
				sendDone()
				return
			}
			if strings.TrimSpace(ev.Data) == "" {
				continue
			}
			var chunk struct {
				Candidates []struct {
					Content struct {
						Role  string `json:"role"`
						Parts []struct {
							Text             string           `json:"text"`
							InlineData       *gemInlineData   `json:"inlineData"`
							Thought          bool             `json:"thought"`
							ThoughtSignature string           `json:"thoughtSignature"`
							FunctionCall     *gemFunctionCall `json:"functionCall"`
							FunctionCallID   string           `json:"id"`
							FunctionCallName string           `json:"name"`
						} `json:"parts"`
					} `json:"content"`
					FinishReason string `json:"finishReason"`
				} `json:"candidates"`
				UsageMetadata *struct {
					PromptTokenCount        int `json:"promptTokenCount"`
					CandidatesTokenCount    int `json:"candidatesTokenCount"`
					ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
					CachedContentTokenCount int `json:"cachedContentTokenCount"`
					TotalTokenCount         int `json:"totalTokenCount"`
					// CandidatesTokensDetails breaks the candidate total down
					// by modality. The image models bill IMAGE tokens at a
					// different rate from the text they arrive with, and this
					// is the only field that says which is which.
					CandidatesTokensDetails []struct {
						Modality   string `json:"modality"`
						TokenCount int    `json:"tokenCount"`
					} `json:"candidatesTokensDetails"`
				} `json:"usageMetadata"`
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
					Status  string `json:"status"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
				continue
			}
			if chunk.Error != nil {
				stop = StopError
				// Gemini's in-stream error block carries an HTTP-style
				// code; classify it exactly like a response status.
				pe := NewHTTPError("google", chunk.Error.Code, "", chunk.Error.Message)
				if chunk.Error.Code == 0 {
					pe = NewAPIError("google", chunk.Error.Message, false)
				}
				finalErr = pe
				sendDone()
				return
			}
			for _, cand := range chunk.Candidates {
				for _, part := range cand.Content.Parts {
					if part.InlineData != nil {
						appendImage(part.InlineData.MimeType, part.InlineData.Data)
						continue
					}
					if part.FunctionCall != nil {
						var args json.RawMessage
						if len(part.FunctionCall.Args) > 0 {
							args = part.FunctionCall.Args
						} else {
							args = json.RawMessage("{}")
						}
						t := startTool(part.FunctionCall.Name, part.FunctionCallID, args, part.ThoughtSignature)
						out <- EventToolStart{ID: t.toolID, Name: t.toolName}
						out <- EventToolArgs{ID: t.toolID, Delta: t.toolArgs.String()}
						out <- EventToolEnd{ID: t.toolID}
						continue
					}
					if part.Text == "" {
						continue
					}
					if part.Thought {
						// Thinking summaries arrive as text parts
						// with thought=true. terva asks for them on
						// every generation-3 request
						// (includeThoughts), so discarding them here
						// meant paying to have them generated and
						// returned and then dropping them. They ride
						// EventReasoningDelta live and land in a
						// ReasoningBlock on the assembled message;
						// they are never appended as reply text.
						reasoningBuf.WriteString(part.Text)
						out <- EventReasoningDelta{Delta: part.Text}
						continue
					}
					appendText(part.Text)
					out <- EventTextDelta{Delta: part.Text}
				}
				if cand.FinishReason != "" {
					// Any non-empty finishReason is a legitimate
					// terminal signal (STOP, MAX_TOKENS, or a block
					// reason); record it so a subsequent channel close
					// is treated as a clean end rather than a drop.
					sawFinish = true
				}
				switch cand.FinishReason {
				case "STOP", "":
					// "" arrives on intermediate chunks; only
					// promote the explicit terminal value.
					if cand.FinishReason == "STOP" {
						stop = StopEnd
					}
				case "MAX_TOKENS":
					stop = StopLength
				case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
					stop = StopError
					finalErr = NewAPIError("google", "response blocked ("+cand.FinishReason+")", false)
				case "MALFORMED_FUNCTION_CALL":
					// 🪤 The model tried to call a tool and produced a call the
					// backend could not parse. It emits NO content with it — the
					// whole response is one empty text part — so before this arm
					// existed the turn fell through to the default StopEnd with a
					// nil error and terva exited 0 having printed NOTHING. Caught
					// live 2026-08-14 on gemini-3.1-pro-preview: the identical
					// prompt succeeded twice and produced this on the third run,
					// which is why it reads as a flake rather than a failure.
					//
					// Transient on that evidence: it is a generation-side glitch
					// and a retry clears it, so the loop should re-attempt rather
					// than hand the user an empty answer.
					stop = StopError
					finalErr = NewAPIError("google", "the model emitted a malformed function call (MALFORMED_FUNCTION_CALL)", true)
				default:
					// Any OTHER terminal reason is abnormal by construction:
					// STOP and MAX_TOKENS are the only two normal ends, and the
					// named blocks are handled above. Google keeps adding to this
					// enum (UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS, LANGUAGE,
					// OTHER …), and every one that lands here without an arm used
					// to become a silent successful empty turn. Naming the
					// unrecognized reason is strictly better than printing
					// nothing: it costs one visible error and it cannot hide.
					//
					// Not purely additive, and worth knowing which way: the
					// StopToolUse promotion below only runs on StopEnd, so a
					// reason that arrives ALONGSIDE usable tool calls ends the
					// turn as an error instead of continuing the loop. The
					// content is still assembled and still reaches the caller —
					// nothing is thrown away — but the calls are not executed.
					// For the two the enum names that could do this, that is
					// the wanted answer: TOO_MANY_TOOL_CALLS and
					// UNEXPECTED_TOOL_CALL both say the model went somewhere it
					// should not have, and running the calls would compound it.
					// The rule to keep is that an unknown reason is refused
					// rather than obeyed.
					//
					// "" never reaches here — the STOP arm above claims it — so
					// an intermediate chunk cannot be mistaken for a terminal
					// one. That is load-bearing, not incidental: without it
					// every streaming chunk would end the turn as an error.
					stop = StopError
					finalErr = NewAPIError("google", "response ended abnormally ("+cand.FinishReason+")", false)
				}
			}
			if chunk.UsageMetadata != nil {
				// Gemini reports cumulative totals on every chunk
				// (or close to it), so assign rather than sum.
				um := chunk.UsageMetadata
				input := um.PromptTokenCount - um.CachedContentTokenCount
				if input < 0 {
					input = um.PromptTokenCount
				}
				usage.InputTokens = input
				usage.OutputTokens = um.CandidatesTokenCount + um.ThoughtsTokenCount
				// Gemini is the one backend that already reported this and
				// then merged it away. Kept INSIDE OutputTokens above (it is
				// billed at the output rate); this only makes the split
				// visible.
				usage.ReasoningTokens = um.ThoughtsTokenCount
				usage.ReasoningTokensKnown = true
				usage.CacheReadTokens = um.CachedContentTokenCount
				// The image models bill IMAGE output at their own rate
				// (Model.PriceOutputImage), 10-20x the text rate on the
				// same model. This breakdown is the only thing that says
				// how much of the candidate total was picture, so without
				// it every image turn is priced as if it were prose.
				// A subset of OutputTokens, like ReasoningTokens above.
				imageTokens := 0
				for _, d := range um.CandidatesTokensDetails {
					if strings.EqualFold(d.Modality, "IMAGE") {
						imageTokens += d.TokenCount
					}
				}
				usage.ImageOutputTokens = imageTokens
			}
			// Promote ToolUse stop when tool calls are present and
			// the candidate finished cleanly.
			if stop == StopEnd {
				for _, b := range blocks {
					if b.kind == "tool_use" {
						stop = StopToolUse
						break
					}
				}
			}
		}
	}
}
