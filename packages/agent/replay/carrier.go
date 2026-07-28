package replay

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// hubBuffer bounds a subscriber's event backlog before a lossy client drops
// (a reliable one blocks instead), mirroring the live workspace hub.
const hubBuffer = 256

// replayStateInterval throttles the replay_state broadcast during autoplay so a
// multi-thousand-frame scene doesn't flood subscribers. The scrubber still moves
// smoothly at ~12 Hz, and the final frame always broadcasts regardless so the
// position lands on 100%/paused when a scene ends on its own.
const replayStateInterval = 80 * time.Millisecond

// Carrier plays a recorded session as a ctrlproto.WorkspaceService. A subscriber
// receives a snapshot of the transcript up to the playhead, then the same
// conversation event stream a live turn produces, paced by the Player. Because
// turns come from a file, the mutating conversation/session/control verbs reject
// with CodeUnsupported and the session is driven by transport (Play/Pause/Step/
// Seek/SetSpeed), not prompts. Playback is pure: no model calls, no tool
// execution, no side effects.
//
// Carrier implements ctrlproto.WorkspaceService and structurally satisfies
// modes.Carrier (it also has SubscribeReliable); the composition root
// asserts modes.Carrier so this package need not import the TUI.
type Carrier struct {
	id       string
	path     string
	meta     core.SessionMeta
	mode     Mode
	autoplay bool
	player   *Player

	totalMessages int
	turnStarts    []int // frame indices of conversation-turn boundaries

	mu          sync.Mutex
	subs        map[chan ctrlproto.Event]bool // value: reliable
	autoStarted bool
	cumUsage    core.WireUsage
	ctxTokens   int
	lastState   time.Time // last onFrame replay_state broadcast; throttle gate
}

var (
	_ ctrlproto.WorkspaceService = (*Carrier)(nil)
	_ ctrlproto.ReplayController = (*Carrier)(nil)
)

// Open reads a session transcript and builds a paused replay Carrier over it.
// Call Close when done to stop the player goroutine and release subscribers.
func Open(path string, opts Options) (*Carrier, error) {
	rows, meta, err := core.ReadReplayRows(path)
	if err != nil {
		return nil, err
	}
	if opts.Mode == "" {
		// Effective: honor checkpoints — the compaction animates and the
		// transcript collapses to the summary mid-scene (Frame.Reset).
		opts.Mode = ModeEffective
	}
	id := meta.ID
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	c := &Carrier{
		id:       id,
		path:     path,
		meta:     meta,
		mode:     opts.Mode,
		autoplay: opts.Autoplay,
		subs:     map[chan ctrlproto.Event]bool{},
	}
	// Seed the usage gauge from the last recorded turn (playback refreshes it
	// live as EvUsage frames emit).
	for _, r := range rows {
		if r.Kind == core.ReplayRowUsage {
			c.cumUsage = toWireUsage(r.Cumulative)
			c.ctxTokens = r.Usage.InputTokens + r.Usage.CacheReadTokens
		}
	}
	frames := Synthesize(rows, opts)
	c.totalMessages = len(foldFrames(frames, len(frames)))
	c.turnStarts = turnBoundaries(frames)
	c.player = NewPlayer(frames, c.onFrame)
	return c, nil
}

// turnBoundaries collects the frame indices where a conversation turn begins —
// each genuine (non-synthetic) user prompt — for seek-by-turn navigation.
func turnBoundaries(frames []Frame) []int {
	var starts []int
	for i, f := range frames {
		if ev, ok := f.Event.(core.EvUserMessage); ok && !ev.Synthetic {
			starts = append(starts, i)
		}
	}
	return starts
}

// turnTarget resolves the frame index to seek to when stepping delta turns from
// the current playhead: delta>0 is the next turn strictly after the playhead
// (or the end past the last), delta<0 is the last turn strictly before it (which
// restarts the current turn when mid-turn). Only the sign of delta is used.
func (c *Carrier) turnTarget(delta int) int {
	pos := c.player.State().Pos
	total := c.player.State().Total
	if len(c.turnStarts) == 0 {
		return pos
	}
	if delta > 0 {
		for _, t := range c.turnStarts {
			if t > pos {
				return t
			}
		}
		return total
	}
	target := 0
	for _, t := range c.turnStarts {
		if t >= pos {
			break
		}
		target = t
	}
	return target
}

