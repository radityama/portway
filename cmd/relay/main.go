package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/relay"
	"github.com/radityama/portway/internal/transport"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.RelayEnvironment(os.LookupEnv)
	if err != nil {
		logger.Error("relay_configuration_failed")
		os.Exit(1)
	}
	tlsConfig, err := transport.ServerConfig(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		logger.Error("relay_tls_configuration_failed")
		os.Exit(1)
	}
	verifier, err := auth.LoadVerifier(cfg.CredentialsFile)
	if err != nil {
		logger.Error("relay_credential_configuration_failed")
		os.Exit(1)
	}
	server := relay.NewServer(logger)
	server.TLSConfig = tlsConfig
	server.Authenticator = verifier
	server.MaxConnections = cfg.MaxConnections
	server.MaxFrame = cfg.MaxFrame
	server.HandshakeTimeout = cfg.HandshakeTimeout
	server.ReadIdleTimeout = cfg.IdleTimeout
	server.WriteTimeout = cfg.WriteTimeout
	server.RegistrationTimeout = cfg.RegistrationTimeout
	server.PublicBaseDomain = cfg.PublicBaseDomain
	server.MaxTunnels = cfg.MaxTunnels
	server.PublicPort = cfg.PublicPort
	server.MaxPublicConnections = cfg.MaxPublicConnections
	server.MaxStreams = uint32(cfg.MaxStreams)
	server.StreamTimeout = cfg.StreamTimeout
	server.ShutdownTimeout = cfg.ShutdownTimeout
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		logger.Error("relay_listen_failed")
		os.Exit(1)
	}
	publicConfig, err := transport.ServerConfig(cfg.PublicCertFile, cfg.PublicKeyFile)
	if err != nil {
		listener.Close()
		logger.Error("public_tls_configuration_failed")
		os.Exit(1)
	}
	publicListener, err := net.Listen("tcp", cfg.PublicAddress)
	if err != nil {
		listener.Close()
		logger.Error("public_listen_failed")
		os.Exit(1)
	}
	logger.Info("relay_listening", "address", cfg.Address, "transport", "tls", "protocol", transport.ALPN)
	logger.Info("public_https_listening", "address", cfg.PublicAddress)
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- server.Serve(life, listener) }()
	go func() { done <- server.ServeHTTPS(life, publicListener, publicConfig) }()
	var first, second error
	select {
	case <-ctx.Done():
		shutdown, stopShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		started := time.Now()
		if err := server.Shutdown(shutdown); err != nil {
			logger.Warn("relay_shutdown_forced", "elapsed_ms", time.Since(started).Milliseconds())
		}
		stopShutdown()
		cancel()
		first, second = <-done, <-done
	case first = <-done:
		cancel()
		second = <-done
	}
	if first != nil || second != nil {
		logger.Error("relay_serve_failed")
		os.Exit(1)
	}
	logger.Info("relay_shutdown_complete")
}
