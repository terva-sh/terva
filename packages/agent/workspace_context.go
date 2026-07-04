package agent

import (
	"context"
	"encoding/json"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// Context computes the /context size breakdown for a session — what fills the
// model's window on the next request (system prompt, tool defs, ext context,
// per-message transcript) versus the window size. It mirrors the TUI's
// buildContextOverview but returns structured data for the client to render.
func (w *Workspace) Context(ctx context.Context, sess string) (ctrlproto.ContextBreakdown, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.ContextBreakdown{}, err
	}
	return s.contextBreakdown(), nil
}

func (s *wsSession) contextBreakdown() ctrlproto.ContextBreakdown {
	ag := s.agent
	var b ctrlproto.ContextBreakdown
	if ag == nil {
		return b
	}

	b.SystemBytes = len(ag.System)
	// Snapshot the tool registry under the agent lock (a concurrent extension/MCP
	// toggle or trust change calls SetTools on another goroutine) before ranging.
	tools := ag.ToolsSnapshot()
	if specs := tools.Specs(); len(specs) > 0 {
		if raw, err := json.Marshal(specs); err == nil {
			b.ToolBytes = len(raw)
		}
	}
	b.ToolCount = len(tools)

	// Ephemeral extension/card context, via the lock-guarded, side-effect-free
	// preview so opening /context never records a phantom "lore fired this turn"
	// and never races a live lore/trust reload swapping the provider.
	b.ExtBytes = len(ag.ContextPreview())
	// Static ext guidance is folded into the system prompt (already inside
	// SystemBytes); surface its share so a small "ext context" isn't read as
	// "extensions inject nothing" when the bulk is guidance.
	if s.extMgr != nil {
		for _, it := range s.extMgr.ContextSnapshot() {
			if it.Kind == "static" {
				b.ExtGuidanceBytes += len(it.Text)
			}
		}
	}

	msgs := ag.Messages()
	b.Messages = make([]ctrlproto.ContextMessage, len(msgs))
	for i, m := range msgs {
		n := ctxMessageBytes(m)
		b.Messages[i] = ctrlproto.ContextMessage{Index: i, Kind: ctxMessageKind(m), Bytes: n}
		b.TranscriptBytes += n
	}
	b.TotalBytes = b.SystemBytes + b.ToolBytes + b.ExtBytes + b.TranscriptBytes

	// Use the session's current model (lock-guarded) rather than reading the
	// agent field directly, so this never races a concurrent model switch.
	prov, model := s.currentModel()
	b.Provider, b.Model = prov, model
	if model != "" {
		if mdl, err := provider.FindModel("", model); err == nil {
			b.Window = mdl.ContextWindow
		}
	}

	// The TUI status bar's live usage picture (shared with the usage surface):
	// real last-turn context tokens, cumulative session usage, and (for OAuth/sub
	// credentials) the provider's plan/credit windows.
	uv := s.usageView()
	b.ContextTokens = uv.ContextTokens
	b.Cumulative = uv.Cumulative
	b.Subscription = uv.Subscription
	b.UsageWindows = uv.Windows
	return b
}

// usageWindows maps the provider's usage windows to the wire form, dropping
// ephemeral rate-limit windows (as the TUI status bar does — they would churn
// an always-visible meter; they stay in the full /usage view).
func usageWindows(ws []provider.UsageWindow) []ctrlproto.UsageWindowInfo {
	var out []ctrlproto.UsageWindowInfo
	for _, w := range ws {
		if w.Kind == provider.WindowRateLimit {
			continue
		}
		kind := "plan"
		if w.Kind == provider.WindowCredit {
			kind = "credit"
		}
		info := ctrlproto.UsageWindowInfo{
			Label:         w.Label,
			UsedPercent:   w.UsedPercent,
			WindowMinutes: w.WindowMinutes,
			Kind:          kind,
		}
		if !w.ResetsAt.IsZero() {
			info.ResetsAt = ctrlTimeString(w.ResetsAt)
		}
		out = append(out, info)
	}
	return out
}

// ctxMessageBytes estimates a message's wire size by marshalling it to JSON
// (close to what a provider serializes, and accounts for tool results + base64
// image data). Falls back to summing text content.
func ctxMessageBytes(m provider.Message) int {
	if b, err := json.Marshal(m); err == nil {
		return len(b)
	}
	n := 0
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			n += len(tb.Text)
		}
	}
	return n
}

// ctxMessageKind labels a message so an oversized tool result or a compaction
// summary stands out in the breakdown.
func ctxMessageKind(m provider.Message) string {
	if m.Meta["compaction"] == "true" {
		return "compaction"
	}
	role := string(m.Role)
	for _, c := range m.Content {
		switch c.(type) {
		case provider.ToolResultBlock:
			return "tool_result"
		case provider.ToolCallBlock:
			return role + "+tool"
		case provider.ImageBlock:
			return role + "+image"
		}
	}
	return role
}
