package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DiscoverAnthropic lists model ids visible to key on api.anthropic.com.
// The API returns a paginated list; we page through until has_more is false.
func DiscoverAnthropic(ctx context.Context, apiKey, baseURL string) ([]Model, error) {
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var out []Model
	after := ""
	for {
		url := strings.TrimRight(baseURL, "/") + "/v1/models?limit=1000"
		if after != "" {
			url += "&after_id=" + after
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("anthropic discover http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("anthropic discover parse: %w", err)
		}
		for _, d := range page.Data {
			out = append(out, Model{
				Provider:    "anthropic",
				ID:          d.ID,
				DisplayName: d.DisplayName,
				Source:      "live",
			})
		}
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// DiscoverOpenAI lists model ids visible to key on api.openai.com.
func DiscoverOpenAI(ctx context.Context, apiKey, baseURL string) ([]Model, error) {
	if baseURL == "" {
		baseURL = openaiDefaultBaseURL
	}
	client := &http.Client{Timeout: 15 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openai discover http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("openai discover parse: %w", err)
	}
	var out []Model
	for _, d := range page.Data {
		// Keep only chat-capable families. OpenAI's /v1/models returns
		// everything including embeddings, TTS, DALL-E, etc.
		if !looksLikeChatModel(d.ID) {
			continue
		}
		out = append(out, Model{
			Provider:    "openai",
			ID:          d.ID,
			DisplayName: d.ID,
			Source:      "live",
		})
	}
	return out, nil
}

// DiscoverGoogle lists Gemini model ids visible to key on
// generativelanguage.googleapis.com. The API paginates with
// nextPageToken; we follow it until exhausted.
func DiscoverGoogle(ctx context.Context, apiKey, baseURL string) ([]Model, error) {
	if baseURL == "" {
		baseURL = geminiDefaultBaseURL
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var out []Model
	pageToken := ""
	for {
		url := strings.TrimRight(baseURL, "/") + "/v1beta/models?pageSize=200"
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("google discover http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var page struct {
			Models []struct {
				Name                       string   `json:"name"` // "models/gemini-2.5-pro"
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				OutputTokenLimit           int      `json:"outputTokenLimit"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("google discover parse: %w", err)
		}
		for _, m := range page.Models {
			// Strip the "models/" prefix Gemini uses on resource names.
			id := strings.TrimPrefix(m.Name, "models/")
			if !looksLikeGeminiChatModel(id, m.SupportedGenerationMethods) {
				continue
			}
			display := m.DisplayName
			if display == "" {
				display = id
			}
			out = append(out, Model{
				Provider:      "google",
				ID:            id,
				DisplayName:   display,
				ContextWindow: m.InputTokenLimit,
				MaxOutput:     m.OutputTokenLimit,
				Source:        "live",
			})
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return out, nil
}

// looksLikeGeminiChatModel filters Gemini's /v1beta/models output
// down to entries usable with streamGenerateContent. The API also
// lists embedding models, AQA, and other non-chat artefacts.
func looksLikeGeminiChatModel(id string, methods []string) bool {
	if !strings.HasPrefix(id, "gemini-") && !strings.HasPrefix(id, "gemma-") {
		return false
	}
	if strings.Contains(id, "embedding") || strings.Contains(id, "aqa") {
		return false
	}
	if len(methods) > 0 {
		ok := false
		for _, m := range methods {
			if m == "generateContent" || m == "streamGenerateContent" {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// looksLikeChatModel returns true for OpenAI ids that can plausibly be
// used with the chat/completions endpoint. Errs on the side of inclusion.
func looksLikeChatModel(id string) bool {
	switch {
	case strings.HasPrefix(id, "gpt-"):
		return true
	case strings.HasPrefix(id, "o1"):
		return true
	case strings.HasPrefix(id, "o3"):
		return true
	case strings.HasPrefix(id, "o4"):
		return true
	case strings.HasPrefix(id, "o5"):
		return true
	case strings.HasPrefix(id, "chatgpt-"):
		return true
	}
	return false
}

// DiscoverOpenAICompatible lists every model id a user-configured
// OpenAI-compatible endpoint reports from /v1/models. The standard
// response carries only ids, so context sizes are best-effort: we read
// the common non-standard hints (vLLM's max_model_len, others'
// context_length / context_window) when present and otherwise fall back
// to defaultCtx. The API key is optional — keyless local servers are
// common. Obvious non-chat artefacts (embeddings, rerankers, audio) are
// skipped to keep the picker focused on usable models.
func DiscoverOpenAICompatible(ctx context.Context, baseURL, key string, defaultCtx int) ([]Model, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("empty base url")
	}
	url := baseURL + "/models"
	if !strings.HasSuffix(baseURL, "/v1") {
		url = baseURL + "/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openai-compatible discover http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var page struct {
		Data []struct {
			ID            string `json:"id"`
			MaxModelLen   int    `json:"max_model_len"`  // vLLM
			ContextLength int    `json:"context_length"` // some gateways
			ContextWindow int    `json:"context_window"` // some gateways
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("openai-compatible discover parse: %w", err)
	}
	var out []Model
	for _, d := range page.Data {
		if d.ID == "" || !looksLikeLocalChatModel(d.ID) {
			continue
		}
		ctxWin := defaultCtx
		for _, hint := range []int{d.MaxModelLen, d.ContextLength, d.ContextWindow} {
			if hint > 0 {
				ctxWin = hint
				break
			}
		}
		out = append(out, Model{
			Provider:      "openai-compatible",
			ID:            d.ID,
			DisplayName:   d.ID,
			ContextWindow: ctxWin,
			BaseURL:       baseURL,
			Source:        "live",
			Caps:          visionCapsFromID(d.ID),
		})
	}
	return out, nil
}

// visionCapsFromID makes an explicit image-input assertion for a model
// discovered through the standard /v1/models list, which carries no
// modality data (unlike OpenRouter's richer schema). Without it the
// catalog's optimistic image-input:true default applies to every
// discovered model — so a text-only gateway model (e.g. opencode-go's
// minimax/kimi/qwen) is handed an image and the backend 400s ("does not
// support image inputs") instead of terva dropping the image with a note.
//
// The assertion biases toward text-only: an unrecognized id is marked
// non-vision, because sending an image to a text-only model is a hard
// error while sending text-only input to a vision model just proceeds
// without the image. Known vision families (hosted Claude/GPT/Gemini that
// flow through gateways, plus common local vision model names) are marked
// vision so they keep working. Users override per-model in models.json
// ("capabilities": {"image-input": true}), which wins as the last layer.
func visionCapsFromID(id string) map[Capability]bool {
	return map[Capability]bool{CapImageInput: looksVisionCapable(id)}
}

// looksVisionCapable reports whether a model id belongs to a known
// vision-capable family. Conservative by design — only high-confidence
// patterns return true, so the cost of a miss is a gracefully-dropped
// image (recoverable, overridable) rather than a turn-breaking 400.
func looksVisionCapable(id string) bool {
	l := strings.ToLower(strings.TrimSpace(id))
	// Local/open vision model families (substring match — ids embed these).
	for _, v := range []string{
		"llava", "bakllava", "moondream", "pixtral", "idefics",
		"vision", "-vl", "vl-", "minicpm-v", "internvl", "cogvlm", "smolvlm",
	} {
		if strings.Contains(l, v) {
			return true
		}
	}
	// Hosted families served through gateways (opencode Zen, litellm, …).
	// Claude 3 and newer are all vision; claude-2/instant are not.
	if strings.HasPrefix(l, "claude-") && !strings.Contains(l, "claude-2") && !strings.Contains(l, "instant") {
		return true
	}
	if strings.HasPrefix(l, "gemini-") {
		return true
	}
	if strings.HasPrefix(l, "gpt-4o") || strings.HasPrefix(l, "gpt-4.1") ||
		strings.HasPrefix(l, "gpt-5") || strings.HasPrefix(l, "chatgpt-4o") {
		return true
	}
	// OpenAI o-series reasoning models (o1/o3/o4) are vision-capable.
	for _, p := range []string{"o1", "o3", "o4"} {
		if l == p || strings.HasPrefix(l, p+"-") {
			return true
		}
	}
	return false
}

// looksLikeLocalChatModel errs heavily toward inclusion — local model
// ids are arbitrary (qwen2.5-coder, llama3, a filesystem path, ...), so
// we can't allow-list by name like the hosted OpenAI catalogue does.
// Instead we only exclude families that clearly aren't chat models.
func looksLikeLocalChatModel(id string) bool {
	lower := strings.ToLower(id)
	for _, bad := range []string{"embed", "rerank", "whisper", "tts", "clip", "moderation"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return true
}

const openrouterDefaultBaseURL = "https://openrouter.ai/api/v1"

// DiscoverOpenRouter lists models from OpenRouter's public /models
// endpoint (no auth). Per-token USD prices are converted to USD per 1M
// tokens to match the rest of the catalog. baseURL defaults to the
// public endpoint.
func DiscoverOpenRouter(ctx context.Context, baseURL string) ([]Model, error) {
	if baseURL == "" {
		baseURL = openrouterDefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: 15 * time.Second}
	url := baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openrouter discover http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var page struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt          string `json:"prompt"`
				Completion      string `json:"completion"`
				InputCacheRead  string `json:"input_cache_read"`
				InputCacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
			TopProvider struct {
				ContextLength       int  `json:"context_length"`
				MaxCompletionTokens *int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("openrouter discover parse: %w", err)
	}
	var out []Model
	for _, d := range page.Data {
		if d.ID == "" {
			continue
		}
		display := d.Name
		if display == "" {
			display = d.ID
		}
		ctxWin := d.ContextLength
		if d.TopProvider.ContextLength > 0 && (ctxWin == 0 || d.TopProvider.ContextLength < ctxWin) {
			ctxWin = d.TopProvider.ContextLength
		}
		maxOut := 0
		if d.TopProvider.MaxCompletionTokens != nil {
			maxOut = *d.TopProvider.MaxCompletionTokens
		}
		out = append(out, Model{
			Provider:        "openrouter",
			ID:              d.ID,
			DisplayName:     display,
			ContextWindow:   ctxWin,
			MaxOutput:       maxOut,
			Reasoning:       openrouterSupportsReasoning(d.SupportedParameters),
			PriceInput:      perMillionTokens(d.Pricing.Prompt),
			PriceOutput:     perMillionTokens(d.Pricing.Completion),
			PriceCacheRead:  perMillionTokens(d.Pricing.InputCacheRead),
			PriceCacheWrite: perMillionTokens(d.Pricing.InputCacheWrite),
			BaseURL:         baseURL,
			Source:          "live",
			Caps:            modalityCaps(d.Architecture.InputModalities, d.Architecture.OutputModalities),
		})
	}
	return out, nil
}

// modalityCaps converts a discovery endpoint's modality lists into
// explicit capability assertions. Nothing is asserted (nil) when the
// endpoint omitted the field, so the per-capability defaults still
// apply — discovery enrichment is opportunistic, never load-bearing.
func modalityCaps(input, output []string) map[Capability]bool {
	caps := map[Capability]bool{}
	if len(input) > 0 {
		caps[CapImageInput] = containsModality(input, "image")
	}
	if len(output) > 0 {
		caps[CapImageOutput] = containsModality(output, "image")
	}
	if len(caps) == 0 {
		return nil
	}
	return caps
}

func containsModality(list []string, want string) bool {
	for _, m := range list {
		if strings.EqualFold(strings.TrimSpace(m), want) {
			return true
		}
	}
	return false
}

// openrouterSupportsReasoning reports whether OpenRouter's
// supported_parameters list marks the model as reasoning-capable.
func openrouterSupportsReasoning(params []string) bool {
	for _, p := range params {
		if p == "reasoning" || p == "include_reasoning" {
			return true
		}
	}
	return false
}

// perMillionTokens converts a per-token USD price string to USD per 1M
// tokens. Empty or unparseable values become 0.
func perMillionTokens(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 1_000_000
}
