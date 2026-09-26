package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// clearCloudEnv unsets every variable cloud_config.go reads and points the home
// directory at an empty one, so each test states exactly what the machine
// holds. os.UserHomeDir reads USERPROFILE on Windows, so both are set.
func clearCloudEnv(t *testing.T) string {
	t.Helper()
	for _, k := range []string{
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
		"AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_RESOURCE_NAME", "AZURE_OPENAI_API_VERSION",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID",
	} {
		t.Setenv(k, "")
	}
	home := testsupport.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Bedrock's order, first match winning: a real credential, the bearer
// variable, the key pair, then the profile file. The region falls back from
// AWS_REGION to AWS_DEFAULT_REGION.
func TestBedrockConfigResolvesInTheOldOrder(t *testing.T) {
	home := clearCloudEnv(t)
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "# comment\n[default]\naws_access_key_id = FILEAK\naws_secret_access_key = FILESK\n\n[work]\naws_access_key_id=WORKAK\naws_secret_access_key=WORKSK\naws_session_token=WORKST\n")

	if cfg := bedrockConfig("<aws>"); cfg.BearerToken != "" || cfg.AccessKeyID != "" {
		t.Errorf("nothing set, yet resolved %v", cfg)
	}
	t.Setenv("AWS_PROFILE", "work")
	if cfg := bedrockConfig("<aws>"); cfg.AccessKeyID != "WORKAK" || cfg.SecretAccessKey != "WORKSK" || cfg.SessionToken != "WORKST" {
		t.Errorf("profile: %v", cfg)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "ENVAK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ENVSK")
	if cfg := bedrockConfig("<aws>"); cfg.AccessKeyID != "ENVAK" || cfg.SessionToken != "" {
		t.Errorf("the key pair should beat the profile: %v", cfg)
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "ENVTOKEN")
	if cfg := bedrockConfig("<aws>"); cfg.BearerToken != "ENVTOKEN" || cfg.AccessKeyID != "" {
		t.Errorf("the bearer variable should beat the keys: %v", cfg)
	}
	if cfg := bedrockConfig("stored-token"); cfg.BearerToken != "stored-token" {
		t.Errorf("a real credential should beat the variable: %v", cfg)
	}

	if cfg := bedrockConfig(""); cfg.Region != "" {
		t.Errorf("region %q with neither variable set; the wire defaults it", cfg.Region)
	}
	t.Setenv("AWS_DEFAULT_REGION", "us-west-2")
	if cfg := bedrockConfig(""); cfg.Region != "us-west-2" {
		t.Errorf("region = %q", cfg.Region)
	}
	t.Setenv("AWS_REGION", "eu-central-1")
	if cfg := bedrockConfig(""); cfg.Region != "eu-central-1" {
		t.Errorf("region = %q", cfg.Region)
	}
}

// A missing or incomplete profile is not a credential, and its error names the
// profile but never a value.
func TestReadAWSCredentialsFileRefusesIncompleteProfiles(t *testing.T) {
	home := clearCloudEnv(t)
	if _, err := readAWSCredentialsFile("default"); err == nil {
		t.Error("a missing file read as a profile")
	}
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "[half]\naws_access_key_id = SECRETAK\n")
	for _, p := range []string{"half", "absent"} {
		_, err := readAWSCredentialsFile(p)
		if err == nil || !strings.Contains(err.Error(), p) || strings.Contains(err.Error(), "SECRETAK") {
			t.Errorf("profile %q: err = %v", p, err)
		}
	}
}

func TestAzureConfigResolvesTheEndpoint(t *testing.T) {
	clearCloudEnv(t)
	if cfg := azureOpenAIConfig(); cfg.BaseURL != "" || !strings.Contains(cfg.Hint, "AZURE_OPENAI_BASE_URL") {
		t.Errorf("nothing set: %+v", cfg)
	}
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "res")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2025-01-01")
	if cfg := azureOpenAIConfig(); cfg.BaseURL != "https://res.openai.azure.com" || cfg.APIVersion != "2025-01-01" {
		t.Errorf("resource name: %+v", cfg)
	}
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://explicit.example")
	if cfg := azureOpenAIConfig(); cfg.BaseURL != "https://explicit.example" {
		t.Errorf("the base URL should beat the resource name: %+v", cfg)
	}
}