// Close stops the player and closes every subscriber channel. Idempotent.
func (c *Carrier) Close() error {
	c.player.Close()
	c.mu.Lock()
	for ch := range c.subs {
		delete(c.subs, ch)
		close(ch)
	}
	c.mu.Unlock()
	return nil
}

// --- transport (drives the Player; backs the replay group + TUI keys) ---

// Play resumes playback from the current position.
func (c *Carrier) Play() { c.player.Play(); c.broadcastState() }

// Pause halts playback, keeping the position.
func (c *Carrier) Pause() { c.player.Pause(); c.broadcastState() }

// Step emits exactly one frame and advances, returning false at end of scene
// (keyboard step / tests).
func (c *Carrier) Step() bool { ok := c.player.Step(); c.broadcastState(); return ok }

// SetSpeed sets the playback speed multiplier.
func (c *Carrier) SetSpeed(x float64) { c.player.SetSpeed(x); c.broadcastState() }

// Transport returns the raw transport snapshot (position/total/playing/speed).
func (c *Carrier) Transport() State { return c.player.State() }

// Seek moves the playhead and resyncs every client with a fresh snapshot.
// Scrubbing pauses playback: pausing is an emission barrier (the player's emit
// guard skips a frame once paused), so the resync can rebuild the transcript at
// the new position without racing an in-flight frame. Play to resume.
func (c *Carrier) Seek(pos int) {
	c.player.Pause()
	// Pause stops the run loop from starting a new emit; UnderEmitLock then waits
	// for any in-flight one to finish. Only inside it is it safe to move the
	// playhead and resync — otherwise a stale in-flight frame could broadcast
	// after the fresh snapshot and corrupt every subscriber's transcript.
	c.player.UnderEmitLock(func() {
		c.player.Seek(pos)
		c.mu.Lock()
		c.broadcastLocked(ctrlproto.SnapshotEvent(c.snapshotLocked()))
		c.broadcastLocked(ctrlproto.ReplayStateEvent(c.wireState()))
		c.mu.Unlock()
	})
}

// --- ctrlproto.ReplayController: transport over the wire ---

