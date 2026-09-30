package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
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
	t.Helper()
	dir := t.TempDir()
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("CLI fixture did not stop")
		}
	})
	env := map[string]string{"PORTWAY_JSON": "1", "PORTWAY_RELAY_ADDR": listener.Addr().String(), "PORTWAY_RELAY_CA_FILE": filepath.Join(dir, "ca.pem"), "PORTWAY_TOKEN_FILE": filepath.Join(dir, "agent-token")}
	lookup := func(key string) (string, bool) { v, ok := env[key]; return v, ok }
	token, err := auth.ReadTokenFile(filepath.Join(dir, "agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	return lookup, token
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
