package transport

import (
	"context"
	"net"
)

// Transport owns the connection used by the tunnel protocol.
// Dial must return a verified TLS 1.3 connection with the Portway ALPN in the
// MVP. QUIC will extend the verified-connection contract in a later phase.
type Transport interface {
	Dial(ctx context.Context, address string) (net.Conn, error)
}
