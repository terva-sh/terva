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
)

func TestBedrockCredentialsRefreshOnEachRequest(t *testing.T) {
	calls := 0
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !strings.Contains(r.Header.Get("Authorization"), fmt.Sprintf("Credential=fixture-key-%d/", requests)) || r.Header.Get("X-Amz-Security-Token") != fmt.Sprintf("fixture-token-%d", requests) {
			t.Errorf("request %d did not use refreshed credentials", requests)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	client := NewBedrock(BedrockConfig{
		AccessKeyID: "static-key", SecretAccessKey: "static-secret",
		Credentials: func(ctx context.Context) (BedrockSigningCredentials, error) {
			calls++
			return BedrockSigningCredentials{fmt.Sprintf("fixture-key-%d", calls), "fixture-secret", fmt.Sprintf("fixture-token-%d", calls)}, nil
		},
	}, srv.URL)
	for range 2 {
		_, err := client.Stream(context.Background(), Request{Model: "amazon.nova-micro-v1:0", Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}}}})
		if err == nil {
			t.Fatal("fixture should return HTTP 400")
		}
	}
	if calls != 2 || requests != 2 {
		t.Fatalf("source calls=%d requests=%d; want two of each", calls, requests)
	}
}

func TestBedrockBearerSkipsSigningSourceAndSourceErrorsSkipNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture-bearer" {
			t.Error("bearer precedence changed")
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	calls := 0
	sourceErr := errors.New("fixture source unavailable")
	source := func(context.Context) (BedrockSigningCredentials, error) {
		calls++
		return BedrockSigningCredentials{}, sourceErr
	}
	req := Request{Model: "amazon.nova-micro-v1:0", Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}}}}
	_, _ = NewBedrock(BedrockConfig{BearerToken: "fixture-bearer", Credentials: source}, srv.URL).Stream(context.Background(), req)
	if calls != 0 || requests != 1 {
		t.Fatal("bearer inference resolved signing credentials")
	}
	_, err := NewBedrock(BedrockConfig{Credentials: source}, srv.URL).Stream(context.Background(), req)
	if !errors.Is(err, sourceErr) || calls != 1 || requests != 1 {
		t.Fatalf("source failure: calls=%d requests=%d err=%v", calls, requests, err)
	}
	_, err = NewBedrock(BedrockConfig{Credentials: func(context.Context) (BedrockSigningCredentials, error) {
		return BedrockSigningCredentials{AccessKeyID: "fixture-incomplete"}, nil
	}}, srv.URL).Stream(context.Background(), req)
	if err == nil || requests != 1 {
		t.Fatal("incomplete source credentials reached the network")
	}
}

func TestBedrockSigningCredentialsRedactAllFormats(t *testing.T) {
	keys := BedrockSigningCredentials{"fixture-access-secret", "fixture-signing-secret", "fixture-session-secret"}
	doc, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("%s %v %+v %#v %s", keys, keys, keys, keys, doc)
	for _, secret := range []string{keys.AccessKeyID, keys.SecretAccessKey, keys.SessionToken} {
		if strings.Contains(text, secret) {
			t.Fatal("formatted credentials exposed a secret")
		}
	}
}
