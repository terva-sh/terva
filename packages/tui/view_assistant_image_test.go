package tui

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// An image the assistant drew inline (native image output) renders in the
// assistant body. Before the RoleAssistant switch grew an ImageBlock case it
// fell through silently. With no terminal image protocol, renderImageBlock
// emits a metadata caption, which is what this asserts.
func TestAssistantInlineImageRenders(t *testing.T) {
	v := View{
		Theme: Dark,
		Messages: []provider.Message{{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.TextBlock{Text: "here is your picture"},
				provider.ImageBlock{MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nfake"), ID: "ig_1"},
			},
		}},
	}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "here is your picture") {
		t.Fatalf("assistant prose missing:\n%s", plain)
	}
	if !strings.Contains(plain, "image/png") {
		t.Fatalf("assistant inline image did not render:\n%s", plain)
	}
}
