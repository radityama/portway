package relay

import (
	"fmt"
	"io"
)

func (s *Server) WritePrometheus(w io.Writer) {
	s.Metrics.WritePrometheus(w)
	r := s.Snapshot()
	s.mu.RLock()
	listeners := make([]*limitedListener, 0, len(s.httpServers))
	for _, l := range s.httpServers {
		listeners = append(listeners, l)
	}
	s.mu.RUnlock()
	publicActive := 0
	for _, l := range listeners {
		l.mu.Lock()
		publicActive += len(l.active)
		l.mu.Unlock()
	}
	draining := 0
	if r.Status == "DRAINING" {
		draining = 1
	}
	for _, v := range []struct {
		name  string
		value int
	}{
		{"public_connections_active", publicActive}, {"draining", draining}, {"tunnels_active", r.ActiveTunnels}, {"tunnels_retained", r.RetainedTunnels},
		{"connections_limit", r.MaxConnections}, {"tunnels_limit", r.MaxTunnels},
		{"streams_per_tunnel_limit", r.MaxStreams}, {"public_connections_limit", s.MaxPublicConnections},
	} {
		fmt.Fprintf(w, "# TYPE portway_relay_%s gauge\nportway_relay_%s %d\n", v.name, v.name, v.value)
	}
}
