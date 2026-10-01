package relay_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/radityama/portway/internal/relay"
)

func TestSSEFlushesHeadersAndStaysActiveBeyondIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		for i := range 12 {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			if http.NewResponseController(w).Flush() != nil {
				return
			}
			select {
			case <-time.After(40 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}, func(s *relay.Server) { s.StreamTimeout = 160 * time.Millisecond })
	done := make(chan *http.Response, 1)
	failed := make(chan error, 1)
	go func() {
		response, err := f.client.Get(f.url)
		if err != nil {
			failed <- err
		} else {
			done <- response
		}
	}()
	var response *http.Response
	select {
	case response = <-done:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("SSE headers waited for first event")
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("SSE headers changed")
	}
	close(release)
	start := time.Now()
	reader := bufio.NewReader(response.Body)
	for i := range 12 {
		line, err := reader.ReadString('\n')
		if err != nil || line != fmt.Sprintf("data: %d\n", i) {
			t.Fatalf("event=%q error=%v", line, err)
		}
		if line, err := reader.ReadString('\n'); err != nil || line != "\n" {
			t.Fatal("event separator changed")
		}
		if i == 0 && time.Since(start) > 120*time.Millisecond {
			t.Fatal("first event was buffered")
		}
	}
	if time.Since(start) < 3*160*time.Millisecond/2 {
		t.Fatal("test did not exercise long-lived SSE")
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
}

func TestSSEIdleAndCancellationCloseLocalService(t *testing.T) {
	for _, cancelClient := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelClient), func(t *testing.T) {
			closed := make(chan struct{})
			f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				defer close(closed)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: first\n\n")
				_ = http.NewResponseController(w).Flush()
				<-r.Context().Done()
			}, func(s *relay.Server) { s.StreamTimeout = 150 * time.Millisecond })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, "GET", f.url, nil)
			response, err := f.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			if line, err := reader.ReadString('\n'); err != nil || line != "data: first\n" {
				t.Fatal("initial event lost")
			}
			if cancelClient {
				cancel()
			}
			if _, err := io.ReadAll(reader); err == nil {
				t.Fatal("stalled/cancelled SSE completed successfully")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("local SSE socket leaked")
			}
		})
	}
}

func TestChunkedRequestAndResponseAreIncremental(t *testing.T) {
	firstReceived := make(chan struct{})
	releaseResponse := make(chan struct{})
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		first := make([]byte, 5)
		if _, err := io.ReadFull(r.Body, first); err != nil || string(first) != "first" {
			http.Error(w, "upload changed", 500)
			return
		}
		close(firstReceived)
		remainder, err := io.ReadAll(r.Body)
		if err != nil || string(remainder) != "second" {
			http.Error(w, "remainder changed", 500)
			return
		}
		_, _ = io.WriteString(w, "response-first")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-releaseResponse:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "-second")
	}, nil)
	body, writer := io.Pipe()
	defer writer.Close()
	request, _ := http.NewRequest("POST", f.url, body)
	done := make(chan *http.Response, 1)
	failed := make(chan error, 1)
	go func() {
		response, err := f.client.Do(request)
		if err != nil {
			failed <- err
		} else {
			done <- response
		}
	}()
	if _, err := io.WriteString(writer, "first"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstReceived:
	case <-time.After(time.Second):
		t.Fatal("chunked upload waited for EOF")
	}
	if _, err := io.WriteString(writer, "second"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var response *http.Response
	select {
	case response = <-done:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("response headers were buffered")
	}
	defer response.Body.Close()
	first := make([]byte, len("response-first"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "response-first" {
		t.Fatal("first response chunk lost")
	}
	if len(response.TransferEncoding) != 1 || response.TransferEncoding[0] != "chunked" {
		t.Fatal("unknown-length response framing lost")
	}
	close(releaseResponse)
	if remainder, err := io.ReadAll(response.Body); err != nil || string(remainder) != "-second" {
		t.Fatal("chunked response remainder changed")
	}
}

func TestStreamingChunkedUploadSurvivesWholeLifetime(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		_, _ = w.Write(body)
	}, func(s *relay.Server) { s.StreamTimeout = 160 * time.Millisecond })
	body, writer := io.Pipe()
	defer writer.Close()
	request, _ := http.NewRequest("POST", f.url, body)
	done := make(chan error, 1)
	go func() {
		response, err := f.client.Do(request)
		if err == nil {
			defer response.Body.Close()
			var value []byte
			value, err = io.ReadAll(response.Body)
			if err == nil && string(value) != strings.Repeat("x", 12) {
				err = fmt.Errorf("upload changed: %d bytes", len(value))
			}
		}
		done <- err
	}()
	for range 12 {
		if _, err := io.WriteString(writer, "x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	writer.Close()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestEarlyResponseCleanupCannotBeExtendedByStreamingReads(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).EnableFullDuplex()
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(413)
		_, _ = io.WriteString(w, "early")
		_ = http.NewResponseController(w).Flush()
		// Read the pending upload so the test server detects agent socket EOF.
		_, _ = io.Copy(io.Discard, r.Body)
	}, func(s *relay.Server) { s.StreamTimeout = 5 * time.Second; s.ShutdownTimeout = 2 * time.Second })
	raw, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn := tls.Client(raw, &tls.Config{RootCAs: f.tlsConfig.RootCAs, ServerName: f.sessionHostname(), MinVersion: tls.VersionTLS13})
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nContent-Length: 16777216\r\n\r\npartial", f.sessionHostname())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 413 || responseBody(t, response) != "early" {
		t.Fatal("early response changed")
	}
	start := time.Now()
	if err := f.server.Shutdown(context.Background()); err != nil {
		t.Fatalf("cleanup exceeded its one-second bound: %v", err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatal("idle reader extended early-response cleanup")
	}
}
