package imagegen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A minimal API-format workflow: one text node with a placeholder.
const testWorkflow = `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":"{{prompt}}"}},` +
	`"7":{"class_type":"CLIPTextEncode","inputs":{"text":"{{negative}}"}}}`

func TestComfyUIGenerate(t *testing.T) {
	var queued map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &queued)
			fmt.Fprint(w, `{"prompt_id":"abc-123"}`)
		case r.URL.Path == "/history/abc-123":
			fmt.Fprint(w, `{"abc-123":{"outputs":{"9":{"images":[{"filename":"out.png","subfolder":"","type":"output"}]}}}}`)
		case r.URL.Path == "/view":
			if got := r.URL.Query().Get("filename"); got != "out.png" {
				t.Errorf("view filename = %q", got)
			}
			w.Write(pngBytes)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()

	c := &ComfyUI{Name: "comfy", BaseURL: srv.URL, Workflow: testWorkflow, PollInterval: time.Millisecond}
	res, err := c.Generate(context.Background(), Request{Prompt: `a "quoted" prompt`, NegativePrompt: "ugly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].MimeType != "image/png" {
		t.Fatalf("images = %+v", res.Images)
	}
	// The workflow's placeholders were replaced with correctly-escaped values.
	wf, _ := json.Marshal(queued["prompt"])
	if !strings.Contains(string(wf), `a \"quoted\" prompt`) {
		t.Errorf("prompt not injected/escaped into workflow: %s", wf)
	}
	if !strings.Contains(string(wf), "ugly") || strings.Contains(string(wf), "{{prompt}}") {
		t.Errorf("placeholders not fully replaced: %s", wf)
	}
}

func TestComfyUIQueueRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid workflow","node_errors":{}}`)
	}))
	defer srv.Close()
	c := &ComfyUI{Name: "comfy", BaseURL: srv.URL, Workflow: testWorkflow, PollInterval: time.Millisecond}
	if _, err := c.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Error("a rejected queue should error")
	}
}

func TestComfyUIBadWorkflowJSON(t *testing.T) {
	c := &ComfyUI{Name: "comfy", BaseURL: "http://unused", Workflow: "{not json"}
	if _, err := c.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Error("invalid workflow JSON should error before any request")
	}
}

func TestInjectWorkflowEscaping(t *testing.T) {
	// A prompt with a quote and newline must not break the JSON.
	out, err := injectWorkflow(`{"n":{"inputs":{"text":"{{prompt}}"}}}`, Request{Prompt: "line1\n\"q\""})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any // must still parse
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("injected workflow is invalid JSON: %v (%s)", err, out)
	}
}
