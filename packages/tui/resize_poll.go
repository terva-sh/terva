package tui

import "time"

func (p *ProcTerm) startResizePolling(size func() (int, int), interval time.Duration) {
	p.resizeMu.Lock()
	for p.resizeStop != nil {
		select {
		case <-p.resizeStop:
			done := p.resizeDone
			p.resizeMu.Unlock()
			<-done
			p.resizeMu.Lock()
			if p.resizeDone == done {
				p.resizeStop, p.resizeDone = nil, nil
			}
		default:
			p.resizeMu.Unlock()
			return
		}
	}
	defer p.resizeMu.Unlock()
	stop, done := make(chan struct{}), make(chan struct{})
	notify := make(chan struct{}, 1)
	p.resizeStop, p.resizeDone = stop, done
	lastW, lastH := size()
	// Callbacks can restore raw mode, so they must not run on the poller we join.
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-notify:
				p.resizeMu.Lock()
				if p.resizeStop != stop {
					p.resizeMu.Unlock()
					return
				}
				cbs := make([]func(), 0, len(p.resizeCBs))
				for _, cb := range p.resizeCBs {
					cbs = append(cbs, cb)
				}
				p.resizeMu.Unlock()
				for _, cb := range cbs {
					select {
					case <-stop:
						return
					default:
					}
					cb()
				}
			}
		}
	}()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				w, h := size()
				if w != lastW || h != lastH {
					lastW, lastH = w, h
					select {
					case <-stop:
						return
					default:
					}
					select {
					case notify <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
}

func (p *ProcTerm) stopResizePolling() <-chan struct{} {
	p.resizeMu.Lock()
	if p.resizeStop != nil {
		select {
		case <-p.resizeStop:
		default:
			close(p.resizeStop)
		}
	}
	done := p.resizeDone
	p.resizeMu.Unlock()
	if done != nil {
		<-done
		p.resizeMu.Lock()
		if p.resizeDone == done {
			p.resizeStop, p.resizeDone = nil, nil
		}
		p.resizeMu.Unlock()
	}
	return done
}
