package httpwire

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWebSocketResponsePreservesBufferedFrames(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Sec-WebSocket-Accept", WebSocketAccept("dGhlIHNhbXBsZSBub25jZQ=="))
	var wire bytes.Buffer
	if err := WriteWebSocketResponse(&wire, headers); err != nil {
		t.Fatal(err)
	}
	wire.Write([]byte{0x82, 3, 0, 128, 255})
	response, reader, err := ReadResponseUpgrade(bufio.NewReader(&wire), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	request := make(http.Header)
	request.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if !ValidWebSocketResponse(response, request) {
		t.Fatal("valid handshake rejected")
	}
	frame, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(frame, []byte{0x82, 3, 0, 128, 255}) {
		t.Fatal("buffered binary frame lost")
	}
}

func TestUnsolicitedUpgradeAndMalformedResponseRemainRejected(t *testing.T) {
	for _, extra := range []string{
		"Transfer-Encoding: chunked\r\n", "Trailer: X-End\r\n",
		"Content-Length: 5\r\n", "Sec-WebSocket-Protocol: not-offered\r\n",
		"Sec-WebSocket-Accept: duplicate\r\n", "Sec-WebSocket-Extensions: permessage-deflate\r\n",
	} {
		wire := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: " + WebSocketAccept("dGhlIHNhbXBsZSBub25jZQ==") + "\r\n" + extra + "\r\n"
		request := &http.Request{Method: "GET", Header: make(http.Header)}
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		if _, err := ReadResponse(bufio.NewReader(strings.NewReader(wire)), request); err == nil {
			t.Fatal("ordinary HTTP accepted upgrade")
		}
		response, _, err := ReadResponseUpgrade(bufio.NewReader(strings.NewReader(wire)), request)
		if err == nil && ValidWebSocketResponse(response, request.Header) {
			t.Fatal("malformed response accepted")
		}
	}
}

func TestIdleSocketActiveOneWayTrafficAndHalfClose(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := &IdleConn{Conn: a, Context: context.Background(), Timeout: 160 * time.Millisecond}
	inbound := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); inbound <- err }()
	for range 12 {
		read := make(chan error, 1)
		go func() { _, err := b.Read(make([]byte, 1)); read <- err }()
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := <-read; err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-inbound:
			t.Fatalf("active one-way stream expired: %v", err)
		default:
		}
		time.Sleep(40 * time.Millisecond)
	}
	select {
	case err := <-inbound:
		if err == nil {
			t.Fatal("idle read did not time out")
		}
	case <-time.After(time.Second):
		t.Fatal("idle socket remained blocked")
	}
}

func FuzzWebSocketResponse(f *testing.F) {
	f.Add([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		request := &http.Request{Method: "GET", Header: make(http.Header)}
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		response, _, err := ReadResponseUpgrade(bufio.NewReader(bytes.NewReader(wire)), request)
		if err == nil && ValidWebSocketResponse(response, request.Header) {
			var output bytes.Buffer
			if err := WriteWebSocketResponse(&output, response.Header); err != nil {
				t.Fatal(err)
			}
			parsed, _, err := ReadResponseUpgrade(bufio.NewReader(&output), request)
			if err != nil || !ValidWebSocketResponse(parsed, request.Header) {
				t.Fatal("validated handshake did not roundtrip")
			}
		}
	})
}
