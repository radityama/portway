package protocol

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func pipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	for _, conn := range []net.Conn{a, b} {
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func awaitIO(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("codec I/O did not terminate")
		return nil
	}
}

func TestNegotiationOverPipe(t *testing.T) {
	agent, relay := pipePair(t)
	offer := helloOffer(CapabilityHeartbeat, CapabilityMultiplexing)
	offer.RequiredCapabilities = []Capability{CapabilityMultiplexing}
	local := helloOffer(CapabilityFlowControl, CapabilityMultiplexing)
	local.MaxPayloadSize = 1024
	done := make(chan error, 1)
	go func() {
		frame, err := Decode(relay)
		if err == nil {
			var remote Hello
			remote, err = DecodeHello(frame)
			if err == nil {
				var ack HelloAck
				ack, err = Negotiate(remote, local)
				if err == nil {
					frame, err = EncodeHelloAck(ack)
					if err == nil {
						err = frame.Encode(relay)
					}
				}
			}
		}
		done <- err
	}()
	frame, err := EncodeHello(offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Encode(agent); err != nil {
		t.Fatal(err)
	}
	frame, err = Decode(agent)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := DecodeHelloAck(frame)
	if err != nil || ack.ValidateFor(offer) != nil || ack.MaxPayloadSize != 1024 {
		t.Fatalf("wire negotiation failed: %v", err)
	}
	if err := awaitIO(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedIOHonorsCallerDeadlines(t *testing.T) {
	for _, phase := range []string{"header read", "payload read", "write"} {
		t.Run(phase, func(t *testing.T) {
			conn, peer := pipePair(t)
			var err error
			if phase == "payload read" {
				done := make(chan error, 1)
				go func() {
					_, err := Decode(conn)
					done <- err
				}()
				// Start the deadline after the header has actually been transferred,
				// so scheduler load cannot turn this into a header-timeout test.
				if _, err := peer.Write(validDataHeader(1)); err != nil {
					t.Fatal(err)
				}
				if err := conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				err = awaitIO(t, done)
			} else {
				if err := conn.SetDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				if phase == "write" {
					err = (Frame{Type: TypePing}).Encode(conn)
				} else {
					_, err = Decode(conn)
				}
			}
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("expected caller deadline error: %v", err)
			}
		})
	}
}

func TestCallerCancellationClosesBlockedIO(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			conn, _ := pipePair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "write" {
					err = (Frame{Type: TypePing}).Encode(conn)
				} else {
					_, err = Decode(conn)
				}
				done <- err
			}()
			cancel()
			if err := awaitIO(t, done); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("cancellation did not unblock codec: %v", err)
			}
		})
	}
}

func TestPeerDisappearsMidPayload(t *testing.T) {
	conn, peer := pipePair(t)
	done := make(chan error, 1)
	go func() {
		wire := append(validDataHeader(3), 1, 2)
		_, err := peer.Write(wire)
		_ = peer.Close()
		done <- err
	}()
	if _, err := Decode(conn); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("peer closure did not report truncated payload: %v", err)
	}
	if err := awaitIO(t, done); err != nil {
		t.Fatal(err)
	}
}
