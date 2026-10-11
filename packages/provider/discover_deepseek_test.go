package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDiscoverDeepSeekIDsAndMetadata(t *testing.T) {
	for _, suffix := range []string{"", "/v1"} {
		t.Run(suffix, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" || r.Method != "GET" {
					t.Errorf("wrong discovery request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("missing discovery credential")
				}
				fmt.Fprint(w, `{"data":[{"id":"deepseek-fixture-new-flash","context_length":1000000},{"id":"deepseek-v4-flash"}]}`)
			}))
			defer srv.Close()
			rows, err := DiscoverDeepSeek(context.Background(), "fixture-key", srv.URL+suffix)
			if err != nil || len(rows) != 2 {
				t.Fatalf("non-OpenAI IDs missing: %+v, %v", rows, err)
			}
			for _, row := range rows {
				if row.Provider != "deepseek" || row.Source != "live" || row.ContextWindow != 0 || row.MaxOutput != 0 || row.Reasoning || len(row.ReasoningEfforts) != 0 || row.PriceInput != 0 || row.Has(CapImageInput) {
					t.Fatalf("discovery invented metadata: %+v", row)
				}
			}
			reg := NewRegistry()
			known, err := reg.FindModel("deepseek", "deepseek-v4-flash")
			if err != nil {
				t.Fatal(err)
			}
			reg.SetLiveModels(rows)
			got, err := reg.FindModel("deepseek", "deepseek-v4-flash")
			if err != nil || got.DisplayName != known.DisplayName || got.ContextWindow != known.ContextWindow || got.MaxOutput != known.MaxOutput || got.Reasoning != known.Reasoning || !reflect.DeepEqual(got.ReasoningEfforts, known.ReasoningEfforts) || got.PriceInput != known.PriceInput || !reflect.DeepEqual(got.Caps, known.Caps) {
				t.Fatalf("catalog metadata lost: %+v, %v", got, err)
			}
		})
	}
}

func TestDiscoverDeepSeekUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	if _, err := DiscoverDeepSeek(context.Background(), "fixture-key", srv.URL); err == nil {
		t.Fatal("unavailable service reported success")
	}
}
