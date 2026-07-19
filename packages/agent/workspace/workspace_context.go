package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/core"
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
	// Tool weight is the ADVERTISED set — what the model actually receives this
	// turn — not the full installed registry: under lazy visibility an inactive
	// group's schemas never reach the wire (only its names ride the ephemeral
	// capability note), so counting them would over-report the context. The
	// installed totals ride separate fields for the "N of M tools" split.
	tools := ag.ToolsSnapshot()
	advertised, filtered := ag.AdvertisedTools()
	var advSpecs, allSpecs []provider.Tool
	for _, spec := range tools.Specs() {
		allSpecs = append(allSpecs, spec)
		if advertised(spec.Name) {
			advSpecs = append(advSpecs, spec)
		}
	}
	if len(advSpecs) > 0 {
		if raw, err := json.Marshal(advSpecs); err == nil {
			b.ToolBytes = len(raw)
		}
	}
	b.ToolCount = len(advSpecs)
	if filtered && len(allSpecs) != len(advSpecs) {
		if raw, err := json.Marshal(allSpecs); err == nil {
			b.ToolBytesInstalled = len(raw)
		}
		b.ToolCountInstalled = len(allSpecs)
	}

	// Ephemeral extension/card context, via the lock-guarded, side-effect-free
	// preview so opening /context never records a phantom "lore fired this turn"
	// and never races a live lore/trust reload swapping the provider.
	b.ExtBytes = len(ag.ContextPreview())
	// The lazy-tool capability note also rides the ephemeral tail (oneTurn appends
	// it after the host context), but it is NOT part of ContextPreview — it is
	// pinned per turn, not produced by the context provider. Fold its bytes into
	// the ephemeral total and expose the attributable share, so /context reflects
	// what deferred discovery costs (schemas gone, names not): the same "sub-share
	// of a section" shape as ExtGuidanceBytes under the system prompt.
	b.LazyNoteBytes = len(ag.CapabilityNote())
	b.ExtBytes += b.LazyNoteBytes
	// The lore that fired last turn — the activation trace behind the ExtBytes
	// tail. Read the retained record (the real turn's trace, not the side-effect-
	// free peek above), so the Usage pane shows which entries fed the tail and why
	// without re-running Select. The Stage drawer's LoreView is the other home.
	s.mu.Lock()
	loreRec := s.loreFired
	s.mu.Unlock()
	if loreRec != nil {
		for _, f := range loreRec.Get() {
			b.LoreFired = append(b.LoreFired, ctrlproto.ContextLoreEntry{
				Name: f.Name, Source: f.Source, Constant: f.Constant, Keys: f.Keys, Dropped: f.Dropped,
			})
		}
	}
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
	msgNodes := make([]ctrlproto.ContextNode, len(msgs))
	for i, m := range msgs {
		n := ctxMessageBytes(m)
		kind := ctxMessageKind(m)
		b.Messages[i] = ctrlproto.ContextMessage{Index: i, Kind: kind, Bytes: n}
		b.TranscriptBytes += n
		// The addressable message id is the live index: context.node (later
		// stages) resolves "tr/m<i>" straight to Messages()[i]. Turn nodes
		// re-parent these but keep the same ids.
		msgNodes[i] = ctrlproto.ContextNode{
			ID:         fmt.Sprintf("tr/m%d", i),
			Kind:       "message",
			Label:      kind,
			Bytes:      n,
			Summary:    ctxMessageSummary(m),
			Expandable: len(m.Content) > 0, // context.node fetches the blocks
			Meta:       map[string]string{"index": strconv.Itoa(i), "role": string(m.Role)},
		}
		// A compaction summary can reveal what it replaced (from the durable
		// transcript), so it reads as an event, not a plain message.
		if m.Meta["compaction"] == "true" {
			msgNodes[i].Kind = "event"
			if s.sess != nil {
				msgNodes[i].Reveal = "compaction"
			}
		}
	}
	b.TotalBytes = b.SystemBytes + b.ToolBytes + b.ExtBytes + b.TranscriptBytes

	// The hierarchical outline (context-tree feature): the same sections, with the
	// transcript grouped into turns down to message stubs. Rev lets a client detect
	// when its positional node ids have gone stale before a context.node call.
	b.Rev = int(ag.Revision())
	b.Tree = ctxBuildTree(b, msgNodes, msgs)

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
// an always-visible meter). The full set, rate-limit windows included, rides
// UsageInfo.Windows from the usage.snapshot verb; this shape is the status
// picture, and the web context pane renders it.
func usageWindows(ws []provider.UsageWindow) []ctrlproto.UsageWindowInfo {
	out := make([]ctrlproto.UsageWindowInfo, 0, len(ws))
	for _, w := range ws {
		if w.Kind == provider.WindowRateLimit {
			continue
		}
		out = append(out, usageWindow(w))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// allUsageWindows keeps every window the provider reports, rate-limit ones
// included: filtering belongs to the client that renders them.
func allUsageWindows(ws []provider.UsageWindow) []ctrlproto.UsageWindowInfo {
	out := make([]ctrlproto.UsageWindowInfo, 0, len(ws))
	for _, w := range ws {
		out = append(out, usageWindow(w))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func usageWindow(w provider.UsageWindow) ctrlproto.UsageWindowInfo {
	kind := "plan"
	switch w.Kind {
	case provider.WindowCredit:
		kind = "credit"
	case provider.WindowRateLimit:
		kind = "rate_limit"
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
	return info
}

// usageInfo converts the provider's snapshot to the wire form. ok=false (no
// agent, or a provider that reports nothing) yields HasData false rather than
// an error: "this provider doesn't report usage limits" is a normal answer.
func usageInfo(snap provider.UsageSnapshot, ok, refreshable bool) ctrlproto.UsageInfo {
	info := ctrlproto.UsageInfo{Refreshable: refreshable}
	if !ok {
		return info
	}
	info.HasData = true
	info.Provider = snap.Provider
	info.Windows = allUsageWindows(snap.Windows)
	if !snap.CapturedAt.IsZero() {
		info.CapturedAt = ctrlTimeString(snap.CapturedAt)
	}
	if c := snap.Credits; c != nil {
		info.Credits = &ctrlproto.CreditsInfo{
			Unlimited:  c.Unlimited,
			HasCredits: c.HasCredits,
			Balance:    c.Balance,
			Used:       c.Used,
		}
	}
	return info
}

// resetInfos maps a provider's reset credits to the wire form.
func resetInfos(rs []provider.UsageReset) []ctrlproto.ResetInfo {
	out := make([]ctrlproto.ResetInfo, 0, len(rs))
	for _, r := range rs {
		out = append(out, resetInfo(r))
	}
	return out
}

func resetInfo(r provider.UsageReset) ctrlproto.ResetInfo {
	info := ctrlproto.ResetInfo{
		ID:          r.ID,
		Kind:        r.Kind,
		Title:       r.Title,
		Description: r.Description,
		Status:      string(r.Status),
	}
	if !r.GrantedAt.IsZero() {
		info.GrantedAt = ctrlTimeString(r.GrantedAt)
	}
	if !r.ExpiresAt.IsZero() {
		info.ExpiresAt = ctrlTimeString(r.ExpiresAt)
	}
	if !r.RedeemedAt.IsZero() {
		info.RedeemedAt = ctrlTimeString(r.RedeemedAt)
	}
	return info
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

// Node resolves one context node by its opaque id (from contextBreakdown's
// tree), filling in the content a lazy expand needs: the system prompt / ext
// context text, per-tool specs, or a message's content blocks. Stage 2 serves
// the expand op only; reveal ops (stage 3) return CodeUnsupported.
func (w *Workspace) Node(_ context.Context, sess, id, op string) (ctrlproto.ContextNode, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.ContextNode{}, err
	}
	return s.contextNode(id, op)
}

func (s *wsSession) contextNode(id, op string) (ctrlproto.ContextNode, error) {
	ag := s.agent
	if ag == nil {
		return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeNoSession, "session has no agent")
	}
	switch op {
	case "compaction":
		return s.revealCompaction(id)
	case "", "expand":
		// the expand switch below
	default:
		return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "context node op %q not supported", op)
	}
	switch {
	case id == "sys":
		// The system prompt, plus which extensions folded static guidance into it.
		node := ctrlproto.ContextNode{ID: "sys", Kind: "section", Label: "system prompt", Bytes: len(ag.System), Content: ag.System}
		node.Children = s.extContextItems("static", "sys/xg")
		return node, nil
	case id == "tools":
		advertised, filtered := ag.AdvertisedTools()
		return ctxToolsNode(ag.ToolsSnapshot(), advertised, filtered), nil
	case id == "xt":
		// The ephemeral context, plus the per-extension cards that make up its
		// ext share (lore/PHI are the remainder, inspectable via the lore surface).
		txt := ag.ContextPreview()
		node := ctrlproto.ContextNode{ID: "xt", Kind: "section", Label: "ext context", Bytes: len(txt), Content: txt}
		node.Children = s.extContextItems("card", "xt/card")
		// The lazy-tool capability note rides the same ephemeral tail but isn't in
		// ContextPreview; surface it as an inspectable leaf so the expanded section
		// sums to its byte total.
		if note := ag.CapabilityNote(); note != "" {
			node.Bytes += len(note)
			node.Children = append(node.Children, ctrlproto.ContextNode{
				ID: "xt/lazynote", Kind: "block", Label: "inactive tool groups (capability note)",
				Bytes: len(note), Summary: ctxFirstLineOf(note), Content: note,
			})
		}
		return node, nil
	case strings.HasPrefix(id, "tr/m"):
		return ctxMessageContentNode(id, ag.Messages())
	}
	return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "unknown context node %q", id)
}

// extContextItems surfaces this session's extensions' context contributions of
// one kind — "static" guidance folded into the system prompt, or "card"
// ephemeral cards — as child nodes, so the inspector shows WHICH extension
// injects what (the stage-4 facet). Sourced from the manager's structured
// snapshot; no wire change.
func (s *wsSession) extContextItems(kind, idPrefix string) []ctrlproto.ContextNode {
	if s.extMgr == nil {
		return nil
	}
	return ctxExtItems(s.extMgr.ContextSnapshot(), kind, idPrefix)
}

// ctxExtItems maps the ext context items of the given kind to leaf content nodes.
func ctxExtItems(items []extensions.ContextItem, kind, idPrefix string) []ctrlproto.ContextNode {
	var out []ctrlproto.ContextNode
	for _, it := range items {
		if it.Kind != kind {
			continue
		}
		label := it.Source
		if it.Label != "" {
			label += ": " + it.Label
		}
		id := idPrefix + "/" + it.Source
		if it.ID != "" {
			id += "/" + it.ID
		}
		out = append(out, ctrlproto.ContextNode{
			ID: id, Kind: "block", Label: label,
			Bytes: len(it.Text), Summary: ctxFirstLineOf(it.Text), Content: it.Text,
		})
	}
	return out
}

// ctxToolsNode expands the tool-defs section into one node per tool, each
// carrying its JSON spec as content (Specs() is already name-sorted). When
// extensions or MCP servers have merged tools, the leaves are bucketed under a
// per-capability-group node with a byte rollup (retro H2·b: core built-ins
// first, then each extension / "mcp:<server>") so an operator can see which
// group is costing the most context — the decision unit for lazy visibility.
// The whole subtree returns in one expansion (leaves carry their spec inline),
// so the extra nesting level needs no per-leaf refetch.
//
// advertised reports whether a tool's schema is actually sent to the model this
// turn; filtered is true when lazy visibility (or a VisibleTool override) is in
// effect. The section's Bytes is the LIVE cost — only advertised schemas — so
// /context reports the real context weight, not the full installed registry.
// Inactive groups are still listed (an operator wants to see what could be
// activated) but contribute 0 live bytes; their deferred weight rides the
// summary/Meta. When nothing is filtered (the default) every tool is advertised
// and this is exactly the old all-in accounting.
func ctxToolsNode(reg core.Registry, advertised func(name string) bool, filtered bool) ctrlproto.ContextNode {
	node := ctrlproto.ContextNode{ID: "tools", Kind: "section", Label: "tool defs"}
	if advertised == nil {
		advertised = func(string) bool { return true }
	}

	leaves := map[string][]ctrlproto.ContextNode{}
	liveByGroup := map[string]int{}     // advertised bytes (counted in the live total)
	deferredByGroup := map[string]int{} // installed-but-hidden bytes (not in context now)
	countByGroup := map[string]int{}
	var installedBytes, advertisedBytes, advertisedTools int
	for _, spec := range reg.Specs() {
		raw, _ := json.MarshalIndent(spec, "", "  ")
		group := build.ToolGroup(reg[spec.Name])
		live := advertised(spec.Name)
		meta := map[string]string{"group": group, "active": strconv.FormatBool(live)}
		leaves[group] = append(leaves[group], ctrlproto.ContextNode{
			ID: "tools/" + spec.Name, Kind: "tool", Label: spec.Name,
			Bytes: len(raw), Summary: ctxTruncate(spec.Description, 72), Content: string(raw),
			Meta: meta,
		})
		countByGroup[group]++
		installedBytes += len(raw)
		if live {
			liveByGroup[group] += len(raw)
			advertisedBytes += len(raw)
			advertisedTools++
			node.Bytes += len(raw)
		} else {
			deferredByGroup[group] += len(raw)
		}
	}

	// Report the advertised-vs-installed split on the section so a panel can show
	// "16 of 62 tools advertised · 3.1 KB live / 12.4 KB installed" without
	// re-summing the tree. Only when filtering is actually in effect.
	if filtered {
		node.Meta = map[string]string{
			"advertised_bytes": strconv.Itoa(advertisedBytes),
			"installed_bytes":  strconv.Itoa(installedBytes),
			"advertised_tools": strconv.Itoa(advertisedTools),
			"installed_tools":  strconv.Itoa(len(reg)),
		}
		node.Summary = fmt.Sprintf("%d of %d tools advertised · %s live / %s installed",
			advertisedTools, len(reg), ctxBytesLabel(advertisedBytes), ctxBytesLabel(installedBytes))
	}

	groups := ctxOrderedGroups(leaves)
	// No extensions/MCP merged (only built-ins): keep the flat per-tool list so
	// the common case gains no needless "core" wrapper level.
	if len(groups) <= 1 {
		for _, g := range groups {
			node.Children = leaves[g]
		}
		return node
	}
	for _, g := range groups {
		children := leaves[g]
		active := liveByGroup[g] > 0 || deferredByGroup[g] == 0 // core/active groups have live bytes
		gnode := ctrlproto.ContextNode{
			ID: "tools#" + g, Kind: "group", Label: g,
			Bytes:    liveByGroup[g], // live cost only; an inactive group is ~free (names ride the ephemeral note)
			Meta:     map[string]string{"group": g, "count": strconv.Itoa(countByGroup[g]), "active": strconv.FormatBool(active)},
			Children: children,
		}
		if active {
			gnode.Summary = fmt.Sprintf("%d tools", countByGroup[g])
		} else {
			// Inactive: schemas are not loaded — only the group's tool names ride the
			// cache-free capability note. Show the deferred weight so the operator
			// sees what activating it would add.
			gnode.Summary = fmt.Sprintf("%d tools · not loaded · ~%s deferred", countByGroup[g], ctxBytesLabel(deferredByGroup[g]))
			gnode.Meta["deferred_bytes"] = strconv.Itoa(deferredByGroup[g])
		}
		node.Children = append(node.Children, gnode)
	}
	return node
}

// ctxOrderedGroups lists the capability groups present, the always-visible core
// group first and the rest alphabetically — a stable, legible order.
func ctxOrderedGroups(byGroup map[string][]ctrlproto.ContextNode) []string {
	rest := make([]string, 0, len(byGroup))
	hasCore := false
	for g := range byGroup {
		if g == build.CoreToolGroup {
			hasCore = true
			continue
		}
		rest = append(rest, g)
	}
	sort.Strings(rest)
	if hasCore {
		return append([]string{build.CoreToolGroup}, rest...)
	}
	return rest
}

// ctxMessageContentNode expands a transcript message (id "tr/m<i>") into its
// content blocks, each carrying the block body as content.
func ctxMessageContentNode(id string, msgs []provider.Message) (ctrlproto.ContextNode, error) {
	idx, err := strconv.Atoi(strings.TrimPrefix(id, "tr/m"))
	if err != nil || idx < 0 || idx >= len(msgs) {
		return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "no context message %q", id)
	}
	m := msgs[idx]
	node := ctrlproto.ContextNode{
		ID: id, Kind: "message", Label: ctxMessageKind(m), Bytes: ctxMessageBytes(m),
		Meta: map[string]string{"index": strconv.Itoa(idx), "role": string(m.Role)},
	}
	for j, c := range m.Content {
		node.Children = append(node.Children, ctxBlockNode(fmt.Sprintf("%s/b%d", id, j), c))
	}
	return node, nil
}

// ctxBlockNode renders one content block as a leaf node with its body as content
// (image bytes are described, not inlined).
func ctxBlockNode(id string, c provider.Content) ctrlproto.ContextNode {
	n := ctrlproto.ContextNode{ID: id, Kind: "block"}
	switch c := c.(type) {
	case provider.TextBlock:
		n.Label, n.Content = "text", c.Text
	case provider.ToolCallBlock:
		n.Label = "tool call → " + c.Name
		n.Content = strings.TrimSpace(c.Name + " " + string(c.Arguments))
	case provider.ToolResultBlock:
		n.Label = "tool result"
		if c.IsError {
			n.Label = "tool result (error)"
		}
		n.Content = ctxContentText(c.Content)
	case provider.ImageBlock:
		n.Label = "image"
		n.Content = fmt.Sprintf("[%s image, %d bytes]", c.MimeType, len(c.Data))
	default:
		n.Label = "block"
	}
	n.Bytes = len(n.Content)
	n.Summary = ctxFirstLineOf(n.Content) // collapsed preview
	return n
}

// ctxFirstLineOf returns the first non-empty line of s, truncated.
func ctxFirstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = strings.TrimSpace(s[:nl])
	}
	return ctxTruncate(s, 72)
}

