package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

func TestBackoffJitterCapAndHealthyReset(t *testing.T) {
	for _, jitter := range []float64{0, 0.25, 0.999999} {
		var backoff Backoff
		for i := 1; i <= 100; i++ {
			attempt, delay := backoff.next(time.Second, jitter)
			base := 30 * time.Second
			if i <= 5 {
				base = time.Second << (i - 1)
			}
			if attempt != uint32(i) || delay < base/2 || delay > base {
				t.Fatal("backoff lost its attempt, jitter or cap")
			}
		}
		attempt, delay := backoff.next(BackoffResetAfter, jitter)
		if attempt != 1 || delay < 500*time.Millisecond || delay > time.Second {
			t.Fatal("healthy session did not reset backoff")
		}
	}
	backoff := Backoff{attempt: ^uint32(0)}
	if attempt, delay := backoff.next(0, 0.5); attempt != ^uint32(0) || delay != 22500*time.Millisecond {
		t.Fatal("backoff counter overflowed")
	}
}

func TestReconnectWaitCancelsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- WaitReconnect(ctx, 30*time.Second) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("backoff ignored cancellation")
	}
}

func TestRetryClassificationFailsClosed(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, mux.ErrHeartbeatTimeout, mux.ErrPeerShutdown, &RegistrationError{Code: protocol.RegisterDraining}, context.DeadlineExceeded, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}} {
		if !Retryable(err) {
			t.Fatalf("transient error not retryable: %T", err)
		}
	}
	for _, err := range []error{ErrConfig, ErrSessionInUse, transport.ErrTLSConfig, protocol.ErrInvalidHandshake, protocol.ErrInvalidHeartbeat, mux.ErrProtocol, context.Canceled, errors.New("unknown"), &AuthenticationError{Code: protocol.AuthInvalid}, &RegistrationError{Code: protocol.RegisterStale}, &net.OpError{Op: "dial", Net: "tcp", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}, x509.HostnameError{}, &net.OpError{Op: "dial", Net: "tcp", Err: tls.RecordHeaderError{}}} {
		if Retryable(err) {
			t.Fatalf("terminal error retried: %T", err)
		}
	}
}

func TestDrainDisconnectReason(t *testing.T) {
	for _, err := range []error{mux.ErrPeerShutdown, &RegistrationError{Code: protocol.RegisterDraining}} {
		if DisconnectReason(err) != "relay_draining" {
			t.Fatal("drain reason lost")
		}
	}
	if Retryable(protocol.ErrInvalidGoAway) {
		t.Fatal("malformed GOAWAY retried")
	}
}
