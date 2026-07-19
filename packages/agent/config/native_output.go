package config

// NativeOutputConfig configures native (in-protocol) image output: the model
// drawing images inline in its own replies via the provider's built-in image
// tool (the OpenAI Responses image_generation tool, on the Codex subscription
// backend). This is deliberately SEPARATE from the `image` block, which gates
// the generate_image tool that calls a standalone image endpoint — the two are
// independent mechanisms and can be enabled in any combination.
//
// See docs/proposals/native-image-output.md.
type NativeOutputConfig struct {
	// Enabled turns native image output on. Default OFF: it spends money with
	// no per-call approval (a native image is not a terva tool call), so it is
	// opt-in and only takes effect on a model that advertises image output.
	Enabled *bool `json:"enabled,omitempty"`

	// Size and Quality configure the built-in tool. Empty uses the provider
	// default. Size: "1024x1024" | "1024x1536" | "1536x1024" | "auto".
	// Quality: "low" | "medium" | "high" | "auto".
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`

	// EditHistory bounds how many of the model's most recent generated images
	// stay editable ("now make it blue"). Each retained image is re-sent to the
	// model WITH ITS BYTES on every following turn, so raising this increases
	// per-turn cost, latency, and context use by roughly one image apiece for as
	// long as it stays in the window. nil defaults to 1 (only the most recent
	// image is editable); 0 disables editing (generation only, cheapest).
	EditHistory *int `json:"edit_history,omitempty"`
}

// EditHistoryOr returns the configured edit-history depth, or def when unset.
func (n *NativeOutputConfig) EditHistoryOr(def int) int {
	if n == nil || n.EditHistory == nil {
		return def
	}
	return *n.EditHistory
}

// IsEnabled reports whether native image output is opted in.
func (n *NativeOutputConfig) IsEnabled() bool {
	return n != nil && n.Enabled != nil && *n.Enabled
}