// ctxContentText flattens nested content (a tool result's blocks) to text,
// describing non-text parts.
func ctxContentText(cs []provider.Content) string {
	var sb strings.Builder
	for _, c := range cs {
		switch c := c.(type) {
		case provider.TextBlock:
			sb.WriteString(c.Text)
		case provider.ImageBlock:
			fmt.Fprintf(&sb, "[%s image, %d bytes]", c.MimeType, len(c.Data))
		}
	}
	return sb.String()
}

// revealCompaction reconstructs and returns the context a compaction summarized
// away — the reveal op. The target checkpoint is the latest when the node is a
// live summary (tr/m<i>), or a specific ordinal for a historical summary
// (ev/c<k>) surfaced by a prior reveal. Needs a persisted session (the originals
// live in the transcript file); read-only.
func (s *wsSession) revealCompaction(id string) (ctrlproto.ContextNode, error) {
	if s.sess == nil {
		return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "reveal needs a persisted session")
	}
	target := -1 // a live compaction summary reveals the latest checkpoint
	if rest, ok := strings.CutPrefix(id, "ev/c"); ok {
		n, err := strconv.Atoi(rest)
		if err != nil {
			return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "bad reveal id %q", id)
		}
		target = n
	} else if !strings.HasPrefix(id, "tr/m") {
		return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "not a compaction node %q", id)
	}
	span, err := core.RevealCompaction(s.sess.Path, target)
	if err != nil {
		return ctrlproto.ContextNode{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "reveal: %v", err)
	}
	return ctxRevealNode(id, span), nil
}

