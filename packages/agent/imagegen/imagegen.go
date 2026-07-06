// Package imagegen turns text prompts into images through a registry of
// swappable backends — hosted (OpenAI/Azure/…) or self-hosted (LocalAI,
// AUTOMATIC1111, ComfyUI) — kept separate from the model catalog. It is the
// substrate for the generate_image tool; the design and phased plan live in
// docs/proposals/image-generation.md.
//
// There is no single self-host standard for image generation (unlike
// OpenAI-compatible chat), so a Backend is one implementation per wire protocol
// and the Registry holds the operator's configured instances.
package imagegen

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Image is one generated image: decoded bytes plus its sniffed MIME type. It
// maps directly onto a provider.ImageBlock in a tool result.
type Image struct {
	Data     []byte
	MimeType string
}

// Request is a backend-agnostic generation request. A backend maps it onto its
// own wire format and ignores fields it doesn't support (e.g. OpenAI's images
// endpoint has no negative prompt).
type Request struct {
	Prompt         string
	NegativePrompt string // a1111/comfyui; LocalAI folds it via a "prompt|negative" pipe
	Size           string // "1024x1024"; "" lets the backend's configured default apply
	N              int    // candidate count; <=0 means 1
}

// Result is what a backend returns: the images plus metadata for the tool's
// caption/usage line.
type Result struct {
	Images  []Image
	Backend string // registry id that produced them
	Model   string // model/checkpoint used, when known
}

// Backend turns a Request into one or more images. One implementation per
// protocol (openai-images, a1111, comfyui, …).
type Backend interface {
	ID() string
	Generate(ctx context.Context, req Request) (Result, error)
}

// Registry holds the configured backends and the default selection. It is
// built once at startup from config (+ opportunistic population from present
// provider credentials) and consulted by the generate_image tool.
type Registry struct {
	backends map[string]Backend
	def      string // explicit default id ("" = none configured)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{backends: map[string]Backend{}} }

// Add registers a backend under its ID (later Add of the same id wins).
func (r *Registry) Add(b Backend) { r.backends[b.ID()] = b }

// SetDefault names the backend Resolve("") returns. A no-op for an unknown id.
func (r *Registry) SetDefault(id string) {
	if _, ok := r.backends[id]; ok {
		r.def = id
	}
}

// Len reports how many backends are configured.
func (r *Registry) Len() int { return len(r.backends) }

// IDs returns the configured backend ids, sorted.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.backends))
	for id := range r.backends {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Resolve returns the backend to use: the explicit id when given, else the
// configured default, else the sole backend when exactly one is registered.
// The error explains what to configure so the tool can surface it to the model.
func (r *Registry) Resolve(id string) (Backend, error) {
	if id != "" {
		b, ok := r.backends[id]
		if !ok {
			return nil, fmt.Errorf("unknown image backend %q (configured: %v)", id, r.IDs())
		}
		return b, nil
	}
	if r.def != "" {
		return r.backends[r.def], nil
	}
	if len(r.backends) == 1 {
		for _, b := range r.backends {
			return b, nil
		}
	}
	if len(r.backends) == 0 {
		return nil, fmt.Errorf("no image backend configured")
	}
	return nil, fmt.Errorf("no default image backend; pass one of %v", r.IDs())
}

// httpClient returns c when non-nil, else http.DefaultClient — a small helper
// so backends can leave the field unset in tests and production alike.
func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

// parseSize splits a "WIDTHxHEIGHT" string (e.g. "1024x768") into pixels.
// Returns ok=false for empty or malformed input so a backend can fall back to
// its own default. The separator is 'x' or 'X'.
func parseSize(s string) (w, h int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	sep := strings.IndexAny(s, "xX")
	if sep <= 0 || sep == len(s)-1 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(s[:sep]))
	h, err2 := strconv.Atoi(strings.TrimSpace(s[sep+1:]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
