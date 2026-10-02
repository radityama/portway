package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/devsetup"
	"github.com/radityama/portway/internal/relay"
	"github.com/radityama/portway/internal/transport"
)

func cliFixture(t *testing.T) (config.Lookup, string) {
	env, token, _ := cliRelayFixture(t)
	return env, token
}

func cliRelayFixture(t *testing.T, configure ...func(*relay.Server)) (config.Lookup, string, *relay.Server) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := devsetup.Ensure(dir, false); err != nil {
		t.Fatal(err)
	}
	server := relay.NewServer(nil)
	var err error
	server.TLSConfig, err = transport.ServerConfig(filepath.Join(dir, "relay-cert.pem"), filepath.Join(dir, "relay-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	server.Authenticator, err = auth.LoadVerifier(filepath.Join(dir, "relay-credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, apply := range configure {
		apply(server)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	public, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server.PublicPort = public.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	publicDone := make(chan error, 1)
	go func() { publicDone <- server.ServeHTTPS(ctx, public, server.TLSConfig) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-publicDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("public CLI fixture did not stop")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("CLI fixture did not stop")
		}
	})
	env := map[string]string{"PORTWAY_JSON": "1", "PORTWAY_STATE_DIR": filepath.Join(dir, "agent-state"), "PORTWAY_RELAY_ADDR": listener.Addr().String(), "PORTWAY_RELAY_CA_FILE": filepath.Join(dir, "ca.pem"), "PORTWAY_TOKEN_FILE": filepath.Join(dir, "agent-token")}
	lookup := func(key string) (string, bool) { v, ok := env[key]; return v, ok }
	token, err := auth.ReadTokenFile(filepath.Join(dir, "agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	return lookup, token, server
}

type readyWriter struct {
	bytes.Buffer
	ready chan Event
}

func (w *readyWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	var event Event
	if json.Unmarshal(data, &event) == nil && event.Event == "ready" {
		w.ready <- event
	}
	return n, err
}
func TestCLIReadyURLActuallyForwardsHTTPS(t *testing.T) {
	env, token := cliFixture(t)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from-the-local-service") }))
	defer local.Close()
	_, port, _ := net.SplitHostPort(local.Listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &readyWriter{ready: make(chan Event, 1)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{port}, env, stdout, &stderr) }()
	var event Event
	select {
	case event = <-stdout.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("CLI never became ready")
	}
	ca, _ := env("PORTWAY_RELAY_CA_FILE")
	config, err := transport.ClientConfig(ca, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{RootCAs: config.RootCAs, MinVersion: tls.VersionTLS13}
	remote := &http.Transport{TLSClientConfig: cfg, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, p, _ := net.SplitHostPort(address)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", p))
	}}
	defer remote.CloseIdleConnections()
	client := &http.Client{Transport: remote, Timeout: 2 * time.Second}
	response, err := client.Get(event.PublicURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != "from-the-local-service" {
		t.Fatal("ready URL did not forward")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal("CLI did not shut down cleanly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CLI workers did not stop")
	}
	if strings.Contains(stdout.String(), token) || stderr.Len() != 0 {
		t.Fatal("CLI leaked credentials or human logs into JSON")
	}
}

func TestRegisteredCLIJSONAndPersistedGeneration(t *testing.T) {
	env, token := cliFixture(t)
	for _, generation := range []string{"1", "2", "3"} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"register", "--once"}, env, &stdout, &stderr); code != 0 {
			t.Fatalf("registration failed: %s", stdout.String())
		}
		if strings.Contains(stdout.String(), token) || strings.Contains(stdout.String(), "\x1b") || stderr.Len() != 0 {
			t.Fatal("unsafe JSON output")
		}
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		if len(lines) != 4 {
			t.Fatal("unexpected registration events")
		}
		var event Event
		if err := json.Unmarshal([]byte(lines[3]), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "tunnel_registered" || event.TunnelID != "tnl_local_dev" || event.Generation != generation || event.PublicHostname == "" || event.ConnectionID == "" {
			t.Fatal("registration metadata missing")
		}
		if strings.Contains(stdout.String(), `"event":"ready"`) || strings.Contains(stdout.String(), `"public_url"`) {
			t.Fatal("CLI claimed public forwarding")
		}
	}
	wrongScope := func(key string) (string, bool) {
		if key == "PORTWAY_TUNNEL_ID" {
			return "tnl_other", true
		}
		return env(key)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"register", "--once"}, wrongScope, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "REGISTER_FORBIDDEN") || strings.Contains(stdout.String(), "tunnel_registered") {
		t.Fatal("CLI hid failed scoped registration")
	}
}

type cancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	if bytes.Contains(data, []byte(`"event":"tunnel_registered"`)) {
		w.cancel()
	}
	return n, err
}
func TestLocalPortCLIRegistersAndCancelsCleanly(t *testing.T) {
	env, _ := cliFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &cancelWriter{cancel: cancel}
	var stderr bytes.Buffer
	if code := run(ctx, []string{port}, env, stdout, &stderr); code != 0 {
		t.Fatalf("port CLI failed: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"event":"tunnel_registered"`) || !strings.Contains(stdout.String(), `"event":"shutdown_complete"`) || !strings.Contains(stdout.String(), `"local_url":"http://127.0.0.1:`) {
		t.Fatal("local-port lifecycle incomplete")
	}
}
func TestAuthenticatedCLIJSONContract(t *testing.T) {
	env, token := cliFixture(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"connect", "--once"}, env, &stdout, &stderr); code != 0 {
		t.Fatalf("CLI failed: %s", stderr.String())
	}
	if stderr.Len() != 0 || strings.Contains(stdout.String(), token) || strings.Contains(stdout.String(), "\x1b") {
		t.Fatal("JSON output contains secrets, ANSI, or stderr output")
	}
	decoder := json.NewDecoder(&stdout)
	var events []Event
	for {
		var event Event
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
			t.Fatal("event lacks a timestamp")
		}
		events = append(events, event)
	}
	if len(events) != 3 || events[0].Event != "starting" || events[1].Event != "tunnel_connecting" || events[2].Event != "relay_authenticated" || events[2].ConnectionID == "" {
		t.Fatal("CLI does not reflect the authenticated milestone")
	}
}
func TestCLIExitCodesAndRedactedErrors(t *testing.T) {
	env := func(key string) (string, bool) {
		if key == "PORTWAY_JSON" {
			return "1", true
		}
		return "", false
	}
	for _, entry := range []struct {
		args []string
		code int
	}{{nil, 2}, {[]string{"invalid"}, 1}, {[]string{"0"}, 1}, {[]string{"connect", "--bad"}, 2}} {
		var stdout, stderr bytes.Buffer
		if got := run(context.Background(), entry.args, env, &stdout, &stderr); got != entry.code {
			t.Fatalf("exit code %d, want %d", got, entry.code)
		}
		var event Event
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); err != nil || event.Event != "error" || stderr.Len() != 0 {
			t.Fatal("error is not JSON-only")
		}
	}
	privateValue := "credential@localhost:443"
	unsafeEnv := func(key string) (string, bool) {
		if key == "PORTWAY_RELAY_ADDR" {
			return privateValue, true
		}
		return env(key)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"connect", "--once"}, unsafeEnv, &stdout, &stderr); code != 1 || strings.Contains(stdout.String(), privateValue) {
		t.Fatal("invalid network configuration was accepted or echoed")
	}
}
