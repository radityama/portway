package transport

import (
	"context"
	"net"
)

// Transport owns the connection used by the tunnel protocol.
// TCP/TLS is the MVP implementation; QUIC can implement the same contract later.
type Transport interface {
	Dial(ctx context.Context, address string) (net.Conn, error)
}
