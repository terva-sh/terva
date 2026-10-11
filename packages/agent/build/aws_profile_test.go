package build

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/auth"
	"terva.sh/terva/packages/provider"
)

func TestAWSProfileBedrockOptInPrecedenceAndDisable(t *testing.T) {
	home := clearCloudEnv(t)
	t.Setenv("AWS_PROFILE", "work")
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "[work]\naws_access_key_id=fixture-file-key\naws_secret_access_key=fixture-file-secret\n")
	if cfg := bedrockConfig("<aws>"); cfg.Credentials != nil || cfg.AccessKeyID != "fixture-file-key" {
		t.Fatal("static profile behavior changed without opt-in")
	}
	set := func(enabled bool) {
		t.Helper()
		if err := config.SaveConfig(config.Config{Providers: map[string]config.ProviderSettings{"amazon-bedrock": {AWSCLICredentials: enabled}}}); err != nil {
			t.Fatal(err)
		}
	}
	set(true)
	cfg := bedrockConfig("<aws>")
	if cfg.Credentials == nil || cfg.AccessKeyID != "" {
		t.Fatal("opt-in did not delegate the full named profile to the CLI")
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "fixture-bearer")
	if inference, discovery := bedrockConfig(""), BedrockDiscoveryConfig(); inference.BearerToken != "fixture-bearer" || discovery.BearerToken != "" || discovery.Credentials == nil || BedrockDiscoveryScope() != "cli:work" {
		t.Fatal("bearer inference prevented independent profile signing for discovery")
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-env-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-env-secret")
	if cfg := bedrockConfig("<aws>"); cfg.Credentials != nil || cfg.AccessKeyID != "fixture-env-key" {
		t.Fatal("environment signing keys lost precedence")
	}
	if discovery := BedrockDiscoveryConfig(); discovery.Credentials != nil || discovery.AccessKeyID != "fixture-env-key" || BedrockDiscoveryScope() != "environment" {
		t.Fatal("discovery ignored environment signing precedence")
	}
	if cfg := bedrockConfig("fixture-bearer"); cfg.Credentials != nil || cfg.BearerToken != "fixture-bearer" {
		t.Fatal("bearer credentials lost precedence")
	}
	set(false)
	if _, err := cfg.Credentials(context.Background()); err == nil {
		t.Fatal("a retained source executed after the user disabled it")
	}
}

func TestAWSProfileOptInRequiresNamedProfile(t *testing.T) {
	clearCloudEnv(t)
	if err := config.SaveConfig(config.Config{Providers: map[string]config.ProviderSettings{"amazon-bedrock": {AWSCLICredentials: true}}}); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"", "   "} {
		t.Setenv("AWS_PROFILE", profile)
		if _, _, err := ResolveCredential("amazon-bedrock", ""); err == nil {
			t.Fatal("opt-in alone advertised a usable credential")
		}
		if hasCredential("amazon-bedrock", auth.Credentials{}, nil) {
			t.Fatal("autopanel admitted Bedrock without a selected profile")
		}
		for _, cfg := range []provider.BedrockConfig{bedrockConfig("<aws>"), BedrockDiscoveryConfig()} {
			if cfg.HasSigningCredentials() || !strings.Contains(cfg.Hint, "explicit AWS_PROFILE") {
				t.Fatal("missing profile installed a source or lost its configuration remedy")
			}
		}
	}
	t.Setenv("AWS_PROFILE", "work")
	if _, _, err := ResolveCredential("amazon-bedrock", ""); err != nil || !hasCredential("amazon-bedrock", auth.Credentials{}, nil) || BedrockDiscoveryConfig().Credentials == nil {
		t.Fatal("positive control: a selected profile did not activate the adapter")
	}
}
