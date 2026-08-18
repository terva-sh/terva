//go:build terva_acp

package acp

// promptToProvider is the whole client -> terva half of the ACP translation
// layer, and it had no unit test. The wire harness only ever sends
// {"type":"text"} prompt blocks, so the text branch was exercised incidentally
// and image, audio, resource and resource_link translation were unpinned.
//
// The failure that costs the most is silent: break decodeImage -- switch it to
// base64.URLEncoding, or let the client start sending a `data:` URI prefix --
// and it returns ok=false, the image is dropped, and the turn runs with text
// only. The model then answers about a screenshot it never saw, confidently and
// with no error anywhere, while the suite stays green.

import (
	"encoding/base64"
	"testing"
)

func TestPromptToProviderTranslatesEveryContentBlockType(t *testing.T) {
	const pngBytes = "\x89PNG\r\n\x1a\npixels"
	stdB64 := base64.StdEncoding.EncodeToString([]byte(pngBytes))

	type img struct {
		mime string
		data string
	}
	cases := []struct {
		name     string
		blocks   []ContentBlock
		embedded bool
		wantText string
		wantImgs []img
	}{{
		name: "text blocks join in order, newline separated",
		blocks: []ContentBlock{
			{Type: ContentText, Text: "first"},
			{Type: ContentText, Text: "second"},
		},
		wantText: "first\nsecond",
	}, {
		name:     "an empty text block adds no separator",
		blocks:   []ContentBlock{{Type: ContentText, Text: "only"}, {Type: ContentText, Text: ""}},
		wantText: "only",
	}, {
		name:     "a valid image decodes and carries its mime type",
		blocks:   []ContentBlock{{Type: ContentImage, Data: stdB64, MimeType: "image/png"}},
		wantImgs: []img{{mime: "image/png", data: pngBytes}},
	}, {
		name:   "undecodable base64 is dropped, not fatal",
		blocks: []ContentBlock{{Type: ContentImage, Data: "!!!not base64!!!", MimeType: "image/png"}},
	}, {
		// Pins the ALPHABET, not just "some base64 works". The url-safe
		// alphabet's - and _ are invalid under StdEncoding, so this block must
		// drop; if someone swaps the decoder to base64.URLEncoding it would
		// start decoding here and this case turns red -- which is the only
		// signal that the wire contract moved.
		name:   "the url-safe alphabet is not accepted",
		blocks: []ContentBlock{{Type: ContentImage, Data: "-_-_", MimeType: "image/png"}},
	}, {
		name:   "an image with no data is dropped",
		blocks: []ContentBlock{{Type: ContentImage, MimeType: "image/png"}},
	}, {
		name:     "audio degrades to a note the model can read",
		blocks:   []ContentBlock{{Type: ContentAudio, Data: stdB64, MimeType: "audio/wav"}},
		wantText: "[audio content omitted: unsupported by this agent]",
	}, {
		name: "a resource folds in when embedded context is on",
		blocks: []ContentBlock{{Type: ContentResource, Resource: &EmbeddedResource{
			URI: "file:///notes.md", Text: "the body",
		}}},
		embedded: true,
		wantText: "Context from file:///notes.md:\nthe body",
	}, {
		name: "the same resource is silent when embedded context is off",
		blocks: []ContentBlock{{Type: ContentResource, Resource: &EmbeddedResource{
			URI: "file:///notes.md", Text: "the body",
		}}},
		embedded: false,
	}, {
		name:     "a resource with no uri still names itself",
		blocks:   []ContentBlock{{Type: ContentResource, Resource: &EmbeddedResource{Text: "anonymous"}}},
		embedded: true,
		wantText: "Context from embedded resource:\nanonymous",
	}, {
		name:     "a nil resource cannot panic the turn",
		blocks:   []ContentBlock{{Type: ContentResource}},
		embedded: true,
	}, {
		name:     "a resource_link prefers its name",
		blocks:   []ContentBlock{{Type: ContentResourceLink, URI: "file:///a.go", Name: "a.go"}},
		wantText: "[linked resource: a.go (file:///a.go)]",
	}, {
		name:     "a resource_link without a name falls back to the uri",
		blocks:   []ContentBlock{{Type: ContentResourceLink, URI: "file:///a.go"}},
		wantText: "[linked resource: file:///a.go]",
	}, {
		name:   "an empty resource_link adds nothing",
		blocks: []ContentBlock{{Type: ContentResourceLink}},
	}, {
		name:     "an unknown block type is skipped, not fatal",
		blocks:   []ContentBlock{{Type: "video"}, {Type: ContentText, Text: "kept"}},
		wantText: "kept",
	}, {
		// The realistic prompt: prose around a screenshot. Text order must
		// survive an interleaved image, and the image must survive the text.
		name: "text order survives an interleaved image",
		blocks: []ContentBlock{
			{Type: ContentText, Text: "look at"},
			{Type: ContentImage, Data: stdB64, MimeType: "image/png"},
			{Type: ContentText, Text: "this bug"},
		},
		wantText: "look at\nthis bug",
		wantImgs: []img{{mime: "image/png", data: pngBytes}},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, images := promptToProvider(tc.blocks, tc.embedded)
			if text != tc.wantText {
				t.Errorf("text =\n%q\nwant\n%q", text, tc.wantText)
			}
			if len(images) != len(tc.wantImgs) {
				t.Fatalf("got %d images, want %d", len(images), len(tc.wantImgs))
			}
			for i, w := range tc.wantImgs {
				if images[i].MimeType != w.mime {
					t.Errorf("image %d mime = %q, want %q", i, images[i].MimeType, w.mime)
				}
				if string(images[i].Data) != w.data {
					t.Errorf("image %d data = %q, want %q", i, images[i].Data, w.data)
				}
			}
		})
	}
}

// decodeImage's contract is that a bad block is DROPPED rather than fatal, and
// that the bytes arrive whole. Asserted directly as well as through
// promptToProvider, because the drop is the branch a caller cannot observe: a
// prompt that silently loses its only image is indistinguishable from one that
// never carried one.
func TestDecodeImageDropsWhatItCannotDecode(t *testing.T) {
	raw := []byte{0x00, 0xff, 0x10, 0x80}
	good := ContentBlock{Data: base64.StdEncoding.EncodeToString(raw), MimeType: "image/jpeg"}
	got, ok := decodeImage(good)
	if !ok {
		t.Fatal("a well-formed standard-base64 block did not decode")
	}
	if got.MimeType != "image/jpeg" || string(got.Data) != string(raw) {
		t.Errorf("decoded to %+v, want the original bytes under image/jpeg", got)
	}

	for _, bad := range []ContentBlock{
		{Data: "", MimeType: "image/png"},
		{Data: "not^base^64", MimeType: "image/png"},
		{Data: "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), MimeType: "image/png"},
	} {
		if _, ok := decodeImage(bad); ok {
			t.Errorf("decodeImage accepted %q — a block it cannot honestly decode", bad.Data)
		}
	}
	// The provider.ImageBlock zero value is what a dropped block yields; a
	// caller appending it unconditionally would send an empty image to the wire.
	if blk, _ := decodeImage(ContentBlock{Data: "!!", MimeType: "image/png"}); blk.MimeType != "" || len(blk.Data) != 0 {
		t.Errorf("a rejected block returned %+v, want the zero ImageBlock", blk)
	}
}
