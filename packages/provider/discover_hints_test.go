package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestDiscoverCompatibleContextHints(t *testing.T) {
	const fallback = 8192
	cases := []struct {
		name, fields string
		want         int
	}{
		{"missing", "", fallback},
		{"precedence", `,"max_model_len":65536,"context_length":32768,"context_window":16384`, 65536},
		{"second hint", `,"max_model_len":"broken","context_length":32768,"context_window":16384`, 32768},
		{"third hint", `,"max_model_len":false,"context_length":{},"context_window":16384`, 16384},
		{"largest int", `,"max_model_len":` + strconv.Itoa(int(^uint(0)>>1)), int(^uint(0) >> 1)},
	}
	for _, key := range []string{"max_model_len", "context_length", "context_window"} {
		for _, value := range []string{`"65536"`, `1.5`, `-1`, `0`, `null`, `false`, `{}`, `[]`, `1e100`, `18446744073709551616`} {
			cases = append(cases, struct {
				name, fields string
				want         int
			}{key + "/" + value, fmt.Sprintf(",%q:%s", key, value), fallback})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"data":[{"id":"qwen-coder"%s},{"id":"llama3","max_model_len":4096}]}`, tc.fields)
			}))
			defer srv.Close()
			models, err := DiscoverOpenAICompatible(t.Context(), srv.URL, "", fallback, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 2 || models[0].ID != "qwen-coder" || models[1].ID != "llama3" {
				t.Fatalf("listing lost models: %+v", models)
			}
			if models[0].ContextWindow != tc.want || models[1].ContextWindow != 4096 {
				t.Fatalf("contexts = %d, %d, want %d, 4096", models[0].ContextWindow, models[1].ContextWindow, tc.want)
			}
		})
	}
}
