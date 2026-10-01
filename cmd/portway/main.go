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
	"sync"
	"syscall"
	"time"

	"github.com/radityama/portway/internal/agent"
	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

type Event struct {
	Event          string `json:"event"`
	Timestamp      string `json:"timestamp"`
	LocalURL       string `json:"local_url,omitempty"`
	ConnectionID   string `json:"connection_id,omitempty"`
	Relay          string `json:"relay,omitempty"`
	Port           int    `json:"port,omitempty"`
	Error          string `json:"error,omitempty"`
	TunnelID       string `json:"tunnel_id,omitempty"`
	Generation     string `json:"generation,omitempty"`
	PublicHostname string `json:"public_hostname,omitempty"`
	PublicURL      string `json:"public_url,omitempty"`
	URL            string `json:"url,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Attempt        uint32 `json:"attempt,omitempty"`
	DelayMS        int64  `json:"delay_ms,omitempty"`
}

var errCredentialFile = errors.New("cannot read a valid private credential file; run make setup")
var errGenerationState = errors.New("cannot reserve tunnel generation; check state directory or concurrent starts")

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
	var shutdownStarted sync.Once
	shutdownRelay := ""
	startShutdown := func(connectionID string) {
		shutdownStarted.Do(func() {
			emit(Event{Event: "shutdown_started", ConnectionID: connectionID, Relay: shutdownRelay, Reason: "user_shutdown"})
		})
	}
	register := false
	port := 0
	if len(args) == 0 {
		return fail("usage: portway <port> | portway connect [--once] | portway register [--once]", 2)
	}
	if args[0] == "connect" || args[0] == "register" {
		register = args[0] == "register"
		if len(args) == 2 && args[1] == "--once" {
			once = true
		} else if len(args) != 1 {
			return fail("usage: portway connect [--once] | portway register [--once]", 2)
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
		register = true
	}
	cfg, err := config.AgentEnvironment(env)
	if err != nil {
		return fail("invalid relay connection configuration", 1)
	}
	shutdownRelay = cfg.Address
	tlsConfig, err := transport.ClientConfig(cfg.CAFile, cfg.ServerName)
	if err != nil {
		return fail("cannot load relay certificate trust; run make setup", 1)
	}
	emit(Event{Event: "starting"})
	if port != 0 && !localReachable(ctx, port) {
		if errors.Is(ctx.Err(), context.Canceled) {
			startShutdown("")
			emit(Event{Event: "shutdown_complete"})
			return 0
		}
		return fail(fmt.Sprintf("localhost:%d is not reachable", port), 1)
	}
	client := agent.NewClient(&transport.TLSDialer{Config: tlsConfig, Timeout: cfg.ConnectTimeout})
	client.HandshakeTimeout = cfg.HandshakeTimeout
	client.IdleTimeout = cfg.IdleTimeout
	client.WriteTimeout = cfg.WriteTimeout
	client.RegistrationTimeout = cfg.RegistrationTimeout
	client.MaxStreams = cfg.MaxStreams
	client.StreamTimeout = cfg.StreamTimeout
	client.ShutdownTimeout = cfg.ShutdownTimeout
	localURL := ""
	if port != 0 {
		localURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	explicitGeneration := cfg.Generation
	var backoff agent.Backoff
	for {
		var connectedAt time.Time
		connectionID := ""
		attemptErr := func() (result error) {
			life, cancelLife := context.WithCancel(context.WithoutCancel(ctx))
			defer cancelLife()
			stopAttempt := context.AfterFunc(ctx, cancelLife)
			defer stopAttempt()
			// Reload the private file for each new handshake; never retain credentials
			// in reconnect state or print an underlying read/transport error.
			token, err := auth.ReadTokenFile(cfg.TokenFile)
			if err != nil {
				return errCredentialFile
			}
			emit(Event{Event: "tunnel_connecting", Relay: cfg.Address, Port: port})
			session, err := client.Connect(life, cfg.Address, token)
			token = ""
			if err != nil {
				return err
			}
			defer session.Close()
			defer func() {
				if !time.Now().Before(session.ExpiresAt) && ctx.Err() == nil {
					result = &agent.AuthenticationError{Code: protocol.AuthExpired}
				}
			}()
			connectionID = session.ConnectionID
			emit(Event{Event: "relay_authenticated", ConnectionID: session.ConnectionID, Relay: cfg.Address, LocalURL: localURL, Port: port})
			publicURL := ""
			if register {
				generation, err := agent.ReserveGeneration(ctx, cfg.StateDir, cfg.TunnelID, explicitGeneration)
				if err != nil {
					return errGenerationState
				}
				explicitGeneration = 0
				mode := ""
				if port != 0 {
					mode = "http"
				}
				ack, err := session.Register(ctx, protocol.Register{TunnelID: cfg.TunnelID, Generation: generation, Protocol: mode})
				if err != nil {
					return err
				}
				emit(Event{Event: "tunnel_registered", TunnelID: ack.TunnelID, ConnectionID: ack.ConnectionID, Generation: ack.Generation.String(), PublicHostname: ack.PublicHostname, Relay: cfg.Address, LocalURL: localURL, Port: port})
				publicURL = ack.PublicURL
				if jsonMode != "1" {
					fmt.Fprintf(stdout, "✓ Tunnel registered\nHostname %s\n", ack.PublicHostname)
				}
			}
			if jsonMode != "1" {
				fmt.Fprintf(stdout, "✓ Relay authenticated: %s\n", cfg.Address)
				if localURL != "" {
					fmt.Fprintf(stdout, "Local %s\n", localURL)
				}
				if !once {
					fmt.Fprintln(stdout, "Press Ctrl+C to disconnect.")
				}
			}
			if once {
				return nil
			}
			onDraining := func() {
				if ctx.Err() != nil {
					startShutdown(session.ConnectionID)
				} else {
					emit(Event{Event: "tunnel_draining", ConnectionID: session.ConnectionID, Relay: cfg.Address, Reason: "peer_shutdown"})
				}
			}
			if register {
				stopAttempt()
			}
			var errWait error
			if port != 0 {
				errWait = session.ServeHTTPGraceful(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), ctx, func() {
					if ctx.Err() != nil {
						return
					}
					connectedAt = time.Now()
					emit(Event{Event: "tunnel_connected", ConnectionID: session.ConnectionID, Relay: cfg.Address})
					emit(Event{Event: "public_url", URL: publicURL})
					emit(Event{Event: "ready", PublicURL: publicURL, LocalURL: localURL, Port: port})
					if jsonMode != "1" {
						fmt.Fprintf(stdout, "Public %s\nReady.\n", publicURL)
					}
				}, onDraining)
			} else {
				if register {
					errWait = session.WaitGraceful(ctx, onDraining)
				} else {
					errWait = session.Wait()
				}
			}
			return errWait
		}()
		if ctx.Err() != nil || attemptErr == nil || (port == 0 && agent.DisconnectReason(attemptErr) == "relay_draining") {
			if !once {
				if ctx.Err() != nil {
					startShutdown(connectionID)
				}
				emit(Event{Event: "shutdown_complete"})
			}
			return 0
		}
		if port == 0 || !agent.Retryable(attemptErr) {
			var authentication *agent.AuthenticationError
			var registration *agent.RegistrationError
			if errors.As(attemptErr, &authentication) {
				return fail("relay authentication failed: "+authentication.Code, 1)
			}
			if errors.As(attemptErr, &registration) {
				return fail("tunnel registration failed: "+registration.Code, 1)
			}
			if errors.Is(attemptErr, errCredentialFile) || errors.Is(attemptErr, errGenerationState) {
				return fail(attemptErr.Error(), 1)
			}
			return fail("relay connection failed; check address, TLS trust, and relay availability", 1)
		}
		reason := agent.DisconnectReason(attemptErr)
		if connectionID != "" {
			emit(Event{Event: "tunnel_disconnected", ConnectionID: connectionID, Relay: cfg.Address, Reason: reason})
		}
		healthyFor := time.Duration(0)
		if !connectedAt.IsZero() {
			healthyFor = time.Since(connectedAt)
		}
		attempt, delay := backoff.Next(healthyFor)
		emit(Event{Event: "reconnect_scheduled", Relay: cfg.Address, Reason: reason, Attempt: attempt, DelayMS: delay.Milliseconds()})
		if jsonMode != "1" {
			fmt.Fprintf(stderr, "Relay disconnected. Reconnecting in %s.\n", delay.Round(time.Millisecond))
		}
		if agent.WaitReconnect(ctx, delay) != nil {
			startShutdown("")
			emit(Event{Event: "shutdown_complete"})
			return 0
		}
	}
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
