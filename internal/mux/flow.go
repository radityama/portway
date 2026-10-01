package mux

import (
	"io"
	"net"

	"github.com/radityama/portway/internal/protocol"
)

const receivePageSize = 4096

type receivePage struct {
	data       [receivePageSize]byte
	start, end int
}

func (c *Conn) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *Conn) wakeControlLocked() {
	select {
	case c.controlWake <- struct{}{}:
	default:
	}
}
func (c *Conn) pageLocked() (*receivePage, error) {
	if n := len(c.freePages); n > 0 {
		page := c.freePages[n-1]
		c.freePages[n-1] = nil
		c.freePages = c.freePages[:n-1]
		page.start, page.end = 0, 0
		return page, nil
	}
	// Partial head/tail pages add at most two pages of slack per active stream.
	limit := int(protocol.InitialConnectionWindow)/receivePageSize + 2*c.opts.MaxStreams
	if c.allocatedPages >= limit {
		return nil, ErrProtocol
	}
	c.allocatedPages++
	return &receivePage{}, nil
}
func (c *Conn) releasePageLocked(page *receivePage) { c.freePages = append(c.freePages, page) }

func (c *Conn) receiveData(id uint64, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	n := uint32(len(payload))
	if n == 0 || n > c.recvWindow || id > c.highest {
		return ErrProtocol
	}
	s := c.streams[id]
	if s == nil {
		// Frames already in flight at reset still spend connection credit.
		c.recvWindow -= n
		c.pendingConnection += n
		c.wakeControlLocked()
		return nil
	}
	s.mu.Lock()
	accepted, ended, closed := s.accepted, s.ended, s.recvClosed
	s.mu.Unlock()
	if ended {
		c.recvWindow -= n
		c.pendingConnection += n
		c.wakeControlLocked()
		return nil
	}
	if !accepted || closed || n > s.recvWindow {
		return ErrProtocol
	}
	c.recvWindow -= n
	s.recvWindow -= n
	for len(payload) > 0 {
		var page *receivePage
		if len(s.pages) > 0 {
			page = s.pages[len(s.pages)-1]
		}
		if page == nil || page.end == receivePageSize {
			var err error
			page, err = c.pageLocked()
			if err != nil {
				return err
			}
			s.pages = append(s.pages, page)
		}
		count := copy(page.data[page.end:], payload)
		page.end += count
		s.buffered += count
		c.queuedBytes += count
		payload = payload[count:]
	}
	c.notifyLocked()
	return nil
}

func (c *Conn) read(s *Stream, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		c.mu.Lock()
		s.mu.Lock()
		ended, err, recvClosed := s.ended, s.endErr, s.recvClosed
		s.mu.Unlock()
		if ended {
			c.mu.Unlock()
			return 0, err
		}
		if err := s.ctx.Err(); err != nil {
			c.mu.Unlock()
			return 0, err
		}
		if c.closed {
			c.mu.Unlock()
			return 0, net.ErrClosed
		}
		if s.buffered > 0 {
			n := 0
			for len(p) > 0 && len(s.pages) > 0 {
				page := s.pages[0]
				count := copy(p, page.data[page.start:page.end])
				page.start += count
				p = p[count:]
				n += count
				if page.start == page.end {
					c.releasePageLocked(page)
					copy(s.pages, s.pages[1:])
					s.pages[len(s.pages)-1] = nil
					s.pages = s.pages[:len(s.pages)-1]
				}
			}
			s.buffered -= n
			c.queuedBytes -= n
			c.pendingConnection += uint32(n)
			if !recvClosed {
				s.pendingWindow += uint32(n)
			}
			c.wakeControlLocked()
			c.mu.Unlock()
			return n, nil
		}
		if recvClosed {
			c.mu.Unlock()
			return 0, io.EOF
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-s.ctx.Done():
		case <-c.ctx.Done():
		}
	}
}

func (c *Conn) discardLocked(s *Stream) {
	for _, page := range s.pages {
		c.releasePageLocked(page)
	}
	s.pages = nil
	c.queuedBytes -= s.buffered
	if !c.closed {
		c.pendingConnection += uint32(s.buffered)
		c.wakeControlLocked()
	}
	s.buffered = 0
	s.pendingWindow = 0
}

