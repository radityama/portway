package observability

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Start binds only loopback. The returned stop closes sockets and joins serving.
// A disabled port allocates neither a listener nor a goroutine.
func Start(port int, write func(io.Writer)) (func(), error) {
	if port == 0 {
		return func() {}, nil
	}
	if port < 1 || port > 65535 || write == nil {
		return nil, errors.New("invalid metrics port")
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, errors.New("metrics listener unavailable")
	}
	listener := &limitedListener{Listener: l, slots: make(chan struct{}, 16)}
	handler := &metricsHandler{write: write}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	var once sync.Once
	return func() {
		once.Do(func() {
			handler.mu.Lock()
			handler.closing = true
			handler.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
			_ = server.Close()
			<-done
			handler.workers.Wait()
		})
	}, nil
}

type metricsHandler struct {
	mu      sync.Mutex
	closing bool
	workers sync.WaitGroup
	write   func(io.Writer)
}

func (h *metricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		w.WriteHeader(503)
		return
	}
	h.workers.Add(1)
	h.mu.Unlock()
	defer h.workers.Done()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != "/metrics" || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	h.write(w)
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: c, parent: l}, nil
		default:
			_ = c.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	parent *limitedListener
	once   sync.Once
}

func (c *limitedConn) Close() error {
	e := c.Conn.Close()
	c.once.Do(func() { <-c.parent.slots })
	return e
}
