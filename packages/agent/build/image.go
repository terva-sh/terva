package build

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/imagegen"
)

const (
	protocolOpenAIImages    = "openai-images"
	protocolA1111           = "a1111"
	protocolComfyUI         = "comfyui"
	defaultOpenAIImageBase  = "https://api.openai.com/v1"
	defaultOpenAIImageModel = "gpt-image-1"
	defaultImageSize        = "1024x1024"
)

// buildImageRegistry builds the image-backend registry from config. It is
// opt-in: no `image` block (or enabled=false) yields an empty registry, so the
// generate_image tool is never registered. When the block is enabled but names
// no backends, it opportunistically uses a present OpenAI API credential. The
// returned string is a one-line status for the startup log.
func buildImageRegistry(cfg config.Config) (*imagegen.Registry, string) {
	reg := imagegen.NewRegistry()
	ic := cfg.Image
	if ic == nil {
		return reg, ""
	}
	if ic.Enabled != nil && !*ic.Enabled {
		return reg, "image generation disabled (image.enabled=false)"
	}
	for id, bc := range ic.Backends {
		b, err := buildImageBackend(id, bc)
		if err != nil {
			return imagegen.NewRegistry(), fmt.Sprintf("image backend %q: %v — image generation off", id, err)
		}
		reg.Add(b)
	}
	// Opportunistic: enabled with no explicit backend → use a present OpenAI
	// API credential against the images endpoint, if one exists.
	if reg.Len() == 0 {
		if cred, _, _, err := ResolveCredentialFull("openai", ""); err == nil && cred != "" {
			reg.Add(&imagegen.OpenAIImages{
				Name: protocolOpenAIImages, BaseURL: defaultOpenAIImageBase,
				APIKey: cred, Model: defaultOpenAIImageModel, DefaultSize: defaultImageSize,
			})
		}
	}
	if reg.Len() == 0 {
		return reg, "image generation enabled but no backend resolved — configure image.backends or set OPENAI_API_KEY"
	}
	reg.SetDefault(ic.Backend)
	return reg, fmt.Sprintf("image generation on (%d backend(s): %v)", reg.Len(), reg.IDs())
}

// buildImageBackend constructs one backend from its config, defaulting the
// protocol and the OpenAI base/model.
func buildImageBackend(id string, bc config.ImageBackendConfig) (imagegen.Backend, error) {
	// The key is opened at the moment the backend is constructed, not when the
	// config loads — the same point-of-use rule the extension delivery path
	// follows. An unopenable key fails the backend (and the caller turns image
	// generation off with a named reason) rather than sending ciphertext as a
	// bearer token and surfacing it as a 401 from the provider.
	key, err := config.DecryptFieldValue(config.ImageBackendKeyPath(id), bc.APIKey)
	if err != nil {
		return nil, err
	}
	bc.APIKey = key
	proto := bc.Protocol
	if proto == "" {
		proto = protocolOpenAIImages
	}
	switch proto {
	case protocolOpenAIImages:
		base := bc.BaseURL
		if base == "" {
			base = defaultOpenAIImageBase
		}
		model := bc.Model
		if model == "" {
			model = defaultOpenAIImageModel
		}
		return &imagegen.OpenAIImages{
			Name: id, BaseURL: base, APIKey: bc.APIKey, Model: model,
			DefaultSize: bc.Size, NegativePipe: bc.NegativePipe,
		}, nil
	case protocolA1111:
		if bc.BaseURL == "" {
			return nil, fmt.Errorf("a1111 backend needs base_url (e.g. http://localhost:7860)")
		}
		return &imagegen.A1111{
			Name: id, BaseURL: bc.BaseURL, APIKey: bc.APIKey, Model: bc.Model,
			DefaultSize: bc.Size, Steps: bc.Steps, Sampler: bc.Sampler, CFGScale: bc.CFGScale,
		}, nil
	case protocolComfyUI:
		if bc.BaseURL == "" {
			return nil, fmt.Errorf("comfyui backend needs base_url (e.g. http://localhost:8188)")
		}
		wf := bc.Workflow
		if wf == "" && bc.WorkflowFile != "" {
			data, rerr := os.ReadFile(bc.WorkflowFile)
			if rerr != nil {
				return nil, fmt.Errorf("comfyui workflow_file: %w", rerr)
			}
			wf = string(data)
		}
		if strings.TrimSpace(wf) == "" {
			return nil, fmt.Errorf("comfyui backend needs a workflow (workflow or workflow_file) with {{prompt}} placeholders")
		}
		return &imagegen.ComfyUI{Name: id, BaseURL: bc.BaseURL, Workflow: wf}, nil
	default:
		return nil, fmt.Errorf("unknown protocol %q (supported: %s, %s, %s)", proto, protocolOpenAIImages, protocolA1111, protocolComfyUI)
	}
}
