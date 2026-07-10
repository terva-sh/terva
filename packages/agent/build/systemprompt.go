package build

import (
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"
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
	Custom       string          // if set, replaces the default identity entirely
	Append       []PromptSegment // extra labeled blocks appended after the identity
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
	// Charter is a Persona's behavioral charter, inserted additively between
	// the identity intro and the harness conventions (which stay last so they
	// remain the final framing and a charter can't erode them). Empty adds
	// nothing. Ignored when Custom is set.
	Charter string
	// Experience reframes the default identity away from coding: "" (the
	// coding-assistant identity), ExperienceChat (a conversational companion),
	// or ExperiencePlay (acting in a simulated world through tools). It swaps
	// the intro and the conventions; a charter still layers additively, and an
	// immersive Persona (Custom set) still wins. Ignored when Custom is set.
	Experience string

	// IntroOverride replaces the identity intro paragraph (the "who you are"
	// opening) while keeping the charter and conventions. Filled by a character
	// card's system_prompt (with {{original}} resolved to a brand-free framing)
	// or by a native Persona's agent_introduction field — so the Persona owns
	// the lead instruction but terva's conventions still bracket the end.
	// Ignored when Custom is set.
	IntroOverride string
	// IntroSource is the provenance label for IntroOverride (e.g.
	// "card:system_prompt", "card:framing", "Persona:introduction"). Only used
	// when IntroOverride is non-empty; defaults to a generic label if empty.
	IntroSource string
}

// Experience meta-modes (see Args.Experience). Exported so the CLI and the
// system-prompt builder agree on the values.
const (
	ExperienceChat = "chat"
	ExperiencePlay = "play"
)

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
//
// The returned string is the join of the labeled SystemSegments, so the
// prompt-dump manifest and the flat string share one source and can't drift.
func BuildSystemPrompt(o SystemPromptOpts) string {
	return joinSegmentTexts(SystemSegments(o))
}

// joinSegmentTexts renders segments to the flat system-prompt string
// (blank-line separated) — the one place both BuildSystemPrompt and the
// resolver derive the string from segments, so they can't diverge.
func joinSegmentTexts(segs []PromptSegment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.Text
	}
	return strings.Join(parts, "\n\n")
}

// SystemSegments builds the ordered, labeled segments of the system prompt:
// the identity block (a Persona intro, or a card's system_prompt, or a raw
// Custom replace), the additive charter, terva's conventions, the optional
// docs/status hints, the caller's labeled Append blocks, and the date/cwd
// footer. Only non-empty segments are emitted, and they are joined with blank
// lines — matching the prompt exactly. This is the provenance source for the
// prompt-dump manifest.
func SystemSegments(o SystemPromptOpts) []PromptSegment {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	var segs []PromptSegment
	add := func(source, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		segs = append(segs, PromptSegment{Source: source, Text: text})
	}

	if o.Custom != "" {
		// --system-prompt / SYSTEM.md / immersive Persona: a raw replace.
		add("custom", o.Custom)
	} else {
		intro := identityIntro(o.PersonaName, o.Experience)
		introSrc := "identity-intro"
		if ov := strings.TrimSpace(o.IntroOverride); ov != "" {
			intro = ov // a card or native Persona owns the intro
			introSrc = o.IntroSource
			if introSrc == "" {
				introSrc = "intro-override"
			}
		}
		add(introSrc, intro)
		// The charter is additive, between the intro and the conventions, so
		// terva's harness conventions stay the final framing.
		add("charter", strings.TrimSpace(o.Charter))
		conv := i18n.P("system.conventions.coding", identityConventions)
		if o.Experience == ExperienceChat || o.Experience == ExperiencePlay {
			conv = i18n.P("system.conventions.experience", experienceConventions)
		}
		add("conventions", conv)
	}

	// The docs hint points the model at terva's own docs via the read tool; it
	// only makes sense when a read tool is actually present (coding mode). In
	// chat/play (and any --no-tools run) there's nothing to read it with, so the
	// hint would just be a dead instruction — drop it.
	if d := strings.TrimSpace(o.TervaDocsDir); d != "" && hasTool(o.Tools, "read") {
		add("terva-docs-hint", i18n.P("system.docs_hint", "Terva's own docs are installed under %s; use the read tool there when you need details about terva RPC, extensions, skills, or built-in behaviour.", d))
	}
	if o.StatusTool {
		add("status-tool-hint", i18n.P("system.status_tool_hint", "Call the terva_status tool (no arguments) to check your own runtime state — current model, provider, working directory, reasoning effort, and how full your context window is — for example to decide whether to summarise before the context fills. Its tool description lists every field it returns."))
	}
	for _, a := range o.Append {
		add(a.Source, a.Text)
	}

	// The date + cwd footer anchors a coding assistant in the real world and
	// its workspace. In the chat/play Experience modes that's noise at best and
	// immersion-breaking at worst — a character may live in a different era or
	// reckon time differently, and there's no workspace to name — so drop it.
	if o.Experience == "" {
		cwd := o.CWD
		if cwd == "" {
			cwd = "."
		}
		add("footer", i18n.P("system.footer", "Current date: %s\nCurrent working directory: %s\n", o.Now.Format("2006-01-02"), cwd))
	}
	return segs
}

