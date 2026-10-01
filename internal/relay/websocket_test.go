package relay_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/radityama/portway/internal/httpwire"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
)

const wsKey = "dGhlIHNhbXBsZSBub25jZQ=="

// Test endpoints own RFC6455 frames. Production transports them without
// assembling complete messages, including payloads larger than credit windows.
func wsFrame(op byte, payload []byte, masked bool) []byte {
	frame := []byte{op}
	maskFlag := byte(0)
	if masked {
		maskFlag = 128
	}
	switch {
	case len(payload) < 126:
		frame = append(frame, maskFlag|byte(len(payload)))
	case len(payload) <= 65535:
		frame = append(frame, maskFlag|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		frame = append(frame, maskFlag|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
	}
	mask := [4]byte{1, 2, 3, 4}
	if masked {
		frame = append(frame, mask[:]...)
	}
	for i, value := range payload {
		if masked {
			value ^= mask[i%4]
		}
		frame = append(frame, value)
	}
	return frame
}

func wsRead(reader io.Reader, wantMasked bool) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(reader, h[:]); err != nil {
		return 0, nil, err
	}
	if (h[1]&128 != 0) != wantMasked {
		return 0, nil, errors.New("wrong masking")
	}
	size := uint64(h[1] & 127)
	switch size {
	case 126:
		var p [2]byte
		if _, err := io.ReadFull(reader, p[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(p[:]))
	case 127:
		var p [8]byte
		if _, err := io.ReadFull(reader, p[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(p[:])
	}
	if size > 2*1024*1024 {
		return 0, nil, errors.New("test frame limit")
	}
	var mask [4]byte
	if wantMasked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, int(size))
	_, err := io.ReadFull(reader, payload)
	if wantMasked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return h[0], payload, err
}

func wsEcho(first []byte, closed chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpwire.WebSocketRequest(r) {
			http.Error(w, "expected upgrade", 400)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if closed != nil {
			defer close(closed)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		var response bytes.Buffer
		headers := make(http.Header)
		headers.Set("Sec-WebSocket-Accept", httpwire.WebSocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		if protocol.HeaderHasToken(r.Header, "Sec-WebSocket-Protocol", "chat") {
			headers.Set("Sec-WebSocket-Protocol", "chat")
		}
		_ = httpwire.WriteWebSocketResponse(&response, headers)
		response.Write(first)
		if _, err := conn.Write(response.Bytes()); err != nil {
			return
		}
		for {
			op, payload, err := wsRead(rw.Reader, true)
			if err != nil {
				return
			}
			if op&15 == 9 {
				op = 0x8a
			}
			if _, err := conn.Write(wsFrame(op, payload, false)); err != nil {
				return
			}
			if op&15 == 8 {
				return
			}
		}
	}
}

func wsDial(t *testing.T, f *httpFixture, initial []byte) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn := tls.Client(raw, &tls.Config{RootCAs: f.tlsConfig.RootCAs, ServerName: f.sessionHostname(), MinVersion: tls.VersionTLS13})
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("GET /ws?q=1 HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive, Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: chat, other\r\nSec-WebSocket-Extensions: permessage-deflate\r\nOrigin: https://application.example\r\nCookie: app=value\r\n\r\n", f.sessionHostname(), wsKey)
	if _, err := conn.Write(append([]byte(request), initial...)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	h := make(http.Header)
	h.Set("Sec-WebSocket-Key", wsKey)
	h.Set("Sec-WebSocket-Protocol", "chat, other")
	if !httpwire.ValidWebSocketResponse(response, h) {
		t.Fatalf("invalid public upgrade: %d", response.StatusCode)
	}
	if response.Header.Get("Sec-WebSocket-Protocol") != "chat" {
		t.Fatal("selected subprotocol lost")
	}
	return conn, reader
}

func expectWS(t *testing.T, reader io.Reader, op byte, payload []byte) {
	t.Helper()
	gotOp, got, err := wsRead(reader, false)
	if err != nil || gotOp != op || !bytes.Equal(got, payload) {
		t.Fatalf("frame changed: op=%x length=%d err=%v", gotOp, len(got), err)
	}
}

func TestWebSocketEchoBufferedFramesBinaryTextFragmentationAndControl(t *testing.T) {
	metadata := make(chan bool, 1)
	echo := wsEcho(wsFrame(0x81, []byte("welcome"), false), nil)
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		metadata <- r.URL.RequestURI() == "/ws?q=1" && r.Header.Get("Origin") == "https://application.example" && r.Header.Get("Cookie") == "app=value" && r.Header.Get("Sec-WebSocket-Extensions") == ""
		echo(w, r)
	}, nil)
	conn, reader := wsDial(t, f, wsFrame(0x81, []byte("pipelined"), true))
	if !<-metadata {
		t.Fatal("upgrade metadata changed")
	}
	expectWS(t, reader, 0x81, []byte("welcome"))
	expectWS(t, reader, 0x81, []byte("pipelined"))
	for _, frame := range []struct {
		op, reply byte
		payload   []byte
	}{
		{0x81, 0x81, []byte("hello")},
		{0x82, 0x82, bytes.Repeat([]byte{0, 128, 255, 7}, 100000)},
		{0x01, 0x01, []byte("fragment")},
		{0x80, 0x80, []byte("end")},
		{0x89, 0x8a, []byte("ping")},
		{0x88, 0x88, []byte{3, 232}},
	} {
		written := make(chan error, 1)
		go func() { _, err := conn.Write(wsFrame(frame.op, frame.payload, true)); written <- err }()
		expectWS(t, reader, frame.reply, frame.payload)
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	}
}

func TestWebSocketInvalidRequestsAndUpstreamHandshakes(t *testing.T) {
	f := setupHTTP(t, wsEcho(nil, nil), nil)
	for name, mutate := range map[string]func(*http.Request){
		"key":                  func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "invalid") },
		"duplicate-key":        func(r *http.Request) { r.Header.Add("Sec-WebSocket-Key", wsKey) },
		"version":              func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "12") },
		"connection-substring": func(r *http.Request) { r.Header.Set("Connection", "upgrades") },
		"method":               func(r *http.Request) { r.Method = "POST" },
		"protocol":             func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", "chat, chat") },
		"nominated-key":        func(r *http.Request) { r.Header.Set("Connection", "Upgrade, Sec-WebSocket-Key") },
		"body":                 func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", f.url, nil)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Key", wsKey)
			r.Header.Set("Sec-WebSocket-Version", "13")
			mutate(r)
			response, err := f.client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Fatalf("invalid request status=%d", response.StatusCode)
			}
		})
	}
	for name, extra := range map[string]string{
		"accept":    "Sec-WebSocket-Accept: invalid\r\n",
		"protocol":  "Sec-WebSocket-Accept: " + httpwire.WebSocketAccept(wsKey) + "\r\nSec-WebSocket-Protocol: unoffered\r\n",
		"extension": "Sec-WebSocket-Accept: " + httpwire.WebSocketAccept(wsKey) + "\r\nSec-WebSocket-Extensions: permessage-deflate\r\n",
		"body":      "Sec-WebSocket-Accept: " + httpwire.WebSocketAccept(wsKey) + "\r\nContent-Length: 1\r\n",
	} {
		t.Run("upstream-"+name, func(t *testing.T) {
			f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n%s\r\n", extra)
			}, nil)
			r, _ := http.NewRequest("GET", f.url, nil)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Key", wsKey)
			r.Header.Set("Sec-WebSocket-Version", "13")
			response, err := f.client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 502 {
				t.Fatalf("invalid upstream status=%d", response.StatusCode)
			}
		})
	}
}

