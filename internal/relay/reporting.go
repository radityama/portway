package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/mux"
	"log/slog"
	"time"
)

// Snapshot reads local counters only; it creates no network dependency on ingress.
func (s *Server) Snapshot() control.RelayReport {
	report := control.RelayReport{Capacity: control.Capacity{MaxConnections: s.MaxConnections, MaxTunnels: s.MaxTunnels, MaxStreams: int(s.MaxStreams)}, Status: "HEALTHY"}
	s.mu.RLock()
	report.ActiveConnections = len(s.active)
	report.RetainedTunnels = len(s.sessions)
	if s.draining {
		report.Status = "DRAINING"
	}
	connections := make([]*mux.Conn, 0, len(s.active))
	for _, owner := range s.sessions {
		if owner.conn != nil && owner.ctx.Err() == nil && time.Now().Before(owner.ExpiresAt) {
			report.ActiveTunnels++
			if owner.connection != nil {
				connections = append(connections, owner.connection)
			}
		}
	}
	s.mu.RUnlock()
	for _, conn := range connections {
		report.ActiveStreams += conn.ActiveStreams()
	}
	return report
}

type Reporter struct {
	Client             *control.Client
	RelayID, TokenFile string
	Interval           time.Duration
	Snapshot           func() control.RelayReport
	OnDrain            func()
	Logger             *slog.Logger
	lastAck            *control.ReportAcknowledgement
}

// Run owns exactly one synchronous request/timer at a time. Transient failures
// keep admitted leases local; stale/auth-invalid reporters drain rather than fight
// a successor or retry a revoked operator key indefinitely.
func (r *Reporter) Run(ctx context.Context) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		r.OnDrain()
		return
	}
	instance := hex.EncodeToString(random[:])
	var prior *control.ReportAcknowledgement
	for {
		if ctx.Err() != nil {
			return
		}
		bearer, err := auth.ReadTokenFile(r.TokenFile)
		var ack control.ReportAcknowledgement
		if err == nil {
			report := r.Snapshot()
			report.InstanceID = instance
			ack, err = r.Client.ReportRelay(ctx, r.RelayID, bearer, report, prior)
		}
		bearer = ""
		// A canceled request may have committed. Preserve that sequence gap before
		// joining Run, so the final drain snapshot cannot reuse its sequence.
		if err != nil && prior != nil {
			prior.Sequence++
		} else if err == nil {
			r.lastAck = &ack
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			var failure *control.Error
			if errors.As(err, &failure) && failure.Code == "PRESENCE_EXPIRED" {
				prior = nil
			} else if !control.Retryable(err) {
				r.Logger.Warn("relay_reporting_rejected")
				r.OnDrain()
				return
			}
			r.Logger.Warn("relay_report_unavailable")
		} else {
			first := prior == nil
			prior = &ack
			if first {
				r.Logger.Info("relay_presence_registered", "relay_id", r.RelayID)
			}
			if ack.DrainRequested {
				r.Logger.Info("relay_drain_requested", "relay_id", r.RelayID)
				r.OnDrain()
				return
			}
		}
		timer := time.NewTimer(r.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Drain is called only after Run has joined, so the last ack has one owner.
func (r *Reporter) Drain(ctx context.Context) error {
	if r.lastAck == nil {
		return nil
	}
	bearer, err := auth.ReadTokenFile(r.TokenFile)
	if err != nil {
		return auth.ErrConfig
	}
	report := r.Snapshot()
	report.InstanceID = r.lastAck.InstanceID
	report.Status = "DRAINING"
	_, err = r.Client.ReportRelay(ctx, r.RelayID, bearer, report, r.lastAck)
	return err
}
