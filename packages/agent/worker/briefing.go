// Package worker composes what terva sends to an agent that is not terva.
//
// The dispatch payload is a SPEC, not a prompt. terva's own system prompt is
// assembled for terva: it names terva's tools, describes terva's surfaces, and
// asserts terva's policy. Forwarding it to a foreign coding agent does not fail
// loudly — the worker runs, and confidently calls tools it does not have. So
// nothing is forwarded. A Briefing is composed from the labeled segments terva
// already assembles, and each backend renders the Briefing into its own shape.
//
// The governing rule is the one ctrlproto follows a layer down (interface-first,
// wire-second), applied to prompts: ASSEMBLY-FIRST, RENDERING-SECOND. The
// labeled segment list is the artifact. terva's system prompt is one rendering
// of it; a Briefing is another. This package is a RENDERER of segments, never a
// rival assembler — and the test is sharp: if it ever needs to re-derive
// something build.SystemSegments already knows, the fix is to teach the core,
// not to duplicate it here. (That test has already been applied once and the
// core learned: PromptSegment.Origin exists because pointing a worker at
// AGENTS.md required paths the assembler had been throwing away.)
//
// See docs/proposals/external-agent-workers.md.
package worker

import "strings"

// Briefing is the portable dispatch payload — everything a foreign agent is
// told, and nothing else.
type Briefing struct {
	// Identity is who the worker is. Portable by contract: a persona "only
	// shapes the agent's identity; it never grants tools or changes
	// permissions" (docs/personas.md), and that capability-neutrality is exactly
	// what lets it cross a harness boundary intact.
	Identity Identity

	// Task is what to do, stated as outcomes. Never as tool calls — see Rule 1
	// in Compose.
	Task Task

	// Workspace is where to do it.
	Workspace Workspace

	// Pointers name files the worker should read for itself. Paths, never
	// payloads: it has its own file-reading tools and its own project
	// discovery, so pasting the contents duplicates what it would find anyway,
	// or contradicts it.
	Pointers []Pointer

	// Reporting is how to signal done. A native swarm child emits an explicit
	// task_end event; a foreign worker just stops talking. So "what done looks
	// like" has to be IN the briefing — it is the one net-new prompt asset this
	// design introduces.
	Reporting string

	// Route and Policy are mapped per backend: model ids are not portable
	// across vendors, and terva's approval postures have only lossy analogs in
	// another harness's coarse permission flags.
	Route  Route
	Policy Policy
}

// Identity is the mind that travels. The vessel — terva, the harness, the
// pine-tar image — stays home; see build.SourceVessel.
type Identity struct {
	Name    string // the persona's name
	Intro   string // the brand-free identity line (build.SourceIdentityIntro)
	Charter string // the persona's behavioral charter, verbatim
}

// travellingText is the part of the briefing that belongs in a system prompt
// rather than a turn: who you are. The intro and the charter, joined — the task
// itself is a user turn because it IS one, an instruction rather than an
// identity. A backend that has no separate system-prompt channel still gets it,
// prepended into Text().
func (i Identity) travellingText() string {
	out := strings.TrimSpace(i.Intro)
	if c := strings.TrimSpace(i.Charter); c != "" {
		if out != "" {
			out += "\n\n"
		}
		out += c
	}
	return out
}

// Task states the work as outcomes.
type Task struct {
	Mission     string   // what to accomplish
	Acceptance  string   // how we will know it is done
	Constraints []string // what not to do
}

// Workspace is the leased working directory the worker owns for the duration.
type Workspace struct {
	Path    string // the leased worktree
	Branch  string // the branch it is on, if any
	BaseRef string // what it was cut from, if any
}

// Pointer names a file worth reading, and why. The Note exists because a bare
// path is an instruction to read something without saying whether it matters.
type Pointer struct {
	Path string
	Note string
}

// Route is the model selection terva resolved, in TERVA's namespace. Each
// backend MAPS it onto its own — model ids are not portable, and a Codex worker
// handed terva's chosen id would not recognise it. The briefing carries the
// intent; translation is the backend's job, and doing it here would mean this
// package knowing every vendor's catalogue.
//
// Provider is meaningful only to a config-transitive backend (terva driving
// terva, which takes a --provider flag verbatim); a foreign backend has no use
// for terva's provider id and ignores it, the same way it maps rather than
// honors the model.
type Route struct {
	Provider string
	Model    string
	Effort   string
}

