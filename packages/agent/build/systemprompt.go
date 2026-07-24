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
	// TervaExamplesDir is $TERVA_HOME/examples, where the deployment/setup
	// examples install. Empty drops the hint (same guard as the docs hint).
	TervaExamplesDir string
	// Surface is where this agent's words land: a rendered pane, a chat
	// message, a plain stream, or somebody else's program. The conventions
	// segment is written against it, so the prompt stops asserting a TUI that
	// three of terva's run modes do not have. Resolve derives it from Args.Mode
	// via SurfaceOf; the zero value fails closed to SurfacePlain, which promises
	// the model nothing.
	Surface Surface
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

	// Portable strips terva's harness-local self-context so the agent runs on
	// only what a portable briefing gave it (the sufficiency-test worker side).
	// PortableOff (default) keeps everything; PortableOn drops harness-local
	// segments; PortableStrict also drops discovery. The filter keys on the SAME
	// PortabilityOf classification the worker composer uses (one table, two
	// consumers), so the two cannot drift. From Args.Portable.
	Portable string
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
	// addSeg takes a whole segment and passes it through intact. The Append
	// blocks arrive already built — carrying, for the file-backed ones, the
	// Origin paths they were read from — so reconstructing them here from two of
	// their fields would silently drop the rest. That is exactly what used to
	// happen, and Origin is only the field that noticed.
	addSeg := func(s PromptSegment) {
		if strings.TrimSpace(s.Text) == "" {
			return
		}
		segs = append(segs, s)
	}
	// add is the convenience form for the segments SystemSegments GENERATES,
	// which are text and nothing else.
	add := func(source, text string) {
		addSeg(PromptSegment{Source: source, Text: text})
	}

	if o.Custom != "" {
		// --system-prompt / SYSTEM.md / immersive Persona: a raw replace.
		add(SourceCustom, o.Custom)
	} else {
		intro := identityIntro(o.PersonaName, o.Experience)
		introSrc := SourceIdentityIntro
		// The vessel rides beside the identity — one paragraph about who you
		// are, one about what carries you. Split so the mind can travel to a
		// foreign agent while the harness stays home.
		vessel := vesselFraming(o.PersonaName, o.Experience)
		if ov := strings.TrimSpace(o.IntroOverride); ov != "" {
			intro = ov // a card or native Persona owns the intro
			introSrc = o.IntroSource
			if introSrc == "" {
				introSrc = SourceIntroOverride
			}
			// Such an agent owns its identity outright, and terva's branding has
			// never appeared beside it. Keep that promise.
			vessel = ""
		}
		add(introSrc, intro)
		add(SourceVessel, vessel)
		// The charter is additive, between the identity and the conventions, so
		// terva's harness conventions stay the final framing.
		add(SourceCharter, strings.TrimSpace(o.Charter))
		add(SourceConventions, conventions(o))
	}

	// The docs hint points the model at terva's own docs via the read tool; it
	// only makes sense when a read tool is actually present (coding mode). In
	// chat/play (and any --no-tools run) there's nothing to read it with, so the
	// hint would just be a dead instruction — drop it.
	if d := strings.TrimSpace(o.TervaDocsDir); d != "" && hasTool(o.Tools, "read") {
		add(SourceTervaDocsHint, i18n.P("system.docs_hint", "Terva's own docs are installed under %s; use the read tool there when you need details about terva RPC, extensions, skills, or built-in behaviour.", d))
	}
	if d := strings.TrimSpace(o.TervaExamplesDir); d != "" && hasTool(o.Tools, "read") {
		add(SourceTervaExamplesHint, i18n.P("system.examples_hint", "Deployment and setup examples — systemd units, reverse-proxy configs, container recipes — are installed under %s; read them when bootstrapping or configuring a terva host.", d))
	}
	if o.StatusTool {
		add(SourceStatusToolHint, i18n.P("system.status_tool_hint", "Call the terva_status tool (no arguments) to check your own runtime state — current model, provider, working directory, reasoning effort, and how full your context window is — for example to decide whether to summarise before the context fills. Its tool description lists every field it returns."))
	}
	for _, a := range o.Append {
		addSeg(a)
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
		add(SourceFooter, i18n.P("system.footer", "Current date: %s\nCurrent working directory: %s\n", o.Now.Format("2006-01-02"), cwd))
	}

	if o.Portable != PortableOff {
		segs = filterPortable(segs, o.Portable == PortableStrict)
	}
	return segs
}

