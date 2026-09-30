package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/radityama/portway/internal/agent"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/transport"
)

type Event struct {
	Event        string `json:"event"`
	Timestamp    string `json:"timestamp"`
	LocalURL     string `json:"local_url,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Relay        string `json:"relay,omitempty"`
	Port         int    `json:"port,omitempty"`
	Error        string `json:"error,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, env config.Lookup, stdout, stderr io.Writer) int {
	jsonMode, _ := env("PORTWAY_JSON")
	emit := func(event Event) {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		if jsonMode == "1" {
			_ = json.NewEncoder(stdout).Encode(event)
		}
	}
	fail := func(message string, code int) int {
		if jsonMode == "1" {
			emit(Event{Event: "error", Error: message})
		} else {
			fmt.Fprintln(stderr, "error:", message)
		}
		return code
	}
	once := false
	port := 0
	if len(args) == 0 {
		return fail("usage: portway <port> | portway connect [--once]", 2)
	}
	if args[0] == "connect" {
		if len(args) == 2 && args[1] == "--once" {
			once = true
		} else if len(args) != 1 {
			return fail("usage: portway connect [--once]", 2)
		}
	} else {
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed < 1 || parsed > 65535 {
			return fail("invalid port", 1)
		}
		if len(args) != 1 {
			return fail("usage: portway <port>", 2)
		}
		port = parsed
	}
	cfg, err := config.AgentEnvironment(env)
	if err != nil {
		return fail("invalid relay connection configuration", 1)
	}
	token, err := auth.ReadTokenFile(cfg.TokenFile)
	if err != nil {
		return fail("cannot read a valid private credential file; run make setup", 1)
	}
	tlsConfig, err := transport.ClientConfig(cfg.CAFile, cfg.ServerName)
	if err != nil {
		return fail("cannot load relay certificate trust; run make setup", 1)
	}
	emit(Event{Event: "starting"})
	if port != 0 && !localReachable(ctx, port) {
		return fail(fmt.Sprintf("localhost:%d is not reachable", port), 1)
	}
	emit(Event{Event: "tunnel_connecting", Relay: cfg.Address, Port: port})
	client := agent.NewClient(&transport.TLSDialer{Config: tlsConfig, Timeout: cfg.ConnectTimeout})
	client.HandshakeTimeout = cfg.HandshakeTimeout
	client.IdleTimeout = cfg.IdleTimeout
	client.WriteTimeout = cfg.WriteTimeout
	session, err := client.Connect(ctx, cfg.Address, token)
	token = ""
	if err != nil {
		if errors.Is(err, context.Canceled) {
			emit(Event{Event: "shutdown_complete"})
			return 0
		}
		var rejected *agent.AuthenticationError
		if errors.As(err, &rejected) {
			return fail("relay authentication failed: "+rejected.Code, 1)
		}
		return fail("relay connection failed; check address, TLS trust, and relay availability", 1)
	}
	defer session.Close()
	localURL := ""
	if port != 0 {
		localURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	emit(Event{Event: "relay_authenticated", ConnectionID: session.ConnectionID, Relay: cfg.Address, LocalURL: localURL, Port: port})
	if jsonMode != "1" {
		fmt.Fprintf(stdout, "✓ Relay authenticated: %s\n", cfg.Address)
		if localURL != "" {
			fmt.Fprintf(stdout, "Local %s\n", localURL)
		}
		if !once {
			fmt.Fprintln(stdout, "Public URLs are not available yet. Press Ctrl+C to disconnect.")
		}
	}
	if once {
		return 0
	}
	if err := session.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return fail("relay connection closed", 1)
	}
	emit(Event{Event: "shutdown_complete"})
	return 0
}

func localReachable(ctx context.Context, port int) bool {
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
