package provider

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type vertexRecordingTransport struct {
	request *http.Request
}

func (r *vertexRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.request = req
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
}

func TestVertexRequestHosts(t *testing.T) {
	for _, tc := range []struct{ location, host string }{
		{"global", "aiplatform.googleapis.com"},
		{"us-central1", "us-central1-aiplatform.googleapis.com"},
	} {
		t.Run(tc.location, func(t *testing.T) {
			client := newVertex(VertexConfig{Project: "project", Location: tc.location, APIKey: "synthetic"}).(*renamedClient).inner.(*geminiClient)
			if client.baseURL != "https://"+tc.host {
				t.Fatalf("base URL = %q", client.baseURL)
			}
			recorder := &vertexRecordingTransport{}
			transport := client.http.Transport.(*vertexTransport)
			transport.inner = recorder
			req, err := http.NewRequest("POST", client.baseURL+"/v1beta/models/gemini-test:streamGenerateContent?alt=sse", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			wantPath := "/v1/projects/project/locations/" + tc.location + "/publishers/google/models/gemini-test:streamGenerateContent"
			if recorder.request.URL.Host != tc.host || recorder.request.URL.Path != wantPath || recorder.request.URL.RawQuery != "alt=sse" {
				t.Fatalf("outbound URL = %s", recorder.request.URL)
			}
			if req.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" {
				t.Fatal("transport mutated the source request")
			}
		})
	}
}

// The wire parses the credentials file's contents; finding and reading the file
// is the host's (terva's is packages/agent/build/cloud_config.go).
func TestVertexConfigParsesAuthorizedUser(t *testing.T) {
	body := `{
	  "type": "authorized_user",
	  "client_id": "fake.apps.googleusercontent.com",
	  "client_secret": "fake-secret",
	  "refresh_token": "1//fake-refresh"
	}`
	cfg, err := loadVertexConfig(VertexConfig{Project: "test-proj", CredentialsJSON: []byte(body)})
	if err != nil {
		t.Fatalf("loadVertexConfig: %v", err)
	}
	if cfg.userClientID != "fake.apps.googleusercontent.com" {
		t.Errorf("client_id = %q", cfg.userClientID)
	}
	if cfg.userRefreshToken != "1//fake-refresh" {
		t.Errorf("refresh_token = %q", cfg.userRefreshToken)
	}
	if cfg.userTokenURI != "https://oauth2.googleapis.com/token" {
		t.Errorf("token_uri default not applied: %q", cfg.userTokenURI)
	}
	if cfg.cacheKey() != "user:fake.apps.googleusercontent.com" {
		t.Errorf("cacheKey = %q", cfg.cacheKey())
	}
	if cfg.location != "us-central1" {
		t.Errorf("location default not applied: %q", cfg.location)
	}
}

func TestVertexConfigRejectsBadType(t *testing.T) {
	_, err := loadVertexConfig(VertexConfig{Project: "test-proj", CredentialsJSON: []byte(`{"type": "something_else"}`)})
	if err == nil {
		t.Fatal("expected error for unknown credential type")
	}
}

// A missing project, or no API key and no credentials, is reported in the
// host's words when it gave a Hint, and in the Config's terms when it did not.
func TestVertexReportsMissingValuesWithTheHint(t *testing.T) {
	for _, c := range []struct {
		name string
		v    VertexConfig
		want string
	}{
		{"no project, hint", VertexConfig{Hint: "host says why"}, "host says why"},
		{"no project", VertexConfig{}, "vertex: VertexConfig.Project not set"},
		{"no auth, hint", VertexConfig{Project: "p", Hint: "host says why"}, "host says why"},
		{"no auth", VertexConfig{Project: "p"}, "vertex: no auth — set VertexConfig.APIKey or CredentialsJSON"},
	} {
		if _, err := loadVertexConfig(c.v); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := loadVertexConfig(VertexConfig{Project: "p", APIKey: "k"}); err != nil {
		t.Errorf("an API key alone should do: %v", err)
	}
}
