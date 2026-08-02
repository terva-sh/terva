package worktree

import (
	"fmt"
	"strings"
)

// shortSHA abbreviates a commit for display.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// panelStatus renders the human-facing status label for one worktree.
func panelStatus(it *ListItem) string {
	switch {
	case it.Unmanaged:
		return "unmanaged"
	case it.ClaimedBy != nil && *it.ClaimedBy == "self":
		return "claimed(self)"
	case it.Status == "claimed" && it.ClaimedBy != nil:
		return "claimed(" + trunc(*it.ClaimedBy, 12) + ")"
	case it.StaleReason != "":
		return "stale"
	default:
		return "available"
	}
}

// PanelLines renders the interactive panel body for a ListResult. selected is the
// index of the highlighted row (ignored if out of range). The row matching
// cwd_worktree is tagged "(here)".
func PanelLines(res *ListResult, selected int) []string {
	if res == nil || len(res.Worktrees) == 0 {
		return []string{"  No worktrees yet.", "", "  Create one with the worktree_create tool."}
	}
	cwd := ""
	if res.CWDWorktree != nil {
		cwd = *res.CWDWorktree
	}
	lines := make([]string, 0, len(res.Worktrees))
	for i, it := range res.Worktrees {
		cursor := "  "
		if i == selected {
			cursor = "▶ "
		}
		base := it.BaseRef
		if it.BaseCommit != "" {
			base += "@" + shortSHA(it.BaseCommit)
		}
		var extra string
		if it.HeadCommit != "" && it.HeadCommit != it.BaseCommit {
			extra += " →" + shortSHA(it.HeadCommit)
		}
		if it.Dirty {
			extra += " ✱dirty"
		}
		if it.Name == cwd {
			extra += " (here)"
		}
		lines = append(lines, fmt.Sprintf("%s%-22s %-15s %s%s",
			cursor, trunc(it.Name, 22), panelStatus(it), base, extra))
	}
	return lines
}

// PanelFooter is the static key-hint line.
func PanelFooter() string {
	return "↑/↓ select · ↵ cd · x remove · X remove all available · c collect · r refresh · esc close"
}

// StatusGlance is the short TUI status-bar segment (not model-facing): worktree
// count and how many this session holds.
func StatusGlance(res *ListResult) string {
	if res == nil || len(res.Worktrees) == 0 {
		return ""
	}
	mine := 0
	for _, it := range res.Worktrees {
		if it.ClaimedBy != nil && *it.ClaimedBy == "self" {
			mine++
		}
	}
	if mine > 0 {
		return fmt.Sprintf("worktrees %d · %d yours", len(res.Worktrees), mine)
	}
	return fmt.Sprintf("worktrees %d", len(res.Worktrees))
}

// PanelTitle is the panel's title bar text.
func PanelTitle(res *ListResult) string {
	if res == nil {
		return "Worktrees"
	}
	return strings.TrimSpace(fmt.Sprintf("Worktrees · %s", res.RepoKey))
}

// CollectLines renders the merge-back overview: per worktree, how far its branch
// is ahead of base, the commit subjects, and dirty/unpushed flags. It is
// read-only — the closing line reminds the user to merge manually.
func CollectLines(res *CollectResult) []string {
	if res == nil || len(res.Worktrees) == 0 {
		return []string{"  No worktrees to collect."}
	}
	lines := make([]string, 0, len(res.Worktrees)*3)
	pending := 0
	for _, it := range res.Worktrees {
		head := fmt.Sprintf("  %s  [%s]  +%d ahead of %s", it.Name, it.Branch, it.Ahead, it.BaseRef)
		if it.Dirty {
			head += " ✱dirty"
		}
		if it.Unpushed {
			head += " ⇡unpushed"
		}
		lines = append(lines, head)
		for _, c := range it.Commits {
			lines = append(lines, "      "+trunc(c, 64))
		}
		if it.Ahead == 0 && !it.Dirty {
			lines = append(lines, "      (nothing to collect)")
		} else {
			pending++
		}
		lines = append(lines, "")
	}
	if pending == 0 {
		lines = append(lines, "  All worktrees are even with their base — nothing to merge back.")
	} else {
		lines = append(lines, "  Review, then merge manually (e.g. git merge <branch>). No auto-merge.")
	}
	return lines
}

// CollectFooter is the key-hint line for the collect view.
func CollectFooter() string { return "l list · r refresh · esc close" }

// CollectTitle is the title bar for the collect view.
func CollectTitle(res *CollectResult) string {
	if res == nil {
		return "Worktrees · collect"
	}
	return strings.TrimSpace(fmt.Sprintf("Worktrees · collect · %s", res.RepoKey))
}
