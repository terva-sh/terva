package agent

import (
	"fmt"
	"strings"
	"time"
)

// ToolSummary is a name+one-line description. Kept as part of the
// public opts type for backwards compatibility with callers that
// still pass tool summaries in; the default prompt no longer lists
// them because the provider already advertises tools in the request
// body's tools[] array, so listing them again in prose is pure
// duplication.
type ToolSummary struct {
	Name        string
	Description string
}

// SystemPromptOpts configures BuildSystemPrompt.
type SystemPromptOpts struct {
	CWD          string
	Tools        []ToolSummary
	Custom       string   // if set, replaces the default identity entirely
	Append       []string // extra text appended at the end
	Now          time.Time
	TervaDocsDir string
	// StatusTool adds a one-line hint that the terva_status tool exists.
	// Set only when that tool is actually in the registry (it can be
	// dropped by --no-tools or a --tools allowlist), so the prompt never
	// advertises a tool the model can't call.
	StatusTool bool
}

// BuildSystemPrompt constructs the system prompt.
//
// Design note: kept intentionally small. Every byte here is part of
// the cached prefix on every request, so bloat is cumulatively
// expensive. We ship only:
//
//   - A one-paragraph identity (who terva is, what the name means,
//     what the TUI expects for output format).
//   - The date + cwd footer so the model has current-context.
//
// Everything else (tool listing, operating guidelines, "don't run
// sudo", "prefer edit over write", etc.) is left out because the
// current-generation frontier models already internalise it, and
// the tool schemas sent alongside the request carry each tool's
// own description.
//
// Users who want extra biasing can use --system-prompt (replace),
// --append-system-prompt (additive, repeatable), or drop a
// SYSTEM.md in $TERVA_HOME that overrides the default identity.
func BuildSystemPrompt(o SystemPromptOpts) string {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	date := o.Now.Format("2006-01-02")
	cwd := o.CWD
	if cwd == "" {
		cwd = "."
	}

	var sb strings.Builder

	if o.Custom != "" {
		sb.WriteString(o.Custom)
	} else {
		sb.WriteString(defaultIdentity)
	}

	if strings.TrimSpace(o.TervaDocsDir) != "" {
		sb.WriteString("\n\nTerva's own docs are installed under ")
		sb.WriteString(o.TervaDocsDir)
		sb.WriteString("; use the read tool there when you need details about terva RPC, extensions, skills, or built-in behaviour.")
	}

	if o.StatusTool {
		sb.WriteString("\n\nCall the terva_status tool (no arguments) to check your own runtime state — current model, provider, working directory, reasoning effort, and how full your context window is — for example to decide whether to summarise before the context fills. Its tool description lists every field it returns.")
	}

	for _, a := range o.Append {
		if strings.TrimSpace(a) == "" {
			continue
		}
		sb.WriteString("\n\n")
		sb.WriteString(a)
	}

	fmt.Fprintf(&sb, "\n\nCurrent date: %s\nCurrent working directory: %s\n", date, cwd)
	return sb.String()
}

const defaultIdentity = `You are Terva, an expert coding assistant operating inside terva, a coding agent harness. Introduce yourself as Terva when asked who you are. If the user asks what terva means: terva is Finnish for pine tar — the traditional preservative and cure-all; answer exactly that.

Your output renders in a TUI that understands markdown for prose and plain text for tool output. Use markdown freely, keep answers concise, and let tool calls speak for themselves rather than narrating them in prose before you invoke them. Act first, then summarise what you did.

When changing file contents, prefer the edit tool for in-place changes and the write tool for creating or fully replacing files. Do not use bash with cat/echo/sed/tee redirections to mutate files; those changes render as opaque shell output while edit renders as a readable diff.`
