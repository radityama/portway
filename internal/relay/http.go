package relay

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/radityama/portway/internal/httpwire"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
)

const earlyResponseDrainTimeout = time.Second

func CanonicalHost(authority string, port int) (string, error) {
	if len(authority) > 259 {
		return "", protocol.ErrInvalidHandshake
	}
	host := authority
	if strings.Contains(authority, ":") {
		var raw string
		var err error
		host, raw, err = net.SplitHostPort(authority)
		number, parseErr := strconv.Atoi(raw)
		if err != nil || parseErr != nil || number != port || raw != strconv.Itoa(number) {
			return "", protocol.ErrInvalidHandshake
		}
	}
	host = strings.ToLower(host)
	if !protocol.ValidHostname(host) {
		return "", protocol.ErrInvalidHandshake
	}
	return host, nil
}

type ingress struct {
	server   *Server
	mu       sync.Mutex
	closing  bool
	handlers sync.WaitGroup
}

func (i *ingress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	if i.closing {
		i.mu.Unlock()
		httpFailure(w, "relay unavailable", 503)
		return
	}
	i.handlers.Add(1)
	i.mu.Unlock()
	defer i.handlers.Done()
	i.server.ServeHTTP(w, r)
}

// ServeHTTPS owns a TLS HTTP/1.1 listener with admission before goroutine creation.
// Cancellation aborts connections and waits for our request handlers to finish.
func (s *Server) ServeHTTPS(ctx context.Context, listener net.Listener, config *tls.Config) error {
	defer listener.Close()
	if config == nil || (len(config.Certificates) == 0 && config.GetCertificate == nil) || config.MinVersion < tls.VersionTLS13 || s.PublicPort < 1 || s.PublicPort > 65535 || s.MaxPublicConnections < 1 || s.MaxPublicConnections > 10000 || s.StreamTimeout <= 0 {
		return ErrConfig
	}
	handler := &ingress{server: s}
	tlsConfig := config.Clone()
	tlsConfig.NextProtos = []string{"http/1.1"}
	httpServer := &http.Server{Handler: handler, TLSConfig: tlsConfig, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: s.StreamTimeout, WriteTimeout: s.StreamTimeout + s.WriteTimeout, IdleTimeout: 120 * time.Second, MaxHeaderBytes: protocol.MaxHTTPHeaderSize, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	limited := &limitedListener{Listener: listener, max: s.MaxPublicConnections, active: make(map[*limitedConn]struct{})}
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return nil
	}
	s.httpServers[httpServer] = limited
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.httpServers, httpServer); s.notifyLocked(); s.mu.Unlock() }()
	closeServer := func() {
		handler.mu.Lock()
		handler.closing = true
		handler.mu.Unlock()
		limited.closeAll()
		_ = httpServer.Close()
	}
	stop := context.AfterFunc(ctx, closeServer)
	err := httpServer.ServeTLS(limited, "", "")
	stop()
	closeServer()
	handler.handlers.Wait()
	if ctx.Err() != nil || errors.Is(err, http.ErrServerClosed) || (s.Draining() && errors.Is(err, net.ErrClosed)) {
		return nil
	}
	return errors.New("public HTTPS listener failed")
}

type limitedListener struct {
	net.Listener
	mu      sync.Mutex
	max     int
	active  map[*limitedConn]struct{}
	closing bool
}
type limitedConn struct {
	net.Conn
	parent *limitedListener
	once   sync.Once
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.closing {
			l.mu.Unlock()
			conn.Close()
			return nil, net.ErrClosed
		}
		if len(l.active) >= l.max {
			l.mu.Unlock()
			conn.Close()
			continue
		}
		bounded := &limitedConn{Conn: conn, parent: l}
		l.active[bounded] = struct{}{}
		l.mu.Unlock()
		return bounded, nil
	}
}

