package talkoot

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The tools allowlist (TKT-01M39VWN6): a member receives only the tools its
// job needs. The list only narrows, as decision 0013 requires of any grant.

// MaxTools bounds one member's list, so a card stays readable.
const MaxTools = 64

// SeatTools are the tools a member keeps whatever its list says. They are how
// a member speaks to its team and asks the person, so they belong to the seat
// and not to the job.
var SeatTools = []string{
	"talkoot_send", "talkoot_handoff", "talkoot_roster", "talkoot_note_write",
	"talkoot_note_read", "talkoot_propose", "ask_user_question",
}

// toolPattern is a tool name, or a name prefix with a trailing *.
var toolPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}\*?$`)

// serverPattern is an mcp:<server> entry, which names every tool of one MCP
// server by the tool's group.
var serverPattern = regexp.MustCompile(`^mcp:[A-Za-z0-9_.-]{1,64}$`)

// mcpGroup is the group prefix of an MCP tool, as in mcp:<server>.
const mcpGroup = "mcp:"

// seatGroup is the group of a built-in tool, core.CoreToolGroup. The seat
// tools are built-ins, and this package does not import the core.
const seatGroup = "core"

// checkTools checks the shape of a tools list.
//
// 🔑 A name that no tool has is not an error. The tools a session has change
// as extensions and MCP servers come and go, so a roster cannot be checked
// against them. A name that matches nothing narrows to nothing, which fails
// closed.
func checkTools(list []string) error {
	if len(list) == 0 {
		// An empty list and an absent one look alike on the JSON path, where
		// absent means the posture's full set.
		return fmt.Errorf("tools is empty; name the tools the member needs, or name one seat tool such as talkoot_send to leave only the seat")
	}
	if len(list) > MaxTools {
		return fmt.Errorf("tools names %d tools, above the %d limit", len(list), MaxTools)
	}
	seen := map[string]bool{}
	for _, t := range list {
		switch {
		case !toolPattern.MatchString(t) && !serverPattern.MatchString(t):
			return fmt.Errorf("tool %q must be letters, digits, _ and -, with an optional * at the end, or mcp:<server>", t)
		case seen[t]:
			return fmt.Errorf("tool %q appears twice", t)
		}
		seen[t] = true
	}
	return nil
}

// MatchTool reports whether list lets a member have the tool named name, in
// the capability group group. A list entry matches the name exactly, or
// matches its prefix when it ends in *, as a permission rule does. An
// mcp:<server> entry matches the group of that server's tools.
//
// A tool with a seat name is kept exactly when it is the built-in, whatever
// the list says. An extension tool that takes a seat name is in its
// extension's group, and no entry keeps it, a seat name on the list or a
// pattern included. So a seat name on a list grants nothing, and Widens is
// right to count it as covered.
//
// 🔑 A name prefix cannot mark where an MCP server's name ends. MCP tools are
// named mcp_<server>_<tool>, and a server name can hold an underscore, so
// mcp_github_* also matches a server named github_x. The group is exact, and
// an extension cannot take a name under mcp:, so mcp:github names one server.
func MatchTool(list []string, name, group string) bool {
	if slices.Contains(SeatTools, name) {
		return group == seatGroup
	}
	for _, t := range list {
		if strings.HasPrefix(t, mcpGroup) {
			if t == group {
				return true
			}
			continue
		}
		if prefix, ok := strings.CutSuffix(t, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if t == name {
			return true
		}
	}
	return false
}

// toolsWiden reports whether moving from the list was to the list is grants
// the member a tool it did not have. Absent is the full set.
func toolsWiden(was, is []string) bool {
	switch {
	case is == nil:
		return was != nil
	case was == nil:
		return false
	}
	for _, t := range is {
		if !coveredBy(was, t) {
			return true
		}
	}
	return false
}

// coveredBy reports whether every tool entry t allows, the list was allows
// too. A pattern is covered only by an equal or wider pattern, and a seat
// tool is always covered.
func coveredBy(was []string, t string) bool {
	if slices.Contains(SeatTools, t) {
		return true
	}
	if strings.HasPrefix(t, mcpGroup) {
		// A server is covered by the same server, or by a name pattern that
		// every MCP tool name matches, such as mcp_*.
		for _, w := range was {
			wp, wPattern := strings.CutSuffix(w, "*")
			if w == t || (wPattern && strings.HasPrefix("mcp_", wp)) {
				return true
			}
		}
		return false
	}
	tp, tPattern := strings.CutSuffix(t, "*")
	for _, w := range was {
		wp, wPattern := strings.CutSuffix(w, "*")
		switch {
		case !wPattern && !tPattern && w == t:
			return true
		case wPattern && strings.HasPrefix(tp, wp):
			return true
		}
	}
	return false
}
