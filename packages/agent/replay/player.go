package replay

import (
	"sync"
	"time"
)

// State is a snapshot of a Player's transport, safe to ship to a client as a
// replay_state event.
type State struct {
	Playing bool    `json:"playing"`
	Pos     int     `json:"position"` // index of the next frame to emit
	Total   int     `json:"total"`    // total frames in the scene
	Speed   float64 `json:"speed"`    // delay multiplier (2 = twice as fast)
}

// Player walks a synthesized frame slice with transport controls. It owns a
// single goroutine that, while playing, waits each frame's speed-scaled delay
// and then emits it to the sink. Play/Pause/Seek/SetSpeed retarget the loop;
// Step emits one frame immediately regardless of timing (keyboard step / tests).
// All methods are safe for concurrent use.
//
// The sink is called with the player's lock released, in emission order, one
// frame at a time; a blocking sink (a reliable subscriber) simply paces the
// loop. The Player never mutates transcript state — a carrier derives snapshots
// from the frames a Seek lands on.
type Player struct {
	frames []Frame
	sink   func(idx int, f Frame)

	mu      sync.Mutex
	pos     int
	playing bool
	speed   float64
	closed  bool
	wake    chan struct{}
	done    chan struct{}
}

// NewPlayer starts a paused player over frames. sink receives each emitted
// frame with its index. Call Close to stop the goroutine.
func NewPlayer(frames []Frame, sink func(idx int, f Frame)) *Player {
	p := &Player{
		frames: frames,
		sink:   sink,
		speed:  1,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	go p.run()
	return p
}

// Play resumes emission from the current position. No-op at end of scene
// (Seek back first to replay).
func (p *Player) Play() {
	p.mu.Lock()
	if p.pos < len(p.frames) {
		p.playing = true
	}
	p.mu.Unlock()
	p.signal()
}

// Pause halts emission, keeping the position.
func (p *Player) Pause() {
	p.mu.Lock()
	p.playing = false
	p.mu.Unlock()
	p.signal()
}

// SetSpeed sets the delay multiplier (>1 faster, <1 slower). Non-positive
// values reset to 1.
func (p *Player) SetSpeed(x float64) {
	if x <= 0 {
		x = 1
	}
	p.mu.Lock()
	p.speed = x
	p.mu.Unlock()
	p.signal()
}

// Seek moves the playhead to pos (clamped to [0, Total]) without emitting the
// skipped frames; a carrier resyncs the client with a fresh snapshot.
func (p *Player) Seek(pos int) {
	p.mu.Lock()
	if pos < 0 {
		pos = 0
	}
	if pos > len(p.frames) {
		pos = len(p.frames)
	}
	p.pos = pos
	p.mu.Unlock()
	p.signal()
}

// Step emits exactly one frame immediately and advances. Returns false when the
// playhead is already at the end. Independent of play/pause.
func (p *Player) Step() bool {
	p.mu.Lock()
	if p.pos >= len(p.frames) {
		p.mu.Unlock()
		return false
	}
	idx := p.pos
	f := p.frames[idx]
	p.pos++
	sink := p.sink
	p.mu.Unlock()
	sink(idx, f)
	return true
}

// State returns the current transport snapshot.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return State{Playing: p.playing, Pos: p.pos, Total: len(p.frames), Speed: p.speed}
}

// Frames exposes the synthesized scene (read-only) so a carrier can derive the
// transcript at any playhead position.
func (p *Player) Frames() []Frame { return p.frames }

// Close stops the player goroutine. Idempotent.
func (p *Player) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	close(p.done)
}

func (p *Player) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Player) run() {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		if !p.playing || p.pos >= len(p.frames) {
			if p.pos >= len(p.frames) {
				p.playing = false // stop at end so State reports paused
			}
			p.mu.Unlock()
			select {
			case <-p.wake:
			case <-p.done:
				return
			}
			continue
		}
		idx := p.pos
		f := p.frames[idx]
		d := scaleDelay(f.Delay, p.speed)
		p.mu.Unlock()

		timer := time.NewTimer(d)
		select {
		case <-timer.C:
			p.mu.Lock()
			// A concurrent Pause/Seek/Step/Close may have moved on; only emit
			// if we're still playing this exact frame.
			if p.closed || !p.playing || p.pos != idx {
				p.mu.Unlock()
				continue
			}
			p.pos++
			if p.pos >= len(p.frames) {
				// Stop at end here, under the same lock as the final advance,
				// so State() reports paused the instant the last frame is
				// emitted — the loop-top stop (below) only re-observes it on the
				// next iteration, a window a State() reader can race into.
				p.playing = false
			}
			sink := p.sink
			p.mu.Unlock()
			sink(idx, f)
		case <-p.wake:
			timer.Stop()
		case <-p.done:
			timer.Stop()
			return
		}
	}
}

func scaleDelay(d time.Duration, speed float64) time.Duration {
	if speed <= 0 || d <= 0 {
		return d
	}
	return time.Duration(float64(d) / speed)
}
