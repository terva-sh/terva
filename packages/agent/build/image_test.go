package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
)

func TestBuildImageRegistryOptIn(t *testing.T) {
	// No image block → off (empty registry, no tool).
	if reg, _ := buildImageRegistry(config.Config{}); reg.Len() != 0 {
		t.Errorf("no image block should yield an empty registry, got %d", reg.Len())
	}

	// Explicit disable → off even with a backend configured.
	off := false
	cfg := config.Config{Image: &config.ImageConfig{
		Enabled:  &off,
		Backends: map[string]config.ImageBackendConfig{"a": {BaseURL: "http://x", Model: "m"}},
	}}
	if reg, _ := buildImageRegistry(cfg); reg.Len() != 0 {
		t.Errorf("enabled=false should disable, got %d", reg.Len())
	}
}

func TestBuildImageRegistryExplicitBackend(t *testing.T) {
	cfg := config.Config{Image: &config.ImageConfig{
		Backend: "openai",
		Backends: map[string]config.ImageBackendConfig{
			"openai": {Protocol: "openai-images", BaseURL: "http://x/v1", Model: "gpt-image-1"},
		},
	}}
	reg, status := buildImageRegistry(cfg)
	if reg.Len() != 1 {
		t.Fatalf("want 1 backend, got %d (%s)", reg.Len(), status)
	}
	b, err := reg.Resolve("") // the configured default
	if err != nil || b.ID() != "openai" {
		t.Errorf("default should resolve to openai: %v", err)
	}
}

func TestBuildImageRegistryA1111(t *testing.T) {
	cfg := config.Config{Image: &config.ImageConfig{
		Backends: map[string]config.ImageBackendConfig{
			"local": {Protocol: "a1111", BaseURL: "http://localhost:7860", Steps: 30},
		},
	}}
	reg, status := buildImageRegistry(cfg)
	if reg.Len() != 1 {
		t.Fatalf("want 1 a1111 backend, got %d (%s)", reg.Len(), status)
	}
	if b, err := reg.Resolve("local"); err != nil || b.ID() != "local" {
		t.Errorf("a1111 backend should resolve: %v", err)
	}

	// a1111 requires an explicit base_url (no hosted default).
	bad := config.Config{Image: &config.ImageConfig{Backends: map[string]config.ImageBackendConfig{"local": {Protocol: "a1111"}}}}
	if reg, _ := buildImageRegistry(bad); reg.Len() != 0 {
		t.Error("a1111 without base_url should fail closed")
	}
}

func TestBuildImageRegistryComfyUI(t *testing.T) {
	cfg := config.Config{Image: &config.ImageConfig{
		Backends: map[string]config.ImageBackendConfig{
			"comfy": {Protocol: "comfyui", BaseURL: "http://localhost:8188", Workflow: `{"6":{"inputs":{"text":"{{prompt}}"}}}`},
		},
	}}
	if reg, status := buildImageRegistry(cfg); reg.Len() != 1 {
		t.Fatalf("want 1 comfyui backend, got %d (%s)", reg.Len(), status)
	}
	// comfyui with no workflow (and no workflow_file) fails closed.
	bad := config.Config{Image: &config.ImageConfig{Backends: map[string]config.ImageBackendConfig{
		"comfy": {Protocol: "comfyui", BaseURL: "http://localhost:8188"},
	}}}
	if reg, _ := buildImageRegistry(bad); reg.Len() != 0 {
		t.Error("comfyui without a workflow should fail closed")
	}
}

func TestBuildImageRegistryUnknownProtocol(t *testing.T) {
	cfg := config.Config{Image: &config.ImageConfig{
		Backends: map[string]config.ImageBackendConfig{"a": {Protocol: "midjourney"}},
	}}
	reg, status := buildImageRegistry(cfg)
	if reg.Len() != 0 {
		t.Errorf("an unknown protocol should fail closed, got %d", reg.Len())
	}
	if status == "" {
		t.Error("want a status explaining the misconfiguration")
	}
}
