package dialogs

// The /shared panel's own rendering of a name it did not write.
//
// A row here is worse-placed than a transcript card: it is padded and colour
// filled to a fixed width, and the selected row is a highlight block. An escape
// inside that block does not merely corrupt the name — it survives into the
// padding and into every row painted after it, which is how one hostile
// filename repaints a panel.
//
// The escape-byte count is taken against a benign control render, because the
// theme emits escapes of its own. The question is not "does this row contain
// escapes" but "did the name contribute any".

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// rawShared renders the panel with its styling intact.
func rawShared(d *SharedDialog) string {
	return strings.Join(d.Render(tui.Dark, 80), "\n")
}

// A hostile name contributes no escape bytes to the panel, on the selected row
// or any other.
func TestSharedRowNeutralisesAHostileName(t *testing.T) {
	benign := rawShared(openShared([]ctrlproto.SharedFileEntry{
		sharedEntry("shr_a", "report.pdf", "document", 2048),
	}))
	hostile := rawShared(openShared([]ctrlproto.SharedFileEntry{
		{
			SharedFile: core.SharedFile{
				ID:   "shr_a",
				Name: "report\x1b[31m\x1b[2J\x1b]0;pwned\x07.pdf",
				Kind: "document",
				Size: 2048,
			},
			Path: "/daemon/shr_a-report.pdf",
		},
	}))

	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the name contributed %d escape bytes to the panel", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the panel:\n%s", hostile)
	}
	if !strings.Contains(sharedBody(openShared([]ctrlproto.SharedFileEntry{
		{
			SharedFile: core.SharedFile{ID: "shr_a", Name: "report\x1b[31m.pdf", Kind: "document", Size: 2048},
			Path:       "/daemon/shr_a-report.pdf",
		},
	})), "report.pdf") {
		t.Error("the readable part of the name should survive")
	}
}

// The expiry refusal interpolates the name into a sentence, so an escape there
// repaints the notice line rather than merely garbling the name.
func TestExpiryNoticeNeutralisesAHostileName(t *testing.T) {
	expired := func(name string) *SharedDialog {
		d := openShared([]ctrlproto.SharedFileEntry{
			{
				SharedFile: core.SharedFile{
					ID:        "shr_a",
					Name:      name,
					Kind:      "document",
					Size:      2048,
					ExpiresAt: sharedDialogNow.Add(-time.Hour).Format(time.RFC3339),
				},
				Path: "/daemon/shr_a-report.pdf",
			},
		})
		// c is copy: any verb takes the expiry refusal.
		d.HandleKey(tui.Key{Rune: 'c'})
		return d
	}

	benign := rawShared(expired("report.pdf"))
	hostile := rawShared(expired("report\x1b[31m\x1b[2J\x1b]0;pwned\x07.pdf"))

	if !strings.Contains(sharedBody(expired("report.pdf")), "has expired") {
		t.Fatal("the fixture did not produce an expiry notice")
	}
	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the name contributed %d escape bytes to the notice", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the notice:\n%s", hostile)
	}
}

// A name that sanitizes to nothing must fall back rather than leave a blank row.
func TestARowNameOfPureEscapesFallsBack(t *testing.T) {
	body := sharedBody(openShared([]ctrlproto.SharedFileEntry{
		{
			SharedFile: core.SharedFile{ID: "shr_a", Name: "\x1b[31m\x1b[0m", Kind: "document", Size: 2048},
			Path:       "/daemon/shr_a-report.pdf",
		},
	}))
	if !strings.Contains(body, "(unnamed)") {
		t.Errorf("a name that sanitizes away should read as unnamed:\n%s", body)
	}
}
