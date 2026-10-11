package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const maxBedrockProfilePages = 100

func validBedrockRegion(region string) bool {
	if region == "" || len(region) > 63 || region[0] == '-' || region[len(region)-1] == '-' {
		return false
	}
	for _, c := range region {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func bedrockEndpoint(service, region string) string {
	suffix := ".amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix += ".cn"
	}
	return "https://" + service + "." + region + suffix
}

type bedrockFoundationSummary struct {
	ID             string   `json:"modelId"`
	Name           string   `json:"modelName"`
	Input          []string `json:"inputModalities"`
	Output         []string `json:"outputModalities"`
	InferenceTypes []string `json:"inferenceTypesSupported"`
	Lifecycle      struct {
		Status string `json:"status"`
	} `json:"modelLifecycle"`
}

type bedrockProfileSummary struct {
	ID     string `json:"inferenceProfileId"`
	Name   string `json:"inferenceProfileName"`
	Status string `json:"status"`
	Models []struct {
		ARN string `json:"modelArn"`
	} `json:"models"`
}

// DiscoverBedrock lists text foundation models and active inference profiles.
// cfg must contain signing keys, even when inference uses a bearer token.
// baseURL overrides the control-plane endpoint; runtime URLs use cfg.Region.
// known supplies verified metadata. Unknown limits and prices stay zero.
// An error can accompany useful partial results; listing does not prove access.
//
// Unstable: per-vendor model discovery carries no promise before 1.0.
func DiscoverBedrock(ctx context.Context, cfg BedrockConfig, baseURL string, known ModelCatalog) ([]Model, error) {
	if !cfg.HasSigningCredentials() {
		return nil, errors.New("Bedrock discovery needs SigV4 keys; bearer tokens support inference only")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	if !validBedrockRegion(region) {
		return nil, errors.New("Bedrock discovery: invalid AWS region; use lowercase letters, digits and hyphens")
	}
	if baseURL == "" {
		baseURL = bedrockEndpoint("bedrock", region)
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	creds := &bedrockSigV4Creds{cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken}
	get := func(path, action string, query url.Values, dst any) error {
		u := strings.TrimRight(baseURL, "/") + path
		if len(query) != 0 {
			u += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("Bedrock %s: invalid control-plane endpoint", action)
		}
		requestCreds := creds
		if cfg.Credentials != nil {
			keys, err := cfg.Credentials(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("Bedrock %s: %w", action, ctx.Err())
				}
				return fmt.Errorf("Bedrock %s: signing credentials unavailable; check AWS CLI v2, AWS_PROFILE, and login", action)
			}
			if keys.AccessKeyID == "" || keys.SecretAccessKey == "" {
				return fmt.Errorf("Bedrock %s: signing source returned incomplete keys", action)
			}
			requestCreds = &bedrockSigV4Creds{keys.AccessKeyID, keys.SecretAccessKey, keys.SessionToken}
		}
		if err := signSigV4(req, nil, "bedrock", region, requestCreds, time.Now().UTC()); err != nil {
			return fmt.Errorf("Bedrock %s: could not sign request", action)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("Bedrock %s: %w", action, ctx.Err())
			}
			// Transport errors can contain URLs and opaque pagination tokens.
			return fmt.Errorf("Bedrock %s: request failed; check network and AWS region", action)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("Bedrock %s: HTTP %d; check signing credentials and bedrock:%s permission", action, resp.StatusCode, action)
			}
			return fmt.Errorf("Bedrock %s: HTTP %d", action, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBodyBytes+1))
		if err != nil {
			return fmt.Errorf("Bedrock %s: could not read response", action)
		}
		if len(body) > maxDiscoveryBodyBytes {
			return fmt.Errorf("Bedrock %s: response exceeds size limit", action)
		}
		if err := json.Unmarshal(body, dst); err != nil {
			return fmt.Errorf("Bedrock %s: invalid response", action)
		}
		return nil
	}

	known = catalogOr(known)
	runtimeURL := bedrockEndpoint("bedrock-runtime", region)
	var out []Model
	seen := map[string]bool{}
	add := func(m Model) {
		if seen[m.ID] {
			return
		}
		seen[m.ID] = true
		m.Caps = mergeCaps(nil, m.Caps)
		m.ReasoningEfforts = slices.Clone(m.ReasoningEfforts)
		m.Provider, m.BaseURL, m.Source, m.Speculative = "amazon-bedrock", runtimeURL, "live", false
		if m.DisplayName == "" {
			m.DisplayName = m.ID
		}
		out = append(out, m)
	}
	var failures []error
	var foundations struct {
		Models []bedrockFoundationSummary `json:"modelSummaries"`
	}
	foundationModels := map[string]Model{}
	nonTextFoundations := map[string]bool{}
	if err := get("/foundation-models", "ListFoundationModels", nil, &foundations); err != nil {
		failures = append(failures, err)
	} else if foundations.Models == nil {
		failures = append(failures, errors.New("Bedrock ListFoundationModels: missing modelSummaries"))
	} else {
		for _, row := range foundations.Models {
			if !slices.Contains(row.Output, "TEXT") {
				nonTextFoundations[row.ID] = true
				continue
			}
			if row.Lifecycle.Status == "INACTIVE" {
				continue
			}
			if !validBedrockDiscoveryID(row.ID) {
				failures = append(failures, errors.New("Bedrock ListFoundationModels: invalid model ID"))
				continue
			}
			m, _ := known.FindModel("amazon-bedrock", row.ID)
			m.ID, m.DisplayName = row.ID, row.Name
			m.Caps = mergeCaps(map[Capability]bool{CapImageInput: slices.Contains(row.Input, "IMAGE")}, m.Caps)
			foundationModels[row.ID] = m
			if slices.Contains(row.InferenceTypes, "ON_DEMAND") {
				add(m)
			}
		}
	}
	seenTokens := map[string]bool{}
	token := ""
	for page := 0; page < maxBedrockProfilePages; page++ {
		query := url.Values{"maxResults": {"1000"}}
		if token != "" {
			query.Set("nextToken", token)
		}
		var profiles struct {
			Models []bedrockProfileSummary `json:"inferenceProfileSummaries"`
			Next   string                  `json:"nextToken"`
		}
		if err := get("/inference-profiles", "ListInferenceProfiles", query, &profiles); err != nil {
			failures = append(failures, err)
			break
		}
		if profiles.Models == nil {
			failures = append(failures, errors.New("Bedrock ListInferenceProfiles: missing inferenceProfileSummaries"))
			break
		}
		for _, row := range profiles.Models {
			if row.Status != "ACTIVE" {
				continue
			}
			if !validBedrockDiscoveryID(row.ID) {
				failures = append(failures, errors.New("Bedrock ListInferenceProfiles: invalid profile ID"))
				continue
			}
			foundationID := bedrockProfileFoundationID(row)
			if nonTextFoundations[foundationID] {
				continue
			}
			m, err := known.FindModel("amazon-bedrock", row.ID)
			if err != nil && foundationID != "" {
				m, err = known.FindModel("amazon-bedrock", foundationID)
				if err != nil {
					m = foundationModels[foundationID]
				}
			}
			m.ID, m.DisplayName = row.ID, row.Name
			add(m)
		}
		if profiles.Next == "" {
			break
		}
		if len(profiles.Next) > 2048 || seenTokens[profiles.Next] {
			failures = append(failures, errors.New("Bedrock ListInferenceProfiles: invalid or repeated pagination token"))
			break
		}
		seenTokens[profiles.Next] = true
		token = profiles.Next
		if page == maxBedrockProfilePages-1 {
			failures = append(failures, errors.New("Bedrock ListInferenceProfiles: page limit reached"))
		}
	}
	return out, errors.Join(failures...)
}

func validBedrockDiscoveryID(id string) bool {
	if id == "" || len(id) > 2048 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

// A cross-region profile can list several ARNs for one foundation model.
// Mixed-model profiles have no single verified set of limits or prices.
func bedrockProfileFoundationID(row bedrockProfileSummary) string {
	id := ""
	for _, model := range row.Models {
		parts := strings.SplitN(model.ARN, ":", 6)
		if len(parts) != 6 || parts[0] != "arn" || parts[2] != "bedrock" || !strings.HasPrefix(parts[5], "foundation-model/") {
			return ""
		}
		next := strings.TrimPrefix(parts[5], "foundation-model/")
		if !validBedrockDiscoveryID(next) || id != "" && next != id {
			return ""
		}
		id = next
	}
	return id
}