func (c *Conn) reserve(s *Stream, wanted int) (int, error) {
	for {
		c.mu.Lock()
		s.mu.Lock()
		ended, err, sendClosed := s.ended, s.endErr, s.sendClosed
		s.mu.Unlock()
		if ended {
			c.mu.Unlock()
			return 0, err
		}
		if err := s.ctx.Err(); err != nil {
			c.mu.Unlock()
			return 0, err
		}
		if c.closed || sendClosed {
			c.mu.Unlock()
			return 0, net.ErrClosed
		}
		n := min(uint32(wanted), s.sendWindow, c.sendWindow)
		if n > 0 {
			s.sendWindow -= n
			c.sendWindow -= n
			c.mu.Unlock()
			return int(n), nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-s.ctx.Done():
		case <-c.ctx.Done():
		}
	}
}

// I/O errors close the connection in write. Only a cancelled/timed-out wait
// before any I/O can refund reserved credit on a still-live connection.
func (c *Conn) refundUnsent(s *Stream, n uint32) {
	c.mu.Lock()
	if c.closed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	valid := n <= protocol.InitialConnectionWindow-c.sendWindow && n <= protocol.InitialStreamWindow-s.sendWindow
	if valid {
		c.sendWindow += n
		s.sendWindow += n
		c.notifyLocked()
	}
	c.mu.Unlock()
	if !valid {
		c.Close()
	}
}

func (c *Conn) applyWindow(frame protocol.Frame) error {
	increment, err := protocol.DecodeWindowUpdate(frame)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if frame.StreamID == 0 {
		if increment > protocol.InitialConnectionWindow-c.sendWindow {
			return ErrProtocol
		}
		c.sendWindow += increment
	} else {
		s := c.streams[frame.StreamID]
		if s == nil {
			if frame.StreamID > c.highest {
				return ErrProtocol
			}
			return nil
		}
		s.mu.Lock()
		accepted, ended := s.accepted, s.ended
		s.mu.Unlock()
		if ended {
			return nil
		}
		if !accepted || increment > protocol.InitialStreamWindow-s.sendWindow {
			return ErrProtocol
		}
		s.sendWindow += increment
	}
	c.notifyLocked()
	return nil
}

func (c *Conn) queueControl(typ protocol.Type, id uint64, code string) error {
	frame, err := protocol.EncodeStreamControl(typ, id, code)
	if err != nil {
		return err
	}
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	select {
	case c.controls <- frame:
		return nil
	default:
		c.Close()
		return ErrProtocol
	}
}

func (c *Conn) nextCredit() (protocol.Frame, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return protocol.Frame{}, false, nil
	}
	if n := c.pendingConnection; n > 0 {
		if n > protocol.InitialConnectionWindow-c.recvWindow {
			return protocol.Frame{}, false, ErrProtocol
		}
		c.pendingConnection = 0
		c.recvWindow += n
		frame, err := protocol.EncodeWindowUpdate(0, n)
		return frame, true, err
	}
	for _, s := range c.streams {
		if n := s.pendingWindow; n > 0 {
			if n > protocol.InitialStreamWindow-s.recvWindow {
				return protocol.Frame{}, false, ErrProtocol
			}
			s.pendingWindow = 0
			s.recvWindow += n
			frame, err := protocol.EncodeWindowUpdate(s.id, n)
			return frame, true, err
		}
	}
	return protocol.Frame{}, false, nil
}

// The reader never waits for application reads or control writes. One owned
// worker coalesces returned credits and sends the bounded reset/rejection queue.
func (c *Conn) controlLoop() {
	for {
		if c.ctx.Err() != nil {
			return
		}
		var frame protocol.Frame
		select {
		case frame = <-c.controls:
		default:
			credit, ok, err := c.nextCredit()
			if err != nil {
				c.Close()
				return
			}
			if ok {
				frame = credit
			} else {
				select {
				case frame = <-c.controls:
				case <-c.controlWake:
					continue
				case <-c.ctx.Done():
					return
				}
			}
		}
		if err := c.write(c.ctx, frame); err != nil {
			c.Close()
			return
		}
	}
}
