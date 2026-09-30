package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		logger.Error("relay_listen_failed")
		os.Exit(1)
	}
	logger.Info("relay_listening", "address", cfg.Address, "transport", "tls", "protocol", transport.ALPN)
	if err := server.Serve(ctx, listener); err != nil {
		logger.Error("relay_serve_failed")
		os.Exit(1)
	}
	logger.Info("relay_shutdown_complete")
}