// ctxRevealNode builds the reveal result: the summarized-away messages as child
// nodes with their content inlined. The span's leading message, when the target
// checkpoint had a predecessor, is that predecessor's summary — surfaced as a
// nested revealable node so a client can walk the compaction history backward.
func ctxRevealNode(id string, span core.CompactionSpan) ctrlproto.ContextNode {
	node := ctrlproto.ContextNode{
		ID: id, Kind: "event", Label: "compaction",
		Meta: map[string]string{
			"replaced": strconv.Itoa(len(span.Replaced)),
			"kept":     strconv.Itoa(span.KeptCount),
			"epoch":    strconv.Itoa(span.Ordinal),
		},
	}
	for j, m := range span.Replaced {
		if j == 0 && span.PrevOrdinal >= 0 && m.Meta["compaction"] == "true" {
			child := ctrlproto.ContextNode{
				ID: fmt.Sprintf("ev/c%d", span.PrevOrdinal), Kind: "event", Label: "compaction",
				Bytes: ctxMessageBytes(m), Summary: ctxMessageSummary(m), Reveal: "compaction",
			}
			node.Children = append(node.Children, child)
			node.Bytes += child.Bytes
			continue
		}
		child := ctxHistMessageNode(fmt.Sprintf("ev/c%d/m%d", span.Ordinal, j), m)
		node.Children = append(node.Children, child)
		node.Bytes += child.Bytes
	}
	return node
}

