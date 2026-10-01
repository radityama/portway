package observability

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestMetricsListenerOwnershipAndSurface(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	stop, err := Start(port, New("relay").WritePrometheus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	client := &http.Client{Timeout: time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for _, check := range []struct {
		method, path string
		status       int
	}{{"GET", "/metrics", 200}, {"POST", "/metrics", 405}, {"GET", "/metrics?secret=one", 404}, {"GET", "/", 404}} {
		r, _ := http.NewRequest(check.method, base+check.path, nil)
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != check.status {
			t.Fatal(response.StatusCode)
		}
	}
	stop()
	stop()
	l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal("listener not released", err)
	}
	_ = l.Close()
	for _, port := range []int{-1, 65536} {
		if _, e := Start(port, func(io.Writer) {}); e == nil {
			t.Fatal("invalid port accepted")
		}
	}
	disabled, e := Start(0, nil)
	if e != nil {
		t.Fatal(e)
	}
	disabled()
}
