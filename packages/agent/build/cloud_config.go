package build

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/provider"
)

// The provider Configs terva fills from its environment and the credentials
// files the cloud CLIs write. The wire reads none of these itself (decision
// 0021, rule 5). The variables, the files, their order, and the messages a
// user sees when they are missing are the ones the wire used before
// TKT-01M35WJZY moved them here.
//
// These read os.Getenv, not envcompat: the names are the cloud vendors', not
// terva's, and have no TERVA_ spelling.

// bedrockConfig resolves Bedrock's credentials, first match winning:
//
//  1. credential, when it is a real token and not the "<aws>" placeholder
//     ResolveCredentialFull returns for the AWS chain: the bearer route.
//  2. AWS_BEARER_TOKEN_BEDROCK: the bearer route.
//  3. AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (+ optional
//     AWS_SESSION_TOKEN): SigV4.
//  4. AWS_PROFILE: that profile's keys from ~/.aws/credentials, SigV4.
//
// The region is AWS_REGION, then AWS_DEFAULT_REGION; the wire defaults it.
func bedrockConfig(credential string) provider.BedrockConfig {
	cfg := provider.BedrockConfig{
		Region: os.Getenv("AWS_REGION"),
		Hint:   "no Bedrock credentials found (set AWS_BEARER_TOKEN_BEDROCK, AWS_ACCESS_KEY_ID+AWS_SECRET_ACCESS_KEY, or AWS_PROFILE)",
	}
	if cfg.Region == "" {
		cfg.Region = os.Getenv("AWS_DEFAULT_REGION")
	}
	token := credential
	if token == "" || token == "<aws>" {
		token = os.Getenv("AWS_BEARER_TOKEN_BEDROCK")
	}
	if token != "" && token != "<aws>" {
		cfg.BearerToken = token
		return cfg
	}
	if ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"); ak != "" && sk != "" {
		cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken = ak, sk, os.Getenv("AWS_SESSION_TOKEN")
		return cfg
	}
	if profile := os.Getenv("AWS_PROFILE"); profile != "" {
		if keys, err := readAWSCredentialsFile(profile); err == nil {
			cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken = keys.accessKeyID, keys.secretAccessKey, keys.sessionToken
		}
	}
	return cfg
}

// awsKeys is one profile's keys from ~/.aws/credentials.
type awsKeys struct {
	accessKeyID, secretAccessKey, sessionToken string
}

// readAWSCredentialsFile parses ~/.aws/credentials and returns the
// access-key/secret-key (and optional session-token) for the named
// profile. The file is an INI-like format with `[profile]` headers. An error
// names the profile and never a value.
func readAWSCredentialsFile(profile string) (awsKeys, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return awsKeys{}, err
	}
	b, err := os.ReadFile(home + "/.aws/credentials")
	if err != nil {
		return awsKeys{}, err
	}
	var current string
	creds := map[string]map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			creds[current] = map[string]string{}
			continue
		}
		if current == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		creds[current][strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
	}
	p, ok := creds[profile]
	if !ok {
		return awsKeys{}, fmt.Errorf("aws profile %q not found in ~/.aws/credentials", profile)
	}
	ak, sk := p["aws_access_key_id"], p["aws_secret_access_key"]
	if ak == "" || sk == "" {
		return awsKeys{}, fmt.Errorf("aws profile %q missing aws_access_key_id or aws_secret_access_key", profile)
	}
	return awsKeys{accessKeyID: ak, secretAccessKey: sk, sessionToken: p["aws_session_token"]}, nil
}

// azureOpenAIConfig resolves the Azure endpoint for a client given no base URL:
// AZURE_OPENAI_BASE_URL, else AZURE_OPENAI_RESOURCE_NAME expanded to its
// default host. The api-version is AZURE_OPENAI_API_VERSION.
func azureOpenAIConfig() provider.AzureOpenAIConfig {
	cfg := provider.AzureOpenAIConfig{
		BaseURL:    os.Getenv("AZURE_OPENAI_BASE_URL"),
		APIVersion: os.Getenv("AZURE_OPENAI_API_VERSION"),
		Hint:       "set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME (or pass --base-url)",
	}
	if cfg.BaseURL == "" {
		if rn := os.Getenv("AZURE_OPENAI_RESOURCE_NAME"); rn != "" {
			cfg.BaseURL = "https://" + rn + ".openai.azure.com"
		}
	}
	return cfg
}

// vertexConfig resolves Vertex's project, location, and auth:
// GOOGLE_CLOUD_PROJECT and GOOGLE_CLOUD_LOCATION, then GOOGLE_CLOUD_API_KEY,
// else the credentials file at GOOGLE_APPLICATION_CREDENTIALS, else gcloud's
// application-default file when it exists (what `gcloud auth
// application-default login` writes). The file is read only when a project is
// set and no API key is. Hint carries the first thing that failed, in the
// words the wire used when it did this itself.
func vertexConfig() provider.VertexConfig {
	cfg := provider.VertexConfig{
		Project:  os.Getenv("GOOGLE_CLOUD_PROJECT"),
		Location: os.Getenv("GOOGLE_CLOUD_LOCATION"),
		APIKey:   os.Getenv("GOOGLE_CLOUD_API_KEY"),
	}
	if cfg.Project == "" {
		cfg.Hint = "vertex: GOOGLE_CLOUD_PROJECT not set"
		return cfg
	}
	if cfg.APIKey != "" {
		return cfg
	}
	credPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := home + "/.config/gcloud/application_default_credentials.json"
			if _, err := os.Stat(candidate); err == nil {
				credPath = candidate
			}
		}
	}
	if credPath == "" {
		cfg.Hint = "vertex: no auth — set GOOGLE_CLOUD_API_KEY or GOOGLE_APPLICATION_CREDENTIALS"
		return cfg
	}
	b, err := os.ReadFile(credPath)
	if err != nil {
		cfg.Hint = fmt.Sprintf("vertex: read credentials %q: %v", credPath, err)
		return cfg
	}
	cfg.CredentialsJSON = b
	return cfg
}

// cloudflareConfig resolves the account and gateway IDs the Cloudflare
// endpoints' placeholders need: CLOUDFLARE_ACCOUNT_ID and
// CLOUDFLARE_GATEWAY_ID. Each has its own hint, and the wire reports the one
// for the first placeholder the URL carries and the machine left unset.
func cloudflareConfig() provider.CloudflareConfig {
	return provider.CloudflareConfig{
		AccountID:     os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		GatewayID:     os.Getenv("CLOUDFLARE_GATEWAY_ID"),
		AccountIDHint: "CLOUDFLARE_ACCOUNT_ID is required but not set in env",
		GatewayIDHint: "CLOUDFLARE_GATEWAY_ID is required but not set in env",
	}
}
