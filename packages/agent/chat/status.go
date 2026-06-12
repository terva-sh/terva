package chat

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/provider"
)

// StatusSnapshot is the small cross-host state bundle rendered for
// Telegram /status replies.
type StatusSnapshot struct {
	Provider     string
	Model        string
	CWD          string
	Usage        provider.Usage
	Subscription bool
	ContextUsed  int
	ContextMax   int
	Busy         bool
	Queued       int
}

// FormatStatus renders the same compact model/usage/cost/context
// information shown in the TUI status bar, plus the current directory.
func FormatStatus(s StatusSnapshot) string {
	providerName := strings.TrimSpace(s.Provider)
	model := strings.TrimSpace(s.Model)
	if providerName == "" {
		providerName = "unknown"
	}
	if model == "" {
		model = "unknown"
	}

	var stats []string
	if s.Usage.InputTokens > 0 {
		stats = append(stats, fmt.Sprintf("↑%s", formatTokens(s.Usage.InputTokens)))
	}
	if s.Usage.OutputTokens > 0 {
		stats = append(stats, fmt.Sprintf("↓%s", formatTokens(s.Usage.OutputTokens)))
	}
	if s.Usage.CacheReadTokens > 0 {
		stats = append(stats, fmt.Sprintf("R%s", formatTokens(s.Usage.CacheReadTokens)))
	}
	if s.Usage.CacheWriteTokens > 0 {
		stats = append(stats, fmt.Sprintf("W%s", formatTokens(s.Usage.CacheWriteTokens)))
	}
	if s.Usage.CostUSD > 0 || s.Subscription {
		cost := fmt.Sprintf("$%.3f", s.Usage.CostUSD)
		if s.Subscription {
			cost += " (sub)"
		}
		stats = append(stats, cost)
	}
	if ctx := contextUsage(s.ContextUsed, s.ContextMax); ctx != "" {
		stats = append(stats, ctx)
	}

	line := fmt.Sprintf("(%s) %s", providerName, model)
	if len(stats) > 0 {
		line += "  " + strings.Join(stats, " ")
	}

	state := "idle"
	if s.Busy {
		state = "working"
	}
	lines := []string{line, "state: " + state}
	if s.Queued > 0 {
		lines = append(lines, fmt.Sprintf("queued: %d", s.Queued))
	}
	if cwd := shortenHome(strings.TrimSpace(s.CWD)); cwd != "" {
		lines = append(lines, "cwd: "+cwd)
	}
	return strings.Join(lines, "\n")
}

func contextUsage(used, max int) string {
	if max <= 0 {
		if used <= 0 {
			return ""
		}
		return formatTokens(used)
	}
	pct := float64(used) / float64(max) * 100
	return fmt.Sprintf("%.1f%%/%s", pct, formatTokens(max))
}

func formatTokens(n int) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n < 10_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%dM", (n+500_000)/1_000_000)
	}
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
