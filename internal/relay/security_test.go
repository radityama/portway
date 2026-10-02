package relay_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
)

func publicTLS(t *testing.T, f *httpFixture) net.Conn {
	t.Helper()
	u, err := url.Parse(f.url)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", f.publicAddress, &tls.Config{
		RootCAs: f.tlsConfig.RootCAs, ServerName: u.Hostname(), MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestSecurityRawHTTPSRejectsUnsafeRequestsBeforeUpstream(t *testing.T) {
	var requests atomic.Int32
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(204)
	}, nil)
	u, err := url.Parse(f.url)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name, target, host, headers string
		status                      int
	}{
		{"duplicate-host", "/", u.Host, "Host: other.portway.localhost\r\n", 400},
		{"host-userinfo", "/", "user@" + u.Host, "", 400},
		{"host-path", "/", u.Host + "/private", "", 400},
		{"host-trailing-dot", "/", u.Hostname() + ".", "", 400},
		{"host-wrong-port", "/", u.Hostname() + ":0", "", 400},
		{"host-leading-zero-port", "/", u.Hostname() + ":0" + u.Port(), "", 400},
		{"host-sni-mismatch", "/", "other.portway.localhost:" + u.Port(), "", 421},
		{"absolute-target", "http://127.0.0.1/private", u.Host, "", 400},
		{"authority-target", "127.0.0.1:22", u.Host, "", 400},
		{"conflicting-lengths", "/", u.Host, "Content-Length: 0\r\nContent-Length: 1\r\n", 400},
		{"repeated-transfer-encoding", "/", u.Host, "Transfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n", 501},
		{"unsupported-transfer-encoding", "/", u.Host, "Transfer-Encoding: gzip\r\n", 501},
		{"advertised-trailer", "/", u.Host, "Transfer-Encoding: chunked\r\nTrailer: Authorization\r\n", 400},
		{"oversized-body", "/", u.Host, fmt.Sprintf("Content-Length: %d\r\nExpect: 100-continue\r\n", protocol.MaxRequestBodySize+1), 413},
		{"oversized-hop-header", "/", u.Host, "Connection: X-Padding\r\nX-Padding: " + strings.Repeat("x", 40000) + "\r\n", 431},
		{"too-many-headers", "/", u.Host, strings.Repeat("X-Entry: x\r\n", 129), 431},
	} {
		t.Run(entry.name, func(t *testing.T) {
			before := requests.Load()
			conn := publicTLS(t, f)
			wire := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\n%sConnection: close\r\n\r\n", entry.target, entry.host, entry.headers)
			if _, err := io.WriteString(conn, wire); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
			if err != nil {
				t.Fatal(err)
			}
			responseBody(t, response)
			if response.StatusCode != entry.status {
				t.Fatalf("status=%d, want %d", response.StatusCode, entry.status)
			}
			conn.Close()
			if requests.Load() != before {
				t.Fatal("rejected request reached the local service")
			}
			response, err = f.client.Get(f.url + "/healthy")
			if err != nil {
				t.Fatal(err)
			}
			responseBody(t, response)
			if response.StatusCode != 204 || requests.Load() != before+1 {
				t.Fatal("hostile request damaged the admitted tunnel")
			}
		})
	}
}