// ctxHistMessageNode renders a historical (revealed) message with its content
// blocks inlined — reveal returns the span in one shot rather than making each
// off-transcript message separately fetchable.
func ctxHistMessageNode(id string, m provider.Message) ctrlproto.ContextNode {
	node := ctrlproto.ContextNode{
		ID: id, Kind: "message", Label: ctxMessageKind(m),
		Bytes: ctxMessageBytes(m), Summary: ctxMessageSummary(m),
		Meta: map[string]string{"role": string(m.Role)},
	}
	for k, c := range m.Content {
		node.Children = append(node.Children, ctxBlockNode(fmt.Sprintf("%s/b%d", id, k), c))
	}
	return node
}

// ctxBuildTree assembles the ContextBreakdown.Tree outline: the sections the
// scalar fields summarize, with the transcript grouped into turns down to
// message stubs. Sizes + labels only — message bodies are fetched lazily via
// context.node (later stages). See docs/proposals/context-inspector.md.
func ctxBuildTree(b ctrlproto.ContextBreakdown, msgNodes []ctrlproto.ContextNode, msgs []provider.Message) *ctrlproto.ContextNode {
	sys := ctrlproto.ContextNode{ID: "sys", Kind: "section", Label: "system prompt", Bytes: b.SystemBytes, Expandable: b.SystemBytes > 0}
	if b.ExtGuidanceBytes > 0 {
		sys.Meta = map[string]string{"ext_guidance_bytes": strconv.Itoa(b.ExtGuidanceBytes)}
	}
	tools := ctrlproto.ContextNode{
		ID: "tools", Kind: "section", Label: "tool defs", Bytes: b.ToolBytes, Expandable: b.ToolCount > 0,
		Meta: map[string]string{"count": strconv.Itoa(b.ToolCount)},
	}
	if b.ToolCountInstalled > b.ToolCount {
		tools.Meta["count_installed"] = strconv.Itoa(b.ToolCountInstalled)
		tools.Meta["bytes_installed"] = strconv.Itoa(b.ToolBytesInstalled)
		tools.Summary = fmt.Sprintf("%d of %d advertised · %s installed",
			b.ToolCount, b.ToolCountInstalled, ctxBytesLabel(b.ToolBytesInstalled))
	}
	ext := ctrlproto.ContextNode{ID: "xt", Kind: "section", Label: "ext context", Bytes: b.ExtBytes, Expandable: b.ExtBytes > 0}
	transcript := ctrlproto.ContextNode{
		ID: "tr", Kind: "section", Label: "transcript", Bytes: b.TranscriptBytes,
		Meta:     map[string]string{"msgs": strconv.Itoa(len(msgNodes))},
		Children: ctxTranscriptTurns(msgNodes, msgs),
	}
	return &ctrlproto.ContextNode{
		ID: "root", Kind: "section", Label: "context", Bytes: b.TotalBytes,
		Children: []ctrlproto.ContextNode{sys, tools, ext, transcript},
	}
}

