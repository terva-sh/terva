package worker

import (
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/i18n"
)

// Compose renders terva's resolved state into a portable Briefing.
//
// It reads r.SystemSegments — the labeled artifact — and lets each segment's
// portability class decide its fate. Nothing here re-reads a file, re-resolves a
// persona, or re-renders a prompt: every fact it needs, the assembler already
// established and labeled. That is the whole discipline. A composer that
// re-derives is a second assembler, and a second assembler drifts.
//
// Two rules govern what comes out, and both are enforced by Scrub rather than by
// remembering:
//
// RULE 1 — NEVER NAME A TOOL. Not terva's (they do not exist over there) and not
// the worker's (we do not know its exact set or version, and its own system
// prompt already covers them). Work is stated as OUTCOMES. This single rule kills
// most of the leak surface, because almost every harness-local segment exists to
// tell the model about a tool.
//
// RULE 2 — INJECT POINTERS, NOT PAYLOADS. A worker has file-reading tools and
// does its own project discovery. Pasting AGENTS.md into its briefing duplicates
// what it will find itself — or contradicts its own CLAUDE.md. Naming the path
// costs nothing and leaks nothing.
// WorkerPosture resolves the approval posture a worker actually runs under, from
// three inputs in strict priority:
//
//  1. override — an EXPLICIT per-worker posture (Agent.Approval) always wins,
//     leased or not. The operator asked for it by name.
//  2. leased — a worker with its OWN worktree runs autonomously ("yolo"). A
//     delegated worker that stopped to ask for every action would defeat the
//     delegation, and its writes at least land in a separate checkout.
//  3. inherited — otherwise the worker shares the host's live cwd, so it keeps
//     the dispatcher's posture rather than silently going yolo where a mistake
//     hits the operator's working tree.
//
// A lease is a git worktree, NOT a security sandbox: a yolo worker still wields
// the operator's own authority (arbitrary bash, network, files outside the
// worktree). This gates AUTONOMY — how often the human is in the loop — not
// privilege. Route a worker's approvals to a human by setting an explicit
// gated posture (which then rides the approval carriers).
func WorkerPosture(override string, leased bool, inherited string) string {
	if o := strings.TrimSpace(override); o != "" {
		return o
	}
	if leased {
		return "yolo"
	}
	return inherited
}

func Compose(r build.Resolved, task Task, ws Workspace) Briefing {
	b := Briefing{
		Task:      task,
		Workspace: ws,
		Reporting: reportingContract(),
		Route:     Route{Provider: r.Provider, Model: r.Model, Effort: r.Reasoning},
		Policy:    Policy{Posture: string(r.ApprovalMode)},
	}
	b.Identity.Name = r.Persona.Name

	for _, seg := range r.SystemSegments {
		switch seg.Portability() {
		case build.PortabilityPortable:
			carryPortable(&b, seg)

		case build.PortabilityDiscoveryOwned:
			// Point, don't paste. Origin is the file list the assembler kept for
			// exactly this. A discovery-owned segment with no Origin has nothing
			// honest to point at — terva's skills manifest lists skill names, and
			// terva's skills are not a foreign agent's skills — so it is dropped
			// rather than turned into a pointer to nowhere.
			for _, path := range seg.Origin {
				b.Pointers = append(b.Pointers, Pointer{
					Path: rerootIntoWorkspace(path, r.CWD, ws.Path),
					Note: pointerNote(seg.Source),
				})
			}

		case build.PortabilityHarnessLocal, build.PortabilityNoAnalog:
			// Dropped, and this is the only correct thing to do with them. They
			// describe terva's tools, terva's screen, and terva's policy, or they
			// ride a per-turn injection hook no foreign agent exposes. Forwarding
			// one does not break the worker; it makes the worker confidently
			// wrong, which is worse.
		}
	}
	return b
}

// carryPortable routes a portable segment into the Briefing. The identity intro
// and the charter have homes of their own; anything else portable is the user's
// own instruction and rides as a constraint, because that is what a user-appended
// block IS — a thing they want honored.
func carryPortable(b *Briefing, seg build.PromptSegment) {
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		return
	}
	switch seg.Source {
	case build.SourceIdentityIntro:
		// Brand-free by construction since the identity/vessel split — which is
		// precisely what makes it portable. Before that split this segment said
		// "operating inside terva … the craft that carries Mieli", and there was
		// nothing here to send.
		b.Identity.Intro = text
	case build.SourceCharter:
		b.Identity.Charter = text
	case build.SourceIntroOverride, build.SourcePersonaIntro,
		build.SourceCardSystem, build.SourceCardFraming:
		// A persona or card that owns its intro outright. Same slot; it already
		// carries no terva branding (that promise predates this work).
		b.Identity.Intro = text
	default:
		// user-append, custom, and a backend's own augmentation.
		b.Task.Constraints = append(b.Task.Constraints, text)
	}
}

// rerootIntoWorkspace translates a path in TERVA's checkout into the same path
// in the WORKER's.
//
// The worker does not work where we do. It works in a leased worktree — its own
// checkout of its own branch — and a pointer at our copy of AGENTS.md sends it
// outside its workspace to read a file that may not even match the branch it was
// given. The file it wants is the one under its own root, at the same relative
// path.
//
// Paths outside terva's cwd are left alone: a global $TERVA_HOME/AGENTS.md or a
// --context-file from elsewhere on disk is the same file for everyone, and has
// no counterpart inside the lease to re-root onto. Likewise when there is no
// lease at all (the worker shares our directory), there is nothing to translate.
func rerootIntoWorkspace(path, cwd, workspace string) string {
	if cwd == "" || workspace == "" {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path // outside our checkout; it has no counterpart in the lease
	}
	return filepath.Join(workspace, rel)
}

// pointerNote says why a file is worth reading. A bare path tells a worker to go
// look at something without telling it whether the thing matters.
func pointerNote(source string) string {
	switch source {
	case build.SourceAgentsMD:
		return i18n.P("worker.pointer.agents_md", "the project's own conventions — follow them")
	case build.SourceContextFiles:
		return i18n.P("worker.pointer.context_files", "context the person who dispatched you wanted you to have")
	}
	return ""
}

// reportingContract is how a foreign worker signals done.
//
// It exists because there is no task_end abroad. A native swarm child emits an
// explicit terminal event; a foreign worker simply stops talking, and "stopped
// talking" is indistinguishable from "crashed", "gave up", and "finished". So
// the convention has to be stated, and it has to be stated portably: what a
// result looks like, and where the work is left.
//
// It names no tool. Rule 1 applies most sharply to the text terva itself writes.
func reportingContract() string {
	return i18n.P("worker.reporting", `## Reporting back

When you are done, your final message is the whole report — it is the only thing the person who dispatched you will read. Summarise what you changed and why, and name the files you touched. If you could not finish, say so plainly and say what blocked you; a clear account of a failure is worth more than an optimistic summary of one.

Leave your work in the workspace on its branch. Do not push it, and do not open a pull request — someone will review it where it stands.`)
}
