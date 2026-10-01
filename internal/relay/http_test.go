package relay_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/agent"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
)

type httpFixture struct {
	*fixture
	client        *http.Client
	publicAddress string
	url           string
	local         *httptest.Server
	session       *agent.Session
}

func setupHTTP(t *testing.T, handler http.HandlerFunc, configure func(*relay.Server)) *httpFixture {
	t.Helper()
	local := httptest.NewServer(handler)
	t.Cleanup(local.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	f := setup(t, func(s *relay.Server, _ []auth.Record) {
		s.PublicPort = port
		if configure != nil {
			configure(s)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.server.ServeHTTPS(ctx, listener, f.server.TLSConfig) }()
	t.Cleanup(func() {
		cancel()
		if err := await(t, done); err != nil {
			t.Error(err)
		}
	})
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.tlsConfig.RootCAs, MinVersion: tls.VersionTLS13}, DisableKeepAlives: true, ExpectContinueTimeout: time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	session := connected(t, f)
	ack, err := session.Register(context.Background(), protocol.Register{TunnelID: "tnl_local_dev", Generation: 1, Protocol: "http"})
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() { agentDone <- session.ServeHTTP(local.Listener.Addr().String()) }()
	t.Cleanup(func() { session.Close(); _ = await(t, agentDone) })
	return &httpFixture{fixture: f, client: &http.Client{Transport: transport, Timeout: 4 * time.Second}, publicAddress: listener.Addr().String(), url: ack.PublicURL, local: local, session: session}
}
func responseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHTTPSForwardsHTTPMetadataAndLargeBodies(t *testing.T) {
	payload := bytes.Repeat([]byte("portway-body-"), 200000)
	var mu sync.Mutex
	var receivedHost, forwarded, authorization, cookie string
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedHost = r.Host
		forwarded = r.Header.Get("X-Forwarded-Proto") + "/" + r.Header.Get("X-Forwarded-For")
		authorization = r.Header.Get("Authorization")
		cookie = r.Header.Get("Cookie")
		mu.Unlock()
		if r.Method != "PATCH" || r.URL.RequestURI() != "/echo?q=a%2Fb" || r.Header.Get("X-Hop") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Forwarded-Port") != "" {
			http.Error(w, "metadata mismatch", 500)
			return
		}
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Set("Connection", "X-Private")
		w.Header().Set("X-Private", "remove")
		w.WriteHeader(201)
		_, _ = io.Copy(w, r.Body)
	}, nil)
	request, _ := http.NewRequest("PATCH", f.url+"/echo?q=a%2Fb", bytes.NewReader(payload))
	request.Header.Set("Connection", "X-Hop")
	request.Header.Set("X-Hop", "remove")
	request.Header.Set("Proxy-Authorization", "never-forward")
	request.Header.Set("X-Forwarded-For", "spoofed")
	request.Header.Set("X-Forwarded-Proto", "spoofed")
	request.Header.Set("X-Forwarded-Port", "spoofed")
	request.Header.Set("Authorization", "Bearer application-value")
	request.Header.Set("Cookie", "session=application-value")
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 201 || len(response.Header.Values("Set-Cookie")) != 2 {
		t.Fatalf("response status=%d, cookies=%d", response.StatusCode, len(response.Header.Values("Set-Cookie")))
	}
	if got := responseBody(t, response); got != string(payload) {
		t.Fatalf("streamed payload changed (%d bytes)", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasSuffix(receivedHost, ".portway.localhost") || forwarded != "https/127.0.0.1" || authorization != "Bearer application-value" || cookie != "session=application-value" {
		t.Fatal("forwarding policy mismatch")
	}
}

func TestHTTPSStreamsBeforeUpstreamCompletes(t *testing.T) {
	finish := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	defer release()
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			w.Write([]byte("last"))
		case <-r.Context().Done():
		}
	}, nil)
	response, err := f.client.Get(f.url + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, data); err != nil || string(data) != "first" {
		t.Fatal("first response bytes were buffered")
	}
	release()
	if got := responseBody(t, response); got != "last" {
		t.Fatal("stream tail changed")
	}
}

