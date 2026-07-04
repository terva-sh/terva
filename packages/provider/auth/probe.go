package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
)

// ProbeAPIKey verifies that key is valid for provider by making a
// lightweight authenticated request. Returns nil on success.
func ProbeAPIKey(ctx context.Context, provider, key string) error {
	if key == "" {
		return i18n.Errorf("empty key")
	}
	c := &http.Client{Timeout: 15 * time.Second}
	var req *http.Request
	var err error

	switch provider {
	case "anthropic":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "openai":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "kimi":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.kimi.com/coding/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "deepseek":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.deepseek.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "google":
		// Google Generative Language: list models with the API key.
		// Accepts the key via x-goog-api-key header (preferred over
		// the ?key= query param so it doesn't show up in proxy logs).
		req, err = http.NewRequestWithContext(ctx, "GET", "https://generativelanguage.googleapis.com/v1beta/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("x-goog-api-key", key)
	// OpenAI-compatible third parties: a GET /v1/models with bearer auth
	// is enough to validate the key. Branches kept explicit (rather than a
	// generic default) so the URL list is searchable and reviewable.
	case "moonshotai":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.moonshot.ai/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "moonshotai-cn":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.moonshot.cn/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "groq":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.groq.com/openai/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "cerebras":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.cerebras.ai/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "xai":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.x.ai/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "together":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.together.ai/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "openrouter":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://openrouter.ai/api/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "huggingface":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://router.huggingface.co/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "zai":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.z.ai/api/coding/paas/v4/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "mistral":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.mistral.ai/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "xiaomi":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.xiaomimimo.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "xiaomi-token-plan-ams":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://token-plan-ams.xiaomimimo.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "xiaomi-token-plan-cn":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://token-plan-cn.xiaomimimo.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "xiaomi-token-plan-sgp":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://token-plan-sgp.xiaomimimo.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "minimax":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.minimax.io/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "minimax-cn":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.minimaxi.com/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "fireworks":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.fireworks.ai/inference/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "vercel-ai-gateway":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://ai-gateway.vercel.sh/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "opencode":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://opencode.ai/zen/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "opencode-go":
		req, err = http.NewRequestWithContext(ctx, "GET", "https://opencode.ai/zen/go/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+key)
	case "azure-openai-responses":
		return nil
	case "amazon-bedrock":
		return nil
	case "google-vertex":
		return nil
	case "cloudflare-workers-ai", "cloudflare-ai-gateway":
		return nil
	case "github-copilot":
		return nil
	default:
		return i18n.Errorf("unknown provider %q", provider)
	}

	if strings.Contains(req.URL.String(), "{CLOUDFLARE_ACCOUNT_ID}") {
		if acct := os.Getenv("CLOUDFLARE_ACCOUNT_ID"); acct != "" {
			u := strings.ReplaceAll(req.URL.String(), "{CLOUDFLARE_ACCOUNT_ID}", acct)
			req.URL, _ = req.URL.Parse(u)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return i18n.Errorf("%s rejected the key (http %d)", provider, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s http %d", provider, resp.StatusCode)
	}
	return nil
}

// ProbeOpenAICompatible verifies a user-supplied OpenAI-compatible
// endpoint by listing its models. The API key is optional: many local
// servers (LM Studio, llama.cpp, vLLM without auth) accept any or no
// bearer token. A reachable server that answers (even with 404, since
// not every server implements /models) counts as success; only 401/403
// means the key was rejected, and a transport error (connection refused
// / DNS failure) means the URL is wrong or the server is down.
func ProbeOpenAICompatible(ctx context.Context, baseURL, key string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return i18n.Errorf("empty base url")
	}
	u := baseURL + "/models"
	if !strings.HasSuffix(baseURL, "/v1") {
		u = baseURL + "/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("could not reach %s", u), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return i18n.Errorf("endpoint rejected the key (http %d)", resp.StatusCode)
	}
	return nil
}
