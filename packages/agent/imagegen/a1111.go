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

// A1111 is the AUTOMATIC1111 / Forge backend: POST <base>/sdapi/v1/txt2img, the
// de-facto Stable Diffusion WebUI API (its own JSON schema; base64 images out).
// Covers AUTOMATIC1111, Forge, and forks that keep the sdapi surface. Negative
// prompts are native (no pipe). See docs/proposals/image-generation.md.
type A1111 struct {
	Name        string
	BaseURL     string // e.g. http://localhost:7860
	APIKey      string // optional "user:pass" for --api-auth (sent as HTTP Basic)
	Model       string // optional checkpoint (override_settings.sd_model_checkpoint)
	DefaultSize string // "WxH"; falls back to 512x512
	Steps       int    // 0 → server default
	Sampler     string // "" → server default
	CFGScale    float64
	HTTP        *http.Client
}

func (a *A1111) ID() string { return a.Name }

type a1111Request struct {
	Prompt           string         `json:"prompt"`
	NegativePrompt   string         `json:"negative_prompt,omitempty"`
	Steps            int            `json:"steps,omitempty"`
	SamplerName      string         `json:"sampler_name,omitempty"`
	CFGScale         float64        `json:"cfg_scale,omitempty"`
	Width            int            `json:"width"`
	Height           int            `json:"height"`
	BatchSize        int            `json:"batch_size,omitempty"`
	OverrideSettings map[string]any `json:"override_settings,omitempty"`
}

type a1111Response struct {
	Images []string `json:"images"`
	// error/detail on failure (FastAPI shape)
	Detail any `json:"detail"`
}

func (a *A1111) Generate(ctx context.Context, req Request) (Result, error) {
	w, h := a.dims(req.Size)
	n := req.N
	if n <= 0 {
		n = 1
	}
	body := a1111Request{
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		Steps:          a.Steps,
		SamplerName:    a.Sampler,
		CFGScale:       a.CFGScale,
		Width:          w,
		Height:         h,
		BatchSize:      n,
	}
	if a.Model != "" {
		body.OverrideSettings = map[string]any{"sd_model_checkpoint": a.Model}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	url := strings.TrimRight(a.BaseURL, "/") + "/sdapi/v1/txt2img"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.APIKey)))
	}
	resp, err := httpClient(a.HTTP).Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("image request to %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))

	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("%s: HTTP %d: %s", a.Name, resp.StatusCode, snippet(raw))
	}
	var parsed a1111Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{}, fmt.Errorf("%s: decode response: %w", a.Name, err)
	}
	if len(parsed.Images) == 0 {
		return Result{}, fmt.Errorf("%s: response carried no images", a.Name)
	}
	out := Result{Backend: a.Name, Model: a.Model}
	for i, b64 := range parsed.Images {
		// a1111 sometimes returns a "data:image/png;base64,…" data URL; strip it.
		if idx := strings.Index(b64, ","); strings.HasPrefix(b64, "data:") && idx >= 0 {
			b64 = b64[idx+1:]
		}
		data, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil {
			return Result{}, fmt.Errorf("%s: decode image %d: %w", a.Name, i, derr)
		}
		out.Images = append(out.Images, Image{Data: data, MimeType: http.DetectContentType(data)})
	}
	return out, nil
}

// dims resolves the pixel dimensions: the request size, else the backend's
// configured default, else 512x512 (Stable Diffusion's classic default).
func (a *A1111) dims(size string) (int, int) {
	if w, h, ok := parseSize(size); ok {
		return w, h
	}
	if w, h, ok := parseSize(a.DefaultSize); ok {
		return w, h
	}
	return 512, 512
}
