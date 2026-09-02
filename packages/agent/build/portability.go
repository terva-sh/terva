package build

import "strings"

// Prompt-segment source labels. Every segment terva assembles is tagged with
// one of these, and each has exactly one row in the portability table below —
// so "may this text leave the harness?" is answered by a lookup, never by
// re-reading the prose.
//
// These are the provenance labels `--dump-prompt` prints, so they are also a
// user-facing vocabulary: rename with care.
const (
	// Identity and framing.
	SourceCustom        = "custom"         // --system-prompt / SYSTEM.md / immersive persona: a raw replace
	SourceIdentityIntro = "identity-intro" // who you are — brand-free, so it travels
	SourceVessel        = "vessel"         // what carries you: terva. Stays home.
	SourceIntroOverride = "intro-override" // a generic override of the intro
	SourcePersonaIntro  = "persona:introduction"
	SourceCardSystem    = "card:system_prompt"
	SourceCardFraming   = "card:framing"
	SourceCharter       = "charter"     // a persona's additive behavioral charter
	SourceConventions   = "conventions" // terva's harness conventions
	SourceFooter        = "footer"      // date + cwd

	// Hints that name a terva tool or terva itself.
	SourceTervaDocsHint     = "terva-docs-hint"
	SourceTervaExamplesHint = "terva-examples-hint"
	SourceStatusToolHint    = "status-tool-hint"

	// Caller-appended blocks.
	SourceUserAppend          = "user-append"
	SourceContextFiles        = "context-files"
	SourceAgentsMD            = "agents-md"
	SourceSkills              = "skills"
	SourcePinnedSkills        = "pinned-skills" // always-on skill BODIES, not the manifest
	SourceLoreConstant        = "lore:constant"
	SourceCardCharacterBook   = "card:character_book"
	SourceRestrictedWorkspace = "restricted-workspace"
	SourceAutoSwarm           = "auto-swarm"
	SourceSwarmWorktrees      = "swarm-worktrees"
	SourceCast                = "cast"
	SourceSwarmChild          = "swarm-child"
	SourceTasks               = "tasks"
	SourceMemory              = "memory"
	SourceExtensionContext    = "extension-context"

	// Tail region (per-turn, after the cache breakpoint).
	SourceCardPostHistory = "card:post_history"
)

// Source-label prefixes whose members share a portability class, so a new
// member needs no table row.
const (
	sourcePrefixLore    = "lore:"    // keyword-triggered lore fires as lore:<entry>
	sourcePrefixBackend = "backend:" // a worker backend's own augmentation
)

// Portability says whether a segment may cross a harness boundary — whether it
// can be sent to an agent that is not terva.
//
// The question is not academic. terva's prompt names terva's tools, describes
// terva's surfaces, and asserts terva's policy; a foreign coding agent has none
// of those. Forwarding such a segment does not fail loudly — it produces a
// working agent that confidently calls tools it does not have. So the class is
// a property of the segment, checked by the composer, not a convention a
// reviewer has to remember.
//
// See docs/proposals/external-agent-workers.md ("What crosses the boundary").
type Portability string

const (
	// PortabilityPortable travels verbatim. The segment is authored by the user or
	// by a persona and describes identity or intent — not terva's machinery.
	PortabilityPortable Portability = "portable"

	// PortabilityHarnessLocal never crosses. The segment describes terva's
	// tools, surfaces, or policy: abroad it is false at best and an invitation
	// to hallucinate a tool at worst.
	PortabilityHarnessLocal Portability = "harness-local"

	// PortabilityDiscoveryOwned is content a foreign agent finds for itself
	// (project instructions, skills). Forwarding it duplicates — or contradicts
	// — that agent's own discovery, so a renderer passes a *pointer* to the path
	// instead of the payload.
	PortabilityDiscoveryOwned Portability = "discovery-owned"

	// PortabilityNoAnalog has no foreign delivery mechanism at all. terva injects
	// lore, card books, and extension context per turn, into a tail region that
	// foreign agents simply do not expose. There is nothing to render it into.
	PortabilityNoAnalog Portability = "no-analog"
)

