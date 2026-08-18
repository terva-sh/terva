package provider

// Azure OpenAI client.
//
// Strategy: reuse the existing openaiClient (Chat Completions wire format)
// but with three Azure-specific adjustments handled at construction time:
//
//   - URL shape: `{base}/openai/deployments/{model}/chat/completions?api-version={v}`
//     This is wired by rewriting the model id into a path-prefix so the
//     downstream code that appends `/v1/chat/completions` lands on the
//     right endpoint. We achieve this by routing requests through a small
//     RoundTripper that rewrites the outgoing URL before the request hits
//     the network. The `openaiClient` itself is untouched.
//   - Auth: `api-key: <key>` header instead of `Authorization: Bearer ...`.
//     Sent via openaiClient.headers; the standard Authorization header
//     remains but Azure ignores it.
//   - API version: appended as `?api-version=...` query string by the
//     same RoundTripper.
//
// We deliberately use Chat Completions (not the newer Responses API) so we
// don't have to duplicate the entire openai-responses wire client. The
// agent loop only needs streamed text + tool calls, both of which work
// fine on Azure's Chat Completions deployment.

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const defaultAzureAPIVersion = "2024-10-21"

// azureRewriteTransport rewrites every outgoing OpenAI Chat Completions
// URL into the Azure deployment-scoped shape:
//
//	/openai/v1/chat/completions  ->
//	/openai/deployments/{model}/chat/completions?api-version={v}
//
// The model id is read from the JSON body of the POST so we don't need to
// thread it through the client at construction time. This keeps the
// openaiClient request-builder untouched.
type azureRewriteTransport struct {
	inner      http.RoundTripper
	apiVersion string
}

func (t *azureRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/chat/completions") {
		// Extract model id from the JSON body. The body is a *bytes.Reader
		// (set up by openaiClient.Stream) so we can peek without consuming
		// the request — Go's http.NewRequestWithContext gives us
		// GetBody() automatically for *bytes.Reader inputs.
		modelID := ""
		if req.GetBody != nil {
			if body, err := req.GetBody(); err == nil {
				buf := make([]byte, 8192)
				n, _ := body.Read(buf)
				_ = body.Close()
				// Cheap manual scan: look for `"model":"<id>"`.
				s := string(buf[:n])
				if i := strings.Index(s, `"model":"`); i >= 0 {
					rest := s[i+len(`"model":"`):]
					if j := strings.Index(rest, `"`); j >= 0 {
						modelID = rest[:j]
					}
				}
			}
		}
		if modelID == "" {
			return nil, fmt.Errorf("azure: could not extract model id from request body")
		}
		// Strip whatever prefix the openaiClient picked and replace with
		// Azure's deployment path. Both `/openai/v1/chat/completions` and
		// plain `/chat/completions` are normalised.
		newPath := "/openai/deployments/" + url.PathEscape(modelID) + "/chat/completions"
		// Anchor at the host root; the openaiClient may have used a
		// base URL with no path or with `/openai/v1`.
		req.URL.Path = newPath
		q := req.URL.Query()
		q.Set("api-version", t.apiVersion)
		req.URL.RawQuery = q.Encode()
	}
	return t.inner.RoundTrip(req)
}

// NewAzureOpenAI returns an Azure OpenAI client.
//
// baseURL examples (any is fine; we normalise trailing slashes):
//
//	https://my-resource.openai.azure.com
//	https://my-resource.openai.azure.com/openai/v1
//
// apiKey is the Azure resource key. The api-version comes from
// AZURE_OPENAI_API_VERSION env var (default "2024-10-21").
func NewAzureOpenAI(apiKey, baseURL string) Client {
	if baseURL == "" {
		// Try env, then resource-name expansion.
		baseURL = os.Getenv("AZURE_OPENAI_BASE_URL")
		if baseURL == "" {
			if rn := os.Getenv("AZURE_OPENAI_RESOURCE_NAME"); rn != "" {
				baseURL = "https://" + rn + ".openai.azure.com"
			}
		}
	}
	if baseURL == "" {
		return &unimplementedClient{
			name: "azure-openai-responses",
			hint: "set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME (or pass --base-url)",
			wire: reasoningWireOpenAICompat,
		}
	}
	apiVersion := os.Getenv("AZURE_OPENAI_API_VERSION")
	if apiVersion == "" {
		apiVersion = defaultAzureAPIVersion
	}
	// Strip a trailing /openai/v1 (and /openai) since the rewrite transport
	// builds the full path itself.
	clean := strings.TrimRight(baseURL, "/")
	clean = strings.TrimSuffix(clean, "/openai/v1")
	clean = strings.TrimSuffix(clean, "/openai")
	httpClient := &http.Client{
		Transport: &azureRewriteTransport{
			inner:      http.DefaultTransport,
			apiVersion: apiVersion,
		},
		Timeout: 0,
	}
	return &openaiClient{
		cred:    StaticCredential(apiKey),
		baseURL: clean,
		name:    "azure-openai-responses",
		headers: map[string]string{"api-key": apiKey},
		http:    httpClient,
	}
}
