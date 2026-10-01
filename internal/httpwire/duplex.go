package httpwire

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
)

// IdleConn bounds each socket operation without imposing a total session lifetime.
type IdleConn struct {
	net.Conn
	Context context.Context
	Timeout time.Duration
}

func (c *IdleConn) deadline() time.Time {
	d := time.Now().Add(c.Timeout)
	if parent, ok := c.Context.Deadline(); ok && parent.Before(d) {
		d = parent
	}
	return d
}
func (c *IdleConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetDeadline(c.deadline()); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		_ = c.Conn.SetDeadline(c.deadline())
	}
	return n, err
}
func (c *IdleConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetDeadline(c.deadline()); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)
	if n > 0 {
		_ = c.Conn.SetDeadline(c.deadline())
	}
	return n, err
}
func (c *IdleConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return nil
}

// Bridge owns one additional pump and joins both directions. Restrict Reader/
// Writer interfaces so optimized copies cannot bypass the fixed buffer/deadlines.
func Bridge(stream *mux.Stream, socket net.Conn, socketReader, streamReader io.Reader) {
	stop := context.AfterFunc(stream.Context(), func() { socket.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.CopyBuffer(struct{ io.Writer }{stream}, struct{ io.Reader }{socketReader}, make([]byte, protocol.MaxDataSize))
		if err == nil {
			err = stream.CloseWrite()
		}
		if err != nil {
			stream.Reset(protocol.StreamCancelled)
			socket.Close()
		}
	}()
	_, err := io.CopyBuffer(struct{ io.Writer }{socket}, struct{ io.Reader }{streamReader}, make([]byte, protocol.MaxDataSize))
	if err != nil {
		stream.Reset(protocol.StreamCancelled)
		socket.Close()
	} else if half, ok := socket.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
	<-done
}
