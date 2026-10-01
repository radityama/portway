package relay_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
)

func TestShutdownDeadlineInterruptsStalledPublicClients(t *testing.T) {
	// A public client stops reading a large response, keeping a relay Write
	// blocked. Tunnel closure alone cannot interrupt that public socket write.
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "33554432")
		block := make([]byte, 32768)
		for range 1024 {
			if _, err := w.Write(block); err != nil {
				return
			}
		}
	}, func(s *relay.Server) { s.ShutdownTimeout = 80 * time.Millisecond })
	tcp, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if c, ok := tcp.(*net.TCPConn); ok {
		c.SetReadBuffer(1024)
	}
	conn := tls.Client(tcp, &tls.Config{RootCAs: f.tlsConfig.RootCAs, ServerName: f.sessionHostname(), MinVersion: tls.VersionTLS13})
	conn.SetDeadline(time.Now().Add(time.Second))
	if err = conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", f.sessionHostname()); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = f.server.Shutdown(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled client drain result=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("public write ignored shutdown deadline")
	}
	if f.server.ActiveConnections() != 0 {
		t.Fatal("forced shutdown retained tunnel")
	}
}

func (f *httpFixture) sessionHostname() string {
	request, _ := http.NewRequest("GET", f.url, nil)
	return request.URL.Hostname()
}

func TestShutdownLegacyDiagnosticReceivesEOF(t *testing.T) {
	f := setup(t, nil)
	conn := authenticatedRaw(t, f)
	frame, _ := protocol.EncodeRegister(protocol.Register{TunnelID: "tnl_local_dev", Generation: 1})
	if err := frame.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = protocol.DecodeRegisterOK(frame); err != nil {
		t.Fatal(err)
	}
	if err = f.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = protocol.Decode(conn); !errors.Is(err, io.EOF) {
		t.Fatalf("legacy peer received unsupported frame: %v", err)
	}
}

func TestShutdownPreservesGenerationWatermark(t *testing.T) {
	f := setup(t, func(s *relay.Server, _ []auth.Record) { s.ShutdownTimeout = time.Second })
	first, one := registered(t, f, 1)
	second, two := registered(t, f, 2)
	first.Close()
	done := make(chan error, 1)
	go func() { done <- second.Wait() }()
	eventually(t, func() bool {
		route, ok := f.server.Lookup(two.PublicHostname)
		return ok && route.Generation == 2 && f.server.ActiveConnections() == 1
	})
	if err := f.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = await(t, done)
	if _, ok := f.server.Lookup(one.PublicHostname); ok {
		t.Fatal("drained owner remained routable")
	}
	if f.server.ActiveConnections() != 0 {
		t.Fatal("replacement cleanup retained old worker")
	}
}
