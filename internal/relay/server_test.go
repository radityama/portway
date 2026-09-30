package relay

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

func TestConfiguredFrameLimitIsEnforced(t *testing.T) {
	server := NewServer(nil)
	server.MaxFrame = 3
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := peer.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeConn(ctx, conn) }()
	// The frame is valid at the protocol default, but exceeds this relay's limit.
	// The relay closes the connection after its header, before accepting the body.
	frame := protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("four")}
	if err := frame.Encode(peer); err == nil {
		t.Fatal("relay accepted an oversized payload")
	}
	select {
	case err := <-done:
		if !errors.Is(err, protocol.ErrPayloadTooLarge) {
			t.Fatalf("expected configured frame-limit rejection: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("relay did not reject oversized frame promptly")
	}
}
