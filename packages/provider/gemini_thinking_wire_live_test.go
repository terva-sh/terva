package provider_test

// Which thinkingConfig values does Gemini actually ACCEPT, and what do they
// cost inside a tight output cap?
//
// This talks to the API directly rather than through terva's mapping, because
// the question is what the wire permits — and terva can only send the values it
// already knows how to name. `thinkingLevel: MINIMAL` looked like the obvious
// off switch and is rejected outright on gemini-flash-latest ("Thinking level
// MINIMAL is not supported for this model"), so the remaining candidates have to
// be tried rather than assumed.
//
// The second half of the table is the more important half: it prices the same
// ask at a LARGER cap. If no thinking level fits an answer inside 200 tokens,
// then the cap is the bug and the thinkingConfig is a red herring.
//
//	TERVA_LIVE_GEMINI_WIRE=1 go test ./packages/provider/ \
//	  -run TestLiveGeminiThinkingWireValues -v -count=1
//
// Skipped otherwise. Never logs the credential.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/testsupport"
)

func TestLiveGeminiThinkingWireValues(t *testing.T) {
	if os.Getenv("TERVA_LIVE_GEMINI_WIRE") == "" {
		t.Skip("live probe: set TERVA_LIVE_GEMINI_WIRE=1 (spends real money)")
	}
	r, err := build.Resolve(build.Args{
		Provider: "google", Model: os.Getenv("TERVA_LIVE_GEMINI_MODEL"), CWD: testsupport.TempDir(t),
	}, true)
	if err != nil {
		t.Fatalf("resolve a google credential: %v", err)
	}
	model := r.Model
	base := r.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	base = strings.TrimSuffix(base, "/")
	if !strings.Contains(base, "/v1") {
		base += "/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:generateContent", base, model)
	t.Logf("model=%s", model)

	type probe struct {
		name     string
		cap      int
		thinking string // raw JSON for generationConfig.thinkingConfig, "" = omit
	}
	probes := []probe{
		{"cap 200, no thinkingConfig (today's OFF)", 200, ""},
		{"cap 200, thinkingLevel MINIMAL", 200, `{"thinkingLevel":"MINIMAL"}`},
		{"cap 200, thinkingLevel OFF", 200, `{"thinkingLevel":"OFF"}`},
		{"cap 200, thinkingLevel NONE", 200, `{"thinkingLevel":"NONE"}`},
		{"cap 200, thinkingBudget 0", 200, `{"thinkingBudget":0}`},
		{"cap 200, thinkingBudget 0 + includeThoughts false", 200, `{"thinkingBudget":0,"includeThoughts":false}`},
		{"cap 200, thinkingLevel LOW", 200, `{"thinkingLevel":"LOW"}`},
		// The cap hypothesis: same ask, room to think AND answer.
		{"cap 800, no thinkingConfig", 800, ""},
		{"cap 800, thinkingLevel LOW", 800, `{"thinkingLevel":"LOW"}`},
	}

	for _, p := range probes {
		gen := map[string]any{"maxOutputTokens": p.cap}
		if p.thinking != "" {
			var tc map[string]any
			if uErr := json.Unmarshal([]byte(p.thinking), &tc); uErr != nil {
				t.Fatalf("bad probe json: %v", uErr)
			}
			gen["thinkingConfig"] = tc
		}
		body, _ := json.Marshal(map[string]any{
			"systemInstruction": map[string]any{
				"parts": []any{map[string]any{"text": "You are a terse assistant."}},
			},
			"contents": []any{map[string]any{
				"role": "user",
				"parts": []any{map[string]any{
					"text": "Reply with one short line and nothing else: the smallest next step after a rename broke one call site.",
				}},
			}},
			"generationConfig": gen,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-goog-api-key", r.Credential)
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			cancel()
			t.Errorf("%-48s transport: %v", p.name, doErr)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()

		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error struct{ Message string } `json:"error"`
			}
			_ = json.Unmarshal(raw, &e)
			t.Logf("%-48s HTTP %d  %s", p.name, resp.StatusCode, strings.TrimSpace(e.Error.Message))
			continue
		}
		var out struct {
			Candidates []struct {
				Content      struct{ Parts []struct{ Text string } } `json:"content"`
				FinishReason string                                  `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata struct {
				ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if uErr := json.Unmarshal(raw, &out); uErr != nil {
			t.Errorf("%-48s decode: %v", p.name, uErr)
			continue
		}
		var text, finish string
		if len(out.Candidates) > 0 {
			finish = out.Candidates[0].FinishReason
			for _, part := range out.Candidates[0].Content.Parts {
				text += part.Text
			}
		}
		t.Logf("%-48s thoughts=%-4d answer=%-4d finish=%-9s text=%q",
			p.name, out.UsageMetadata.ThoughtsTokenCount, out.UsageMetadata.CandidatesTokenCount,
			finish, strings.TrimSpace(text))
	}
}
