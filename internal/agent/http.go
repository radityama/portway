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
	return s.serveHTTP(address, nil, ready, nil)
}

// ServeHTTPGraceful preserves admitted work on the session lifetime context and
// uses a separate cancellation signal to start deadline-bound draining.
func (s *Session) ServeHTTPGraceful(address string, shutdown context.Context, ready, draining func()) error {
	return s.serveHTTP(address, shutdown, ready, draining)
}

func (s *Session) serveHTTP(address string, shutdown context.Context, ready, draining func()) error {
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
	options := s.streamOptions(false)
	if shutdown != nil {
		options.Shutdown = shutdown.Done()
		options.OnDraining = draining
	}
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

func (s *Session) streamOptions(diagnostic bool) mux.Options {
	return mux.Options{MaxStreams: s.maxStreams, MaxFrame: s.MaxPayloadSize, StreamTimeout: s.streamTimeout, WriteTimeout: s.writeTimeout, IdleTimeout: s.idleTimeout, ExpiresAt: s.ExpiresAt, Heartbeat: s.heartbeat, Diagnostic: diagnostic, GracefulShutdown: s.graceful, ShutdownTimeout: s.shutdownTimeout, Streaming: !diagnostic && s.streaming, WebSocket: !diagnostic && s.websocket}
}

func (s *Session) forwardHTTP(stream *mux.Stream, open protocol.OpenStream, address string) {
	if open.Host != s.registeredHost || open.Upgrade != "" && !s.websocket {
		stream.Reject(protocol.StreamInvalid)
		return
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(stream.Context(), "tcp", address)
	if err != nil {
		stream.Reject(protocol.StreamUnavailable)
		return
	}
	rawConn := conn
	defer rawConn.Close()
	stop := context.AfterFunc(stream.Context(), func() { rawConn.Close() })
	defer stop()
	deadline, _ := stream.Context().Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		stream.Reject(protocol.StreamUnavailable)
		return
	}
	if s.streaming {
		conn = &httpwire.IdleConn{Conn: conn, Context: stream.Context(), Timeout: s.streamTimeout}
	}
	if err := stream.Accept(); err != nil {
		return
	}
	target, err := url.ParseRequestURI(open.Target)
	if err != nil {
		stream.Reset(protocol.StreamInvalid)
		return
	}
	// This socket still serves exactly one request and is closed by owned
	// cleanup. Do not request an immediate upstream close: an early response
	// with unread upload bytes can otherwise be lost to a TCP reset.
	request := &http.Request{Method: open.Method, URL: target, Host: open.Host, Header: make(http.Header), ContentLength: open.ContentLength}
	for _, pair := range open.Headers {
		request.Header.Add(pair[0], pair[1])
	}
	request.Body = io.NopCloser(&httpwire.LimitedBody{Reader: stream, Remaining: protocol.MaxRequestBodySize})
	if open.ContentLength == 0 {
		request.Body = http.NoBody
	}
	if open.Upgrade == "websocket" {
		s.forwardWebSocket(stream, conn, request)
		return
	}
	uploaded := make(chan struct{})
	go func() {
		defer close(uploaded)
		if uploadErr := request.Write(conn); uploadErr != nil {
			// A local service can reply and close before its upload finishes.
			// Preserve a buffered response on socket-write failures; other body
			// failures close the socket to interrupt a response waiting for input.
			var socketError net.Error
			if !errors.As(uploadErr, &socketError) {
				conn.Close()
			}
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
	// Send response FIN before waiting for the request direction. The relay can
	// finish this response and cancel a pending upload. Cleanup must not reset
	// response bytes still queued at the relay, including bodyless requests.
	if err := stream.CloseWrite(); err != nil {
		return
	}
	<-uploaded
	_ = stream.WaitReceiveClose()
}

func (s *Session) forwardWebSocket(stream *mux.Stream, conn net.Conn, request *http.Request) {
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	if err := request.Write(conn); err != nil {
		stream.Reset(protocol.StreamUnavailable)
		return
	}
	response, reader, err := httpwire.ReadResponseUpgrade(bufio.NewReader(conn), request)
	if err != nil {
		stream.Reset(protocol.StreamUnavailable)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 101 {
		if response.ContentLength > protocol.MaxResponseBodySize {
			stream.Reset(protocol.StreamBodyLimit)
			return
		}
		httpwire.StripHopHeaders(response.Header)
		response.Close = true
		response.Body = struct {
			io.Reader
			io.Closer
		}{&httpwire.LimitedBody{Reader: response.Body, Remaining: protocol.MaxResponseBodySize}, response.Body}
		if err := response.Write(stream); err != nil {
			stream.Reset(protocol.StreamUnavailable)
			return
		}
		if err := stream.CloseWrite(); err == nil {
			_ = stream.WaitReceiveClose()
		}
		return
	}
	if !httpwire.ValidWebSocketResponse(response, request.Header) {
		stream.Reset(protocol.StreamInvalid)
		return
	}
	if err := httpwire.WriteWebSocketResponse(stream, response.Header); err != nil {
		return
	}
	httpwire.Bridge(stream, conn, reader, stream)
}
