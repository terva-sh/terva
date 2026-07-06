package imagegen

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
)

// ComfyUI is the ComfyUI / SwarmUI backend. ComfyUI runs a node GRAPH rather
// than a fixed txt2img call, so a backend carries a workflow template (the
// "Save (API Format)" JSON) with `{{prompt}}` / `{{negative}}` placeholders in
// its text nodes; everything else (checkpoint, size, steps, seed, sampler)
// lives in the workflow the operator authored. Generation is three HTTP steps:
// POST /prompt to queue, poll /history/<id> for completion, GET /view to fetch
// each output image. See docs/proposals/image-generation.md.
type ComfyUI struct {
	Name     string
	BaseURL  string // e.g. http://localhost:8188
	Workflow string // API-format workflow JSON with {{prompt}}/{{negative}} placeholders
	HTTP     *http.Client

	// Tunable for tests; zero values use sane defaults.
	PollInterval time.Duration
	PollTimeout  time.Duration
	ClientID     string
}

func (c *ComfyUI) ID() string { return c.Name }

func (c *ComfyUI) Generate(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(c.Workflow) == "" {
		return Result{}, fmt.Errorf("%s: no workflow configured", c.Name)
	}
	wf, err := injectWorkflow(c.Workflow, req)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", c.Name, err)
	}
	promptID, err := c.queue(ctx, wf)
	if err != nil {
		return Result{}, err
	}
	refs, err := c.await(ctx, promptID)
	if err != nil {
		return Result{}, err
	}
	out := Result{Backend: c.Name}
	for _, ref := range refs {
		data, err := c.view(ctx, ref)
		if err != nil {
			return Result{}, err
		}
		out.Images = append(out.Images, Image{Data: data, MimeType: http.DetectContentType(data)})
	}
	if len(out.Images) == 0 {
		return Result{}, fmt.Errorf("%s: workflow produced no images", c.Name)
	}
	return out, nil
}

// injectWorkflow parses the workflow template, replaces the {{prompt}} and
// {{negative}} placeholders wherever they appear in string values, and returns
// the concrete workflow JSON. Walking the parsed value (rather than the raw
// text) means Go re-marshals the substituted strings with correct escaping, so
// a prompt with quotes or newlines can't break the JSON.
func injectWorkflow(tmpl string, req Request) (json.RawMessage, error) {
	var root any
	if err := json.Unmarshal([]byte(tmpl), &root); err != nil {
		return nil, fmt.Errorf("workflow is not valid JSON: %w", err)
	}
	rep := strings.NewReplacer("{{prompt}}", req.Prompt, "{{negative}}", req.NegativePrompt)
	root = walkReplace(root, rep)
	return json.Marshal(root)
}

func walkReplace(v any, rep *strings.Replacer) any {
	switch t := v.(type) {
	case string:
		return rep.Replace(t)
	case map[string]any:
		for k, val := range t {
			t[k] = walkReplace(val, rep)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = walkReplace(val, rep)
		}
		return t
	default:
		return v
	}
}

func (c *ComfyUI) clientID() string {
	if c.ClientID != "" {
		return c.ClientID
	}
	return "terva"
}

// queue POSTs the workflow to /prompt and returns the queued prompt id.
func (c *ComfyUI) queue(ctx context.Context, wf json.RawMessage) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": wf, "client_id": c.clientID()})
	if err != nil {
		return "", err
	}
	raw, status, err := c.do(ctx, http.MethodPost, "/prompt", bytes.NewReader(body), "application/json")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s: queue HTTP %d: %s", c.Name, status, snippet(raw))
	}
	var resp struct {
		PromptID   string `json:"prompt_id"`
		NodeErrors any    `json:"node_errors"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("%s: decode queue response: %w", c.Name, err)
	}
	if resp.PromptID == "" {
		return "", fmt.Errorf("%s: workflow rejected: %s", c.Name, snippet(raw))
	}
	return resp.PromptID, nil
}

type comfyImageRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// await polls /history/<id> until the prompt completes, returning its output
// image references.
func (c *ComfyUI) await(ctx context.Context, promptID string) ([]comfyImageRef, error) {
	interval := c.PollInterval
	if interval <= 0 {
		interval = 750 * time.Millisecond
	}
	timeout := c.PollTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		raw, status, err := c.do(ctx, http.MethodGet, "/history/"+url.PathEscape(promptID), nil, "")
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			var hist map[string]struct {
				Outputs map[string]struct {
					Images []comfyImageRef `json:"images"`
				} `json:"outputs"`
			}
			if err := json.Unmarshal(raw, &hist); err != nil {
				return nil, fmt.Errorf("%s: decode history: %w", c.Name, err)
			}
			if entry, ok := hist[promptID]; ok {
				var refs []comfyImageRef
				for _, out := range entry.Outputs {
					refs = append(refs, out.Images...)
				}
				return refs, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: workflow did not complete within %s", c.Name, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// view fetches one output image's bytes.
func (c *ComfyUI) view(ctx context.Context, ref comfyImageRef) ([]byte, error) {
	q := url.Values{"filename": {ref.Filename}, "subfolder": {ref.Subfolder}, "type": {ref.Type}}
	raw, status, err := c.do(ctx, http.MethodGet, "/view?"+q.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s: view %q HTTP %d", c.Name, ref.Filename, status)
	}
	return raw, nil
}

// do issues one request to the ComfyUI server and returns the body + status.
func (c *ComfyUI) do(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpClient(c.HTTP).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: request to %s: %w", c.Name, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	return raw, resp.StatusCode, nil
}
