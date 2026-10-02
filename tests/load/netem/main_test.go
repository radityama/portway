package main

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestBridgeClosesBothDirectionsAndJoinsCopies(t *testing.T) {
	for _, cause := range []string{"agent disappears", "relay disappears", "cancel during delay"} {
		t.Run(cause, func(t *testing.T) {
			agent, inbound := net.Pipe()
			outbound, relay := net.Pipe()
			defer agent.Close()
			defer relay.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var delay atomic.Int64
			if cause == "cancel during delay" {
				delay.Store(500)
			}
			done := make(chan struct{})
			go func() { forward(ctx, inbound, outbound, &delay); close(done) }()
			_ = agent.SetDeadline(time.Now().Add(time.Second))
			_ = relay.SetDeadline(time.Now().Add(time.Second))
			written := make(chan error, 1)
			go func() { _, err := agent.Write([]byte("intact")); written <- err }()
			if cause == "cancel during delay" {
				// Reading the source has completed, but delivery is still delayed.
				if err := <-written; err != nil {
					t.Fatal(err)
				}
				cancel()
			} else {
				got := make([]byte, 6)
				if _, err := io.ReadFull(relay, got); err != nil || string(got) != "intact" {
					t.Fatal("bridge changed application bytes", err)
				}
				if err := <-written; err != nil {
					t.Fatal(err)
				}
				if cause == "agent disappears" {
					agent.Close()
				} else {
					relay.Close()
				}
			}
			select {
			case <-done:
			case <-time.After(250 * time.Millisecond):
				t.Fatal("disconnect/cancellation retained a copy worker")
			}
			for _, peer := range []net.Conn{agent, relay} {
				if _, err := peer.Read(make([]byte, 1)); err == nil {
					t.Fatal("disconnected bridge kept a peer socket open")
				}
			}
		})
	}
}