// Close raw public sockets before TLS Close can wait for close-notify. This
// interrupts slow header readers, uploads and blocked response writes together.
func (l *limitedListener) closeAll() {
	l.mu.Lock()
	l.closing = true
	connections := make([]*limitedConn, 0, len(l.active))
	for conn := range l.active {
		connections = append(connections, conn)
	}
	l.mu.Unlock()
	l.Listener.Close()
	for _, conn := range connections {
		conn.Close()
	}
}
func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.parent.mu.Lock(); delete(c.parent.active, c); c.parent.mu.Unlock() })
	return err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		w.Header().Set("Connection", "close")
		httpFailure(w, "relay draining", 503)
		return
	}
	s.httpActive++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.httpActive--; s.notifyLocked(); s.mu.Unlock() }()
	host, err := CanonicalHost(r.Host, s.PublicPort)
	if err != nil || r.URL.IsAbs() || r.URL.Host != "" {
		httpFailure(w, "invalid host or request target", 400)
		return
	}
	if r.TLS == nil || !strings.EqualFold(r.TLS.ServerName, host) {
		httpFailure(w, "TLS hostname mismatch", 421)
		return
	}
	if strings.EqualFold(r.Method, "CONNECT") {
		httpFailure(w, "method unavailable", 405)
		return
	}
	upgrade := r.Header.Get("Upgrade") != "" || protocol.HeaderHasToken(r.Header, "Connection", "upgrade")
	if upgrade {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			httpFailure(w, "HTTP upgrade unavailable", 501)
			return
		}
		if !httpwire.WebSocketRequest(r) {
			httpFailure(w, "invalid WebSocket upgrade", 400)
			return
		}
	}
	if len(r.Trailer) > 0 || len(r.Header.Values("Trailer")) > 0 {
		httpFailure(w, "HTTP trailers unavailable", 400)
		return
	}
	if r.ContentLength > protocol.MaxRequestBodySize {
		httpFailure(w, "request body exceeds limit", 413)
		return
	}
	s.mu.RLock()
	id, known := s.resolveLocked(host)
	owner := s.sessions[id]
	var session *mux.Conn
	var registrationDone <-chan struct{}
	if owner != nil && owner.conn != nil && owner.ctx.Err() == nil && time.Now().Before(owner.ExpiresAt) {
		session = owner.streams
		if session == nil {
			registrationDone = owner.registrationDone
		}
	}
	s.mu.RUnlock()
	if registrationDone != nil {
		// The peer can receive REGISTER_OK before its writer returns and the
		// relay publishes routing. Wait outside the registry lock, within the
		// existing write deadline, then recheck the exact current owner.
		timer := time.NewTimer(s.WriteTimeout)
		defer timer.Stop()
		select {
		case <-registrationDone:
			s.mu.RLock()
			id, known = s.resolveLocked(host)
			owner = s.sessions[id]
			if owner != nil && owner.conn != nil && owner.ctx.Err() == nil && time.Now().Before(owner.ExpiresAt) {
				session = owner.streams
			}
			s.mu.RUnlock()
		case <-r.Context().Done():
			w.Header().Set("Connection", "close")
			httpFailure(w, "tunnel unavailable", 503)
			return
		case <-timer.C:
		}
	}
	if !known {
		httpFailure(w, "tunnel not found", 404)
		return
	}
	if session == nil {
		httpFailure(w, "tunnel unavailable", 503)
		return
	}
	if upgrade && !session.WebSocket() {
		httpFailure(w, "WebSocket capability unavailable", 501)
		return
	}
	header := r.Header.Clone()
	httpwire.StripHopHeaders(header)
	if upgrade {
		header.Del("Sec-WebSocket-Extensions")
	}
	for key := range header {
		if strings.EqualFold(key, "Forwarded") || strings.HasPrefix(strings.ToLower(key), "x-forwarded-") {
			header.Del(key)
		}
	}
	header.Set("X-Forwarded-Host", r.Host)
	header.Set("X-Forwarded-Proto", "https")
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && net.ParseIP(ip) != nil {
		header.Set("X-Forwarded-For", ip)
	}
	pairs := make([][]string, 0, len(header))
	for key, values := range header {
		for _, value := range values {
			pairs = append(pairs, []string{key, value})
		}
	}
	if host != owner.PublicHostname && !session.CustomDomains() {
		httpFailure(w, "custom domain capability unavailable", 501)
		return
	}
	open := protocol.OpenStream{Method: r.Method, Target: r.URL.RequestURI(), Host: owner.PublicHostname, Headers: pairs, ContentLength: r.ContentLength}
	if host != owner.PublicHostname {
		open.PublicHost = host
	}
	if upgrade {
		open.Upgrade = "websocket"
	}
	if err := open.Validate(); err != nil {
		if upgrade {
			httpFailure(w, "invalid WebSocket upgrade", 400)
			return
		}
		httpFailure(w, "invalid or oversized request headers", 431)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	if !session.Streaming() {
		cancel()
		ctx, cancel = context.WithTimeout(r.Context(), s.StreamTimeout)
	}
	defer cancel()
	controller := http.NewResponseController(w)
	if session.Streaming() {
		_ = controller.SetWriteDeadline(time.Time{})
	}
	stream, err := session.Open(ctx, open)
	if err != nil {
		_ = controller.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		httpFailure(w, "tunnel stream unavailable", streamStatus(ctx, err))
		return
	}
	if upgrade {
		s.serveWebSocket(w, r, stream, header)
		return
	}
	_ = controller.EnableFullDuplex()
	refreshWrite := func() {
		if session.Streaming() {
			_ = controller.SetWriteDeadline(time.Now().Add(s.StreamTimeout))
		}
	}
	uploaded := make(chan struct{})
	uploadResult := make(chan struct{})
	stopUpload := make(chan struct{})
	successfulResponse := false // Published to the upload worker by stopUpload.
	var responseBody io.Closer
	var uploadErr error
	var idleBody *idleRequestBody
	if session.Streaming() {
		idleBody = &idleRequestBody{ReadCloser: r.Body, controller: controller, timeout: s.StreamTimeout}
	}
	go func() {
		defer close(uploaded)
		var source io.ReadCloser = r.Body
		if idleBody != nil {
			source = idleBody
		}
		limitedBody := http.MaxBytesReader(nil, source, protocol.MaxRequestBodySize)
		_, uploadErr = io.Copy(stream, limitedBody)
		if uploadErr == nil {
			// net/http can already be watching this socket for disconnect.
			// A finished upload must not leave a read deadline that cancels
			// an active one-way SSE response through that background reader.
			if idleBody != nil {
				idleBody.complete()
			}
			uploadErr = stream.CloseWrite()
		}
		close(uploadResult)
		if uploadErr != nil {
			code := protocol.StreamInvalid
			var tooLarge *http.MaxBytesError
			if errors.As(uploadErr, &tooLarge) {
				code = protocol.StreamBodyLimit
			}
			stream.Reset(code)
			select {
			case <-stopUpload:
				if successfulResponse && r.Context().Err() == nil {
					// Finishing an early response with unread TCP input can reset
					// the socket before the client receives it. Drain only within
					// the remaining body cap and the cleanup deadline below.
					_, _ = io.Copy(io.Discard, limitedBody)
				}
			default:
			}
		}
	}()
	defer func() {
		deadline := time.Now()
		if successfulResponse {
			deadline = deadline.Add(earlyResponseDrainTimeout)
			if streamDeadline, ok := ctx.Deadline(); ok && streamDeadline.Before(deadline) {
				deadline = streamDeadline
			}
		}
		if idleBody != nil {
			idleBody.expireAt(deadline)
		} else {
			_ = controller.SetReadDeadline(deadline)
		}
		close(stopUpload)
		stream.Close()
		if responseBody != nil {
			responseBody.Close()
		}
		<-uploaded
		r.Body.Close()
		_ = controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	response, err := httpwire.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		status := streamStatus(ctx, err)
		select {
		case <-uploadResult:
			var tooLarge *http.MaxBytesError
			if errors.As(uploadErr, &tooLarge) {
				status = 413
			}
		default:
		}
		// Cleanup interrupts unread input with a deadline. That can cancel the
		// HTTP connection's context permanently; do not reuse it for a new request.
		w.Header().Set("Connection", "close")
		refreshWrite()
		httpFailure(w, "upstream response unavailable", status)
		return
	}
	responseBody = response.Body
	if response.ContentLength > protocol.MaxResponseBodySize {
		w.Header().Set("Connection", "close")
		refreshWrite()
		httpFailure(w, "upstream response exceeds limit", 502)
		return
	}
	httpwire.StripHopHeaders(response.Header)
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if response.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	refreshWrite()
	w.WriteHeader(response.StatusCode)
	if err := controller.Flush(); err != nil {
		return
	}
	body := &httpwire.LimitedBody{Reader: response.Body, Remaining: protocol.MaxResponseBodySize}
	buffer := make([]byte, protocol.MaxDataSize)
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			refreshWrite()
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			panic(http.ErrAbortHandler)
		}
	}
	refreshWrite()
	if controller.Flush() == nil {
		successfulResponse = true
	}
}

