package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DiscoverLocalCompatible reads native metadata for an explicitly selected local server.
// Unsupported or unavailable native APIs fall back to the compatible model list.
//
// Unstable: per-vendor model discovery carries no promise before 1.0.
func DiscoverLocalCompatible(ctx context.Context, baseURL, key, kind string, defaultCtx int, known []Model) ([]Model, error) {
	root := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
	kind = strings.ToLower(strings.TrimSpace(kind))
	nativeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	var rows []Model
	var err error
	switch kind {
	case "lmstudio":
		rows, err = discoverLMStudio(nativeCtx, root, baseURL, key, defaultCtx, known)
	case "llamacpp":
		rows, err = discoverLlamaCPP(nativeCtx, root, baseURL, key, defaultCtx, known)
	default:
		err = fmt.Errorf("unsupported native discovery")
	}
	cancel()
	if err == nil {
		return rows, nil
	}
	rows, err = DiscoverOpenAICompatible(ctx, baseURL, key, defaultCtx, known)
	if err == nil && kind == "llamacpp" && len(rows) == 1 {
		propsCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		if window := llamaCPPContext(propsCtx, root, key, rows[0].ID); window > 0 {
			rows[0] = localDiscoveredModel(rows[0].ID, rows[0].DisplayName, baseURL, window, defaultCtx, known)
		}
	}
	return rows, err
}

func llamaCPPContext(ctx context.Context, root, key, id string) int {
	var props struct {
		ModelPath string `json:"model_path"`
		Sleeping  bool   `json:"is_sleeping"`
		Defaults  struct {
			Context int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	// Routers can autoload a model on GET. The query selects an existing model only.
	query := url.Values{"model": {id}, "autoload": {"false"}}
	if localDiscoveryGET(ctx, root+"/props?"+query.Encode(), key, &props) != nil || props.ModelPath == "" || props.Sleeping {
		return 0
	}
	return props.Defaults.Context
}

func localDiscoveryGET(ctx context.Context, endpoint, key string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid native discovery URL")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("native discovery request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("native discovery HTTP %d", resp.StatusCode)
	}
	body, truncated := readBodyCapped(resp.Body, maxDiscoveryBodyBytes)
	if truncated {
		return fmt.Errorf("native discovery response exceeds limit or cannot be read")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid native discovery response")
	}
	return nil
}

func localDiscoveredModel(id, name, baseURL string, window, defaultCtx int, known []Model) Model {
	if name == "" {
		name = id
	}
	if window <= 0 {
		window, _ = localKnownLimits(id, known)
		if window <= 0 {
			window = defaultCtx
		}
	}
	_, output := localKnownLimits(id, known)
	if window > 0 && output > window {
		output = window
	}
	caps := openAICompatCaps(known, id)
	return Model{Provider: OpenAICompatProvider, ID: id, DisplayName: name,
		ContextWindow: window, MaxOutput: output, BaseURL: baseURL, Source: "live",
		Reasoning: caps.Reasoning, ReasoningEfforts: caps.ReasoningEfforts,
		Caps: mergeCaps(visionCapsFromID(id), map[Capability]bool{CapReasoning: caps.Reasoning})}
}

func localKnownLimits(id string, known []Model) (window, output int) {
	for _, m := range known {
		// Endpoint rows can contain yesterday's runtime limits.
		if m.ID != id || m.BaseURL != "" || m.Synthetic {
			continue
		}
		if m.ContextWindow > 0 && (window == 0 || m.ContextWindow < window) {
			window = m.ContextWindow
		}
		if m.MaxOutput > 0 && (output == 0 || m.MaxOutput < output) {
			output = m.MaxOutput
		}
	}
	return window, output
}

func discoverLMStudio(ctx context.Context, root, baseURL, key string, defaultCtx int, known []Model) ([]Model, error) {
	var page struct {
		Models []struct {
			Type   string `json:"type"`
			Key    string `json:"key"`
			Name   string `json:"display_name"`
			Loaded []struct {
				ID     string `json:"id"`
				Config struct {
					Context int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"models"`
	}
	if err := localDiscoveryGET(ctx, root+"/api/v1/models", key, &page); err != nil {
		return nil, err
	}
	if page.Models == nil {
		return nil, fmt.Errorf("native discovery response has no models")
	}
	out := []Model{}
	index := map[string]int{}
	for _, m := range page.Models {
		if m.Type != "llm" {
			continue
		}
		for _, instance := range m.Loaded {
			id := instance.ID
			if id == "" {
				id = m.Key
			}
			if strings.TrimSpace(id) == "" {
				continue
			}
			row := localDiscoveredModel(id, m.Name, baseURL, instance.Config.Context, defaultCtx, known)
			if i, ok := index[id]; ok {
				// Repeated aliases must fit the smallest active instance.
				out[i].ContextWindow = min(out[i].ContextWindow, row.ContextWindow)
				if out[i].MaxOutput > out[i].ContextWindow && out[i].ContextWindow > 0 {
					out[i].MaxOutput = out[i].ContextWindow
				}
				continue
			}
			index[id] = len(out)
			out = append(out, row)
		}
	}
	return out, nil
}

func discoverLlamaCPP(ctx context.Context, root, baseURL, key string, defaultCtx int, known []Model) ([]Model, error) {
	var page struct {
		Data []struct {
			ID     string `json:"id"`
			Status struct {
				Value string `json:"value"`
			} `json:"status"`
			Meta struct {
				Context int `json:"n_ctx"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := localDiscoveryGET(ctx, root+"/models", key, &page); err != nil {
		return nil, err
	}
	if page.Data == nil {
		return nil, fmt.Errorf("native discovery response has no data")
	}
	out := []Model{}
	missing := []int{}
	for _, m := range page.Data {
		if m.ID == "" || m.Status.Value == "" {
			return nil, fmt.Errorf("native discovery requires router metadata")
		}
		if m.Status.Value == "loaded" {
			if m.Meta.Context <= 0 && len(missing) < 16 {
				missing = append(missing, len(out))
			}
			out = append(out, localDiscoveredModel(m.ID, m.ID, baseURL, m.Meta.Context, defaultCtx, known))
		}
	}
	// Four bounded probes keep an unresponsive model from starving later rows.
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, i := range missing {
		limit <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-limit }()
			probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			defer cancel()
			if window := llamaCPPContext(probeCtx, root, key, out[i].ID); window > 0 {
				out[i] = localDiscoveredModel(out[i].ID, out[i].DisplayName, baseURL, window, defaultCtx, known)
			}
		}(i)
	}
	wg.Wait()
	return out, nil
}