func TestHTTPHeadAndRedirectSemantics(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://destination.example/elsewhere")
			w.WriteHeader(307)
			return
		}
		w.Header().Set("Content-Length", "4")
		fmt.Fprint(w, "body")
	}, nil)
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := f.client.Head(f.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.ContentLength != 4 || responseBody(t, response) != "" {
		t.Fatal("HEAD response semantics changed")
	}
	response, err = f.client.Get(f.url + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 307 || response.Header.Get("Location") != "https://destination.example/elsewhere" {
		t.Fatal("upstream redirect was not returned intact")
	}
}

func TestHTTPSRoutingAndUnsupportedRequests(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }, nil)
	for _, entry := range []struct {
		host, method, upgrade string
		status                int
	}{
		{"unassigned.portway.localhost", "GET", "", 404},
		{"different.portway.localhost", "GET", "", 421},
		{"bad@host", "GET", "", 400},
		{"", "CONNECT", "", 405}, {"", "GET", "websocket", 501},
	} {
		target := f.url + "/"
		if entry.status == 404 {
			target = "https://unassigned.portway.localhost:" + fmt.Sprint(f.server.PublicPort) + "/"
		}
		request, _ := http.NewRequest(entry.method, target, nil)
		if entry.host != "" && entry.status != 404 {
			request.Host = entry.host
		}
		if entry.upgrade != "" {
			request.Header.Set("Upgrade", entry.upgrade)
			request.Header.Set("Connection", "Upgrade")
		}
		response, err := f.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != entry.status {
			t.Fatalf("routing status %d, want %d", response.StatusCode, entry.status)
		}
	}
	f.local.Close()
	response, err := f.client.Get(f.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 502 {
		t.Fatal("unavailable upstream not mapped to 502")
	}
}

func TestHTTPTimeoutCancellationAndStreamCapacity(t *testing.T) {
	entered := make(chan struct{}, 8)
	cancelled := make(chan struct{}, 8)
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fast" {
			w.Write([]byte("ok"))
			return
		}
		entered <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}, func(s *relay.Server) { s.MaxStreams = 1; s.StreamTimeout = 200 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", f.url+"/slow", nil)
	done := make(chan error, 1)
	go func() {
		response, err := f.client.Do(request)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("local request never started")
	}
	response, err := f.client.Get(f.url + "/fast")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("stream capacity was not bounded")
	}
	cancel()
	if err := await(t, done); err == nil {
		t.Fatal("public cancellation was not preserved")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close local TCP")
	}
	response, err = f.client.Get(f.url + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 504 {
		t.Fatalf("timeout status %d", response.StatusCode)
	}
	response, err = f.client.Get(f.url + "/fast")
	if err != nil {
		t.Fatal(err)
	}
	if responseBody(t, response) != "ok" {
		t.Fatal("timeout damaged remaining tunnel session")
	}
}

func TestHTTPConcurrentStreamsAndOwnerReplacement(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.URL.Path) }, nil)
	var requests sync.WaitGroup
	for n := 0; n < 12; n++ {
		requests.Add(1)
		go func(n int) {
			defer requests.Done()
			path := fmt.Sprintf("/request-%d", n)
			response, err := f.client.Get(f.url + path)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != path {
				t.Error("concurrent stream crossed responses")
			}
		}(n)
	}
	requests.Wait()
	replacement := connected(t, f.fixture)
	ack, err := replacement.Register(context.Background(), protocol.Register{TunnelID: "tnl_local_dev", Generation: 2, Protocol: "http"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- replacement.ServeHTTP(f.local.Listener.Addr().String()) }()
	t.Cleanup(func() { replacement.Close(); _ = await(t, done) })
	if ack.PublicURL != f.url {
		t.Fatal("owner replacement changed public URL")
	}
	response, err := f.client.Get(f.url + "/new-owner")
	if err != nil {
		t.Fatal(err)
	}
	if responseBody(t, response) != "/new-owner" {
		t.Fatal("new owner is not routable")
	}
}

