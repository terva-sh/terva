package core

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

func TestIsImageRejectionError(t *testing.T) {
	reject := []string{
		"openai-codex: http 400: The image data you provided does not represent a valid image.",
		"http 400: invalid image",
		"unable to process the image you provided",
		"could not decode the image",
		"deepseek: http 400: messages[19]: unknown variant `image_url`, expected `text`",
	}
	for _, s := range reject {
		if !isImageRejectionError(provider.NewAPIError("p", s, false)) {
			t.Errorf("want image-rejection for %q", s)
		}
	}
	notReject := []string{
		"http 429: overloaded_error",
		"http 500: internal error",
		"context deadline exceeded",
		"the image looks great", // success-ish phrasing, no rejection
	}
	for _, s := range notReject {
		if isImageRejectionError(provider.NewAPIError("p", s, false)) {
			t.Errorf("did not want image-rejection for %q", s)
		}
	}
	if isImageRejectionError(nil) {
		t.Error("nil error must not be an image rejection")
	}
}

// imageRejectFakeClient rejects any request that still carries the specific
// "bad" image (matched by its leading bytes), and succeeds once it's gone —
// modelling a backend that 400s on one unreadable image among possibly several.
type imageRejectFakeClient struct {
	bad   []byte
	calls int32
}

func (c *imageRejectFakeClient) Name() string { return "image-reject-fake" }

func (c *imageRejectFakeClient) hasBad(req provider.Request) bool {
	isBad := func(b provider.ImageBlock) bool { return bytes.HasPrefix(b.Data, c.bad) }
	for _, m := range req.Messages {
		for _, blk := range m.Content {
			switch v := blk.(type) {
			case provider.ImageBlock:
				if isBad(v) {
					return true
				}
			case provider.ToolResultBlock:
				for _, inner := range v.Content {
					if ib, ok := inner.(provider.ImageBlock); ok && isBad(ib) {
						return true
					}
				}
			}
		}
	}
	return false
}

func (c *imageRejectFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	bad := c.hasBad(req)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "image-reject-fake", Model: req.Model}
		if bad {
			out <- provider.EventDone{Stop: provider.StopError,
				Err: provider.NewAPIError("image-reject-fake",
					"http 400: The image data you provided does not represent a valid image.", false)}
			return
		}
		out <- provider.EventTextDelta{Delta: "ok"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

func imageMsgs(a *Agent) []provider.ImageBlock {
	var out []provider.ImageBlock
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			switch v := b.(type) {
			case provider.ImageBlock:
				out = append(out, v)
			case provider.ToolResultBlock:
				for _, inner := range v.Content {
					if ib, ok := inner.(provider.ImageBlock); ok {
						out = append(out, ib)
					}
				}
			}
		}
	}
	return out
}

// The common case: one bad image, recovered in a single round — the turn
// succeeds, the image becomes a note, and there's exactly one extra round-trip.
func TestImageRecoverySingle(t *testing.T) {
	bad := []byte{0xBA, 0xDD}
	client := &imageRejectFakeClient{bad: bad}
	a := NewAgent(client, "gpt-5.5", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.ImageBlock{MimeType: "image/png", Data: bad}}},
	})

	if err := a.Prompt(context.Background(), "continue", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt returned %v (recovery should let the turn succeed)", err)
	}
	if got := atomic.LoadInt32(&client.calls); got != 2 {
		t.Fatalf("Stream calls = %d; want 2 (reject, then recover)", got)
	}
	if imgs := imageMsgs(a); len(imgs) != 0 {
		t.Fatalf("want 0 images after recovery, got %d", len(imgs))
	}
	noteSeen := false
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if tb, ok := b.(provider.TextBlock); ok && strings.Contains(tb.Text, "image omitted") {
				noteSeen = true
			}
		}
	}
	if !noteSeen {
		t.Error("recovery note was not inserted in place of the image")
	}
}

// Recovery fires OnImageExcluded with the image's content hash so the host can
// persist an exclude_image directive (pay the recovery once).
func TestImageRecoveryFiresExclusionHook(t *testing.T) {
	bad := []byte{0xBA, 0xDD}
	client := &imageRejectFakeClient{bad: bad}
	a := NewAgent(client, "gpt-5.5", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond
	var excluded []string
	a.OnImageExcluded = func(sha string) { excluded = append(excluded, sha) }
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.ImageBlock{MimeType: "image/png", Data: bad}}},
	})
	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 1 || excluded[0] != imageSHA256(bad) {
		t.Fatalf("OnImageExcluded = %v; want one hash %s", excluded, imageSHA256(bad))
	}
}

// Newest-first isolation: a bad image sits between two good ones. Recovery peels
// the newer good image, then the bad one, and stops — so the OLDER good image
// (before the culprit) survives and the cached prefix up to it is untouched.
func TestImageRecoveryNewestFirstPreservesOlder(t *testing.T) {
	good1 := []byte{0x60, 0x01} // oldest, before the bad one — must survive
	bad := []byte{0xBA, 0xDD}   // the culprit
	good2 := []byte{0x60, 0x02} // newer than the bad one — collateral
	client := &imageRejectFakeClient{bad: bad}
	a := NewAgent(client, "gpt-5.5", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond
	img := func(d []byte) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.ImageBlock{MimeType: "image/png", Data: d}}}
	}
	a.SetMessages([]provider.Message{img(good1), img(bad), img(good2)})

	if err := a.Prompt(context.Background(), "continue", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	// reject(good1,bad,good2) → peel good2 → reject(good1,bad) → peel bad → ok.
	if got := atomic.LoadInt32(&client.calls); got != 3 {
		t.Fatalf("Stream calls = %d; want 3", got)
	}
	imgs := imageMsgs(a)
	if len(imgs) != 1 || !bytes.Equal(imgs[0].Data, good1) {
		t.Fatalf("want exactly the older good image preserved, got %d images: %+v", len(imgs), imgs)
	}
}