// ctxTranscriptTurns groups the transcript message nodes into turns by
// user-prompt boundary (the /jump derivation): a real user message starts a new
// turn; assistant/tool messages join it. A compaction summary is not a prompt,
// so a leading run of non-prompt messages (a summary and the tail it preserved)
// collects under a "preserved context" group rather than masquerading as turn #1.
func ctxTranscriptTurns(msgNodes []ctrlproto.ContextNode, msgs []provider.Message) []ctrlproto.ContextNode {
	var turns []ctrlproto.ContextNode
	curIdx := -1
	turnNo := 0
	for i, node := range msgNodes {
		m := msgs[i]
		isPrompt := m.Role == provider.RoleUser && m.Meta["compaction"] != "true"
		switch {
		case isPrompt:
			turnNo++
			turns = append(turns, ctrlproto.ContextNode{
				ID: fmt.Sprintf("tr/t%d", turnNo), Kind: "turn",
				Label: fmt.Sprintf("turn #%d", turnNo), Summary: ctxFirstLine(m),
			})
			curIdx = len(turns) - 1
		case curIdx < 0:
			turns = append(turns, ctrlproto.ContextNode{
				ID: "tr/t0", Kind: "turn", Label: "preserved context",
			})
			curIdx = len(turns) - 1
		}
		turns[curIdx].Children = append(turns[curIdx].Children, node)
		turns[curIdx].Bytes += node.Bytes
	}
	for i := range turns {
		turns[i].Meta = map[string]string{"msgs": strconv.Itoa(len(turns[i].Children))}
	}
	return turns
}

