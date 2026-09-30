package agent

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/radityama/portway/internal/transport"
)

type plainTransport struct {
	conn  net.Conn
	calls int
}

func (p *plainTransport) Dial(context.Context, string) (net.Conn, error) {
	p.calls++
	return p.conn, nil
}
func TestUnverifiedTransportNeverReceivesCredential(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	client := NewClient(&plainTransport{conn: conn})
	if session, err := client.Connect(context.Background(), "relay:443", "fixture_credential_only_for_tests"); session != nil || !errors.Is(err, transport.ErrTLSConfig) {
		t.Fatal("unverified transport accepted")
	}
	if n, err := peer.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal("unverified transport received bytes")
	}
}
func TestInvalidConfigurationDoesNotDial(t *testing.T) {
	for _, mutate := range []func(*Client){func(c *Client) { c.MaxFrame = 0 }, func(c *Client) { c.HandshakeTimeout = 0 }, func(c *Client) { c.WriteTimeout = 0 }, func(c *Client) { c.IdleTimeout = 0 }} {
		dialer := &plainTransport{}
		client := NewClient(dialer)
		mutate(client)
		if _, err := client.Connect(context.Background(), "relay:443", "fixture_credential_only_for_tests"); !errors.Is(err, ErrConfig) || dialer.calls != 0 {
			t.Fatal("invalid config dialed")
		}
	}
}
