package relay

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

func (s *Server) Draining() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.draining }
func (s *Server) notifyLocked()  { close(s.changed); s.changed = make(chan struct{}) }

func (s *Server) forceCloseIO() {
	s.mu.RLock()
	connections := make([]net.Conn, 0, len(s.active))
	for conn := range s.active {
		connections = append(connections, conn)
	}
	public := make(map[*http.Server]*limitedListener, len(s.httpServers))
	for server, listener := range s.httpServers {
		public[server] = listener
	}
	s.mu.RUnlock()
	for _, conn := range connections {
		conn.Close()
	}
	for server, listener := range public {
		listener.closeAll()
		_ = server.Close()
	}
}

// Shutdown permanently closes admissions and drains registered connections.
// Serve/ServeHTTPS retain their hard lifetime context; the owner cancels it
// after this method returns, then joins both serving calls and their workers.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	first := !s.draining
	s.draining = true
	s.mu.Unlock()
	if !first {
		select {
		case <-s.shutdownDone:
			s.mu.RLock()
			err := s.shutdownErr
			s.mu.RUnlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, s.ShutdownTimeout)
	defer cancel()
	err := s.shutdown(ctx)
	s.mu.Lock()
	s.shutdownErr = err
	close(s.shutdownDone)
	s.mu.Unlock()
	return err
}

func (s *Server) shutdown(ctx context.Context) error {
	s.Logger.Info("relay_shutdown_started")
	stop := context.AfterFunc(ctx, s.forceCloseIO)
	defer stop()
	s.mu.RLock()
	registered := make(map[net.Conn]*registryEntry)
	for _, owner := range s.sessions {
		if owner.conn != nil {
			registered[owner.conn] = owner
		}
	}
	unregistered := make([]net.Conn, 0, len(s.active))
	for conn := range s.active {
		if registered[conn] == nil {
			unregistered = append(unregistered, conn)
		}
	}
	s.mu.RUnlock()
	for _, conn := range unregistered {
		conn.Close()
	}
	// At most MaxConnections registered workers; no network operation holds mu.
	var workers sync.WaitGroup
	for raw, owner := range registered {
		workers.Add(1)
		go func() {
			defer workers.Done()
			// ACK may already be visible to the peer before its writer returns.
			// Never send GOAWAY concurrently with or ahead of REGISTER_OK.
			select {
			case <-owner.registrationDone:
			case <-ctx.Done():
				raw.Close()
				return
			}
			s.mu.RLock()
			conn := owner.connection
			s.mu.RUnlock()
			if conn == nil {
				raw.Close()
				return
			}
			_ = conn.Shutdown(ctx)
		}()
	}
	workers.Wait()
	for {
		s.mu.RLock()
		finished := len(s.active) == 0 && s.httpActive == 0
		changed := s.changed
		s.mu.RUnlock()
		if finished {
			// Connection cleanup can finish before the context's child timer
			// observes the same deadline. Keep forced shutdown reporting stable.
			if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return ctx.Err()
		}
		if ctx.Err() != nil {
			s.forceCloseIO()
			// Closing tunnel I/O wakes HTTP response readers. Their owned cleanup
			// interrupts upload reads and joins workers before releasing admission.
			<-changed
		} else {
			select {
			case <-changed:
			case <-ctx.Done():
			}
		}
	}
}
