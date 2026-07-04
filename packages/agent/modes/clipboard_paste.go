package modes

import (
	"fmt"
	"regexp"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// clipboardImageAttachment pairs a visible editor placeholder with the image
// it stands for. The user sees "[clipboard image #N]" in the prompt; on
// submit, markers still present are swapped for their images and any the
// user deleted are dropped.
type clipboardImageAttachment struct {
	marker string
	image  provider.ImageBlock
}

// horizWS matches runs of horizontal whitespace (spaces/tabs) but not
// newlines, so tidying a line after marker removal never flattens a
// multi-line prompt.
var horizWS = regexp.MustCompile(`[^\S\n]+`)

// preparePromptWithClipboardImages reconciles submitted text against the
// pending clipboard images: each marker still in the text strips out and
// attaches its image (in paste order); a marker the user deleted drops its
// image. When at least one image attaches, horizontal whitespace left by the
// removed markers is tidied per line (newlines preserved). With nothing
// attached the text is returned byte-for-byte unchanged.
func preparePromptWithClipboardImages(text string, pending []clipboardImageAttachment) (string, []provider.ImageBlock) {
	out := text
	images := make([]provider.ImageBlock, 0, len(pending))
	for _, item := range pending {
		if item.marker == "" || !strings.Contains(out, item.marker) {
			continue
		}
		out = strings.ReplaceAll(out, item.marker, " ")
		images = append(images, item.image)
	}
	if len(images) > 0 {
		lines := strings.Split(out, "\n")
		for j, ln := range lines {
			lines[j] = strings.TrimRight(horizWS.ReplaceAllString(ln, " "), " ")
		}
		out = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return out, images
}

// pasteClipboard reads an image off the system clipboard, stashes it, and
// drops a visible "[clipboard image #N]" marker at the cursor. Bound to
// ctrl+v (clean on macOS / native Linux) and the /paste command (works
// everywhere, including WSL where the terminal may claim ctrl+v). Text paste
// is untouched — it flows through the terminal's bracketed paste as a
// separate event.
func (i *Interactive) pasteClipboard() {
	data, ok, err := tui.ReadClipboardImagePNG()
	if err != nil {
		i.setStatusErr(i18n.T("clipboard paste failed: %s", err))
		return
	}
	if !ok {
		i.setStatusErr(i18n.T("clipboard has no image to paste"))
		return
	}
	marker := fmt.Sprintf("[clipboard image #%d]", len(i.clipboardImages)+1)
	i.clipboardImages = append(i.clipboardImages, clipboardImageAttachment{
		marker: marker,
		image:  provider.ImageBlock{MimeType: "image/png", Data: data},
	})
	i.ed.Insert(marker + " ")
	i.setStatusOK("pasted " + marker + " — send to attach it, or delete the marker to discard")
	i.invalidate()
}