func TestWebSocketRejectionRemainsHTTP(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "application denied", 403) }, nil)
	r, _ := http.NewRequest("GET", f.url, nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Key", wsKey)
	r.Header.Set("Sec-WebSocket-Version", "13")
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 403 || responseBody(t, response) != "application denied\n" {
		t.Fatal("upgrade rejection changed")
	}
}

func TestWebSocketIdleTimeoutDisconnectAndSupersession(t *testing.T) {
	for _, mode := range []string{"idle", "disconnect", "supersession"} {
		t.Run(mode, func(t *testing.T) {
			closed := make(chan struct{})
			f := setupHTTP(t, wsEcho(nil, closed), func(s *relay.Server) { s.StreamTimeout = 150 * time.Millisecond })
			conn, reader := wsDial(t, f, nil)
			switch mode {
			case "disconnect":
				conn.Close()
			case "supersession":
				next, _ := registered(t, f.fixture, 2)
				defer next.Close()
			}
			if _, _, err := wsRead(reader, false); err == nil {
				t.Fatal("closed upgrade remained readable")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("local upgrade leaked")
			}
			if mode != "supersession" {
				eventually(t, func() bool {
					response, err := f.client.Get(f.url)
					if err != nil {
						return false
					}
					response.Body.Close()
					return response.StatusCode == 400
				})
			}
		})
	}
}

func TestWebSocketShutdownWaitsForCloseAndForcesStalledPeer(t *testing.T) {
	for _, graceful := range []bool{true, false} {
		t.Run(fmt.Sprint(graceful), func(t *testing.T) {
			closed := make(chan struct{})
			f := setupHTTP(t, wsEcho(nil, closed), func(s *relay.Server) { s.ShutdownTimeout = 200 * time.Millisecond })
			conn, reader := wsDial(t, f, nil)
			done := make(chan error, 1)
			go func() { done <- f.server.Shutdown(context.Background()) }()
			eventually(t, f.server.Draining)
			if graceful {
				_, _ = conn.Write(wsFrame(0x88, []byte{3, 232}, true))
				expectWS(t, reader, 0x88, []byte{3, 232})
				// Send TLS EOF after the WebSocket close exchange.
				conn.Close()
			}
			err := await(t, done)
			if graceful && err != nil || !graceful && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("drain=%v", err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("shutdown retained local upgrade")
			}
			if f.server.ActiveConnections() != 0 {
				t.Fatal("shutdown retained tunnel workers")
			}
		})
	}
}

