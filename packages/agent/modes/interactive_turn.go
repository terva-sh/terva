package modes

// The turn lifecycle: prompt dispatch, agent-event consumption,
// auto/manual compaction, and the streaming pacer.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// buildStudyPrompt returns the canned prompt the /study command
// submits to the agent.
//
// With no argument, /study targets the current directory — the
// historical behaviour. With an argument, /study targets that path
// instead; either a directory ("read every file in here") or a
// single file ("read this file"). The argument can be:
//
//   - a relative path (resolved against cwd)
//   - an absolute path
//   - an @-picker chip, which has already been expanded to an
//     absolute path by expandFileChips before runSlash sees it
//
// The path is stat'd to pick the right wording ("directory" vs
// "file"). If the path doesn't exist, we still build a sensible
// prompt rather than erroring — the agent will surface the
// missing-file failure itself when it tries to read it, which is
// more useful than a refusal here.
func buildStudyPrompt(arg, cwd string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "Read and understand everything in the current directory."
	}
	abs := arg
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	display := arg
	if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		display = rel
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		return "Read and understand the file " + display + "."
	}
	return "Read and understand everything in the directory " + display + "."
}

// applyModelSelection switches the active model (and provider, if the
// new model belongs to a different one). It rebuilds the underlying
// client when needed so the provider wire-protocol matches.
// cancelAndWaitForIdle cancels the active turn (if any) and blocks
// briefly until the turn goroutine has released the turn slot. Used
// before destructive slash commands so transcript-mutating work
// (/clear, /compact, /logout, /login completion, cross-provider
// /model swap) doesn't race with the still-running stream.
//
// The wait is bounded; if the turn doesn't release within the timeout
// we proceed anyway. Worst case is a brief overlap that the agent's
// own mutex protects against.
func (i *Interactive) cancelAndWaitForIdle() {
	if !i.turns.Busy() {
		return
	}
	i.turns.cancelActive()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !i.turns.Busy() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runCompact invokes core.Agent.Compact and reflects the progress in
// the tui. It runs in a goroutine so the ui stays responsive; esc/ctrl+c
// cancel via the same cancelTurn channel used for normal turns.
//
// When auto is true the spinner message is pinned to "condensing
// history" and the status bar surfaces "(auto)" next to the context
// percentage so it's obvious the system triggered this, not the user.
func (i *Interactive) runCompact(parent context.Context, auto bool) {
	ag := i.turns.Agent()
	if ag == nil {
		i.setStatusErr("not logged in. type /login first.")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	if !i.turns.claimCompact(cancel, auto) {
		// A turn is already in flight (raced a concurrent producer).
		// That turn's completion re-evaluates the auto-compact policy,
		// so just stand down.
		cancel()
		return
	}
	i.mu.Lock()
	if auto {
		i.spin.StartFixed("condensing history")
	} else {
		i.spin.StartFixed("compacting")
	}
	i.statusErr = ""
	i.statusOK = ""
	// The stream is deliberately NOT armed: the summary text should
	// not be visible in the chat while compacting. The user just sees
	// the spinner and can keep typing / queue prompts.
	i.scrollOffset = 0
	i.helpBlock = nil
	i.mu.Unlock()
	i.invalidate()
	i.runClaimedCompact(ctx, ag, auto)
}

// runClaimedCompact runs the compaction body on a goroutine for an
// already-claimed compact slot (spinner/status are the caller's job).
// On completion it releases the slot atomically — dropping the queue
// on failure, shifting the head to re-fire on success.
func (i *Interactive) runClaimedCompact(ctx context.Context, ag *core.Agent, auto bool) {
	go func() {
		// Sink discards deltas — we don't stream the summary to the UI.
		sink := func(delta string) {}
		// Interactive compaction runs outside the Prompt loop, so emit the
		// lifecycle events through OnEvent ourselves; without this an
		// extension's OnCompaction / OnCompactStart never fires here.
		reason := "compaction"
		if auto {
			reason = "context near limit"
		}
		ag.EmitLifecycle(core.EvCompactStart{Reason: reason})
		summary, err := ag.Compact(ctx, 4, sink)
		_ = summary
		end := core.EvCompactEnd{}
		if err != nil {
			end.Err = err.Error()
		}
		ag.EmitLifecycle(end)
		failed := err != nil || ctx.Err() != nil
		next, hasNext := i.turns.release(failed)
		i.turns.ResetStream()

		i.mu.Lock()
		switch {
		case err != nil && ctx.Err() != nil:
			i.statusErr = ""
			if auto {
				i.statusOK = "auto-condense cancelled"
			} else {
				i.statusOK = "compaction cancelled"
			}
		case err != nil:
			i.statusErr = "compaction failed: " + err.Error()
			i.statusOK = ""
		default:
			i.statusErr = ""
			// Read token count from the compaction message meta.
			tokens := ""
			msgs := ag.Messages()
			if len(msgs) > 0 && msgs[0].Meta["compaction"] == "true" {
				tokens = msgs[0].Meta["tokens_before"]
			}
			switch {
			case i.pendingPostCompactNote != "":
				i.statusOK = i.pendingPostCompactNote
			case tokens != "":
				i.statusOK = fmt.Sprintf("compacted from ~%s tokens (ctrl+o to expand)", tokens)
			default:
				i.statusOK = "compacted (ctrl+o to expand)"
			}
			i.pendingPostCompactNote = ""
			i.extNotes = stripAutoCompactNotes(i.extNotes)
			i.lastCtxInput = 0
			i.toolCalls = map[string]*tui.ToolCallView{}
			i.toolOrder = nil
			i.turns.ResetGates()
			i.view.InvalidateRenderCache()
		}
		i.mu.Unlock()
		i.invalidate()

		if hasNext {
			p := i.runCtx
			if p == nil {
				p = context.Background()
			}
			i.startTurn(p, next)
		}
	}()
}

func (i *Interactive) startTurn(parent context.Context, prompt string) {
	i.startTurnWithImages(parent, prompt, nil)
}

func (i *Interactive) startTurnWithImages(parent context.Context, prompt string, images []provider.ImageBlock) {
	ag := i.turns.Agent()
	if ag == nil {
		return
	}
	// Surface a dropped-image note up front: the provider layer
	// silently strips images for models without the image-input
	// capability (better than 400-bricking the session), but silence
	// here would leave the user wondering why the model never saw
	// their screenshot.
	if len(images) > 0 {
		i.mu.Lock()
		provName, modelID := i.cfg.Provider, i.cfg.Model
		i.mu.Unlock()
		if m, err := provider.FindModel(provName, modelID); err == nil && !m.Has(provider.CapImageInput) {
			i.mu.Lock()
			i.statusErr = fmt.Sprintf("note: %s can't see images — %d attachment(s) will be dropped", modelID, len(images))
			i.mu.Unlock()
			i.invalidate()
		}
	}

	// Pre-turn safety: if the most recent context measurement is
	// already past the auto-compact threshold, condense before
	// sending so the next outbound request stays under the limit.
	// The prompt is re-armed at the FRONT of the queue so it fires
	// the moment the condensed transcript is ready, ahead of anything
	// queued while waiting. Decided before claiming the slot; if a
	// concurrent producer wins the claim race in between, claimCompact
	// stands down and the prompt path below queues normally.
	if i.shouldAutoCompact() {
		compactCtx, compactCancel := context.WithCancel(parent)
		if i.turns.claimCompact(compactCancel, true) {
			// Re-arm under the claimed slot so the release-side shift
			// finds it first.
			if prompt != "" {
				i.turns.RequeueFront(prompt)
			}
			i.mu.Lock()
			i.statusErr = ""
			i.extNotes = append(i.extNotes, autoCompactNoteLine(i.cfg.Theme, "context near limit — condensing history before sending..."))
			i.pendingPostCompactNote = "context auto-compacted; sending your last message"
			i.spin.StartFixed("condensing history")
			i.scrollOffset = 0
			i.helpBlock = nil
			i.mu.Unlock()
			i.invalidate()
			i.runClaimedCompact(compactCtx, ag, true)
			return
		}
		compactCancel()
	}

	ctx, cancel := context.WithCancel(parent)
	// Atomically claim the turn slot or fold the prompt into the
	// queue. The busy check, the busy claim, and the enqueue all
	// happen under the engine's lock so two producers on different
	// goroutines (key loop, telegram bridge, auto-swarm watcher,
	// extension prompt actions) can't both observe idle and launch
	// concurrent agent.Prompt calls — and a prompt queued as a turn
	// ends can't strand (release shifts the queue under the same
	// lock). See deep-review Part B #1/#3.
	if !i.turns.claimOrQueue(prompt, cancel) {
		cancel()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.spin.Start()
	i.statusErr = ""
	i.statusOK = ""
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.shellBlock = nil // sending a prompt clears any parked shell-escape log
	i.extNotes = nil   // ext notes are one-shot; a new prompt clears them
	i.scrollOffset = 0 // jump back to the bottom on new turn
	// Reset the auto-follow baseline so the very next render doesn't
	// see a synthetic shrink between "last frame had the previous
	// turn's tool overlay" and "this frame had it cleared above".
	// Without this, the guard reads delta = -(rows in cleared
	// overlay) and decrements scrollOffset, which on terminals that
	// mirror terva's pane scroll into the host scrollbar visibly
	// yanks the viewport. See autofollow_shrink_test.go.
	i.prevChatLen = 0
	i.prevChatCols = 0
	i.parkedTurn = 0 // starting a turn clears the /jump parked state
	i.parkedTotal = 0
	i.helpBlock = nil // hide the help block once the user asks something
	i.mu.Unlock()
	i.invalidate()

	sink := func(ev core.AgentEvent) {
		i.handleEvent(ev)
		i.invalidate()
	}

	go func() {
		err := ag.Prompt(ctx, prompt, images, sink)
		failed := err != nil || ctx.Err() != nil
		// Atomically end the turn: cancelled/errored turns drop the
		// queue (no stale follow-ups after an interrupt); clean turns
		// shift the next queued prompt — including one that arrived in
		// the final instants of this turn, which used to strand.
		next, hasNext := i.turns.release(failed)

		i.mu.Lock()
		if err != nil && ctx.Err() == nil {
			i.statusErr = err.Error()
		}
		// Decide whether to offer a model rescue picker for recoverable
		// provider failures (auth/rate/temporary). The picker opens after
		// the mutex is released so it can take its own locks freely.
		var (
			offer       bool
			rescueWhy   string
			rescueImgs  []provider.ImageBlock
			rescueModel string
			rescueProv  string
			rescueFprov string
		)
		if err != nil && ctx.Err() == nil {
			if ok, reason := core.ClassifyRecoverable(err); ok {
				offer = true
				rescueWhy = reason
				rescueImgs = images
				rescueModel = i.cfg.Model
				rescueProv = i.cfg.Provider
				rescueFprov = core.ExtractFailedProvider(err)
				if rescueFprov == "" {
					rescueFprov = i.cfg.Provider
				}
				// Suppress the red banner — the rescue dialog already
				// surfaces the failure.
				i.statusErr = ""
			}
		}
		// Detect HTTP 413 "payload too large" responses. The provider
		// rejected the request because the request body exceeded its
		// per-request limit. Token-based auto-compact can miss this
		// because the limit is on raw bytes, not tokens. Re-queue the
		// prompt so it survives the condense pass and trigger one.
		payloadTooLarge := err != nil && ctx.Err() == nil && core.IsPayloadTooLargeError(err)
		if payloadTooLarge {
			i.statusErr = ""
			i.turns.RequeueFront(prompt)
			i.extNotes = append(i.extNotes, autoCompactNoteLine(i.cfg.Theme, "request was too large. condensing history before retrying ..."))
			i.pendingPostCompactNote = "context auto-compacted; retrying your last message"
		}
		// Persist the assistant's reply (and every tool row before
		// it) to the session file while the turn memory is hot.
		// Without this, WriteNewTranscript only fires at terva exit,
		// meaning a crash or ungraceful kill drops the whole
		// conversation. FlushSession is idempotent (it advances the
		// baseline so subsequent flushes only write new rows).
		flush := i.cfg.FlushSession
		i.mu.Unlock()
		if flush != nil {
			flush()
		}
		// Auto-compact only when the turn completed cleanly AND no
		// queued prompt is about to run (otherwise the queued message
		// would race the condense).
		shouldAutoCompact := !hasNext && !failed && i.shouldAutoCompact()
		i.invalidate()
		parent := i.runCtx
		if parent == nil {
			parent = context.Background()
		}
		switch {
		case hasNext:
			i.startTurn(parent, next)
		case offer:
			i.openRescueDialog(rescueProv, rescueFprov, rescueModel, rescueWhy, prompt, rescueImgs)
		case payloadTooLarge:
			i.runCompact(parent, true)
		case shouldAutoCompact:
			i.runCompact(parent, true)
		}
	}()
}

func stripAutoCompactNotes(notes []string) []string {
	if len(notes) == 0 {
		return notes
	}
	out := notes[:0]
	for _, n := range notes {
		if strings.Contains(n, "condensing history") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// autoCompactNoteLine returns a styled chat-area note for the
// inline auto-compact heads-up. Lives in extNotes so it survives
// the busy-spinner overwrite of the status row.
func autoCompactNoteLine(th tui.Theme, msg string) string {
	return "  " + th.FG256(th.Warning, "⚠ "+msg)
}

// shouldAutoCompact reports whether the last turn pushed context
// usage past the auto-compact threshold. The decision is core policy
// (Agent.ShouldAutoCompact); this wrapper only adds the "not while a
// compaction is already in flight" guard.
func (i *Interactive) shouldAutoCompact() bool {
	ag := i.turns.Agent()
	if ag == nil || i.turns.AutoCompacting() {
		return false
	}
	return ag.ShouldAutoCompact(core.AutoCompactThreshold)
}

func (i *Interactive) handleEvent(ev core.AgentEvent) {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch e := ev.(type) {
	case core.EvAssistantStart:
		// Fires at the top of every oneTurn, including follow-up
		// turns after tool use. Without this, the streaming buffer
		// is still marked off from the previous assistant message
		// and the final summary text pops in all at once instead
		// of typewriter-streaming delta by delta.
		i.turns.BeginAssistant()
		// Clear the live tool-call overlay. Any tools from the
		// previous round are now fully folded into the transcript
		// (assistant tool_use block + tool role message with the
		// result), so keeping them in the overlay would duplicate
		// them in the view — once inside the finalised transcript
		// and once below the streaming block, with the streaming
		// summary sandwiched in between. The next EvToolUseStart
		// will populate fresh entries for this turn's tools.
		i.toolCalls = map[string]*tui.ToolCallView{}
		i.toolOrder = nil
	case core.EvTextDelta:
		// Buffer behind the pacer; the paintPace ticker paints a
		// few runes at a time for a smooth typewriter effect
		// independent of upstream chunk size.
		i.turns.AppendDelta(e.Delta)
	case core.EvAssistantMessage:
		// OnAssistant + telegram mirroring always fire on message
		// arrival — they read the FINAL message content, which is
		// complete regardless of what's still in the pacer.
		i.assistantMessageSideEffects(e.Message)
		// If the pacer still has characters to drain this latches the
		// flushing phase and the paintPace ticker finishes the reveal;
		// otherwise (rare: full-replay sessions, abort paths) state
		// clears synchronously so a later render doesn't show stale
		// text.
		if i.turns.FinishMessage() {
			return
		}
	case core.EvToolUseStart:
		// Live streaming: pre-create the view so the user sees the
		// tool call being composed in real time. Any subsequent
		// EvToolCall for the same ID updates the same struct (the
		// final parsed args + name are already known here).
		if _, exists := i.toolCalls[e.ID]; !exists {
			i.toolCalls[e.ID] = &tui.ToolCallView{
				ID:        e.ID,
				Name:      e.Name,
				Streaming: true,
			}
			i.toolOrder = append(i.toolOrder, e.ID)
			i.turns.GateTool(e.ID)
		}
	case core.EvToolUseArgs:
		if tc, ok := i.toolCalls[e.ID]; ok {
			tc.RawJSONBuf += e.Delta
			// Refresh the live path as soon as it parses; used in
			// the header (write /Users/pat/Desktop/demo.ts)
			// while the content is still streaming.
			if p, pok, _ := tui.ExtractPartialStringField(tc.RawJSONBuf, "path"); pok {
				tc.LivePath = p
			} else if p, pok, _ := tui.ExtractPartialStringField(tc.RawJSONBuf, "file_path"); pok {
				tc.LivePath = p
			}
		}
	case core.EvToolUseEnd:
		if tc, ok := i.toolCalls[e.ID]; ok {
			tc.Streaming = false
		}
	case core.EvToolCall:
		// If we already pre-created the view during streaming, just
		// refresh the final Args summary. Otherwise create a new one
		// (non-streaming providers or legacy paths).
		if tc, ok := i.toolCalls[e.ID]; ok {
			tc.Args = tui.ShortArgs(e.Name, e.Args)
			tc.Streaming = false
		} else {
			i.toolCalls[e.ID] = &tui.ToolCallView{
				ID:   e.ID,
				Name: e.Name,
				Args: tui.ShortArgs(e.Name, e.Args),
			}
			i.toolOrder = append(i.toolOrder, e.ID)
			i.turns.GateTool(e.ID)
		}
	case core.EvToolResult:
		if tc, ok := i.toolCalls[e.ID]; ok {
			tc.Done = true
			tc.Error = e.Result.IsError
			var text strings.Builder
			for _, c := range e.Result.Content {
				if tb, ok := c.(provider.TextBlock); ok {
					if text.Len() > 0 {
						text.WriteString("\n")
					}
					text.WriteString(tb.Text)
				}
			}
			tc.Result = text.String()
		}
		if i.cfg.OnToolResult != nil {
			i.cfg.OnToolResult(e.ID, e.Result)
		}
	case core.EvUsage:
		i.cumUsage = e.Cumulative
		if e.Usage.InputTokens > 0 {
			i.lastCtxInput = e.Usage.InputTokens + e.Usage.CacheReadTokens + e.Usage.CacheWriteTokens
		}
	case core.EvUserMessageRejected:
		// A user_message guard refused the prompt: it never reached the
		// model. Surface the reason so the submit doesn't just vanish.
		reason := e.Reason
		if reason == "" {
			reason = "message blocked by extension"
		}
		i.statusErr = reason
		i.statusOK = ""
		return
	case core.EvTurnEnd:
		if e.Stop == provider.StopAborted {
			i.turns.ResetStream()
			i.statusErr = ""
			i.statusOK = "cancelled"
			return
		}
		if e.Stop == provider.StopLength {
			// The model hit its output-token cap mid-response, so the
			// reply (often a long write/edit) is truncated. Surface it
			// explicitly, otherwise the turn just ends and reads like
			// the UI gave up. The agent already requests the model's
			// full MaxOutput budget, so this means the response genuinely
			// exceeded that ceiling; ask the user to continue.
			i.statusErr = "response hit the model's output-token limit and was cut off, ask it to continue"
			i.statusOK = ""
			return
		}
		// Don't surface mid-loop stream errors as a red banner here.
		// EvTurnEnd fires after every step in a multi-step tool loop,
		// so a transient 503 / network blip would briefly paint a red
		// banner over the still-streaming chat before the agent loop
		// either retries or exits. The final error (if any) is set by
		// startTurnWithImages once Prompt() returns, and recoverable
		// failures are routed to the rescue picker instead — which
		// keeps the chat clean while the agent is working.
		_ = e.Err
	}
}

// assistantText returns the concatenated text of every TextBlock in
// m. Used by the streaming-view dedupe guard to tell when a live
// streamed reply has already been promoted into the transcript.
func assistantText(m provider.Message) string {
	var sb strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// assistantMessageSideEffects runs the non-visual hooks attached to
// EvAssistantMessage: the host-provided OnAssistant callback and the
// telegram-bridge mirror. Called with i.mu held.
//
// Factored out of handleEvent because the streaming pacer may defer
// visual reset until after the last buffered rune has painted, but
// the callbacks themselves must fire on message arrival so
// downstream observers (session persistence, telegram, cost panels)
// don't wait on a UI animation to catch up.
func (i *Interactive) assistantMessageSideEffects(m provider.Message) {
	if i.cfg.OnAssistant != nil {
		i.cfg.OnAssistant(m)
	}
	if i.chatBridge != nil && i.chatBridge.Active() {
		var sb strings.Builder
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(tb.Text)
			}
		}
		if text := sb.String(); strings.TrimSpace(text) != "" {
			go i.chatBridge.OnAssistantText(text)
		}
	}
}

// paintPaceRate is how many runes the streaming pacer releases per
// tick. With a 16ms tick, 6 runes/tick is ~375 runes/s — fast enough
// that a 500-rune summary finishes in ~1.3s, slow enough to look
// like a human typing. Empirically matches the feel of provider
// paths that already drip-stream natively.
const paintPaceRate = 6

// paintPaceInterval is the tick interval for the streaming pacer.
// 16ms lines up with the redraw throttle so we never paint faster
// than the terminal can keep up.
const paintPaceInterval = 16 * time.Millisecond
