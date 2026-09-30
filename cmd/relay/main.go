package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/radityama/portway/internal/relay"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := ":" + getenv("RELAY_PORT", "8081")

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("relay_listen_failed", "error", err)
		os.Exit(1)
	}
	defer ln.Close()

	server := relay.NewServer(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("relay_listening", "address", addr, "transport", "tcp", "mode", "starter")
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				logger.Error("relay_accept_failed", "error", err)
				continue
			}
		}
		go func() {
			if err := server.ServeConn(ctx, conn); err != nil {
				logger.Debug("relay_connection_closed", "error", err)
			}
		}()
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