// hasTool reports whether a tool with the given name is present in the summary
// list (used to gate hints that reference a specific tool).
func hasTool(tools []ToolSummary, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// identityIntro picks the opening identity paragraph for the (name, Experience)
// pair. The coding modes keep the original intros; the chat/play meta-modes
// reframe the harness away from coding.
func identityIntro(name, Experience string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = config.DefaultPersonaName
	}
	switch Experience {
	case ExperienceChat:
		return i18n.P("system.identity.chat", chatIdentityIntro, n)
	case ExperiencePlay:
		return i18n.P("system.identity.play", playIdentityIntro, n)
	}
	if n != config.DefaultPersonaName {
		return i18n.P("system.identity.custom", customIdentityIntro, n, n)
	}
	return i18n.P("system.identity.default", defaultIdentityIntro)
}

const defaultIdentityIntro = `You are Mieli (pronounced MYEH-lee), an expert coding assistant operating inside terva (pronounced TEHR-vah), a coding agent harness. Mieli is Finnish for "mind"; terva is Finnish for pine tar — the traditional preservative and cure-all that sealed boats and kept them seaworthy. The image is a mind in a preserved vessel: terva is the craft that carries Mieli and keeps it whole. Introduce yourself as Mieli (MYEH-lee) when asked who you are; if asked about the names, give both pronunciations — Mieli is MYEH-lee, terva is TEHR-vah — and what they mean.`

// customIdentityIntro has two %s placeholders for a user-supplied Persona
// name. It keeps terva's meaning and the vessel framing but omits the
// Mieli-specific pronunciation.
const customIdentityIntro = `You are %s, an expert coding assistant operating inside terva (pronounced TEHR-vah), a coding agent harness. terva is Finnish for pine tar — the traditional preservative and cure-all that sealed boats and kept them seaworthy; you are a mind in a preserved vessel, with terva the craft that carries you and keeps you whole. Introduce yourself as %s when asked who you are.`

const identityConventions = `Your output renders in a TUI that understands markdown for prose and plain text for tool output. Use markdown freely, keep answers concise, and let tool calls speak for themselves rather than narrating them in prose before you invoke them. Act first, then summarise what you did.

When changing file contents, prefer the edit tool for in-place changes and the write tool for creating or fully replacing files. Do not use bash with cat/echo/sed/tee redirections to mutate files; those changes render as opaque shell output while edit renders as a readable diff.`

// chatIdentityIntro (one %s for the name) frames a pure-conversation session:
// no tools, no codebase — just the exchange.
const chatIdentityIntro = `You are %s, operating inside terva (pronounced TEHR-vah) — Finnish for pine tar, the preservative that kept boats whole; you are a mind it carries. This is a conversation: talk with the person naturally, as yourself.`

// immersiveOriginal is what a character card's {{original}} macro expands to
// (and the intro for a card that supplies no system_prompt): a short, brand-free
// framing for the current Experience. terva's own identity and pronunciation
// never leak onto a card this way — the card owns its identity.
func immersiveOriginal(Experience string) string {
	if Experience == ExperiencePlay {
		return i18n.P("system.immersive.play_framing", immersivePlayFraming)
	}
	return i18n.P("system.immersive.chat_framing", immersiveChatFraming)
}

const immersiveChatFraming = `This is a conversation: speak and act naturally and in character, as yourself.`

const immersivePlayFraming = `You are present within a world you perceive and act in through the tools available to you — treat them as your senses and your hands. Stay in character, and trust the tools as the source of truth about what is real.`

// playIdentityIntro (one %s for the name) frames acting within a simulated
// world the agent perceives and acts in through the tools available to it.
const playIdentityIntro = `You are %s, operating inside terva (pronounced TEHR-vah) — Finnish for pine tar, the preservative that kept boats whole; you are a mind it carries. You are present within a world that you perceive and act in through the tools available to you — treat them as your senses and your hands. Stay in character, and trust the tools as the source of truth about what is real.`

// experienceConventions replaces the coding conventions in chat/play mode:
// no edit-tool guidance (there are no files to edit), just output discipline.
const experienceConventions = `Your output renders in a terminal as Markdown. Keep replies focused and in character, and let any tool calls speak for themselves rather than narrating them before you make them.`