// segmentPortability is the one table. Every source constant above appears
// exactly once; TestPortabilityTableIsExhaustive pins that.
var segmentPortability = map[string]Portability{
	// Portable — authored by the user or the persona, about identity or intent.
	// A charter is portable *by contract*: docs/personas.md is explicit that a
	// persona "only shapes the agent's identity; it never grants tools or changes
	// permissions." That capability-neutrality is what lets it cross intact.
	SourceCustom:        PortabilityPortable,
	SourceCharter:       PortabilityPortable,
	SourceUserAppend:    PortabilityPortable,
	SourcePersonaIntro:  PortabilityPortable,
	SourceIntroOverride: PortabilityPortable,
	// Card intros are brand-free by construction — a card owns its identity and
	// terva's name has never appeared beside it. That is the same property a
	// foreign briefing needs, which is why the two paths now share one framing.
	SourceCardSystem:  PortabilityPortable,
	SourceCardFraming: PortabilityPortable,
	// The identity names the agent and nothing else — the harness it runs in
	// moved to the vessel segment, which is precisely what makes this portable.
	SourceIdentityIntro: PortabilityPortable,

	// Harness-local — terva describing terva.
	//
	// The vessel is terva describing itself: the harness, the pine-tar image, the
	// pronunciations. A foreign agent has no use for it and should not be told it
	// lives in one.
	SourceVessel: PortabilityHarnessLocal,
	// conventions names terva's edit/write tools and asserts a TUI that a
	// headless worker does not have.
	SourceConventions:       PortabilityHarnessLocal,
	SourceTervaDocsHint:     PortabilityHarnessLocal, // points at terva's installed docs
	SourceTervaExamplesHint: PortabilityHarnessLocal, // points at terva's installed deploy examples
	SourceStatusToolHint:    PortabilityHarnessLocal, // names terva_status
	SourceAutoSwarm:         PortabilityHarnessLocal, // advertises swarm_spawn
	SourceSwarmWorktrees:    PortabilityHarnessLocal, // describes terva's own lease layout
	SourceCast:              PortabilityHarnessLocal, // advertises actor_spawn
	SourceSwarmChild:        PortabilityHarnessLocal, // the native child's protocol
	SourceTasks:             PortabilityHarnessLocal, // terva's task-tool policy
	// Harness-local because the block LEADS with terva's memory-tool curation
	// policy, which names a tool a foreign agent does not have. The facts under
	// it — what this repo does, how this person works — are genuinely portable,
	// and splitting the rendered block so a briefing can carry them without the
	// policy is worth doing; until then this fails closed, which is the safe
	// direction the table is designed around.
	SourceMemory:              PortabilityHarnessLocal,
	SourceRestrictedWorkspace: PortabilityHarnessLocal, // terva's trust/jail model
	// The footer states the date and cwd. A foreign agent is told its own cwd by
	// its own harness, and the date belongs in the briefing if it matters — so
	// this is ours, not theirs.
	SourceFooter: PortabilityHarnessLocal,

	// Discovery-owned — point, don't paste.
	SourceContextFiles: PortabilityDiscoveryOwned,
	SourceAgentsMD:     PortabilityDiscoveryOwned,
	SourceSkills:       PortabilityDiscoveryOwned,
	// A pinned body is the manifest's payload rather than its index, so it
	// classifies with the manifest. A foreign agent runs its own skill
	// discovery, and pasting a body terva chose to pin either duplicates what
	// that agent already found or overrides a choice its operator made.
	SourcePinnedSkills: PortabilityDiscoveryOwned,

	// No analog — terva-native per-turn injection with no foreign hook.
	SourceLoreConstant:      PortabilityNoAnalog,
	SourceCardCharacterBook: PortabilityNoAnalog,
	SourceCardPostHistory:   PortabilityNoAnalog,
	SourceExtensionContext:  PortabilityNoAnalog,
}

// PortabilityOf classifies a segment source.
//
// It fails *closed*: an unrecognised source is harness-local, so a segment
// added without a table row is withheld from foreign agents rather than leaked
// to them. That is the safe direction — an over-strip degrades a worker's
// briefing and is caught by driving terva itself through the same briefing
// (the terva:portable conformance run), whereas a leak degrades nothing
// visibly and surfaces only as an agent inventing tool calls.
func PortabilityOf(source string) Portability {
	if p, ok := segmentPortability[source]; ok {
		return p
	}
	switch {
	case strings.HasPrefix(source, sourcePrefixLore):
		// Triggered lore fires as lore:<entry-name>; same mechanism, same gap.
		return PortabilityNoAnalog
	case strings.HasPrefix(source, sourcePrefixBackend):
		// A worker backend's own augmentation is authored *for* that agent, so
		// it is portable to it by construction.
		return PortabilityPortable
	}
	return PortabilityHarnessLocal
}

// Portability classifies this segment. It is derived from Source rather than
// stored, so it cannot be set wrong, forgotten at a construction site, or drift
// from the label the dump prints.
func (s PromptSegment) Portability() Portability { return PortabilityOf(s.Source) }

// CrossesHarnessBoundary reports whether this segment may be sent verbatim to a
// non-terva agent. Only portable segments may; discovery-owned content is
// replaced by a pointer and everything else is dropped.
func (s PromptSegment) CrossesHarnessBoundary() bool {
	return s.Portability() == PortabilityPortable
}
