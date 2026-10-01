package mux

import (
	"time"

	"github.com/radityama/portway/internal/protocol"
)

// A single owned timer worker supervises at most MaxStreams idle lifetimes.
// Credit and connection heartbeats are intentionally not application progress.
func (c *Conn) touchLocked(s *Stream) {
	if !c.opts.Streaming || s.idleExpired || c.streams[s.id] != s {
		return
	}
	s.lastActivity = time.Now()
	select {
	case c.idleWake <- struct{}{}:
	default:
	}
}

func (c *Conn) streamIdleLoop() {
	timer := time.NewTimer(c.opts.StreamTimeout)
	defer timer.Stop()
	for {
		now := time.Now()
		next := now.Add(c.opts.StreamTimeout)
		var expired []*Stream
		c.mu.Lock()
		for _, s := range c.streams {
			deadline := s.lastActivity.Add(c.opts.StreamTimeout)
			if !deadline.After(now) {
				s.idleExpired = true
				expired = append(expired, s)
			} else if deadline.Before(next) {
				next = deadline
			}
		}
		c.mu.Unlock()
		for _, s := range expired {
			s.Reset(protocol.StreamTimeout)
		}
		timer.Reset(time.Until(next))
		select {
		case <-timer.C:
		case <-c.idleWake:
		case <-c.ctx.Done():
			return
		}
	}
}
