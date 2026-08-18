package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/provider"
)

// MetaSharedKey is the tool-role message Meta key carrying the files a step's
// tool calls published for the user. It is the string form of core.MetaShared.
//
// A string literal rather than the constant, because packages/tui does not
// import packages/core and must not start: the renderer already reads
// "compaction", "tokens_before" and "clear" the same way (see hashMessage), and
// a rendering package that depends on the agent core is the dependency this
// tree is organised to avoid.
//
// Exported so the duplication is checkable. Two constants holding the same
// string in packages that cannot see each other is a silent break waiting for
// whoever renames one, so a package that imports BOTH asserts they agree — see
// TestTUISharedMetaKeyMatchesCore in packages/agent/modes.
const MetaSharedKey = "shared"

// SharedFile is one file the agent handed to the user through share_file: the
// renderer's view of core.SharedFile, carrying only the fields a card shows.
//
// Decoded from the message Meta rather than delivered as a typed value for the
// import reason above. Unknown fields are ignored by encoding/json, so a newer
// daemon adding one cannot break an older renderer.
type SharedFile struct {
	ID      string `json:"id"`
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Mime    string `json:"mime,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Caption string `json:"caption,omitempty"`
	// ExpiresAt (RFC3339) is when the store's sweeper may remove the bytes. An
	// upper bound, not a promise, and empty from a daemon that does not send it
	// — which a reader must treat as "unknown", never as "expired".
	ExpiresAt string `json:"expires_at,omitempty"`
}

// parseSharedFiles decodes the shares recorded on a tool-role message.
//
// A record that will not parse yields nothing rather than an error: the cost is
// the user's cards, and the alternative — failing the message — costs them the
// conversation. That is the same trade core.messageToWire makes on the way out.
func parseSharedFiles(meta map[string]string) []SharedFile {
	raw := meta[MetaSharedKey]
	if raw == "" {
		return nil
	}
	var out []SharedFile
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// sharedFilesFor returns the shares belonging to one tool call.
//
// Filtered by CallID because a single tool-role message batches a whole step's
// results: without the filter, every box in a step that shared anything would
// claim every one of that step's files.
func sharedFilesFor(files []SharedFile, callID string) []SharedFile {
	var out []SharedFile
	for _, f := range files {
		if f.CallID == callID {
			out = append(out, f)
		}
	}
	return out
}

// shareKindGlyph is the leading mark for a card, by kind. Distinct glyphs let
// the eye sort a list of shares without reading any of it; the fallback covers
// a kind a newer daemon invented.
func shareKindGlyph(kind string) string {
	switch kind {
	case "image":
		return "🖼"
	case "audio":
		return "♪"
	case "video":
		return "▶"
	default:
		return "⇩"
	}
}

// humanShareBytes renders a file size for a card. Whole units below the
// thousands mark and one decimal above, so "980 B" and "2.0 KB" both read as
// sizes at a glance rather than as digit counts to parse.
func humanShareBytes(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

// shareExpiry renders the remaining lifetime of a share, given the clock.
//
// Three outcomes, and the difference between the last two matters: an empty
// string when the daemon sent no expiry (unknown — say nothing rather than
// guess), "expired" once the sweeper is entitled to the bytes, and otherwise
// the coarse time left. Coarse on purpose: the value is an upper bound that cap
// eviction can beat, so a countdown to the minute would claim a precision the
// record does not have.
func shareExpiry(expiresAt string, now time.Time) string {
	if expiresAt == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return ""
	}
	left := t.Sub(now)
	switch {
	case left <= 0:
		return "expired"
	case left < time.Hour:
		return fmt.Sprintf("%dm left", int(left.Minutes()))
	case left < 24*time.Hour:
		return fmt.Sprintf("%dh left", int(left.Hours()))
	}
	return fmt.Sprintf("%dd left", int(left.Hours()/24))
}

// renderSharedFileCards renders the cards for one tool call's shares: what was
// handed over, and — for an image whose bytes this client has — a look at it.
//
// The cards sit BELOW the tool box rather than inside it. The box is the call's
// mechanics (what ran, what it printed); a shared file is the deliverable, and
// burying the deliverable in the machinery is what the transcript already did
// by rendering nothing at all. They are indented to the box's own margin so the
// two read as one column.
//
// Rows are returned raw, with image footprints still tagged by their sentinel:
// the caller wraps or strips them, exactly as it does for tool-result content.
func (v *View) renderSharedFileCards(files []SharedFile, width int) []string {
	var lines []string
	for _, f := range files {
		lines = append(lines, v.renderSharedFileCard(f, width)...)
	}
	return lines
}

func (v *View) renderSharedFileCard(f SharedFile, width int) []string {
	th := v.Theme
	margin := strings.Repeat(" ", toolBoxOuterMargin)

	// Sanitized at the render boundary rather than on the way in: the record
	// keeps what was actually shared, and on `terva attach` this is the first
	// point that sees a value from a daemon this client does not control. A
	// name that sanitizes to nothing is treated as no name at all.
	name := SanitizeLabel(f.Name)
	if name == "" {
		name = "(unnamed)"
	}

	// The detail row: kind, size, and expiry, each omitted when it has nothing
	// to say. An expired share keeps its card — the transcript still records
	// what was handed over — but says so, and in the error colour, because the
	// alternative is a user hunting for a file the sweeper already took.
	var details []string
	if f.Kind != "" {
		details = append(details, f.Kind)
	}
	if size := humanShareBytes(f.Size); size != "" {
		details = append(details, size)
	}
	expiry := shareExpiry(f.ExpiresAt, v.now())
	if expiry != "" {
		details = append(details, expiry)
	}

	head := margin + "  " + th.FG256(th.Accent, shareKindGlyph(f.Kind)+" "+name)
	if len(details) > 0 {
		colour := th.Muted
		if expiry == "expired" {
			colour = th.Error
		}
		head += th.FG256(colour, "  "+strings.Join(details, " · "))
	}
	lines := appendTruncated(nil, head, width)

	// The caption is the one field here the MODEL writes freely — share_file
	// passes the tool argument through untouched — so a prompt-injected agent
	// picks it. Sanitized after the emptiness check, so a caption of nothing
	// but escapes draws no line at all rather than an empty one.
	if caption := SanitizeLabel(f.Caption); caption != "" {
		lines = appendTruncated(lines, margin+"    "+th.FG256(th.Muted, caption), width)
	}

	// The preview, when this client fetched the bytes and the terminal can draw
	// them. Absent bytes are the normal case for everything that is not an
	// image, and for an image whose fetch has not landed yet, so the card is
	// complete without one rather than degraded by its absence.
	if data := v.sharedPreview(f.ID); len(data) > 0 {
		for _, row := range v.renderImageBlockData(data, f.Mime, width) {
			lines = append(lines, row)
		}
	}
	return lines
}

// appendTruncated appends a rendered row clipped to the terminal width, so a
// long name or caption cannot spill past the right edge and wrap.
func appendTruncated(lines []string, row string, width int) []string {
	return append(lines, truncateToWidth(row, width))
}

// now is the clock the expiry column reads. A field so a test can pin it:
// "6d left" has to be assertable without the test being a race against the
// TTL it was written under.
func (v *View) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// sharedPreview returns the image bytes this client holds for a share, or nil.
//
// The renderer never reads the file itself. A share is a handle resolved by the
// daemon, which may be on another machine (terva attach), and a renderer that
// reached for a local path would draw the right picture on one host and the
// wrong one — or someone else's — on another. Interactive fetches through the
// carrier and fills this map; the card is complete without it.
func (v *View) sharedPreview(id string) []byte {
	if v.SharedPreviews == nil || id == "" {
		return nil
	}
	return v.SharedPreviews[id]
}

// sharedPreviewSig fingerprints the preview bytes this client holds for the
// shares m carries, for the render cache key.
//
// hashMessage already covers Meta["shared"], but that is the share HANDLE,
// which arrives with the message. The picture does not: interactive fetches it
// through the carrier afterwards and drops it into SharedPreviews, leaving the
// handle — and so the message hash — unchanged. Without this component the
// cache hits on the row it painted before the bytes landed, and the card keeps
// its empty frame for the rest of the session; only a resize, ctrl+o or
// /compact happens to evict it.
//
// Length rather than content, the same trade the tool-result case in
// hashMessage makes: a fetch either has the bytes or does not, and the carrier
// does not swap a drawn image for a different one of identical length.
func (v *View) sharedPreviewSig(m provider.Message) uint64 {
	// The overwhelmingly common case is a message with no shares at all, and
	// this runs for every message on every frame — so no JSON parse unless
	// there is something to parse.
	if m.Meta[MetaSharedKey] == "" {
		return 0
	}
	h := fnv64aInit
	for _, f := range parseSharedFiles(m.Meta) {
		h = fnv64aWrite(h, []byte(f.ID))
		h = fnv64aWriteUint(h, uint64(len(v.sharedPreview(f.ID))))
	}
	return h
}
