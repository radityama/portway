package httpwire

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHopHeadersAndRepeatedApplicationHeaders(t *testing.T) {
	h := http.Header{"Connection": {"X-Private, Keep-Alive"}, "X-Private": {"secret"}, "Keep-Alive": {"timeout=5"}, "Proxy-Authorization": {"secret"}, "Transfer-Encoding": {"chunked"}, "Set-Cookie": {"a=1", "b=2"}}
	StripHopHeaders(h)
	if h.Get("X-Private") != "" || h.Get("Proxy-Authorization") != "" || h.Get("Transfer-Encoding") != "" || len(h.Values("Set-Cookie")) != 2 {
		t.Fatal("HTTP hop header policy failed")
	}
}
func TestResponseHeaderBoundsAndInterimResponses(t *testing.T) {
	for _, wire := range []string{"HTTP/1.1 200 OK\r\nX-Large: " + strings.Repeat("x", 40000) + "\r\n\r\n", "HTTP/1.1 101 Switching Protocols\r\n\r\n", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Late\r\n\r\n", "not HTTP\r\n\r\n"} {
		if _, err := ReadResponse(bufio.NewReader(strings.NewReader(wire)), &http.Request{Method: "GET"}); err == nil {
			t.Fatal("unsafe upstream response accepted")
		}
	}
	wire := "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nbody"
	response, err := ReadResponse(bufio.NewReader(strings.NewReader(wire)), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "body" {
		t.Fatal("interim response lost data")
	}
}
func TestBodyLimitChecksOneBytePastBound(t *testing.T) {
	for _, entry := range []struct {
		body string
		over bool
	}{{"1234", false}, {"12345", true}} {
		data, err := io.ReadAll(&LimitedBody{Reader: bytes.NewBufferString(entry.body), Remaining: 4})
		if string(data) != "1234" || errors.Is(err, ErrBody) != entry.over {
			t.Fatal("stream body limit failed")
		}
	}
}

func TestRejectedTrailersDoNotDrainSlowBody(t *testing.T) {
	reader, writer := io.Pipe()
	finished := make(chan struct{})
	var responseErr error
	go func() {
		defer close(finished)
		_, responseErr = ReadResponse(bufio.NewReader(reader), &http.Request{Method: "GET"})
	}()
	defer func() { reader.Close(); writer.Close(); <-finished }()
	if _, err := io.WriteString(writer, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Late\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
		if !errors.Is(responseErr, ErrHeaders) {
			t.Fatal("upstream trailer rejection failed")
		}
	case <-time.After(time.Second):
		t.Fatal("rejected response drained a slow body")
	}
}