// Policy is terva's approval posture, to be mapped onto the backend's own
// permission flags. Deliberately a string and not core.ApprovalMode: what
// crosses is a posture to be translated, not terva's enum to be honored.
type Policy struct {
	Posture string
}

// Text renders the WHOLE Briefing as portable prose — identity and work
// together. It is the string Scrub checks, because it is everything that
// crosses: whatever a backend puts in a system prompt PLUS whatever it puts in
// the opening turn sums to exactly this. Scrub what you send.
//
// A backend that sends the identity and the work through separate channels
// (Claude Code appends the identity to the system prompt and opens with the
// work) renders them from Identity.travellingText() and Instructions() rather
// than pasting this blob into one turn — but the union is still Text(), so
// scrubbing this covers both channels.
func (b Briefing) Text() string {
	id := b.Identity.travellingText()
	work := b.Instructions()
	switch {
	case id == "":
		return work
	case work == "":
		return id
	default:
		return id + "\n\n" + work
	}
}

// Instructions renders the WORK half of the briefing — the task, the workspace,
// the pointers, the reporting contract — and nothing about who the worker is.
// It is what a config-OPAQUE backend sends as the opening turn: a foreign worker
// inherits nothing from us, so the workspace and the pointers are the value.
// (A config-transitive backend renders selfAssembledTask instead — see there.)
func (b Briefing) Instructions() string {
	return joinBlocks(b.taskBlock(), b.workspaceBlock(), b.pointersBlock(), strings.TrimSpace(b.Reporting))
}

// selfAssembledTask is the opening turn for a config-TRANSITIVE backend (terva
// driving terva): the task and the reporting contract, and NOTHING about the
// workspace or which files to read. A self-assembling worker reads the same
// config the parent did, so the workspace is a --cwd flag and the pointers are
// files it discovers itself — sending them would be noise it already has. This
// is what "only the Task crosses" means concretely.
func (b Briefing) selfAssembledTask() string {
	return joinBlocks(b.taskBlock(), strings.TrimSpace(b.Reporting))
}

// taskBlock is the task stated as outcomes — mission, acceptance, constraints —
// shared by every rendering so the section headers never drift between them.
func (b Briefing) taskBlock() string {
	var blocks []string
	if m := strings.TrimSpace(b.Task.Mission); m != "" {
		blocks = append(blocks, "## Your task\n\n"+m)
	}
	if a := strings.TrimSpace(b.Task.Acceptance); a != "" {
		blocks = append(blocks, "## Done means\n\n"+a)
	}
	if len(b.Task.Constraints) > 0 {
		var c strings.Builder
		c.WriteString("## Constraints\n")
		for _, s := range b.Task.Constraints {
			if s = strings.TrimSpace(s); s != "" {
				c.WriteString("\n- " + s)
			}
		}
		blocks = append(blocks, c.String())
	}
	return joinBlocks(blocks...)
}

func (b Briefing) workspaceBlock() string {
	p := strings.TrimSpace(b.Workspace.Path)
	if p == "" {
		return ""
	}
	line := "## Workspace\n\nYou are working in " + p + "."
	if br := strings.TrimSpace(b.Workspace.Branch); br != "" {
		line += " The branch is " + br + "."
	}
	if base := strings.TrimSpace(b.Workspace.BaseRef); base != "" {
		line += " It was cut from " + base + "."
	}
	return line
}

func (b Briefing) pointersBlock() string {
	if len(b.Pointers) == 0 {
		return ""
	}
	var p strings.Builder
	p.WriteString("## Worth reading\n")
	for _, ptr := range b.Pointers {
		path := strings.TrimSpace(ptr.Path)
		if path == "" {
			continue
		}
		p.WriteString("\n- " + path)
		if note := strings.TrimSpace(ptr.Note); note != "" {
			p.WriteString(" — " + note)
		}
	}
	return p.String()
}

// joinBlocks concatenates non-empty blocks with a blank line between them.
func joinBlocks(blocks ...string) string {
	var out []string
	for _, b := range blocks {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n\n")
}
