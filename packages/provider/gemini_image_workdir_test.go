package provider

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// geminiImagePayload is the image geminiImageFrame carries.
var geminiImagePayload = []byte("\xff\xd8\xff\xe0JFIF-not-a-real-jpeg")

// geminiImageFrame is an SSE frame carrying one inline image, the shape Gemini
// returns for a generated picture.
func geminiImageFrame(t *testing.T) string {
	t.Helper()
	data := base64.StdEncoding.EncodeToString(geminiImagePayload)
	return `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"inlineData":{"mimeType":"image/jpeg","data":"` + data + `"}}` +
		`]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1450,` +
		`"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]}}`
}

// streamGeminiImage runs one image response and returns the final message.
func streamGeminiImage(t *testing.T, opts ...ClientOption) Message {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + geminiImageFrame(t) + "\n\n"))
	}))
	defer srv.Close()

	evs, err := NewGemini("k", srv.URL, opts...).Stream(context.Background(), Request{
		Model:    "gemini-3.1-flash-image",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "draw"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg Message
	for ev := range evs {
		if e, ok := ev.(EventDone); ok {
			msg = e.Message
		}
	}
	return msg
}

// The wire saves nothing itself (decision 0021, rule 5). With no saver the
// image still reaches the transcript, no file appears anywhere, and no path is
// claimed for a file that does not exist.
func TestWithoutASaverTheImageIsKeptAndNothingIsWritten(t *testing.T) {
	dir := testsupport.TempDir(t)
	t.Chdir(dir)
	msg := streamGeminiImage(t)
	if len(msg.Content) != 1 {
		t.Fatalf("content = %+v, want the image alone", msg.Content)
	}
	if img, ok := msg.Content[0].(ImageBlock); !ok || string(img.Data) != string(geminiImagePayload) {
		t.Errorf("content[0] = %+v, want the image bytes", msg.Content[0])
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("the wire wrote %v", ents)
	}
}

// The saver gets the mime type and the bytes, and the path it returns is told
// to the model beside the image. Where it saves is the host's; the wire passes
// no directory.
func TestTheSaverGetsTheImageAndItsPathIsReported(t *testing.T) {
	var gotMime string
	var gotData []byte
	save := func(mime string, data []byte) (string, error) {
		gotMime, gotData = mime, data
		return "/ws/pic.jpg", nil
	}
	msg := streamGeminiImage(t, WithImageSaver(save))
	if gotMime != "image/jpeg" || string(gotData) != string(geminiImagePayload) {
		t.Errorf("saver got (%q, %q)", gotMime, gotData)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("content = %+v, want the image and its path", msg.Content)
	}
	if tb, ok := msg.Content[1].(TextBlock); !ok || tb.Text != "Saved image: `/ws/pic.jpg`" {
		t.Errorf("content[1] = %+v", msg.Content[1])
	}
}

// A save that fails leaves the image without a path rather than a path to
// nothing.
func TestAFailedSaveReportsNoPath(t *testing.T) {
	save := func(string, []byte) (string, error) { return "", os.ErrPermission }
	msg := streamGeminiImage(t, WithImageSaver(save))
	for _, c := range msg.Content {
		if tb, ok := c.(TextBlock); ok && strings.HasPrefix(tb.Text, "Saved image") {
			t.Errorf("a failed save reported %q", tb.Text)
		}
	}
}

// Vertex serves Gemini through its own transport and takes the same options.
func TestVertexTakesTheImageSaver(t *testing.T) {
	called := false
	c := newVertex(VertexConfig{Project: "p", APIKey: "k"}, WithImageSaver(func(string, []byte) (string, error) { called = true; return "", nil }))
	rc, ok := c.(*renamedClient)
	if !ok {
		t.Fatalf("newVertex returned %T", c)
	}
	g, ok := rc.inner.(*geminiClient)
	if !ok || g.host.saveImage == nil {
		t.Fatalf("the Vertex Gemini client has no saver: %+v", rc.inner)
	}
	_, _ = g.host.saveImage("", nil)
	if !called {
		t.Error("the saver given to newVertex is not the one it kept")
	}
}
