package worker

import (
	"fmt"
	"strings"
	"unicode"

	"terva.sh/terva/packages/agent/build"
)

// Leak is one way a rendered briefing would lie to a foreign agent.
type Leak struct {
	Kind    string // "segment" | "tool"
	Detail  string // the source label, or the tool name
	Excerpt string
}

func (l Leak) Error() string {
	return fmt.Sprintf("worker: %s leak (%s): %s", l.Kind, l.Detail, l.Excerpt)
}

// Scrub reports everything in a rendered briefing that must not have crossed.
//
// Prompt leakage is the ONLY silent failure in this design. A leaked segment
// does not crash the worker and does not fail a request — it produces an agent
// that works, and that reaches for a tool it does not have. Nothing downstream
// will ever tell us. So the check cannot be a review norm; it has to be
// mechanical, and it is, because terva already made both halves enumerable: the
// tool registry has names, and every prompt segment has a labeled source with a
// portability class.
//
// Scrub what you SEND, not what you composed — pass the backend's final rendered
// text. A backend that decorates the briefing with its own framing is scrubbed
// on the decoration too, which is the point.
//
// An empty result means the text names no terva-specific tool and carries no
// segment that was classified as staying home.
func Scrub(text string, r build.Resolved) []Leak {
	var leaks []Leak
	// Collapse whitespace on BOTH sides of the comparison. A backend that
	// word-wraps, re-indents, or JSON-escapes the text it was handed has still
	// leaked it, and a scrub that only catches byte-identical copies would be
	// defeated by a line break.
	hay := strings.ToLower(strings.Join(strings.Fields(text), " "))

	// 1. No segment that stays home may appear in the text. This is the exact
	//    check, and it catches the failure that actually happens: a composer
	//    forwarding a segment it should have dropped. Substring containment on
	//    the real segment text — no heuristics, no false positives.
	for _, seg := range r.SystemSegments {
		switch seg.Portability() {
		case build.PortabilityHarnessLocal, build.PortabilityNoAnalog:
		default:
			continue
		}
		body := strings.TrimSpace(seg.Text)
		if body == "" {
			continue
		}
		// Compare on a distinctive run of the segment rather than the whole
		// thing: a backend may wrap or re-flow the text it was given, and a leak
		// that survived a line-wrap is still a leak.
		if probe := distinctiveRun(body); probe != "" && strings.Contains(hay, probe) {
			leaks = append(leaks, Leak{Kind: "segment", Detail: seg.Source, Excerpt: truncate(body, 80)})
		}
	}

	// 2. No terva-specific tool may be named.
	for name := range r.ToolRegistry {
		if !tervaSpecificTool(name) {
			continue
		}
		if strings.Contains(hay, strings.ToLower(name)) {
			leaks = append(leaks, Leak{Kind: "tool", Detail: name, Excerpt: around(text, name)})
		}
	}
	return leaks
}

// tervaSpecificTool reports whether naming this tool abroad would be a lie.
//
// The distinction is not a heuristic dressed up as a rule — it falls out of what
// the names ARE. terva's harness-specific tools are compound and snake_cased:
// terva_status, swarm_spawn, actor_spawn, activate_tools, task_create,
// session_inspect. None of them exists in another agent's harness, so naming one
// is exactly the failure this whole package is built to prevent.
//
// The bare single-word tools — bash, edit, read, write, glob, grep, skill — are
// the universal primitives of coding agents, and Claude Code, Codex, and OpenCode
// all ship them under those same names. Naming one is not a leak; it is a word.
// And they are ordinary English besides, so scrubbing for them would flag "read
// the spec" and "write it up" forever, and a check that cries wolf gets switched
// off. Rule 1 still says don't name them. This says we cannot MECHANICALLY prove
// it, and pretending otherwise would buy noise instead of safety.
func tervaSpecificTool(name string) bool { return strings.Contains(name, "_") }

// distinctiveRun returns a chunk of a segment long enough to be an unambiguous
// fingerprint of it, taken from the start and trimmed at a word boundary so a
// re-flowed copy still matches.
func distinctiveRun(s string) string {
	const want = 48
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	if len(s) <= want {
		return s
	}
	cut := s[:want]
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > 8 {
		cut = cut[:i]
	}
	return cut
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// around returns a window of text centred on the first occurrence of needle, so
// a failure message shows the offending phrase in situ rather than making the
// reader go hunting for it.
func around(text, needle string) string {
	i := strings.Index(strings.ToLower(text), strings.ToLower(needle))
	if i < 0 {
		return ""
	}
	start, end := i-40, i+len(needle)+40
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	return "…" + strings.Join(strings.Fields(text[start:end]), " ") + "…"
}