// ReplayControl applies a transport action and returns the new state. Every
// action also broadcasts an EventReplayState so other clients' scrubbers track.
func (c *Carrier) ReplayControl(ctx context.Context, sess string, p ctrlproto.ReplayControlParams) (ctrlproto.ReplayState, error) {
	switch p.Action {
	case "play":
		c.Play()
	case "pause":
		c.Pause()
	case "step":
		c.Step()
	case "seek":
		c.Seek(p.Position)
	case "turn":
		// Position carries the signed turn delta (+1 next, -1 previous).
		c.Seek(c.turnTarget(p.Position))
	case "speed":
		c.SetSpeed(p.Multiplier)
	default:
		return ctrlproto.ReplayState{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown replay action %q", p.Action)
	}
	return c.wireState(), nil
}

// ReplayState returns the current transport state.
func (c *Carrier) ReplayState(ctx context.Context, sess string) (ctrlproto.ReplayState, error) {
	return c.wireState(), nil
}

func (c *Carrier) wireState() ctrlproto.ReplayState {
	return c.wireStateFrom(c.player.State())
}

func (c *Carrier) wireStateFrom(s State) ctrlproto.ReplayState {
	return ctrlproto.ReplayState{
		Playing:  s.Playing,
		Position: s.Pos,
		Total:    s.Total,
		Speed:    s.Speed,
		Mode:     string(c.mode),
	}
}

func (c *Carrier) broadcastState() {
	c.mu.Lock()
	c.broadcastLocked(ctrlproto.ReplayStateEvent(c.wireState()))
	c.mu.Unlock()
}

// --- event fan-out ---

func (c *Carrier) onFrame(idx int, f Frame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u, ok := f.Event.(core.EvUsage); ok {
		c.cumUsage = toWireUsage(u.Cumulative)
		c.ctxTokens = u.Usage.InputTokens + u.Usage.CacheReadTokens
	}
	c.broadcastLocked(ctrlproto.ConversationEvent(core.EventToWire(f.Event)))
	if f.Reset != nil {
		// Compaction resync: after compact_end the transcript is the checkpoint
		// summary. Mirror the live daemon's post-compact snapshot so every
		// client collapses its transcript to the compacted state.
		c.broadcastLocked(ctrlproto.SnapshotEvent(c.snapshotLocked()))
	}
	// Keep scrubbers moving during autoplay: broadcast replay_state as frames
	// emit (throttled so a huge scene doesn't flood the wire), and always on the
	// final frame so the position lands on 100%/paused when playback ends on its
	// own — the transport verbs only broadcast on an explicit action.
	s := c.player.State()
	if s.Pos >= s.Total || time.Since(c.lastState) >= replayStateInterval {
		c.lastState = time.Now()
		c.broadcastLocked(ctrlproto.ReplayStateEvent(c.wireStateFrom(s)))
	}
}

func (c *Carrier) broadcastLocked(ev ctrlproto.Event) {
	for ch, reliable := range c.subs {
		if reliable {
			ch <- ev
			continue
		}
		select {
		case ch <- ev:
		default: // a lossy subscriber that fell behind drops rather than stall
		}
	}
}

func (c *Carrier) subscribe(ctx context.Context, reliable bool) <-chan ctrlproto.Event {
	ch := make(chan ctrlproto.Event, hubBuffer)
	c.mu.Lock()
	ch <- ctrlproto.SnapshotEvent(c.snapshotLocked()) // guaranteed first
	c.subs[ch] = reliable
	// Autoplay fires on the first subscriber — the earliest point a played
	// frame reaches a live channel. Play() is deferred out of the lock (it
	// re-enters c.mu via broadcastState).
	startNow := c.autoplay && !c.autoStarted
	if startNow {
		c.autoStarted = true
	}
	c.mu.Unlock()
	if startNow {
		c.Play()
	}
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.mu.Unlock()
	}()
	return ch
}

// snapshotLocked builds the transcript as of the current playhead by folding the
// frames already played. Callers hold c.mu.
func (c *Carrier) snapshotLocked() ctrlproto.Snapshot {
	ts := foldFrames(c.player.Frames(), c.player.State().Pos)
	wire := make([]core.WireMessage, len(ts))
	for i, m := range ts {
		wire[i] = core.MessageToWire(m)
	}
	return ctrlproto.Snapshot{Session: c.sessionInfoLocked(), Messages: wire}
}

func (c *Carrier) sessionInfoLocked() ctrlproto.SessionInfo {
	return ctrlproto.SessionInfo{
		ID:            c.id,
		Title:         c.meta.Title,
		Provider:      c.meta.Provider,
		Model:         c.meta.Model,
		Path:          c.path,
		Messages:      c.totalMessages,
		Usage:         c.cumUsage,
		Current:       true,
		ContextTokens: c.ctxTokens,
	}
}

// --- ctrlproto.WorkspaceService: read-only surface ---

// Subscribe streams the replay: a snapshot up to the playhead, then paced events.
func (c *Carrier) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	return c.subscribe(ctx, false), nil
}

// SubscribeReliable is Subscribe with no-drop delivery (the in-process TUI).
func (c *Carrier) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	return c.subscribe(ctx, true), nil
}

// Sessions reports the single replay session.
func (c *Carrier) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []ctrlproto.SessionInfo{c.sessionInfoLocked()}, nil
}

// ResumeSession returns the replay session's descriptor.
func (c *Carrier) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionInfoLocked(), nil
}

// Usage returns the recorded session's cumulative totals.
func (c *Carrier) Usage(ctx context.Context, sess string) (core.WireUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cumUsage, nil
}

// UsageSnapshot has nothing to report: a replay has no provider client, so
// there is no subscription or credit picture behind it. HasData=false is the
// same answer a live session on a provider that reports no usage gives, and
// the TUI renders it the same way — "doesn't report usage limits" — rather
// than an error banner on every turn.
func (c *Carrier) UsageSnapshot(ctx context.Context, sess string, refresh bool) (ctrlproto.UsageInfo, error) {
	return ctrlproto.UsageInfo{}, nil
}