// ctxMessageSummary is a one-line preview of a message for a collapsed tree row:
// the first non-empty text line, else a hint derived from the first non-text block.
func ctxMessageSummary(m provider.Message) string {
	if s := ctxFirstLine(m); s != "" {
		return s
	}
	for _, c := range m.Content {
		switch c := c.(type) {
		case provider.ToolCallBlock:
			return "→ " + c.Name
		case provider.ToolResultBlock:
			return "tool result"
		case provider.ImageBlock:
			return "[image]"
		}
	}
	return ""
}

// ctxFirstLine returns the first non-empty text line of a message, truncated.
func ctxFirstLine(m provider.Message) string {
	for _, c := range m.Content {
		tb, ok := c.(provider.TextBlock)
		if !ok {
			continue
		}
		s := strings.TrimSpace(tb.Text)
		if s == "" {
			continue
		}
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = strings.TrimSpace(s[:nl])
		}
		return ctxTruncate(s, 72)
	}
	return ""
}

// ctxBytesLabel renders a byte count for a node summary — "812 B", "3.1 KB" —
// so the advertised-vs-installed split reads at a glance. Kilobytes use 1000
// (matching the ~tokens ≈ bytes/4 convention operators already read elsewhere).
func ctxBytesLabel(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1000)
}

// ctxTruncate shortens s to at most n runes, appending an ellipsis when it cuts.
func ctxTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
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
