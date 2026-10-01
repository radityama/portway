package observability

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

type Body struct {
	io.ReadCloser
	Request *Request
}

func (b *Body) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.Request.AddIn(n)
	if err != nil && err != io.EOF {
		b.Request.Fail()
	}
	return n, err
}

type Writer struct {
	http.ResponseWriter
	Request *Request
	Status  int
}

func (w *Writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *Writer) WriteHeader(status int) {
	if w.Status != 0 {
		return
	}
	if status == 101 || status >= 200 {
		w.Status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *Writer) Write(p []byte) (int, error) {
	if w.Status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	w.Request.AddOut(n)
	if err != nil {
		w.Request.Fail()
	}
	return n, err
}
func (w *Writer) FlushError() error {
	if w.Status == 0 {
		w.WriteHeader(200)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.Request.Fail()
	}
	return err
}
func (w *Writer) Flush() { _ = w.FlushError() }
func (w *Writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		w.Request.Fail()
		return conn, rw, err
	}
	w.Status = 101
	w.Request.AddIn(rw.Reader.Buffered())
	return &countedConn{Conn: conn, request: w.Request}, rw, nil
}

type countedConn struct {
	net.Conn
	request *Request
}

func (c *countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.request.AddIn(n)
	return n, err
}
func (c *countedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.request.AddOut(n)
	if err != nil {
		c.request.Fail()
	}
	return n, err
}
