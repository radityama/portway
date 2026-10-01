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
	if config == nil || len(config.Certificates) == 0 || config.MinVersion < tls.VersionTLS13 || s.PublicPort < 1 || s.PublicPort > 65535 || s.MaxPublicConnections < 1 || s.MaxPublicConnections > 10000 || s.StreamTimeout <= 0 {
		return ErrConfig
	}
	handler := &ingress{server: s}
	tlsConfig := config.Clone()
	tlsConfig.NextProtos = []string{"http/1.1"}
	httpServer := &http.Server{Handler: handler, TLSConfig: tlsConfig, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: s.StreamTimeout, WriteTimeout: s.StreamTimeout + s.WriteTimeout, IdleTimeout: 120 * time.Second, MaxHeaderBytes: protocol.MaxHTTPHeaderSize, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	limited := &limitedListener{Listener: listener, max: s.MaxPublicConnections, active: make(map[*limitedConn]struct{})}
	closeServer := func() { handler.mu.Lock(); handler.closing = true; handler.mu.Unlock(); _ = httpServer.Close() }
	stop := context.AfterFunc(ctx, closeServer)
	err := httpServer.ServeTLS(limited, "", "")
	stop()
	closeServer()
	handler.handlers.Wait()
	if ctx.Err() != nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("public HTTPS listener failed")
}

type limitedListener struct {
	net.Listener
	mu     sync.Mutex
	max    int
	active map[*limitedConn]struct{}
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
func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.parent.mu.Lock(); delete(c.parent.active, c); c.parent.mu.Unlock() })
	return err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		httpFailure(w, "HTTP upgrades unavailable", 501)
		return
	}
	if len(r.Trailer) > 0 {
		httpFailure(w, "HTTP trailers unavailable", 400)
		return
	}
	if r.ContentLength > protocol.MaxRequestBodySize {
		httpFailure(w, "request body exceeds limit", 413)
		return
	}
	s.mu.RLock()
	id, known := s.hostnames[host]
	owner := s.sessions[id]
	var session *mux.Conn
	if owner != nil && owner.conn != nil && owner.ctx.Err() == nil && time.Now().Before(owner.ExpiresAt) {
		session = owner.streams
	}
	s.mu.RUnlock()
	if !known {
		httpFailure(w, "tunnel not found", 404)
		return
	}
	if session == nil {
		httpFailure(w, "tunnel unavailable", 503)
		return
	}
	header := r.Header.Clone()
	httpwire.StripHopHeaders(header)
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
	open := protocol.OpenStream{Method: r.Method, Target: r.URL.RequestURI(), Host: host, Headers: pairs, ContentLength: r.ContentLength}
	if err := open.Validate(); err != nil {
		httpFailure(w, "invalid or oversized request headers", 431)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.StreamTimeout)
	defer cancel()
	stream, err := session.Open(ctx, open)
	if err != nil {
		httpFailure(w, "tunnel stream unavailable", streamStatus(ctx, err))
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.EnableFullDuplex()
	uploaded := make(chan struct{})
	uploadResult := make(chan struct{})
	stopUpload := make(chan struct{})
	successfulResponse := false // Published to the upload worker by stopUpload.
	var responseBody io.Closer
	var uploadErr error
	go func() {
		defer close(uploaded)
		limitedBody := http.MaxBytesReader(nil, r.Body, protocol.MaxRequestBodySize)
		_, uploadErr = io.Copy(stream, limitedBody)
		if uploadErr == nil {
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
		_ = controller.SetReadDeadline(deadline)
		close(stopUpload)
		stream.Close()
		if responseBody != nil {
			responseBody.Close()
		}
		<-uploaded
		r.Body.Close()
		_ = controller.SetReadDeadline(time.Time{})
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
		httpFailure(w, "upstream response unavailable", status)
		return
	}
	responseBody = response.Body
	if response.ContentLength > protocol.MaxResponseBodySize {
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
	w.WriteHeader(response.StatusCode)
	body := &httpwire.LimitedBody{Reader: response.Body, Remaining: protocol.MaxResponseBodySize}
	buffer := make([]byte, protocol.MaxDataSize)
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
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
	if controller.Flush() == nil {
		successfulResponse = true
	}
}
func streamStatus(ctx context.Context, err error) int {
	if errors.Is(err, protocol.ErrPayloadTooLarge) {
		return 431
	}
	if errors.Is(err, mux.ErrLimit) {
		return 503
	}
	var remote *mux.RemoteError
	if errors.As(err, &remote) {
		if remote.Code == protocol.StreamLimit {
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
