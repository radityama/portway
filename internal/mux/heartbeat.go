package mux

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

var ErrHeartbeatTimeout = errors.New("tunnel heartbeat timeout")

type heartbeatProbe struct {
	value protocol.Heartbeat
	sent  time.Time
}

// Liveness is a read-only snapshot. Probe deadlines never depend on wall clocks
// supplied by a peer or on application traffic.
type Liveness struct {
	AwaitingPong bool
	LastPong     time.Time
}

func (c *Conn) Liveness() Liveness {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Liveness{AwaitingPong: c.probe != nil, LastPong: c.lastPong}
}

func (c *Conn) failHeartbeat(err error) {
	c.mu.Lock()
	if c.terminalError == nil {
		c.terminalError = err
	}
	c.mu.Unlock()
	c.Close()
}

func (c *Conn) heartbeatLoop() {
	timer := time.NewTimer(c.opts.HeartbeatInterval)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.heartbeatWake:
		case <-timer.C:
		}
		c.mu.Lock()
		probe := c.probe
		last := c.lastPong
		c.mu.Unlock()
		now := time.Now()
		if probe != nil {
			remaining := probe.sent.Add(c.opts.HeartbeatTimeout).Sub(now)
			if remaining <= 0 {
				c.failHeartbeat(ErrHeartbeatTimeout)
				return
			}
			timer.Reset(remaining)
			continue
		}
		if !last.IsZero() {
			remaining := last.Add(c.opts.HeartbeatInterval).Sub(now)
			if remaining > 0 {
				timer.Reset(remaining)
				continue
			}
		}
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			c.failHeartbeat(protocol.ErrInvalidHeartbeat)
			return
		}
		probe = &heartbeatProbe{value: protocol.Heartbeat{Nonce: hex.EncodeToString(nonce[:]), Timestamp: now.UTC().Format(time.RFC3339Nano)}, sent: now}
		f, err := protocol.EncodeHeartbeat(protocol.TypePing, probe.value)
		if err != nil {
			c.failHeartbeat(err)
			return
		}
		c.mu.Lock()
		c.probe = probe
		c.mu.Unlock()
		if err := c.write(c.ctx, f); err != nil {
			c.failHeartbeat(err)
			return
		}
		timer.Reset(time.Until(probe.sent.Add(c.opts.HeartbeatTimeout)))
	}
}

func (c *Conn) receiveHeartbeat(f protocol.Frame) error {
	h, err := protocol.DecodeHeartbeat(f)
	if err != nil {
		return err
	}
	if f.Type == protocol.TypePing {
		f.Type = protocol.TypePong
		select {
		case c.controls <- f:
			return nil
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
			return ErrLimit
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probe == nil || c.probe.value != h {
		return protocol.ErrInvalidHeartbeat
	}
	if !time.Now().Before(c.probe.sent.Add(c.opts.HeartbeatTimeout)) {
		return ErrHeartbeatTimeout
	}
	c.probe = nil
	c.lastPong = time.Now()
	select {
	case c.heartbeatWake <- struct{}{}:
	default:
	}
	return nil
}
