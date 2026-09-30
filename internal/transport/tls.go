package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

const ALPN = "portway/1"

var ErrTLSConfig = errors.New("invalid verified TLS configuration")

type TLSDialer struct {
	Config  *tls.Config
	Timeout time.Duration
}

func ClientConfig(caFile, serverName string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName, NextProtos: []string{ALPN}}
	if caFile != "" {
		file, err := os.Open(caFile)
		if err != nil {
			return nil, ErrTLSConfig
		}
		defer file.Close()
		pem, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		if err != nil || len(pem) > 1024*1024 {
			return nil, ErrTLSConfig
		}
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(pem) {
			return nil, ErrTLSConfig
		}
	}
	return config, nil
}

func ServerConfig(certFile, keyFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, ErrTLSConfig
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate},
		NextProtos: []string{ALPN}, SessionTicketsDisabled: true,
	}, nil
}

func (d *TLSDialer) Dial(ctx context.Context, address string) (net.Conn, error) {
	if d.Config == nil || d.Config.InsecureSkipVerify || d.Config.MinVersion < tls.VersionTLS13 || (d.Config.MaxVersion != 0 && d.Config.MaxVersion < tls.VersionTLS13) || d.Timeout <= 0 {
		return nil, ErrTLSConfig
	}
	config := d.Config.Clone()
	config.NextProtos = []string{ALPN}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: d.Timeout}, Config: config}
	connectCtx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	conn, err := dialer.DialContext(connectCtx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if conn.(*tls.Conn).ConnectionState().NegotiatedProtocol != ALPN {
		Close(conn)
		return nil, ErrTLSConfig
	}
	return conn, nil
}

func Verified(conn net.Conn) bool {
	secure, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	if !ok {
		return false
	}
	state := secure.ConnectionState()
	return state.HandshakeComplete && state.Version >= tls.VersionTLS13 && state.NegotiatedProtocol == ALPN && len(state.VerifiedChains) > 0
}

// Close interrupts I/O without waiting for a TLS close alert write.
func Close(conn net.Conn) {
	if secure, ok := conn.(*tls.Conn); ok {
		_ = secure.NetConn().Close()
	} else {
		_ = conn.Close()
	}
}
