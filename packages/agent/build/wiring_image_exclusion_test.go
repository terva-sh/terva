package build

// Image-rejection recovery must be PERSISTED, not just performed.
//
// The agent drops an image the provider 400'd on and fires its image-excluded
// observers; core.Session can write an exclude_image directive; the session
// loader re-applies it on resume. Every piece existed — and nothing joined
// them, because the hook was an assignable field nobody assigned. So each
// resume re-sent the rejected image and re-failed, though the doc comments on
// core.Agent.OnImageExcluded and core.Session.AppendImageExclusion both
// promised "the recovery is paid once".
//
// WireHeadlessSessionPersist now registers the missing observer. This test
// drives a real turn through a provider that rejects the image and asserts the
// directive lands on disk and survives a reload. Deleting the registration
// makes it fail.

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// imageRejectClient 400s on any request still carrying the bad image bytes,
// exactly as a real provider does, and otherwise answers normally.
type imageRejectClient struct{ bad []byte }

func (c *imageRejectClient) Name() string { return "image-reject-fake" }

func (c *imageRejectClient) hasBad(req provider.Request) bool {
	for _, m := range req.Messages {
		for _, blk := range m.Content {
			if ib, ok := blk.(provider.ImageBlock); ok && bytes.HasPrefix(ib.Data, c.bad) {
				return true
			}
		}
	}
	return false
}

func (c *imageRejectClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	bad := c.hasBad(req)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		if bad {
			out <- provider.EventDone{Stop: provider.StopError,
				Err: provider.NewAPIError(c.Name(),
					"http 400: The image data you provided does not represent a valid image.", false)}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

func TestWiredPersistenceRecordsImageExclusion(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/ws", "openai", "gpt-5.5", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}

	bad := []byte{0xBA, 0xDD}
	imageTurn := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.ImageBlock{MimeType: "image/png", Data: bad}},
	}

	// An EARLIER turn already persisted the image. This is the whole scenario:
	// the bytes are on disk, so only a directive can stop a resume from
	// re-sending them. (SetMessages seeds the in-memory transcript without
	// firing message observers, so seeding alone would leave the JSONL empty and
	// make the reload assertion below vacuously true — it did, until this
	// append was added and the ablation caught it.)
	if err := sess.AppendMessage(imageTurn); err != nil {
		t.Fatalf("seed image turn: %v", err)
	}
	if !sessionHasImage(t, path, bad) {
		t.Fatal("seeded image is not on disk; the test cannot detect the bug")
	}

	ag := core.NewAgent(&imageRejectClient{bad: bad}, "gpt-5.5", "system", core.Registry{})
	ag.RetryBaseDelay = time.Millisecond

	WireHeadlessSessionPersist(ag, sess)

	ag.SetMessages([]provider.Message{imageTurn})
	if err := ag.Prompt(context.Background(), "go", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// The raw image row is append-only and still in the file (audit); what must
	// have changed is that a directive now sits beside it.
	if !sessionHasImage(t, path, bad) {
		t.Fatal("the raw image row vanished; this session log is append-only")
	}

	// Reload: the loader must apply the directive, so the rejected image never
	// comes back into the transcript the next turn would send.
	_, msgs, err := core.OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, m := range msgs {
		for _, blk := range m.Content {
			if ib, ok := blk.(provider.ImageBlock); ok && bytes.HasPrefix(ib.Data, bad) {
				t.Fatal("the provider-rejected image survived a reload: " +
					"the exclude_image directive was never persisted, so the recovery " +
					"is paid again on every resume")
			}
		}
	}
}

// sessionHasImage reports whether the raw JSONL still carries the image bytes,
// independent of the loader's directive handling — so the test can tell "the
// directive worked" apart from "the image was never written".
func sessionHasImage(t *testing.T, path string, data []byte) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	return bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(data)))
}
