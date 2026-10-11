package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func bedrockDiscoveryFixtureConfig() BedrockConfig {
	return BedrockConfig{Region: "eu-west-1", AccessKeyID: "FIXTUREAK", SecretAccessKey: "FIXTURESK", SessionToken: "FIXTUREST", BearerToken: "unused-bearer"}
}

func bedrockDiscoveryFoundation(id string) map[string]any {
	return map[string]any{"modelId": id, "modelName": id, "inputModalities": []string{"TEXT"}, "outputModalities": []string{"TEXT"}, "inferenceTypesSupported": []string{"ON_DEMAND"}, "modelLifecycle": map[string]string{"status": "ACTIVE"}}
}

func bedrockDiscoveryProfile(id string, foundations ...string) map[string]any {
	models := []map[string]string{}
	for _, foundation := range foundations {
		models = append(models, map[string]string{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/" + foundation})
	}
	return map[string]any{"inferenceProfileId": id, "inferenceProfileName": "Profile " + id, "status": "ACTIVE", "models": models}
}

func TestDiscoverBedrockPaginationMetadataAndRouting(t *testing.T) {
	const knownID = "anthropic.claude-opus-4-1-20250805-v1:0"
	const token = "opaque +/=& value"
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=FIXTUREAK/") || !strings.Contains(r.Header.Get("Authorization"), "/eu-west-1/bedrock/aws4_request") || r.Header.Get("X-Amz-Security-Token") != "FIXTUREST" {
			t.Error("request lacks the region-scoped SigV4 session credential")
		}
		if r.Method != http.MethodGet || r.Header.Get("X-Amz-Content-Sha256") != sigSHA256Hex(nil) {
			t.Error("discovery must sign a bodyless GET")
		}
		if r.URL.Path == "/foundation-models" {
			image := bedrockDiscoveryFoundation("image-only")
			image["outputModalities"] = []string{"IMAGE"}
			provisioned := bedrockDiscoveryFoundation("provisioned-only")
			provisioned["inferenceTypesSupported"] = []string{"PROVISIONED"}
			_ = json.NewEncoder(w).Encode(map[string]any{"modelSummaries": []any{bedrockDiscoveryFoundation(knownID), bedrockDiscoveryFoundation("new.text-v1:0"), image, provisioned}})
			return
		}
		if r.URL.Path != "/inference-profiles" || r.URL.Query().Get("maxResults") != "1000" {
			t.Errorf("unexpected list request: %s", r.URL.Path)
		}
		pages++
		if pages == 1 {
			inactive := bedrockDiscoveryProfile("inactive", knownID)
			inactive["status"] = "INACTIVE"
			_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []any{bedrockDiscoveryProfile("eu."+knownID, knownID), inactive, bedrockDiscoveryProfile("image-profile", "image-only")}, "nextToken": token})
		} else {
			if r.URL.Query().Get("nextToken") != token {
				t.Error("opaque token did not survive URL encoding")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []any{bedrockDiscoveryProfile("eu."+knownID, knownID), bedrockDiscoveryProfile("app123456789", knownID, knownID), bedrockDiscoveryProfile("mixed", knownID, "amazon.nova-lite-v1:0"), bedrockDiscoveryProfile("unknown", "unlisted.text")}})
		}
	}))
	defer server.Close()
	models, err := DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, Builtin())
	if err != nil || pages != 2 || len(models) != 6 {
		t.Fatalf("discovery: %d models, %d pages, %v", len(models), pages, err)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
		if m.BaseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" || m.Provider != "amazon-bedrock" || m.Source != "live" {
			t.Errorf("wrong routing/source for %s", m.ID)
		}
	}
	verified, _ := Builtin().FindModel("amazon-bedrock", knownID)
	for _, id := range []string{knownID, "eu." + knownID, "app123456789"} {
		m := byID[id]
		if m.MaxOutput != verified.MaxOutput || m.MaxOutput != 32000 || m.ContextWindow != verified.ContextWindow || m.PriceInput != verified.PriceInput || m.PriceCacheWrite != verified.PriceCacheWrite {
			t.Errorf("%s lost verified metadata: %+v", id, m)
		}
	}
	for _, id := range []string{"new.text-v1:0", "mixed", "unknown"} {
		m := byID[id]
		if m.MaxOutput != 0 || m.ContextWindow != 0 || m.PriceInput != 0 || m.PriceOutput != 0 {
			t.Errorf("%s received invented metadata", id)
		}
	}
	reg := NewRegistry()
	reg.SetLiveModels(models)
	m, _ := reg.FindModel("amazon-bedrock", knownID)
	if m.BaseURL != byID[knownID].BaseURL {
		t.Fatal("catalog merge retained the baked us-east-1 route")
	}
	reg.SetUserModels([]Model{{Provider: "amazon-bedrock", ID: knownID, ContextWindow: 99999, BaseURL: "https://user.example"}})
	m, _ = reg.FindModel("amazon-bedrock", knownID)
	if m.BaseURL != "https://user.example" || m.ContextWindow != 99999 {
		t.Fatal("discovery displaced the user layer")
	}
	profile := bedrockCatalogModel(reg, "app123456789")
	if profile.MaxOutput != verified.MaxOutput || !bedrockModelSupportsCaching(reg, "app123456789") {
		t.Fatal("application-profile runtime ignored discovered metadata")
	}
}

func TestDiscoverBedrockPartialFailuresAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"denied", "FIXTURESK echoed-token", http.StatusForbidden, "bedrock:ListInferenceProfiles"},
		{"malformed", "{FIXTURESK", http.StatusOK, "invalid response"},
		{"missing-list", `{}`, http.StatusOK, "missing inferenceProfileSummaries"},
		{"oversize", strings.Repeat("s", maxDiscoveryBodyBytes+1), http.StatusOK, "size limit"},
		{"token-loop", `{"inferenceProfileSummaries":[{"inferenceProfileId":"partial","status":"ACTIVE"}],"nextToken":"echoed-token"}`, http.StatusOK, "repeated pagination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/foundation-models" {
					_ = json.NewEncoder(w).Encode(map[string]any{"modelSummaries": []any{bedrockDiscoveryFoundation("kept.text")}})
					return
				}
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			models, err := DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(models) < 1 || models[0].ID != "kept.text" {
				t.Fatalf("partial result: %v, %v", models, err)
			}
			if strings.Contains(err.Error(), "FIXTURESK") || strings.Contains(err.Error(), "echoed-token") || calls > 2 {
				t.Fatalf("unsafe or unbounded failure: %v (%d calls)", err, calls)
			}
		})
	}
}

func TestDiscoverBedrockProfilesSurviveFoundationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/foundation-models" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []any{bedrockDiscoveryProfile("app", "amazon.nova-lite-v1:0")}})
	}))
	defer server.Close()
	models, err := DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, nil)
	if err == nil || len(models) != 1 || models[0].ContextWindow != 300000 || models[0].MaxOutput != 8192 {
		t.Fatalf("profiles after foundation failure: %+v, %v", models, err)
	}
}

func TestDiscoverBedrockPaginationBounds(t *testing.T) {
	for _, oversizedToken := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized-token-%v", oversizedToken), func(t *testing.T) {
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/foundation-models" {
					fmt.Fprint(w, `{"modelSummaries":[]}`)
					return
				}
				pages++
				token := fmt.Sprintf("page-%d", pages)
				if oversizedToken {
					token = strings.Repeat("x", 2049)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []any{bedrockDiscoveryProfile("kept")}, "nextToken": token})
			}))
			defer server.Close()
			models, err := DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, nil)
			if err == nil || len(models) != 1 {
				t.Fatalf("unbounded or discarded partial list: %+v, %v", models, err)
			}
			wantPages := maxBedrockProfilePages
			if oversizedToken {
				wantPages = 1
			}
			if pages != wantPages {
				t.Fatalf("made %d requests; want %d", pages, wantPages)
			}
		})
	}
}

func TestDiscoverBedrockInterruptedBodyAndDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "200")
		fmt.Fprint(w, `{}`)
	}))
	_, err := DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "could not read response") {
		t.Fatalf("interrupted body: %v", err)
	}
	server.Close()
	_, err = DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "check network and AWS region") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("unavailable endpoint diagnostic: %v", err)
	}
	started := make(chan struct{}, 1)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = DiscoverBedrock(ctx, bedrockDiscoveryFixtureConfig(), server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("in-flight deadline: %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("deadline fixture never received a request")
	}
}

func TestDiscoverBedrockCredentialsRedirectAndCancellation(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := DiscoverBedrock(context.Background(), BedrockConfig{BearerToken: "bearer"}, server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "SigV4") {
		t.Fatal("bearer-only credentials attempted discovery")
	}
	_, err = DiscoverBedrock(context.Background(), bedrockDiscoveryFixtureConfig(), server.URL, nil)
	if err == nil || calls != 0 || !strings.Contains(err.Error(), "307") {
		t.Fatal("signed request followed a redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = DiscoverBedrock(ctx, bedrockDiscoveryFixtureConfig(), server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation: %v", err)
	}
	for _, region := range []string{"cn-north-1", "us-gov-west-1"} {
		want := "https://bedrock." + region + ".amazonaws.com"
		if strings.HasPrefix(region, "cn-") {
			want += ".cn"
		}
		if bedrockEndpoint("bedrock", region) != want {
			t.Errorf("wrong partition host for %s", region)
		}
	}
}

