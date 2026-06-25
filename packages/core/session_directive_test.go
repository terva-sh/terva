package core

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// An exclude_image directive persists across a reload: every copy of the
// fingerprinted image (a tool result and the codex mirror) comes back as the
// rejected-image note, while other images are untouched — so a resumed session
// never re-sends a provider-rejected image. Append-only: the raw rows stay on
// disk, the loader applies the directive when rebuilding the transcript.
func TestImageExclusionDirectiveRoundTrip(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-codex", "gpt-5.5", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}

	badData := []byte{0x89, 0x50, 0x4e, 0x47, 0xBA, 0xDD} // the image the provider rejected
	goodData := []byte{0x89, 0x50, 0x4e, 0x47, 0x60, 0x0D}
	badHash := imageSHA256(badData)

	// A read tool result carrying the bad image, then the codex mirror with the
	// same bytes, plus a later good image that must survive.
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(provider.Message{Role: provider.RoleTool, Content: []provider.Content{
		provider.ToolResultBlock{CallID: "c1", Content: []provider.Content{
			provider.ImageBlock{MimeType: "image/png", Data: badData},
		}},
	}}))
	must(s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{
		provider.ImageBlock{MimeType: "image/png", Data: badData}, // the mirror copy
	}}))
	must(s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{
		provider.ImageBlock{MimeType: "image/png", Data: goodData},
	}}))
	// The directive recovery would write after a 400.
	must(s.AppendImageExclusion(badHash, "rejected"))
	must(s.Close())

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	// OpenSession reopens the file for appending; Windows can't remove the
	// TempDir while that handle is live.
	defer reopened.Close()

	badImages, goodImages, notes := 0, 0, 0
	for _, m := range msgs {
		walk := func(c provider.Content) {
			switch v := c.(type) {
			case provider.ImageBlock:
				if string(v.Data) == string(badData) {
					badImages++
				} else {
					goodImages++
				}
			case provider.TextBlock:
				if strings.Contains(v.Text, "image omitted") {
					notes++
				}
			}
		}
		for _, c := range m.Content {
			if tr, ok := c.(provider.ToolResultBlock); ok {
				for _, inner := range tr.Content {
					walk(inner)
				}
				continue
			}
			walk(c)
		}
	}
	if badImages != 0 {
		t.Errorf("excluded image survived the reload (%d copies)", badImages)
	}
	if notes != 2 {
		t.Errorf("want 2 rejected-image notes (tool result + mirror), got %d", notes)
	}
	if goodImages != 1 {
		t.Errorf("the unrelated good image must survive, got %d", goodImages)
	}
}
