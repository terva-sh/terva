package provider

// imageInputOmittedNote replaces an image bound for a model that does not
// accept image input.
//
// The capability's doc says such images are "dropped at serialization", and
// the one builder that enforced it dropped them outright. Doing that at the
// message level on the other four wires produces an EMPTY content array, which
// is the very 400 this capability exists to prevent (Anthropic and Bedrock
// both reject a message with no content). A note keeps the message well-formed
// and tells the model why there is a gap, instead of leaving it to answer a
// question about a picture it was never told existed.
//
// It reads like core's imageRejectedNote on purpose — that is the same event
// from the other direction (there the provider refused an image; here terva
// declines to send one), and a session can contain both.
const imageInputOmittedNote = "[image omitted: this model does not accept image input]"

// enforceImageInput applies CapImageInput to a request transcript: when m does
// not advertise the capability, every ImageBlock in user or tool content —
// including one nested inside a tool result — is replaced by a short note.
//
// Every wire builder must call this. CapImageInput is documented as the verdict
// that GOVERNS THE WIRE ("it must track what the API accepts, not the model's
// latent ability"), and for most of its life exactly one of the five builders
// consulted it, while two user-facing surfaces told the user that all of them
// did — the TUI printed "%s can't see images — %d attachment(s) will be
// dropped" and the chat bridge printed "only your text reaches it", and on
// Anthropic, Gemini, Bedrock and Codex the images went anyway. A user who
// turned image input off in the /model dialog because their gateway 400s on
// multimodal parts got billed for image tokens they were told would not be
// sent, and each rejection then wrote a permanent exclude_image directive into
// their session.
//
// Scoped to user/tool content because that is what the capability names:
// "ImageBlocks in user/tool content serialize". Assistant images are the image
// OUTPUT path — Codex replays the most recent assistant images to edit them,
// gated separately on CapImageOutput — and are none of this function's
// business.
//
// The messages are shared with the caller's live transcript, so this is
// copy-on-write throughout: a message with no image keeps its original Content
// slice, and a transcript with no image at all is returned unchanged.
func enforceImageInput(m Model, msgs []Message) []Message {
	if m.Has(CapImageInput) {
		return msgs
	}
	var out []Message
	for i, msg := range msgs {
		if msg.Role != RoleUser && msg.Role != RoleTool {
			continue
		}
		replaced, changed := contentWithoutImageInput(msg.Content)
		if !changed {
			continue
		}
		if out == nil {
			out = make([]Message, len(msgs))
			copy(out, msgs)
		}
		out[i].Content = replaced
	}
	if out == nil {
		return msgs
	}
	return out
}

// contentWithoutImageInput returns blocks with every ImageBlock replaced by the
// note, recursing into tool results. changed is false when there was nothing to
// replace, in which case the original slice is returned untouched.
func contentWithoutImageInput(blocks []Content) (out []Content, changed bool) {
	clone := func() {
		if out == nil {
			out = make([]Content, len(blocks))
			copy(out, blocks)
		}
	}
	for i, b := range blocks {
		switch v := b.(type) {
		case ImageBlock:
			clone()
			out[i] = TextBlock{Text: imageInputOmittedNote}
			changed = true
		case ToolResultBlock:
			inner, innerChanged := contentWithoutImageInput(v.Content)
			if !innerChanged {
				continue
			}
			clone()
			v.Content = inner
			out[i] = v
			changed = true
		}
	}
	if !changed {
		return blocks, false
	}
	return out, true
}
