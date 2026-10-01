package mux

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

var ErrDraining = errors.New("tunnel connection draining")
var ErrPeerShutdown = errors.New("peer shutting down")

func (c *Conn) Draining() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.draining }

func (c *Conn) receiveGoAway(frame protocol.Frame) error {
	value, err := protocol.DecodeGoAway(frame)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if value.Code == protocol.GoAwayDrained {
		if !c.peerDraining || c.peerDrained {
			return protocol.ErrInvalidGoAway
		}
		c.peerDrained = true
		c.notifyLocked()
		return nil
	}
	if c.peerDraining {
		return protocol.ErrInvalidGoAway
	}
	c.peerDraining = true
	c.draining = true
	c.notifyLocked()
	select {
	case c.drainWake <- struct{}{}:
	default:
	}
	return nil
}

// Shutdown closes admission, sends negotiated GOAWAY and DRAINED and waits for
// stream/acceptance cleanup. Run remains the reader and joins owned workers.
// A repeated call can shorten, but never extend, the first drain deadline.
func (c *Conn) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return net.ErrClosed
	}
	deadline := time.Now().Add(c.opts.ShutdownTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if c.drainDeadline.IsZero() || deadline.Before(c.drainDeadline) {
		c.drainDeadline = deadline
	}
	deadline = c.drainDeadline
	c.draining = true
	first := !c.drainStarted
	c.drainStarted = true
	c.notifyLocked()
	c.mu.Unlock()
	life, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result := func(err error) error {
		// Socket closure may wake Run before the child observes its deadline.
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	// Cancellation/deadline must interrupt a blocked writer, including TLS I/O.
	stop := context.AfterFunc(life, c.Close)
	defer stop()
	if first {
		if c.opts.OnDraining != nil {
			c.opts.OnDraining()
		}
		var err error
		if c.opts.GracefulShutdown {
			// An admitted OPEN must reach the wire before our admission boundary.
			select {
			case c.opening <- struct{}{}:
				frame, _ := protocol.EncodeGoAway(protocol.GoAway{Code: protocol.GoAwayShutdown})
				err = c.write(life, frame)
				<-c.opening
			case <-life.Done():
				err = life.Err()
			case <-c.ctx.Done():
				err = c.ctx.Err()
			}
		}
		c.mu.Lock()
		c.drainWriteErr = err
		close(c.drainWriteDone)
		c.mu.Unlock()
	} else {
		select {
		case <-c.drainWriteDone:
		case <-life.Done():
			return result(life.Err())
		case <-c.ctx.Done():
			return result(c.ctx.Err())
		}
	}
	c.mu.Lock()
	writeErr := c.drainWriteErr
	c.mu.Unlock()
	if writeErr != nil {
		c.Close()
		return result(writeErr)
	}
	for {
		c.mu.Lock()
		finished := len(c.streams) == 0 && c.accepting == 0
		sendDrained := finished && c.ctx.Err() == nil && life.Err() == nil && c.opts.GracefulShutdown && !c.drainedStarted
		if sendDrained {
			c.drainedStarted = true
		}
		// A peer may still be consuming DATA that its reader has buffered. Its
		// DRAINED is the proof that closing our socket cannot discard that data.
		complete := finished && ((!c.opts.GracefulShutdown && c.opts.Accept == nil) || (c.opts.GracefulShutdown && c.drainedSent && c.peerDrained))
		changed := c.changed
		c.mu.Unlock()
		if sendDrained {
			frame, _ := protocol.EncodeGoAway(protocol.GoAway{Code: protocol.GoAwayDrained})
			if err := c.write(life, frame); err != nil {
				c.Close()
				return result(err)
			}
			c.mu.Lock()
			c.drainedSent = true
			c.notifyLocked()
			c.mu.Unlock()
			continue
		}
		if complete || life.Err() != nil || c.ctx.Err() != nil {
			c.mu.Lock()
			if c.terminalError == nil {
				c.terminalError = ErrDraining
				if c.peerDraining {
					c.terminalError = ErrPeerShutdown
				}
			}
			c.mu.Unlock()
			c.Close()
			if err := life.Err(); err != nil {
				return result(err)
			}
			if !complete {
				return result(c.ctx.Err())
			}
			return result(nil)
		}
		select {
		case <-changed:
		case <-life.Done():
		case <-c.ctx.Done():
		}
	}
}

func (c *Conn) drainLoop() {
	select {
	case <-c.ctx.Done():
		return
	case <-c.opts.Shutdown:
	case <-c.drainWake:
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.opts.ShutdownTimeout)
	defer cancel()
	_ = c.Shutdown(ctx)
}