func TestWebSocketCapacityReleasesOnDisconnect(t *testing.T) {
	closed := make(chan struct{})
	echo := wsEcho(nil, closed)
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		echo(w, r)
	}, func(s *relay.Server) { s.MaxStreams = 1 })
	conn, _ := wsDial(t, f, nil)
	response, err := f.client.Get(f.url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("upgrade bypassed stream capacity")
	}
	conn.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("disconnect retained local socket")
	}
	eventually(t, func() bool {
		response, err := f.client.Get(f.url)
		if err != nil {
			return false
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		return err == nil && response.StatusCode == 200 && string(body) == "ok"
	})
}

func TestWebSocketSlowReaderDoesNotBlockHTTPAndForcedShutdownClosesHijack(t *testing.T) {
	closed := make(chan struct{})
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			_, _ = io.WriteString(w, "fast")
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(closed)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		header := make(http.Header)
		header.Set("Sec-WebSocket-Accept", httpwire.WebSocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		header.Set("Sec-WebSocket-Protocol", "chat")
		if httpwire.WriteWebSocketResponse(conn, header) != nil {
			return
		}
		// 32 MiB exceeds public TCP, stream and connection buffering.
		prefix := binary.BigEndian.AppendUint64([]byte{0x82, 127}, 32*1024*1024)
		if _, err := conn.Write(prefix); err != nil {
			return
		}
		block := make([]byte, 32768)
		for range 1024 {
			if _, err := conn.Write(block); err != nil {
				return
			}
		}
	}, func(s *relay.Server) { s.MaxStreams = 2; s.ShutdownTimeout = 100 * time.Millisecond })
	conn, reader := wsDial(t, f, nil)
	defer conn.Close()
	if _, err := reader.ReadByte(); err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Get(f.url)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("independent HTTP blocked: %v", err)
	}
	if responseBody(t, response) != "fast" {
		t.Fatal("HTTP bytes changed")
	}
	start := time.Now()
	if err := f.server.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow reader drain=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("hijacked public write ignored shutdown deadline")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("slow local sender leaked")
	}
}

func TestWebSocketLegacyPeerReturns501AndOrdinaryHTTPStillWorks(t *testing.T) {
	f := setupHTTP(t, wsEcho(nil, nil), nil)
	conn := rawTLS(t, f.fixture)
	hello, _ := protocol.EncodeHello(protocol.Hello{Version: 1, MaxPayloadSize: protocol.MaxPayloadSize, Capabilities: []protocol.Capability{protocol.CapabilityMultiplexing, protocol.CapabilityFlowControl}})
	if err := hello.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err := protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := protocol.DecodeHelloAck(frame)
	if err != nil || len(ack.Capabilities) != 2 {
		t.Fatal("legacy negotiation changed")
	}
	auth, _ := protocol.EncodeAuth(protocol.Auth{Token: f.token})
	if err := auth.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err = protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := protocol.DecodeAuthOK(frame)
	if err != nil {
		t.Fatal(err)
	}
	register, _ := protocol.EncodeRegister(protocol.Register{TunnelID: "tnl_local_dev", Generation: 2, Protocol: "http"})
	if err := register.Encode(conn); err != nil {
		t.Fatal(err)
	}
	frame, err = protocol.Decode(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeRegisterOK(frame); err != nil {
		t.Fatal(err)
	}
	old, err := mux.New(context.Background(), conn, conn, mux.Options{MaxStreams: 2, MaxFrame: protocol.MaxPayloadSize, StreamTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ExpiresAt: identity.ExpiresAt, Accept: func(s *mux.Stream, o protocol.OpenStream) {
		if o.Upgrade != "" {
			s.Reject(protocol.StreamInvalid)
			return
		}
		if s.Accept() != nil {
			return
		}
		if _, err := io.Copy(io.Discard, s); err != nil {
			return
		}
		_, _ = io.WriteString(s, "HTTP/1.1 200 OK\r\nContent-Length: 6\r\n\r\nlegacy")
		_ = s.CloseWrite()
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- old.Run() }()
	t.Cleanup(func() { old.Close(); _ = await(t, done) })
	r, _ := http.NewRequest("GET", f.url, nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Key", wsKey)
	r.Header.Set("Sec-WebSocket-Version", "13")
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 501 {
		t.Fatal("legacy peer received unsupported metadata")
	}
	response, err = f.client.Get(f.url)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || responseBody(t, response) != "legacy" {
		t.Fatal("ordinary legacy HTTP failed")
	}
}
