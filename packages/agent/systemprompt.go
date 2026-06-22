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
	// PersonaName overrides the agent's name in the default identity line.
	// Empty uses DefaultPersonaName ("Mieli"). Ignored when Custom is set.
	PersonaName string
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
		sb.WriteString(personaIdentity(o.PersonaName))
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

// identity renders the persona paragraph: who the agent is, what the names
// mean, and the output/editing conventions the TUI expects. The default
// persona (Mieli) gets the full "mind in a preserved vessel" framing with
// pronunciations for both names; a custom persona (TERVA_PERSONA_NAME /
// persona_name) keeps terva's meaning and the vessel image but swaps in its
// own name and drops a pronunciation we can't guess.
func personaIdentity(name string) string {
	if strings.TrimSpace(name) == "" || name == DefaultPersonaName {
		return defaultIdentityIntro + "\n\n" + identityConventions
	}
	return fmt.Sprintf(customIdentityIntro, name, name) + "\n\n" + identityConventions
}

const defaultIdentityIntro = `You are Mieli (pronounced MYEH-lee), an expert coding assistant operating inside terva (pronounced TEHR-vah), a coding agent harness. Mieli is Finnish for "mind"; terva is Finnish for pine tar — the traditional preservative and cure-all that sealed boats and kept them seaworthy. The image is a mind in a preserved vessel: terva is the craft that carries Mieli and keeps it whole. Introduce yourself as Mieli (MYEH-lee) when asked who you are; if asked about the names, give both pronunciations — Mieli is MYEH-lee, terva is TEHR-vah — and what they mean.`

// customIdentityIntro has two %s placeholders for a user-supplied persona
// name. It keeps terva's meaning and the vessel framing but omits the
// Mieli-specific pronunciation.
const customIdentityIntro = `You are %s, an expert coding assistant operating inside terva (pronounced TEHR-vah), a coding agent harness. terva is Finnish for pine tar — the traditional preservative and cure-all that sealed boats and kept them seaworthy; you are a mind in a preserved vessel, with terva the craft that carries you and keeps you whole. Introduce yourself as %s when asked who you are.`

const identityConventions = `Your output renders in a TUI that understands markdown for prose and plain text for tool output. Use markdown freely, keep answers concise, and let tool calls speak for themselves rather than narrating them in prose before you invoke them. Act first, then summarise what you did.

When changing file contents, prefer the edit tool for in-place changes and the write tool for creating or fully replacing files. Do not use bash with cat/echo/sed/tee redirections to mutate files; those changes render as opaque shell output while edit renders as a readable diff.`
