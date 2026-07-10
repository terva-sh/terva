package config

// The image-generation config schema. The backends these describe are built
// in packages/agent/image.go, which needs imagegen; the schema itself does not.

// ImageConfig configures image generation (the generate_image tool) via a
// registry of backends kept separate from the model catalog. Opt-in: with no
// `image` block the tool is absent. See docs/proposals/image-generation.md.
type ImageConfig struct {
	// Enabled is the master switch. A present block with nil or true is on;
	// false disables even when backends are configured.
	Enabled *bool `json:"enabled,omitempty"`
	// Backend names the default backend the tool uses when a call omits one.
	Backend string `json:"backend,omitempty"`
	// Backends are the named image backends, keyed by id.
	Backends map[string]ImageBackendConfig `json:"backends,omitempty"`
}

// ImageBackendConfig is one image backend. Protocol selects the adapter; phase
// one ships "openai-images" (hosted OpenAI/Azure, self-hosted LocalAI, or any
// OpenAI-compatible endpoint).
type ImageBackendConfig struct {
	Protocol     string `json:"protocol,omitempty"` // "openai-images" (default) or "a1111"
	BaseURL      string `json:"base_url,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	Model        string `json:"model,omitempty"`
	Size         string `json:"size,omitempty"`          // default size, e.g. 1024x1024
	NegativePipe bool   `json:"negative_pipe,omitempty"` // openai-images: LocalAI "prompt|negative"

	// a1111-only sampling defaults (AUTOMATIC1111/Forge /sdapi/v1/txt2img);
	// zero values let the server pick.
	Steps    int     `json:"steps,omitempty"`
	Sampler  string  `json:"sampler,omitempty"`
	CFGScale float64 `json:"cfg_scale,omitempty"`

	// comfyui-only: the API-format workflow JSON (inline, or a path via
	// workflow_file), carrying {{prompt}}/{{negative}} placeholders.
	Workflow     string `json:"workflow,omitempty"`
	WorkflowFile string `json:"workflow_file,omitempty"`
}
