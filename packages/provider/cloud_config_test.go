package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// decoyCloudEnv sets every variable the wire used to read to a value no test
// expects, so a constructor that still looked at the environment would show it.
func decoyCloudEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
		"AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_RESOURCE_NAME", "AZURE_OPENAI_API_VERSION",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID",
	} {
		t.Setenv(k, "decoy-from-env")
	}
	t.Setenv("HOME", "")
}

func unavailableHint(t *testing.T, c Client) string {
	t.Helper()
	u, ok := c.(*unimplementedClient)
	if !ok {
		t.Fatalf("got a working %T, want the unavailable stub", c)
	}
	return u.hint
}

func TestBedrockIsBuiltFromValuesAlone(t *testing.T) {
	decoyCloudEnv(t)
	bearer := NewBedrock(BedrockConfig{Region: "eu-west-1", BearerToken: "tok"}, "").(*bedrockClient)
	if bearer.bearerToken != "tok" || bearer.sigv4 != nil || bearer.region != "eu-west-1" ||
		bearer.baseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("bearer client = %+v", bearer)
	}
	keys := NewBedrock(BedrockConfig{AccessKeyID: "ak", SecretAccessKey: "sk", SessionToken: "st"}, "").(*bedrockClient)
	if keys.bearerToken != "" || keys.sigv4 == nil || keys.sigv4.accessKeyID != "ak" || keys.sigv4.sessionToken != "st" || keys.region != "us-east-1" {
		t.Errorf("SigV4 client = %+v, %+v", keys, keys.sigv4)
	}
	if got := unavailableHint(t, NewBedrock(BedrockConfig{Hint: "host hint"}, "")); got != "host hint" {
		t.Errorf("hint = %q", got)
	}
	if got := unavailableHint(t, NewBedrock(BedrockConfig{AccessKeyID: "ak"}, "")); !strings.Contains(got, "BedrockConfig") {
		t.Errorf("generic hint = %q, want it to name the Config", got)
	}
}

func TestAzureIsBuiltFromValuesAlone(t *testing.T) {
	decoyCloudEnv(t)
	c := newAzureOpenAI("k", "", AzureOpenAIConfig{BaseURL: "https://r.openai.azure.com/openai/v1", APIVersion: "2025-01-01"}).(*openaiClient)
	if c.baseURL != "https://r.openai.azure.com" || c.http.Transport.(*azureRewriteTransport).apiVersion != "2025-01-01" {
		t.Errorf("client = %+v", c)
	}
	// The argument wins over the Config, as --base-url did over the environment.
	c = newAzureOpenAI("k", "https://arg.openai.azure.com", AzureOpenAIConfig{BaseURL: "https://cfg.openai.azure.com"}).(*openaiClient)
	if c.baseURL != "https://arg.openai.azure.com" || c.http.Transport.(*azureRewriteTransport).apiVersion != defaultAzureAPIVersion {
		t.Errorf("client = %+v", c)
	}
	if got := unavailableHint(t, newAzureOpenAI("k", "", AzureOpenAIConfig{Hint: "host hint"})); got != "host hint" {
		t.Errorf("hint = %q", got)
	}
}

func TestVertexIsBuiltFromValuesAlone(t *testing.T) {
	decoyCloudEnv(t)
	c := newVertex(VertexConfig{Project: "p", Location: "europe-west4", APIKey: "k"})
	g := c.(*renamedClient).inner.(*geminiClient)
	if g.baseURL != "https://europe-west4-aiplatform.googleapis.com" {
		t.Errorf("baseURL = %q", g.baseURL)
	}
	if got := unavailableHint(t, newVertex(VertexConfig{Hint: "host hint"})); got != "host hint" {
		t.Errorf("hint = %q", got)
	}
}

func TestCloudflareIsBuiltFromValuesAlone(t *testing.T) {
	decoyCloudEnv(t)
	w := NewCloudflareWorkersAI("k", "", CloudflareConfig{AccountID: "acct"})
	if got := fmt.Sprint(w.(interface{ Name() string }).Name()); got != "cloudflare-workers-ai" {
		t.Fatalf("Workers AI client = %T %s", w, got)
	}
	g := NewCloudflareAIGateway("k", "", CloudflareConfig{AccountID: "acct", GatewayID: "gw"}).(*openaiClient)
	if g.baseURL != "https://gateway.ai.cloudflare.com/v1/acct/gw/compat" {
		t.Errorf("gateway baseURL = %q", g.baseURL)
	}
	if got := unavailableHint(t, NewCloudflareAIGateway("k", "", CloudflareConfig{AccountID: "acct", AccountIDHint: "wrong one", GatewayIDHint: "host hint"})); got != "host hint" {
		t.Errorf("hint = %q", got)
	}
	if got := unavailableHint(t, NewCloudflareAIGateway("k", "", CloudflareConfig{AccountID: "acct"})); got != "CloudflareConfig.GatewayID is required but not set" {
		t.Errorf("generic hint = %q", got)
	}
	// A base URL with no placeholders needs no IDs.
	if _, ok := NewCloudflareWorkersAI("k", "https://example.invalid/v1", CloudflareConfig{}).(*unimplementedClient); ok {
		t.Error("a URL without placeholders was refused for want of IDs")
	}
}

// No formatting verb and no JSON encoding prints a secret a Config holds, so a
// Config that reaches a log line or an error by accident leaks nothing.
func TestConfigsNeverPrintTheirSecrets(t *testing.T) {
	const secret = "s3cr3t-value"
	values := []any{
		BedrockConfig{Region: "r", BearerToken: secret + "1", AccessKeyID: secret + "2", SecretAccessKey: secret + "3", SessionToken: secret + "4"},
		VertexConfig{Project: "p", APIKey: secret + "5", CredentialsJSON: []byte(`{"private_key":"` + secret + `6"}`)},
	}
	for _, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{
			fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v),
			fmt.Sprintf("%v", &v), fmt.Sprint([]any{v}), string(b),
		} {
			if strings.Contains(out, secret) {
				t.Errorf("%T printed a secret: %s", v, out)
			}
		}
	}
	// Present secrets say so; absent ones say nothing, which is what someone
	// debugging a configuration needs to see.
	if got := fmt.Sprint(BedrockConfig{BearerToken: "x"}); !strings.Contains(got, `BearerToken:"<redacted>"`) || !strings.Contains(got, `AccessKeyID:""`) {
		t.Errorf("redaction lost presence: %s", got)
	}
}
