//go:build terva_acp

package acp

import (
	"encoding/base64"
	"strings"

	"terva.sh/terva/packages/provider"
)

// promptToProvider converts an ACP prompt (a slice of ContentBlocks from
// session/prompt) into the (text, images) pair the core agent's Prompt
// takes. The §5 content-block handling:
//
//   - text          -> appended to the prompt text
//   - image         -> provider.ImageBlock (advertised: promptCapabilities.image)
//   - audio         -> degraded to a text note (advertised off)
//   - resource      -> embedded-context text folded into the prompt, only
//     when embeddedContext is enabled (advertised off this pass, so a
//     resource is folded best-effort but the capability is not promised)
//   - resource_link -> passed through as a text reference (no capability)
//
// Unknown block types are skipped rather than failing the turn (tolerance).
func promptToProvider(blocks []ContentBlock, embeddedContext bool) (string, []provider.ImageBlock) {
	var sb strings.Builder
	var images []provider.ImageBlock

	appendText := func(s string) {
		if s == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(s)
	}

	for _, b := range blocks {
		switch b.Type {
		case ContentText:
			appendText(b.Text)
		case ContentImage:
			if img, ok := decodeImage(b); ok {
				images = append(images, img)
			}
		case ContentAudio:
			appendText("[audio content omitted: unsupported by this agent]")
		case ContentResource:
			if embeddedContext && b.Resource != nil && b.Resource.Text != "" {
				ref := b.Resource.URI
				if ref == "" {
					ref = "embedded resource"
				}
				appendText("Context from " + ref + ":\n" + b.Resource.Text)
			}
		case ContentResourceLink:
			ref := b.URI
			if b.Name != "" {
				ref = b.Name + " (" + b.URI + ")"
			}
			if ref != "" {
				appendText("[linked resource: " + ref + "]")
			}
		}
	}
	return sb.String(), images
}

// decodeImage turns an ACP image block (base64 data + mimeType) into a
// provider.ImageBlock. Returns ok=false when the data can't be decoded so
// a malformed block is dropped rather than aborting the prompt.
func decodeImage(b ContentBlock) (provider.ImageBlock, bool) {
	if b.Data == "" {
		return provider.ImageBlock{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil {
		return provider.ImageBlock{}, false
	}
	return provider.ImageBlock{MimeType: b.MimeType, Data: raw}, true
}

// textContentBlock builds an ACP text ContentBlock — the agent_message_chunk
// payload.
func textContentBlock(text string) *ContentBlock {
	return &ContentBlock{Type: ContentText, Text: text}
}
