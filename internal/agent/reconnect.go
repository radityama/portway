package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"time"

	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
)

const BackoffResetAfter = 60 * time.Second

// Backoff is owned by one reconnect loop. Successful short-lived connections
// do not reset it, preventing a flapping relay from causing a retry storm.
type Backoff struct{ attempt uint32 }

func (b *Backoff) Next(connectedFor time.Duration) (uint32, time.Duration) {
	return b.next(connectedFor, rand.Float64())
}
func (b *Backoff) next(connectedFor time.Duration, jitter float64) (uint32, time.Duration) {
	if connectedFor >= BackoffResetAfter {
		b.attempt = 0
	}
	if b.attempt < ^uint32(0) {
		b.attempt++
	}
	base := 30 * time.Second
	if b.attempt <= 5 {
		base = time.Second << (b.attempt - 1)
	}
	return b.attempt, base/2 + time.Duration(float64(base/2)*jitter)
}

func WaitReconnect(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Retryable fails closed on unknown errors. Certificate/auth/protocol/state
// failures require operator action and must never become transport retries.
func Retryable(err error) bool {
	var registration *RegistrationError
	if errors.Is(err, mux.ErrPeerShutdown) || errors.As(err, &registration) && registration.Code == protocol.RegisterDraining {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var certificate *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var unknownCA x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &certificate) || errors.As(err, &record) || errors.As(err, &unknownCA) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate) {
		return false
	}
	if errors.Is(err, mux.ErrHeartbeatTimeout) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var network net.Error
	return errors.As(err, &network)
}

func DisconnectReason(err error) string {
	var registration *RegistrationError
	if errors.Is(err, mux.ErrPeerShutdown) || errors.As(err, &registration) && registration.Code == protocol.RegisterDraining {
		return "relay_draining"
	}
	if errors.Is(err, mux.ErrHeartbeatTimeout) {
		return "heartbeat_timeout"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "transport_timeout"
	}
	return "transport_closed"
}
