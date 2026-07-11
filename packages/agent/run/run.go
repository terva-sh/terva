// Package run holds the neutral "run one headless turn and return its final
// text" primitive. It depends only on core + provider, so daemon/control-plane
// code (packages/agent/workspace) can drive a throwaway agent turn without
// importing the frontend packages/agent/modes — keeping the dependency
// direction (frontend → workspace → core) intact. modes.RunPrint's print mode
// and workspace's Raati summarizer/clerk both build on this.
package run

import (
	"context"
	"fmt"
	"io"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Print runs the agent to completion and writes only the final assistant text
// block to out. The exit code (if any) is the caller's concern.
func Print(ctx context.Context, ag *core.Agent, prompt string, images []provider.ImageBlock, out io.Writer) error {
	var finalText strings.Builder
	var lastAssistant string
	var runErr error

	sink := func(ev core.AgentEvent) {
		switch e := ev.(type) {
		case core.EvAssistantMessage:
			// Keep the most recent assistant text block; by the end it's the final answer.
			var sb strings.Builder
			for _, c := range e.Message.Content {
				if tb, ok := c.(provider.TextBlock); ok {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(tb.Text)
				}
			}
			if sb.Len() > 0 {
				lastAssistant = sb.String()
			}
		case core.EvTurnEnd:
			if e.Err != nil {
				runErr = e.Err
			}
		}
	}

	// PromptWithPolicy adds the core turn policy interactive users already get:
	// pre-turn compaction for an over-threshold resumed transcript, and one
	// compact-and-retry on HTTP 413.
	if err := ag.PromptWithPolicy(ctx, prompt, images, sink); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}

	finalText.WriteString(lastAssistant)
	if finalText.Len() > 0 && !strings.HasSuffix(finalText.String(), "\n") {
		finalText.WriteString("\n")
	}
	_, err := fmt.Fprint(out, finalText.String())
	return err
}