func TestHTTPReplacementCancelsActiveLocalRequest(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		fmt.Fprint(w, "replacement")
	}, nil)
	oldRequest := make(chan error, 1)
	go func() {
		response, err := f.client.Get(f.url + "/blocked")
		if response != nil {
			response.Body.Close()
			if response.StatusCode != 502 {
				err = fmt.Errorf("replaced request status %d", response.StatusCode)
			}
		}
		oldRequest <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old local request did not start")
	}
	replacement := connected(t, f.fixture)
	if _, err := replacement.Register(context.Background(), protocol.Register{TunnelID: "tnl_local_dev", Generation: 2, Protocol: "http"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- replacement.ServeHTTP(f.local.Listener.Addr().String()) }()
	t.Cleanup(func() { replacement.Close(); _ = await(t, done) })
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("owner replacement retained old local TCP work")
	}
	if err := await(t, oldRequest); err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Get(f.url + "/new")
	if err != nil {
		t.Fatal(err)
	}
	if responseBody(t, response) != "replacement" {
		t.Fatal("old cleanup removed replacement route")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestHTTPBodyAndHeaderLimits(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/response-limit" {
			w.Header().Set("Content-Length", fmt.Sprint(protocol.MaxResponseBodySize+1))
			w.WriteHeader(200)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(204)
	}, nil)
	for _, known := range []bool{true, false} {
		request, _ := http.NewRequest("POST", f.url+"/upload", io.LimitReader(zeroReader{}, protocol.MaxRequestBodySize+1))
		if known {
			request.ContentLength = protocol.MaxRequestBodySize + 1
			request.Header.Set("Expect", "100-continue")
		}
		response, err := f.client.Do(request)
		if err != nil {
			t.Fatalf("known=%v: %v", known, err)
		}
		response.Body.Close()
		if response.StatusCode != 413 {
			t.Fatalf("request body limit status=%d", response.StatusCode)
		}
	}
	request, _ := http.NewRequest("GET", f.url+"/", nil)
	request.Header.Set("X-Large", strings.Repeat("x", 40000))
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 431 {
		t.Fatal("oversized headers accepted")
	}
	response, err = f.client.Get(f.url + "/response-limit")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 502 {
		t.Fatal("oversized upstream response accepted")
	}
}

func TestPublicConnectionAdmissionAndShutdown(t *testing.T) {
	f := setupHTTP(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }, func(s *relay.Server) { s.MaxPublicConnections = 1 })
	first, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// This connection holds the single TLS admission slot before any handshake.
	second, err := net.DialTimeout("tcp", f.publicAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	expectClosed(t, second)
	first.Close()
	// Remote EOF is observed asynchronously. Wait for admission to be released
	// instead of requiring the server to run before this test's next dial.
	var response *http.Response
	deadline := time.Now().Add(time.Second)
	for {
		response, err = f.client.Get(f.url + "/")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed peer retained public connection capacity")
		}
		time.Sleep(time.Millisecond)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("closed peer retained public connection capacity")
	}
}

func TestCanonicalAuthorityRejectsInjectionAndWrongPorts(t *testing.T) {
	for _, host := range []string{"host.example:80", "host.example:0443", "host.example:", "host.example.", "host.example/path", "host@evil.example", "127.0.0.1", "[::1]:443", "host..example"} {
		if _, err := relay.CanonicalHost(host, 443); err == nil {
			t.Fatal("unsafe authority accepted")
		}
	}
	if got, err := relay.CanonicalHost("UPPER.EXAMPLE:443", 443); err != nil || got != "upper.example" {
		t.Fatal("valid host normalization failed")
	}
}