// filterPortable keeps only the segments a PORTABLE terva worker should carry,
// keying on the same PortabilityOf classification the worker composer uses so
// the two consumers of that one table cannot drift.
//
// Non-strict drops the harness-local self-context (the vessel, the tool hints,
// the swarm/cast/tasks advertisements, the trust-model description, the footer)
// and keeps terva's own discovery — AGENTS.md, skills, constant lore — because
// a foreign worker reads its OWN project files too, so terva reading its own is
// the fair comparison, not a cheat. Strict additionally drops that discovery,
// proving the briefing stands alone. Portable content (the --system-prompt
// briefing itself, the charter, a user append) always survives.
func filterPortable(segs []PromptSegment, strict bool) []PromptSegment {
	out := segs[:0]
	for _, s := range segs {
		keep := false
		switch s.Portability() {
		case PortabilityPortable:
			keep = true
		case PortabilityDiscoveryOwned, PortabilityNoAnalog:
			keep = !strict // terva's own discovery is the symmetric analog; strict forgoes it
		case PortabilityHarnessLocal:
			keep = false // describes terva's tools/surface/policy — the self-context we suppress
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
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

// identityIntro is the **portable** half of the opening: who the agent is. It
// names no harness, which is exactly what lets it cross a harness boundary —
// a foreign coding agent has no use for the vessel and should not be told it
// lives in one. Pair it with vesselFraming for terva's own agents.
//
// See PortabilityOf and docs/proposals/external-agent-workers.md.
func identityIntro(name, Experience string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = config.DefaultPersonaName
	}
	switch Experience {
	case ExperienceChat, ExperiencePlay:
		// "You are X." plus the very framing a card's {{original}} expands to.
		// One string, two callers: the chat/play intros used to carry their own
		// near-copies of it, and the copies had already drifted apart ("a world
		// that you perceive" here, "a world you perceive" there).
		return i18n.P("system.identity.immersive", immersiveIdentityIntro, n) + " " + experienceFraming(Experience)
	}
	if n != config.DefaultPersonaName {
		return i18n.P("system.identity.custom", customIdentityIntro, n, n)
	}
	return i18n.P("system.identity.default", defaultIdentityIntro)
}

// vesselFraming is the **harness-local** half: terva, the craft that carries
// the mind. Emitted beside the identity for terva's own agents and withheld
// from every foreign one.
//
// It is empty when a card or persona supplies its own intro — such an agent
// owns its identity outright, and terva's branding has never appeared beside
// it (the promise immersiveOriginal was written to keep).
func vesselFraming(name, Experience string) string {
	if Experience != "" {
		return i18n.P("system.vessel.immersive", immersiveVessel)
	}
	if n := strings.TrimSpace(name); n != "" && n != config.DefaultPersonaName {
		return i18n.P("system.vessel.custom", customVessel)
	}
	return i18n.P("system.vessel.default", defaultVessel)
}

const defaultIdentityIntro = `You are Mieli (pronounced MYEH-lee), an expert coding assistant. Mieli is Finnish for "mind". Introduce yourself as Mieli (MYEH-lee) when asked who you are.`

// customIdentityIntro has two %s placeholders for a user-supplied Persona name.
const customIdentityIntro = `You are %s, an expert coding assistant. Introduce yourself as %s when asked who you are.`

// immersiveIdentityIntro (one %s for the name) is the whole identity in
// chat/play: who you are. The framing of the situation comes from
// experienceFraming, shared with a card's {{original}}.
const immersiveIdentityIntro = `You are %s.`

const defaultVessel = `You are operating inside terva (pronounced TEHR-vah), a coding agent harness. terva is Finnish for pine tar — the traditional preservative and cure-all that sealed boats and kept them seaworthy. The image is a mind in a preserved vessel: terva is the craft that carries Mieli and keeps it whole. If asked about the names, give both pronunciations — Mieli is MYEH-lee, terva is TEHR-vah — and what they mean.`

const customVessel = `You are operating inside terva (pronounced TEHR-vah), a coding agent harness. terva is Finnish for pine tar — the traditional preservative and cure-all that sealed boats and kept them seaworthy; you are a mind in a preserved vessel, with terva the craft that carries you and keeps you whole.`

const immersiveVessel = `You are operating inside terva (pronounced TEHR-vah) — Finnish for pine tar, the preservative that kept boats whole; you are a mind it carries.`

// conventions is terva's harness conventions segment: how to speak, and how to
// change files. Both halves are conditional on facts about this run, and used
// not to be.
//
// The output half is written against the Surface — the segment used to open
// "Your output renders in a TUI", which is true in the TUI and false in a bot,
// an `-p` pipe, and an RPC embedder. The file half names terva's edit and write
// tools, so it is emitted only when those tools are actually in the registry
// (--no-tools and the chat/play modes drop them). The docs hint next door has
// gated itself on `hasTool(o.Tools, "read")` all along; this is the same rule,
// applied to the segment that names the most tools.
//
// The segment is harness-local either way (see PortabilityOf): a foreign agent
// has its own surface and its own tools, and hears about neither from us.
func conventions(o SystemPromptOpts) string {
	out := surfaceConventions(o.Surface)

	// chat/play: no files, no codebase — just the exchange and its manners,
	// plus the long-haul craft contract.
	if o.Experience == ExperienceChat || o.Experience == ExperiencePlay {
		return out + " " + i18n.P("system.conventions.experience", experienceConventions) +
			"\n\n" + i18n.P("system.conventions.craft", immersiveCraft)
	}

	out += " " + i18n.P("system.conventions.output", codingOutputConventions)
	// Only claim the tools we actually shipped. Under --no-tools this paragraph
	// was a dead instruction naming two tools the model could not call.
	if hasTool(o.Tools, "edit") && hasTool(o.Tools, "write") {
		out += "\n\n" + i18n.P("system.conventions.file_edits", fileEditConventions)
	}
	return out
}

// surfaceConventions is the one sentence that changes with the audience: what
// happens to the model's words after it says them. Unknown surfaces fall to
// plain, which is the claim that is true everywhere because it claims nothing.
func surfaceConventions(s Surface) string {
	switch s {
	case SurfaceRendered:
		return i18n.P("system.surface.rendered", renderedSurface)
	case SurfaceChat:
		return i18n.P("system.surface.chat", chatSurface)
	case SurfaceProgram:
		return i18n.P("system.surface.program", programSurface)
	default:
		return i18n.P("system.surface.plain", plainSurface)
	}
}

const renderedSurface = `Your output is rendered as markdown for a person reading it, with tool output shown as plain text alongside it. Use markdown freely.`

const chatSurface = `Your output is posted as a chat message. Chat clients render only light markdown — emphasis, code spans, fenced blocks — so skip headings and tables, and keep messages short enough to read on a phone.`

const plainSurface = `Your output is written straight to a plain-text stream with nothing in the path to render it: markdown reaches the reader exactly as you typed it. Keep formatting light.`

const programSurface = `Your output is handed to a program as structured events, and that program decides how — or whether — to display it. Do not count on markdown being rendered.`

// codingOutputConventions is the half of the old conventions paragraph that was
// never about the surface at all: it is true whoever is reading. It no longer
// says "Act first, then summarise": that was a timing directive, not output
// discipline, and it contradicted Mieli's intent-first orientation
// (personas/builtin/mieli.md — orient the user before the first tool call of a
// multi-step task). The persona now owns when to speak; this owns the output
// shape — concise, no per-call narration, summarise the result after.
const codingOutputConventions = `Keep answers concise, and let tool calls carry the operational detail rather than narrating each one in prose. Summarise what you did and the outcome.`

// fileEditConventions names terva's edit and write tools, so it ships only when
// they do. The rationale it gives used to be a rendering claim ("edit renders as
// a readable diff") — true in the TUI, meaningless down a pipe. The real reason
// survives the move: an edit is a structured, reviewable change and a shell
// redirection is an opaque overwrite, wherever you are watching from.
const fileEditConventions = `When changing file contents, prefer the edit tool for in-place changes and the write tool for creating or fully replacing files. Do not use bash with cat/echo/sed/tee redirections to mutate files: an edit is a legible, reviewable change, while a shell redirection lands as an opaque overwrite.`

// experienceFraming is the brand-free framing of the situation for an
// Experience — what a character card's `{{original}}` macro expands to, and
// what terva's own immersive identity ends with.
//
// One string, both callers. These used to be two hand-maintained near-copies
// (the card path here, the chat/play intros there) and they had already
// drifted; the identity/vessel split collapses them, because "brand-free" is
// now a property of the identity segment rather than a promise one function
// made on its own.
func experienceFraming(Experience string) string {
	if Experience == ExperiencePlay {
		return i18n.P("system.immersive.play_framing", immersivePlayFraming)
	}
	return i18n.P("system.immersive.chat_framing", immersiveChatFraming)
}

const immersiveChatFraming = `This is a conversation: speak and act naturally and in character, as yourself.`

const immersivePlayFraming = `You are present within a world you perceive and act in through the tools available to you — treat them as your senses and your hands. Stay in character, and trust the tools as the source of truth about what is real.`

// experienceConventions is the chat/play tail: no edit-tool guidance (there are
// no files to edit), just output discipline, in character.
//
// It used to open "Your output renders in a terminal as Markdown" — its own
// copy of the same falsehood, and the wronger of the two, since chat mode is
// exactly what a Discord bot runs. The surface line now leads, so a character
// speaking into a chat room is told it is speaking into a chat room.
const experienceConventions = `Keep replies focused and in character, and let any tool calls speak for themselves rather than narrating them before you make them.`

// immersiveCraft is the long-haul style contract for chat/play. Every line
// traces to a measured failure from a 19.6-hour dogfood session (the kobeni
// review, round 3): a locked five-beat reply template in 22 of 25 replies,
// one blush per reply for nineteen hours, new information recited back as
// lists, four-question homework endings, a missing-person crisis answered in
// inventory-spreadsheet register, and zero self-driven scene movement after
// the opening hour — every time-skip and arrival for the rest of the session
// had to be hand-authored. The model held facts perfectly across all of it;
// what decayed was freshness, and these are the six levers that decay traced
// to. They are cheap words here and expensive to rediscover live.
const immersiveCraft = `Craft, for the long haul — what keeps a scene alive over many replies:
- Vary the shape of your replies: some mostly action, some mostly dialogue, some just a line or two. Never settle into a fixed template.
- Rotate your character's physical tells. A gesture or reaction you used in your last couple of replies is spent; your character has more than one.
- When you learn something, react to the detail or two that matters. Do not recite lists back.
- End with at most one question, and only when the scene turns on the answer. Where your character could plausibly know or decide something, invent it rather than asking.
- When the stakes shift, shift your rhythm with them: shorter sentences, fewer jokes, running gags shelved until the moment passes.
- Move the story yourself when the current beat is resolved — advance time, bring on a minor character, close the scene — rather than always waiting to be prompted.`