func TestSecurityAmbiguousFramingIsCanonicalizedWithoutSmuggling(t *testing.T) {
	type received struct {
		path, body, remote string
		lengths            []string
	}
	seen := make(chan received, 4)
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body rejected", 400)
			return
		}
		seen <- received{r.URL.Path, string(body), r.RemoteAddr, r.Header.Values("Content-Length")}
		w.Header().Set("Content-Length", "2")
		fmt.Fprint(w, "ok")
	}, nil)
	u, _ := url.Parse(f.url)
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint("chunked=", chunked), func(t *testing.T) {
			conn := publicTLS(t, f)
			payload := "hello"
			headers := "Content-Length: 5\r\nContent-Length: 5\r\n"
			body := payload
			if chunked {
				// Go accepts CL+TE, selects chunked and removes the supplied CL.
				// The proxy must forward the parsed body with fresh framing. An
				// HTTP-looking body must never become another upstream request.
				payload = "GET /smuggled HTTP/1.1\r\nHost: victim.example\r\n\r\n"
				headers = "Content-Length: 5\r\nTransfer-Encoding: chunked\r\n"
				body = fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(payload), payload)
			}
			wire := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\n%s\r\n%sGET /after HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", u.Host, headers, body, u.Host)
			if _, err := io.WriteString(conn, wire); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			for _, method := range []string{"POST", "GET"} {
				response, err := http.ReadResponse(reader, &http.Request{Method: method})
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != 200 || responseBody(t, response) != "ok" {
					t.Fatal("pipelined response framing changed")
				}
			}
			first, second := <-seen, <-seen
			if first.path != "/upload" || first.body != payload || second.path != "/after" || second.body != "" || first.remote == second.remote {
				t.Fatal("request boundary or one-request-per-upstream-socket isolation failed")
			}
			if len(first.lengths) > 1 || chunked && len(first.lengths) != 0 {
				t.Fatal("client framing headers crossed the tunnel boundary")
			}
			select {
			case <-seen:
				t.Fatal("body contents were executed as an upstream request")
			default:
			}
		})
	}
}

func TestSecuritySlowTLSAndHeadersReleasePublicAdmission(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }, func(s *relay.Server) {
		s.MaxPublicConnections = 2
	})
	headerPeer := publicTLS(t, f)
	if err := headerPeer.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(headerPeer, "GET / HTTP/1.1\r\nHost: "); err != nil {
		t.Fatal(err)
	}
	tlsPeer, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tlsPeer.Close()
	if err := tlsPeer.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	publicConnections := func(n int) bool {
		var text bytes.Buffer
		f.server.WritePrometheus(&text)
		return strings.Contains(text.String(), fmt.Sprintf("portway_relay_public_connections_active %d\n", n))
	}
	eventually(t, func() bool { return publicConnections(2) })
	excess, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer excess.Close()
	_ = excess.SetReadDeadline(time.Now().Add(time.Second))
	expectClosed(t, excess)
	// Both peers are admitted but provide no further bytes. The public header
	// deadline also bounds a TLS handshake before net/http starts a handler.
	expectClosed(t, headerPeer)
	expectClosed(t, tlsPeer)
	eventually(t, func() bool { return publicConnections(0) })
	response, err := f.client.Get(f.url + "/recovered")
	if err != nil {
		t.Fatal(err)
	}
	responseBody(t, response)
	if response.StatusCode != 204 {
		t.Fatal("slow peers retained public admission")
	}
}

func TestSecurityHostileUpstreamHeadersDoNotPoisonTunnel(t *testing.T) {
	cases := map[string]string{
		"conflicting-lengths":        "HTTP/1.1 200 OK\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\nx",
		"repeated-transfer-encoding": "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		"oversized-headers":          "HTTP/1.1 200 OK\r\nX-Padding: " + strings.Repeat("x", 40000) + "\r\n\r\n",
		"trailers":                   "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Late\r\n\r\n",
		"unsolicited-upgrade":        "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
		"interim-exhaustion":         strings.Repeat("HTTP/1.1 100 Continue\r\n\r\n", 9) + "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n",
	}
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if wire, hostile := cases[strings.TrimPrefix(r.URL.Path, "/")]; hostile {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(conn, wire)
			return
		}
		w.WriteHeader(204)
	}, nil)
	for name := range cases {
		t.Run(name, func(t *testing.T) {
			response, err := f.client.Get(f.url + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			responseBody(t, response)
			if response.StatusCode != 502 {
				t.Fatalf("hostile response accepted: %d", response.StatusCode)
			}
			response, err = f.client.Get(f.url + "/healthy")
			if err != nil {
				t.Fatal(err)
			}
			responseBody(t, response)
			if response.StatusCode != 204 {
				t.Fatal("hostile local service response damaged the tunnel")
			}
		})
	}
}
