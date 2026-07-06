package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIImages is the `openai-images` backend: the OpenAI Images API
// (POST <base>/images/generations, always base64 out). One adapter covers
// hosted OpenAI/Azure AND the recommended low-friction self-host path —
// LocalAI is a drop-in that serves Stable Diffusion / SDXL / Flux on this exact
// API — plus any other OpenAI-compatible endpoint. See
// docs/proposals/image-generation.md.
type OpenAIImages struct {
	Name         string       // registry id (e.g. "openai-images")
	BaseURL      string       // API base incl. version, e.g. https://api.openai.com/v1
	APIKey       string       // bearer token; may be empty for an unauthenticated self-host
	Model        string       // gpt-image-1 / dall-e-3 / a LocalAI model name
	DefaultSize  string       // applied when a Request leaves Size empty
	NegativePipe bool         // LocalAI convention: fold NegativePrompt as "prompt|negative"
	HTTP         *http.Client // nil → http.DefaultClient
}

func (o *OpenAIImages) ID() string { return o.Name }

type oaiRequest struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
	N      int    `json:"n,omitempty"`
	Size   string `json:"size,omitempty"`
}

type oaiResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAIImages) Generate(ctx context.Context, req Request) (Result, error) {
	prompt := req.Prompt
	if o.NegativePipe && req.NegativePrompt != "" {
		prompt = prompt + "|" + req.NegativePrompt
	}
	size := req.Size
	if size == "" {
		size = o.DefaultSize
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	payload, err := json.Marshal(oaiRequest{Model: o.Model, Prompt: prompt, N: n, Size: size})
	if err != nil {
		return Result{}, err
	}
	url := strings.TrimRight(o.BaseURL, "/") + "/images/generations"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := httpClient(o.HTTP).Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("image request to %s: %w", o.Name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))

	var parsed oaiResponse
	_ = json.Unmarshal(body, &parsed)
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return Result{}, fmt.Errorf("%s: %s (HTTP %d)", o.Name, parsed.Error.Message, resp.StatusCode)
		}
		return Result{}, fmt.Errorf("%s: HTTP %d: %s", o.Name, resp.StatusCode, snippet(body))
	}
	if len(parsed.Data) == 0 {
		return Result{}, fmt.Errorf("%s: response carried no images", o.Name)
	}
	out := Result{Backend: o.Name, Model: o.Model}
	for i, d := range parsed.Data {
		if d.B64JSON == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return Result{}, fmt.Errorf("%s: decode image %d: %w", o.Name, i, err)
		}
		out.Images = append(out.Images, Image{Data: raw, MimeType: http.DetectContentType(raw)})
	}
	if len(out.Images) == 0 {
		return Result{}, fmt.Errorf("%s: response images were empty", o.Name)
	}
	return out, nil
}

// snippet trims a response body to a short, single-line excerpt for errors.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
