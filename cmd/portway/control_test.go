package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/relay"
	"github.com/radityama/portway/internal/transport"
)

func TestCLIBootstrapLeaseRefreshAndControlOutage(t *testing.T) {
	const bearer = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	tokenPath := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(tokenPath, []byte(bearer), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	leases := map[string]control.Identity{}
	generation := protocol.Generation(40)
	relayPort := 0
	outage := false
	calls := 0
	var issued []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(401)
			return
		}
		if outage {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/tunnels/tnl_local_dev/connect" {
			var body struct {
				Minimum protocol.Generation `json:"minimumGeneration"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			generation++
			if generation < body.Minimum {
				generation = body.Minimum
			}
			now := time.Now().UTC()
			token := strings.Repeat("x", 40) + generation.String()
			issued = append(issued, token)
			hash := sha256.Sum256([]byte(token))
			hostnameHash := sha256.Sum256([]byte("tnl_local_dev"))
			expiry := now.Add(2 * time.Second)
			leases[hex.EncodeToString(hash[:])] = control.Identity{TunnelID: "tnl_local_dev", Generation: generation, ExpiresAt: expiry}
			a := control.Assignment{Relay: control.Relay{ID: "rel_test", Hostname: "localhost", Port: relayPort, Protocol: "tls", Status: "HEALTHY"}, Credential: control.Credential{ID: "cred_test", TunnelID: "tnl_local_dev", Scope: "connect", IssuedAt: now, ExpiresAt: expiry, Token: token}, Generation: generation, PublicHostname: "p-" + hex.EncodeToString(hostnameHash[:16]) + ".portway.localhost"}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": a, "error": nil, "meta": map[string]any{}})
		} else if r.URL.Path == "/api/v1/internal/credentials/verify" {
			var body struct {
				RelayID   string `json:"relayId"`
				TokenHash string `json:"tokenHash"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			lease, ok := leases[body.TokenHash]
			if !ok || body.RelayID != "rel_test" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": lease, "error": nil, "meta": map[string]any{}})
		} else {
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	apiClient, err := control.NewClient(api.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer apiClient.Close()
	env, _, server := cliRelayFixture(t, func(s *relay.Server) {
		s.Authenticator = &auth.ControlVerifier{Client: apiClient, RelayID: "rel_test", TokenFile: tokenPath}
	})
	address, _ := env("PORTWAY_RELAY_ADDR")
	_, p, _ := net.SplitHostPort(address)
	relayPort, _ = strconv.Atoi(p)
	lookup := func(key string) (string, bool) {
		switch key {
		case "PORTWAY_API_URL":
			return api.URL + "/api/v1", true
		case "PORTWAY_API_TOKEN_FILE":
			return tokenPath, true
		}
		return env(key)
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "through-control-bootstrap") }))
	defer local.Close()
	_, port, _ := net.SplitHostPort(local.Listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &readyWriter{ready: make(chan Event, 8)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{port}, lookup, stdout, &stderr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("CLI workers failed to stop")
		}
	})
	var ready Event
	select {
	case ready = <-stdout.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("bootstrap not ready")
	}
	ca, _ := env("PORTWAY_RELAY_CA_FILE")
	trust, err := transport.ClientConfig(ca, "")
	if err != nil {
		t.Fatal(err)
	}
	remote := &http.Transport{TLSClientConfig: trust, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, p, _ := net.SplitHostPort(address)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", p))
	}}
	remote.TLSClientConfig.NextProtos = nil
	defer remote.CloseIdleConnections()
	public := &http.Client{Transport: remote, Timeout: time.Second}
	mu.Lock()
	outage = true
	before := calls
	mu.Unlock()
	response, err := public.Get(ready.PublicURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "through-control-bootstrap" {
		t.Fatal("outage broke admitted data path")
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != before {
		t.Fatal("public request queried API")
	}
	// The existing lease expires locally while API unavailable. Recovery must
	// acquire a new credential and register a strictly higher generation.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		retried := calls > after
		mu.Unlock()
		if retried {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired lease did not retry control plane")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	outage = false
	mu.Unlock()
	select {
	case <-stdout.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("API recovery did not become ready")
	}
	session, ok := server.Lookup("p-" + hostnameDigest("tnl_local_dev") + ".portway.localhost")
	if !ok || session.Generation <= 41 {
		t.Fatal("reconnect reused generation")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal("shutdown failed")
		}
		done <- code
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(issued) < 2 || issued[0] == issued[1] {
		t.Fatal("credential replayed")
	}
	for _, token := range append(issued, bearer) {
		if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
			t.Fatal("CLI leaked credential")
		}
	}
	if !strings.Contains(stdout.String(), "credential_expired") || !strings.Contains(stdout.String(), "control_unavailable") {
		t.Fatal("missing bounded recovery events")
	}
}
func hostnameDigest(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:16])
}