// ListResets reports no reset credits: a replay has no provider client to ask.
// Supported=false hides the affordance, matching UsageSnapshot's posture.
func (c *Carrier) ListResets(ctx context.Context, sess string) (ctrlproto.ResetsListResult, error) {
	return ctrlproto.ResetsListResult{}, nil
}

// ConsumeReset is unsupported on a replay — there is no live provider to redeem
// against — so it refuses cleanly rather than pretending to spend a credit.
func (c *Carrier) ConsumeReset(ctx context.Context, sess, id string) (ctrlproto.ResetConsumeResult, error) {
	return ctrlproto.ResetConsumeResult{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "replay sessions have no usage resets")
}

// Surfaces reports no auxiliary panes for a replay (nothing to act on).
func (c *Carrier) Surfaces(ctx context.Context, sess string) ([]ctrlproto.SurfaceMeta, error) {
	return nil, nil
}

// --- ctrlproto.WorkspaceService: rejected (read-only session) ---

// Cancel/Approve/Answer are benign no-ops: a replay has no live turn to
// interrupt and no pending permission/ask to resolve.
func (c *Carrier) Cancel(ctx context.Context, sess string) error { return nil }
func (c *Carrier) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	return nil
}
func (c *Carrier) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error {
	return nil
}

func (c *Carrier) Prompt(ctx context.Context, sess string, p ctrlproto.PromptParams) error {
	return unsupported("prompt")
}
func (c *Carrier) Queue(ctx context.Context, sess, text string) error { return unsupported("queue") }
func (c *Carrier) SetQueue(ctx context.Context, sess string, texts []string) error {
	return unsupported("queue")
}
func (c *Carrier) Compact(ctx context.Context, sess string) error { return unsupported("compact") }
func (c *Carrier) Clear(ctx context.Context, sess string) error   { return unsupported("clear") }
func (c *Carrier) EditMessage(ctx context.Context, sess string, epoch uint64, index int, text string) error {
	return unsupported("message.edit")
}
func (c *Carrier) DeleteMessage(ctx context.Context, sess string, epoch uint64, index int) error {
	return unsupported("message.delete")
}
func (c *Carrier) SwipeTurn(ctx context.Context, sess string, epoch uint64, variant int) error {
	return unsupported("turn.swipe")
}

func (c *Carrier) SwipeMessage(ctx context.Context, sess string, epoch uint64, index, variant int) error {
	return unsupported("turn.swipe")
}
func (c *Carrier) RetryTurn(ctx context.Context, sess string, p ctrlproto.TurnRetryParams) error {
	return unsupported("turn.retry")
}
func (c *Carrier) ForkSession(ctx context.Context, sess string, fromIndex int) (ctrlproto.SessionInfo, error) {
	return ctrlproto.SessionInfo{}, unsupported("sessions.fork")
}

// A replay has no provider client, so there is nothing to complete against.
func (c *Carrier) SideChatOpen(ctx context.Context, sess string) (string, error) {
	return "", unsupported("side chat")
}
func (c *Carrier) SideChatAsk(ctx context.Context, sess, id string, prior []ctrlproto.SideChatTurn, question string) (string, error) {
	return "", unsupported("side chat")
}
func (c *Carrier) SideChatClose(ctx context.Context, sess, id string) error { return nil }
func (c *Carrier) CreateSession(ctx context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	return ctrlproto.SessionInfo{}, unsupported("create session")
}
func (c *Carrier) RenameSession(ctx context.Context, sess, title string) error {
	return unsupported("rename")
}
func (c *Carrier) GenerateSessionTitle(ctx context.Context, sess string) (string, error) {
	return "", unsupported("generate title")
}
func (c *Carrier) DeleteSession(ctx context.Context, sess string) error {
	return unsupported("delete")
}
func (c *Carrier) Context(ctx context.Context, sess string) (ctrlproto.ContextBreakdown, error) {
	return ctrlproto.ContextBreakdown{}, unsupported("context")
}
func (c *Carrier) Node(ctx context.Context, sess, id, op string) (ctrlproto.ContextNode, error) {
	return ctrlproto.ContextNode{}, unsupported("context node")
}

