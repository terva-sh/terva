package provider

import "fmt"

// The four providers whose credentials or endpoint are not a single API key
// take them as a Config value. The wire reads no environment variable and no
// credentials file to fill one (decision 0021, rule 5): terva's harness does
// that in packages/agent/build, and any other host fills the struct from
// wherever it keeps them.
//
// Each Config's Hint is what the host wants the user told when the values
// cannot authenticate or address the provider. terva words it with its own
// environment variables. An empty Hint gets a generic message that names the
// Config's fields.
//
// Secret fields are excluded from JSON, and String and GoString redact them,
// so no formatting verb prints one.

// redacted stands in for a secret value that is set. An unset one prints as
// empty, since whether a secret is present is what a reader debugging a
// configuration needs to know.
func redacted(s string) string {
	if s == "" {
		return ""
	}
	return "<redacted>"
}

// BedrockConfig is what an Amazon Bedrock client needs. A BearerToken is used
// when set; otherwise AccessKeyID and SecretAccessKey (with an optional
// SessionToken) sign each request with SigV4.
//
// Unstable: a vendor's configuration carries no promise before 1.0.
type BedrockConfig struct {
	// Region is the AWS region. Empty means us-east-1.
	Region string

	BearerToken     string `json:"-"`
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
	SessionToken    string `json:"-"`

	Hint string
}

func (c BedrockConfig) String() string {
	return fmt.Sprintf("BedrockConfig{Region:%q BearerToken:%q AccessKeyID:%q SecretAccessKey:%q SessionToken:%q Hint:%q}",
		c.Region, redacted(c.BearerToken), redacted(c.AccessKeyID), redacted(c.SecretAccessKey), redacted(c.SessionToken), c.Hint)
}

func (c BedrockConfig) GoString() string { return "provider." + c.String() }

// AzureOpenAIConfig is what an Azure OpenAI client needs besides its key.
//
// Unstable: a vendor's configuration carries no promise before 1.0.
type AzureOpenAIConfig struct {
	// BaseURL is the resource's endpoint, used when the constructor's baseURL
	// argument is empty.
	BaseURL string
	// APIVersion is the api-version query parameter. Empty means the compiled
	// default.
	APIVersion string

	Hint string
}

// VertexConfig is what a Google Vertex AI client needs. An APIKey is used
// when set; otherwise CredentialsJSON, a service-account key or an
// authorized_user file as gcloud writes it, mints OAuth tokens.
//
// Unstable: a vendor's configuration carries no promise before 1.0.
type VertexConfig struct {
	Project string
	// Location is the Vertex region. Empty means us-central1.
	Location string

	APIKey          string `json:"-"`
	CredentialsJSON []byte `json:"-"`

	Hint string
}

func (c VertexConfig) String() string {
	creds := ""
	if len(c.CredentialsJSON) > 0 {
		creds = "<redacted>"
	}
	return fmt.Sprintf("VertexConfig{Project:%q Location:%q APIKey:%q CredentialsJSON:%q Hint:%q}",
		c.Project, c.Location, redacted(c.APIKey), creds, c.Hint)
}

func (c VertexConfig) GoString() string { return "provider." + c.String() }

// CloudflareConfig fills the account and gateway placeholders in the
// Cloudflare endpoints ({CLOUDFLARE_ACCOUNT_ID} and {CLOUDFLARE_GATEWAY_ID}).
// Neither is a secret; the API key is passed on its own.
//
// It has a hint per ID rather than one Hint, because which ID is missing
// depends on the URL: a custom base URL may carry only one placeholder, and
// the message must name the one it needs.
//
// Unstable: a vendor's configuration carries no promise before 1.0.
type CloudflareConfig struct {
	AccountID string
	GatewayID string

	AccountIDHint string
	GatewayIDHint string
}