func TestBedrockRegionRejectsEndpointInjection(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	for _, region := range []string{"x@host.example/", "x/host.example", "x#fragment", "US-EAST-1", "us east-1", "us-éast-1", "-us-east-1", "us-east-1-", strings.Repeat("a", 64)} {
		cfg := bedrockDiscoveryFixtureConfig()
		cfg.Region = region
		_, err := DiscoverBedrock(context.Background(), cfg, server.URL, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid AWS region") || strings.Contains(err.Error(), region) {
			t.Errorf("unsafe region accepted or echoed: %q", region)
		}
		if _, ok := newBedrockClient(cfg, "").(*unimplementedClient); !ok {
			t.Errorf("runtime accepted unsafe region %q", region)
		}
	}
	if requests != 0 {
		t.Fatal("invalid regions reached the signed request path")
	}
	for _, region := range []string{"us-east-1", "us-gov-west-1", "cn-north-1", "eu-isoe-west-1", "eusc-de-east-1"} {
		if !validBedrockRegion(region) {
			t.Errorf("rejected valid AWS region %s", region)
		}
	}
}

func TestSigV4QueryEncodingFixture(t *testing.T) {
	// The expected signature was computed independently with Python hashlib.
	// It covers prefix keys, repeated keys, spaces, and opaque tokens.
	req, _ := http.NewRequest(http.MethodGet, "https://bedrock.eu-west-1.amazonaws.com/inference-profiles?nextToken=a+b%2B%2F%3D%26&z=2&z=1&%C3%A9=x&k=1&k-=2", nil)
	creds := &bedrockSigV4Creds{"AKIDEXAMPLE", "fixture-secret", "fixture-session"}
	_ = signSigV4(req, nil, "bedrock", "eu-west-1", creds, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	const wantSignature = "a02654900caaa8a67814620560260ec4897ed9288c862b09a7123d936bbd1d13"
	if !strings.HasSuffix(req.Header.Get("Authorization"), "Signature="+wantSignature) {
		t.Fatalf("signature mismatch: %s", req.Header.Get("Authorization"))
	}
}

func TestDiscoverBedrockRefreshesCredentialsForEachPage(t *testing.T) {
	resolutions, requests := 0, 0
	cfg := BedrockConfig{Region: "eu-west-1", Credentials: func(context.Context) (BedrockSigningCredentials, error) {
		resolutions++
		return BedrockSigningCredentials{AccessKeyID: fmt.Sprintf("fixture-key-%d", resolutions), SecretAccessKey: "fixture-secret", SessionToken: fmt.Sprintf("fixture-token-%d", resolutions)}, nil
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !strings.Contains(r.Header.Get("Authorization"), fmt.Sprintf("Credential=fixture-key-%d/", requests)) || r.Header.Get("X-Amz-Security-Token") != fmt.Sprintf("fixture-token-%d", requests) {
			t.Error("page did not use its refreshed signing credentials")
		}
		if r.URL.Path == "/foundation-models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"modelSummaries": []any{bedrockDiscoveryFoundation("fixture.text")}})
			return
		}
		response := map[string]any{"inferenceProfileSummaries": []any{bedrockDiscoveryProfile("fixture.profile", "fixture.text")}}
		if requests == 2 {
			response["nextToken"] = "second-page"
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	models, err := DiscoverBedrock(context.Background(), cfg, server.URL, nil)
	if err != nil || len(models) != 2 || resolutions != 3 || requests != 3 {
		t.Fatalf("refresh per page: %d models, %d resolutions, %d requests, %v", len(models), resolutions, requests, err)
	}
}

func TestDiscoverBedrockCredentialSourceFailures(t *testing.T) {
	for _, kind := range []string{"error", "incomplete", "canceled", "later-error"} {
		t.Run(kind, func(t *testing.T) {
			resolutions, requests := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := BedrockConfig{Credentials: func(context.Context) (BedrockSigningCredentials, error) {
				resolutions++
				if kind == "later-error" && resolutions == 1 {
					return BedrockSigningCredentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"}, nil
				}
				if kind == "incomplete" {
					return BedrockSigningCredentials{AccessKeyID: "fixture-secret"}, nil
				}
				if kind == "canceled" {
					cancel()
				}
				return BedrockSigningCredentials{}, errors.New("fixture-secret helper output")
			}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				_ = json.NewEncoder(w).Encode(map[string]any{"modelSummaries": []any{bedrockDiscoveryFoundation("retained.text")}})
			}))
			defer server.Close()
			models, err := DiscoverBedrock(ctx, cfg, server.URL, nil)
			if err == nil || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("missing or unsafe source error: %v", err)
			}
			if kind == "later-error" {
				if requests != 1 || len(models) != 1 || models[0].ID != "retained.text" {
					t.Fatal("refresh failure discarded useful earlier results")
				}
			} else if requests != 0 || len(models) != 0 {
				t.Fatal("unavailable signing source reached the network")
			}
			if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("credential refresh lost context cancellation")
			}
		})
	}
}
