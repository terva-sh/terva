package provider

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestVertexConfigParsesAuthorizedUser(t *testing.T) {
	// Write a fake ADC user-OAuth file and point GOOGLE_APPLICATION_CREDENTIALS at it.
	tmp := testsupport.TempDir(t)
	path := filepath.Join(tmp, "adc.json")
	body := `{
	  "type": "authorized_user",
	  "client_id": "fake.apps.googleusercontent.com",
	  "client_secret": "fake-secret",
	  "refresh_token": "1//fake-refresh"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
	t.Setenv("GOOGLE_CLOUD_API_KEY", "") // ensure SA / user path is chosen
	cfg, err := loadVertexConfig()
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
}

func TestVertexConfigRejectsBadType(t *testing.T) {
	tmp := testsupport.TempDir(t)
	path := filepath.Join(tmp, "adc.json")
	body := `{"type": "something_else"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
	t.Setenv("GOOGLE_CLOUD_API_KEY", "")
	_, err := loadVertexConfig()
	if err == nil {
		t.Fatal("expected error for unknown credential type")
	}
}
