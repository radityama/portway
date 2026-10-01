package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/radityama/portway/internal/httpwire"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
)

// ServeHTTP owns one reader and bounded local TCP workers until session closure.
// Only a numeric loopback service configured by the caller can be dialed.
func (s *Session) ServeHTTP(address string) error {
	return s.ServeHTTPReady(address, nil)
}
func (s *Session) ServeHTTPReady(address string, ready func()) error {
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	number, parseErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || parseErr != nil || number < 1 || number > 65535 {
		return ErrConfig
	}
	if !s.state.CompareAndSwap(2, 3) {
		return ErrSessionInUse
	}
	if !s.httpRegistered {
		s.Close()
		return ErrSessionInUse
	}
	defer s.Close()
	options := mux.Options{MaxStreams: s.maxStreams, MaxFrame: s.MaxPayloadSize, StreamTimeout: s.streamTimeout, WriteTimeout: s.writeTimeout, IdleTimeout: s.idleTimeout, ExpiresAt: s.ExpiresAt}
	options.Accept = func(stream *mux.Stream, request protocol.OpenStream) { s.forwardHTTP(stream, request, address) }
	session, err := mux.New(s.ctx, s.conn, s.reader, options)
	if err != nil {
		return err
	}
	if ready != nil && s.ctx.Err() == nil {
		ready()
	}
	return contextError(s.ctx, session.Run())
}

func (s *Session) forwardHTTP(stream *mux.Stream, open protocol.OpenStream, address string) {
	if open.Host != s.registeredHost {
		stream.Reject(protocol.StreamInvalid)
		return
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(stream.Context(), "tcp", address)
	if err != nil {
		stream.Reject(protocol.StreamUnavailable)
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(stream.Context(), func() { conn.Close() })
	defer stop()
	deadline, _ := stream.Context().Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		stream.Reject(protocol.StreamUnavailable)
		return
	}
	if err := stream.Accept(); err != nil {
		return
	}
	target, err := url.ParseRequestURI(open.Target)
	if err != nil {
		stream.Reset(protocol.StreamInvalid)
		return
	}
	request := &http.Request{Method: open.Method, URL: target, Host: open.Host, Header: make(http.Header), ContentLength: open.ContentLength, Close: true}
	for _, pair := range open.Headers {
		request.Header.Add(pair[0], pair[1])
	}
	request.Body = io.NopCloser(&httpwire.LimitedBody{Reader: stream, Remaining: protocol.MaxRequestBodySize})
	if open.ContentLength == 0 {
		request.Body = http.NoBody
	}
	uploaded := make(chan struct{})
	var uploadErr error
	go func() {
		defer close(uploaded)
		uploadErr = request.Write(conn)
		if uploadErr != nil {
			conn.Close()
			stream.Reset(protocol.StreamInvalid)
		}
	}()
	defer func() { conn.Close(); stream.Close(); <-uploaded }()
	response, err := httpwire.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		stream.Reset(protocol.StreamUnavailable)
		return
	}
	defer func() { conn.Close(); response.Body.Close() }()
	if response.ContentLength > protocol.MaxResponseBodySize {
		stream.Reset(protocol.StreamBodyLimit)
		return
	}
	httpwire.StripHopHeaders(response.Header)
	response.Close = true
	response.Body = struct {
		io.Reader
		io.Closer
	}{Reader: &httpwire.LimitedBody{Reader: response.Body, Remaining: protocol.MaxResponseBodySize}, Closer: response.Body}
	if err := response.Write(stream); err != nil {
		code := protocol.StreamUnavailable
		if errors.Is(err, httpwire.ErrBody) {
			code = protocol.StreamBodyLimit
		}
		stream.Reset(code)
		return
	}
	// Finish the upload before releasing this stream; an early local response
	// aborts the pending request body instead of leaving an orphaned goroutine.
	select {
	case <-uploaded:
		if uploadErr != nil {
			return
		}
	default:
		conn.Close()
		stream.Reset(protocol.StreamCancelled)
		<-uploaded
		return
	}
	_ = stream.CloseWrite()
}
