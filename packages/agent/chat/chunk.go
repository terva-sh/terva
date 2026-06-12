package chat

import "strings"

// ChunkMessage splits s into chunks no larger than limit bytes, on
// line boundaries when possible. limit <= 0 means no limit.
func ChunkMessage(s string, limit int) []string {
	if limit <= 0 || len(s) <= limit {
		return []string{s}
	}
	var out []string
	lines := strings.Split(s, "\n")
	var cur strings.Builder
	for _, l := range lines {
		if cur.Len()+len(l)+1 > limit && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		if len(l) > limit {
			// Line itself too long; hard-split.
			for len(l) > limit {
				out = append(out, l[:limit])
				l = l[limit:]
			}
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(l)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// IsStopCommand reports whether text should abort the active turn.
// Chat users often type plain "stop" rather than bot-style "/stop";
// keep this intentionally narrow so normal prompts like "stop doing
// X" still go to the agent.
func IsStopCommand(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "stop")
}