type idleRequestBody struct {
	io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
	mu         sync.Mutex
	cutoff     time.Time
}

func (b *idleRequestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	deadline := time.Now().Add(b.timeout)
	if !b.cutoff.IsZero() && b.cutoff.Before(deadline) {
		deadline = b.cutoff
	}
	err := b.controller.SetReadDeadline(deadline)
	b.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}
func (b *idleRequestBody) complete() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cutoff.IsZero() {
		_ = b.controller.SetReadDeadline(time.Time{})
	}
}
func (b *idleRequestBody) expireAt(deadline time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cutoff = deadline
	_ = b.controller.SetReadDeadline(deadline)
}
func streamStatus(ctx context.Context, err error) int {
	if errors.Is(err, protocol.ErrPayloadTooLarge) {
		return 431
	}
	if errors.Is(err, mux.ErrLimit) || errors.Is(err, mux.ErrDraining) {
		return 503
	}
	var remote *mux.RemoteError
	if errors.As(err, &remote) {
		if remote.Code == protocol.StreamLimit || remote.Code == protocol.StreamDraining {
			return 503
		}
		if remote.Code == protocol.StreamTimeout {
			return 504
		}
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return 504
	}
	return 502
}

func httpFailure(w http.ResponseWriter, message string, status int) {
	http.Error(w, message, status)
	_ = http.NewResponseController(w).Flush()
}
