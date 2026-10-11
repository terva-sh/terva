package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscoverLocalCompatibleLoadedRuntimeMetadata(t *testing.T) {
	for _, tc := range []struct{ kind, path, body string }{
		{"lmstudio", "/api/v1/models", `{"models":[
			{"type":"llm","key":"downloaded","max_context_length":1000000,"loaded_instances":[]},
			{"type":"embedding","loaded_instances":[{"id":"embed"}]},
			{"type":"llm","key":"model","display_name":"Friendly","max_context_length":1000000,"loaded_instances":[{"id":"active","config":{"context_length":16384}},{"id":"active","config":{"context_length":8192}},{"id":"unknown-limit"}]}]}`},
		{"llamacpp", "/models", `{"data":[
			{"id":"downloaded","status":{"value":"unloaded"}},
			{"id":"active","status":{"value":"loaded"},"meta":{"n_ctx_train":1000000}},
			{"id":"unknown-limit","status":{"value":"loaded"},"meta":{"n_ctx_train":1000000}}]}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.kind == "llamacpp" && r.URL.Path == "/props" {
					if r.URL.Query().Get("autoload") != "false" {
						t.Error("unsafe props request")
					}
					if r.URL.Query().Get("model") == "active" {
						fmt.Fprint(w, `{"model_path":"fixture.gguf","default_generation_settings":{"n_ctx":8192}}`)
						return
					}
					http.NotFound(w, r)
					return
				}
				if r.Method != "GET" || r.URL.Path != tc.path || r.URL.RawQuery != "" {
					t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("missing auth header")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			got, err := DiscoverLocalCompatible(context.Background(), srv.URL+"/v1", "fixture-key", tc.kind, 4096, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].ID != "active" || got[0].ContextWindow != 8192 || got[1].ContextWindow != 4096 {
				t.Fatalf("runtime metadata: %+v", got)
			}
			if got[0].BaseURL != srv.URL+"/v1" || got[0].PriceInput != 0 || got[0].MaxOutput != 0 {
				t.Fatalf("unexpected inferred metadata: %+v", got[0])
			}
		})
	}
}

func TestDiscoverLocalKnownSizing(t *testing.T) {
	known := []Model{
		{ID: "active", ContextWindow: 8192, MaxOutput: 4096},
		{ID: "active", ContextWindow: 16384, MaxOutput: 8192},
		{ID: "active", ContextWindow: 512, MaxOutput: 128, BaseURL: "http://old-endpoint"},
		{ID: "active", ContextWindow: 256, MaxOutput: 64, Synthetic: true},
	}
	for _, kind := range []string{"lmstudio", "llamacpp"} {
		for _, window := range []int{0, 2048, 32768} {
			t.Run(fmt.Sprintf("%s/%d", kind, window), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/models":
						fmt.Fprintf(w, `{"models":[{"type":"llm","loaded_instances":[{"id":"active","config":{"context_length":%d}}]}]}`, window)
					case "/models":
						fmt.Fprintf(w, `{"data":[{"id":"active","status":{"value":"loaded"},"meta":{"n_ctx":%d}}]}`, window)
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				got, err := DiscoverLocalCompatible(context.Background(), srv.URL, "", kind, 131072, known)
				wantWindow := window
				if wantWindow == 0 {
					wantWindow = 8192
				}
				if err != nil || len(got) != 1 || got[0].ContextWindow != wantWindow || got[0].MaxOutput != min(4096, wantWindow) {
					t.Fatalf("known sizing: %+v, %v", got, err)
				}
			})
		}
	}
}

func TestDiscoverLlamaCPPSlowProbeDoesNotStarveLaterModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"slow","status":{"value":"loaded"}},{"id":"fast","status":{"value":"loaded"}}]}`)
			return
		}
		if r.URL.Path != "/props" || r.URL.Query().Get("autoload") != "false" {
			t.Error("unexpected request")
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("model") == "slow" {
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"model_path":"fixture.gguf","default_generation_settings":{"n_ctx":8192}}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := DiscoverLocalCompatible(ctx, srv.URL, "", "llamacpp", 4096, nil)
	if err != nil || len(got) != 2 || got[0].ContextWindow != 4096 || got[1].ContextWindow != 8192 || ctx.Err() != nil {
		t.Fatalf("slow probe starved discovery: %+v, %v", got, err)
	}
}

func TestDiscoverLocalCompatibleFallbackAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       int
	}{
		{"unsupported", "", 404, 1}, {"unavailable", "credential must not appear", 503, 1},
		{"malformed", "{", 200, 1}, {"missing", `{}`, 200, 1},
		{"oversized", strings.Repeat(" ", int(maxDiscoveryBodyBytes)+1), 200, 1},
		{"empty", `{"models":[]}`, 200, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generic := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					generic++
					fmt.Fprint(w, `{"data":[{"id":"fallback","context_length":2048}]}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			got, err := DiscoverLocalCompatible(context.Background(), srv.URL, "", "lmstudio", 4096, nil)
			if err != nil || len(got) != tc.want || generic != tc.want {
				t.Fatalf("fallback: %+v, %v, requests=%d", got, err, generic)
			}
		})
	}
}

func TestDiscoverLlamaCPPDirectPropsDoNotAutoload(t *testing.T) {
	for _, sleeping := range []bool{false, true} {
		t.Run(fmt.Sprint(sleeping), func(t *testing.T) {
			propsCalls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					http.NotFound(w, r)
				case "/v1/models":
					fmt.Fprint(w, `{"data":[{"id":"active"}]}`)
				case "/props":
					propsCalls++
					if r.Method != "GET" || r.URL.Query().Get("autoload") != "false" || r.URL.Query().Get("model") != "active" {
						t.Error("props could change server state")
					}
					fmt.Fprintf(w, `{"model_path":"fixture.gguf","is_sleeping":%t,"default_generation_settings":{"n_ctx":8192}}`, sleeping)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			known := []Model{{ID: "active", ContextWindow: 16384, MaxOutput: 32768}}
			got, err := DiscoverLocalCompatible(context.Background(), srv.URL+"/v1", "", "llamacpp", 4096, known)
			want := 4096
			if !sleeping {
				want = 8192
			}
			if err != nil || len(got) != 1 || got[0].ContextWindow != want || propsCalls != 1 {
				t.Fatalf("direct props: %+v, %v", got, err)
			}
			if !sleeping && got[0].MaxOutput != 8192 {
				t.Fatalf("direct props lost or exceeded the active output budget: %+v", got[0])
			}
		})
	}
}
