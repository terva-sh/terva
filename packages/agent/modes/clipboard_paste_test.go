package modes

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

func clipImg(marker, data string) clipboardImageAttachment {
	return clipboardImageAttachment{marker: marker, image: provider.ImageBlock{MimeType: "image/png", Data: []byte(data)}}
}

func TestPreparePromptStripsMarkerAndAttaches(t *testing.T) {
	pending := []clipboardImageAttachment{clipImg("[clipboard image #1]", "png-1")}
	text, images := preparePromptWithClipboardImages("describe this [clipboard image #1] please", pending)
	if text != "describe this please" {
		t.Fatalf("text = %q, want %q", text, "describe this please")
	}
	if len(images) != 1 || string(images[0].Data) != "png-1" {
		t.Fatalf("images = %+v", images)
	}
}

func TestPreparePromptImageOnly(t *testing.T) {
	pending := []clipboardImageAttachment{clipImg("[clipboard image #1]", "png-1")}
	text, images := preparePromptWithClipboardImages("[clipboard image #1]", pending)
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if len(images) != 1 {
		t.Fatalf("len(images) = %d, want 1", len(images))
	}
}

// A deleted marker drops its image, and because nothing attached the prompt
// is returned byte-for-byte — multi-line and internal spacing preserved.
func TestPreparePromptDeletedMarkerKeepsTextVerbatim(t *testing.T) {
	pending := []clipboardImageAttachment{clipImg("[clipboard image #1]", "png-1")}
	in := "line one\nline  two"
	text, images := preparePromptWithClipboardImages(in, pending)
	if text != in {
		t.Fatalf("text = %q, want unchanged %q", text, in)
	}
	if len(images) != 0 {
		t.Fatalf("len(images) = %d, want 0", len(images))
	}
}

func TestPreparePromptMultipleInPasteOrder(t *testing.T) {
	pending := []clipboardImageAttachment{
		clipImg("[clipboard image #1]", "png-1"),
		clipImg("[clipboard image #2]", "png-2"),
	}
	text, images := preparePromptWithClipboardImages("compare [clipboard image #2] with [clipboard image #1]", pending)
	if text != "compare with" {
		t.Fatalf("text = %q, want %q", text, "compare with")
	}
	if len(images) != 2 || string(images[0].Data) != "png-1" || string(images[1].Data) != "png-2" {
		t.Fatalf("images not in paste order: %q, %q", string(images[0].Data), string(images[1].Data))
	}
}

// Attaching tidies horizontal whitespace from the removed marker but keeps
// newlines, so a multi-line prompt survives the paste.
func TestPreparePromptPreservesNewlines(t *testing.T) {
	pending := []clipboardImageAttachment{clipImg("[clipboard image #1]", "png-1")}
	text, images := preparePromptWithClipboardImages("first line [clipboard image #1]\nsecond line", pending)
	if text != "first line\nsecond line" {
		t.Fatalf("text = %q, want newline preserved", text)
	}
	if len(images) != 1 {
		t.Fatalf("len(images) = %d, want 1", len(images))
	}
}

func TestPreparePromptDuplicateMarkerAttachesOnce(t *testing.T) {
	pending := []clipboardImageAttachment{clipImg("[clipboard image #1]", "png-1")}
	text, images := preparePromptWithClipboardImages("[clipboard image #1] and again [clipboard image #1]", pending)
	if text != "and again" {
		t.Fatalf("text = %q, want %q", text, "and again")
	}
	if len(images) != 1 {
		t.Fatalf("len(images) = %d, want 1", len(images))
	}
}