// Vertex's hints are the wire's old messages, each for the first thing that
// failed, and the credentials file is read only when it is needed.
func TestVertexConfigResolvesAuthAndSaysWhatIsMissing(t *testing.T) {
	home := clearCloudEnv(t)
	if cfg := vertexConfig(); cfg.Hint != "vertex: GOOGLE_CLOUD_PROJECT not set" {
		t.Errorf("no project: %v", cfg)
	}
	t.Setenv("GOOGLE_CLOUD_PROJECT", "proj")
	if cfg := vertexConfig(); cfg.Hint != "vertex: no auth — set GOOGLE_CLOUD_API_KEY or GOOGLE_APPLICATION_CREDENTIALS" {
		t.Errorf("no auth: %v", cfg)
	}

	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	writeFile(t, adc, `{"type":"authorized_user"}`)
	if cfg := vertexConfig(); string(cfg.CredentialsJSON) != `{"type":"authorized_user"}` || cfg.Hint != "" {
		t.Errorf("gcloud's default file: %v", cfg)
	}

	explicit := filepath.Join(home, "sa.json")
	writeFile(t, explicit, `{"type":"service_account"}`)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", explicit)
	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-east1")
	if cfg := vertexConfig(); string(cfg.CredentialsJSON) != `{"type":"service_account"}` || cfg.Location != "asia-east1" {
		t.Errorf("the named file should beat gcloud's: %v", cfg)
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(home, "missing.json"))
	// The hint quotes the path with %q, which doubles a Windows backslash.
	if cfg := vertexConfig(); !strings.HasPrefix(cfg.Hint, fmt.Sprintf("vertex: read credentials %q: ", filepath.Join(home, "missing.json"))) {
		t.Errorf("unreadable file: %v", cfg)
	}

	t.Setenv("GOOGLE_CLOUD_API_KEY", "key")
	if cfg := vertexConfig(); cfg.APIKey != "key" || cfg.CredentialsJSON != nil || cfg.Hint != "" {
		t.Errorf("an API key should need no file: %v", cfg)
	}
}

func TestCloudflareConfigReadsBothIDs(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	t.Setenv("CLOUDFLARE_GATEWAY_ID", "gw")
	if cfg := cloudflareConfig(); cfg.AccountID != "acct" || cfg.GatewayID != "gw" {
		t.Errorf("config = %+v", cfg)
	}
}

// The message names the placeholder the URL actually needs. A custom gateway
// URL with only {CLOUDFLARE_GATEWAY_ID} must not be told about the account ID,
// which is what one hint for both got wrong (review of #1346).
func TestCloudflareNamesTheIDTheURLNeeds(t *testing.T) {
	clearCloudEnv(t)
	for _, c := range []struct{ baseURL, want string }{
		{"", "CLOUDFLARE_ACCOUNT_ID is required but not set in env"},
		{"https://gw.example/{CLOUDFLARE_GATEWAY_ID}/compat", "CLOUDFLARE_GATEWAY_ID is required but not set in env"},
	} {
		_, err := provider.NewCloudflareAIGateway("k", c.baseURL, cloudflareConfig()).Stream(context.Background(), provider.Request{Model: "m"})
		if err == nil || !strings.HasSuffix(err.Error(), ": "+c.want) {
			t.Errorf("base URL %q: err = %v, want it to end with %q", c.baseURL, err, c.want)
		}
	}
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	_, err := provider.NewCloudflareAIGateway("k", "", cloudflareConfig()).Stream(context.Background(), provider.Request{Model: "m"})
	if err == nil || !strings.HasSuffix(err.Error(), ": CLOUDFLARE_GATEWAY_ID is required but not set in env") {
		t.Errorf("account set, gateway not: err = %v", err)
	}
}

// Through terva's own registry rows, a machine with nothing configured gets
// the same messages it did when the wire read the environment itself.
func TestTervasCloudRowsReportMissingValuesAsBefore(t *testing.T) {
	clearCloudEnv(t)
	for id, want := range map[string]string{
		"amazon-bedrock":         "no Bedrock credentials found (set AWS_BEARER_TOKEN_BEDROCK, AWS_ACCESS_KEY_ID+AWS_SECRET_ACCESS_KEY, or AWS_PROFILE)",
		"google-vertex":          "vertex: GOOGLE_CLOUD_PROJECT not set",
		"azure-openai-responses": "set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME (or pass --base-url)",
		"cloudflare-workers-ai":  "CLOUDFLARE_ACCOUNT_ID is required but not set in env",
		"cloudflare-ai-gateway":  "CLOUDFLARE_ACCOUNT_ID is required but not set in env",
	} {
		spec, ok := specFor(id)
		if !ok {
			t.Fatalf("no provider %s", id)
		}
		_, err := spec.newClient(clientConfig{Provider: id, Credential: "<aws>"}).Stream(context.Background(), provider.Request{Model: "m"})
		if err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
			t.Errorf("%s: err = %v, want it to end with %q", id, err, want)
		}
	}
}