// History is unsupported here for the same reason Reveal is: a replay carrier hands
// the whole scene to the player, which owns the playhead. There is no window to page.
func (c *Carrier) History(ctx context.Context, sess string, before, limit int, epoch uint64) (ctrlproto.HistoryResult, error) {
	return ctrlproto.HistoryResult{}, unsupported("history")
}

// Reveal is unsupported here, and deliberately so: a replay carrier already
// answers this question at the scene level. ModeRaw plays the full original
// history — every turn a compaction later summarized away — so there is nothing
// behind a divider left to reveal. Wiring it up would give a player two different
// ways to show the same messages.
func (c *Carrier) Reveal(ctx context.Context, sess string, ordinal int) (ctrlproto.RevealResult, error) {
	return ctrlproto.RevealResult{}, unsupported("reveal")
}
func (c *Carrier) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	return ctrlproto.Surface{}, unsupported("surface")
}
func (c *Carrier) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	return unsupported("surface action")
}
func (c *Carrier) Catalog(ctx context.Context, lang string) (ctrlproto.CatalogView, error) {
	return ctrlproto.CatalogView{}, unsupported("catalog")
}
func (c *Carrier) ListFiles(ctx context.Context, opts ctrlproto.FilesListParams) (ctrlproto.FilesListResult, error) {
	return ctrlproto.FilesListResult{}, unsupported("files.list")
}

// A recording has no credentials — it is a transcript, not a daemon. Reporting
// the LOCAL machine's providers here would be a lie about the session being
// replayed, and possibly about a different machine entirely.
func (c *Carrier) AuthProviders(ctx context.Context) (ctrlproto.ProvidersView, error) {
	return ctrlproto.ProvidersView{}, unsupported("auth.providers")
}
func (c *Carrier) Models(ctx context.Context, sess string) ([]ctrlproto.ModelInfo, error) {
	return nil, unsupported("models")
}
func (c *Carrier) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	return unsupported("switch model")
}
func (c *Carrier) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	return unsupported("favorite model")
}
func (c *Carrier) SetDefaultModel(ctx context.Context, provider, model string, scope ctrlproto.DefaultScope) error {
	return unsupported("set default model")
}
func (c *Carrier) Trust(ctx context.Context, parent bool) error { return unsupported("trust") }
func (c *Carrier) Untrust(ctx context.Context) error            { return unsupported("untrust") }
func (c *Carrier) Restart(ctx context.Context) error            { return unsupported("restart") }

func unsupported(what string) error {
	return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "replay session: %s not supported", what)
}

// foldFrames rebuilds the transcript by folding the first pos frames' events —
// appending user/assistant messages and folding consecutive tool results into
// one tool-role message (the same rule the TUI uses to reconstruct a transcript
// from the wire).
func foldFrames(frames []Frame, pos int) []provider.Message {
	pos = min(pos, len(frames))
	var ts []provider.Message
	for i := 0; i < pos; i++ {
		if frames[i].Reset != nil {
			// A compaction checkpoint collapses the transcript to its summary.
			ts = append([]provider.Message(nil), frames[i].Reset...)
			continue
		}
		ts = foldEvent(ts, frames[i].Event)
	}
	return ts
}

func foldEvent(ts []provider.Message, ev core.AgentEvent) []provider.Message {
	switch e := ev.(type) {
	case core.EvUserMessage:
		return append(ts, e.Message)
	case core.EvAssistantMessage:
		return append(ts, e.Message)
	case core.EvToolResult:
		block := provider.ToolResultBlock{CallID: e.ID, Content: e.Result.Content, IsError: e.Result.IsError}
		if n := len(ts); n > 0 && ts[n-1].Role == provider.RoleTool {
			ts[n-1].Content = append(ts[n-1].Content, block)
			return ts
		}
		return append(ts, provider.Message{Role: provider.RoleTool, Content: []provider.Content{block}})
	}
	return ts
}

func toWireUsage(u provider.Usage) core.WireUsage {
	return core.WireUsage{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheRead:  u.CacheReadTokens,
		CacheWrite: u.CacheWriteTokens,
		CostUSD:    u.CostUSD,
	}
}
