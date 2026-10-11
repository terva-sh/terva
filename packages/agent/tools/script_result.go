//go:build terva_scripting

package tools

import (
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Match the scripting engine's default per-binding import budget.
const maxScriptResultBytes = 1 << 20

func scriptResultEnvelope(res core.ToolResult) (any, error) {
	if res.StructuredContentOmitted || len(res.StructuredContent) > maxScriptResultBytes {
		return nil, fmt.Errorf("structured result exceeds the 1 MiB import limit")
	}
	attachments := []map[string]any{}
	for _, block := range res.Content {
		switch b := block.(type) {
		case provider.TextBlock:
		case provider.ImageBlock:
			attachments = append(attachments, map[string]any{"type": "image", "mimeType": b.MimeType, "bytes": len(b.Data), "omitted": true})
		default:
			attachments = append(attachments, map[string]any{"type": "other", "omitted": true})
		}
	}
	envelope := struct {
		Text              string           `json:"text"`
		StructuredContent json.RawMessage  `json:"structuredContent"`
		IsError           bool             `json:"isError"`
		Attachments       []map[string]any `json:"attachments"`
	}{textFromContent(res.Content), res.StructuredContent, res.IsError, attachments}
	if len(envelope.Text) > maxScriptResultBytes {
		return nil, fmt.Errorf("tool result exceeds the 1 MiB import limit")
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("tool returned invalid structured JSON")
	}
	if len(encoded) > maxScriptResultBytes {
		return nil, fmt.Errorf("tool result exceeds the 1 MiB import limit")
	}
	// JSON normalization prevents Go objects or methods from entering the VM.
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("tool returned invalid structured JSON")
	}
	return out, nil
}
