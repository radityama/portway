// This bounded TCP bridge is test infrastructure, never a released Portway
// command. Docker publishes its listener on loopback; netem changes only its
// dedicated network namespace. TLS remains end-to-end between agent and relay.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	target := flag.String("target", "", "explicit fixture relay address")
	delay := flag.Int("delay-ms", -1, "update the bridge delivery delay (fixture control only)")
	flag.Parse()
	if *delay >= 0 && *delay <= 500 {
		if err := os.WriteFile("/tmp/portway-delay", []byte(strconv.Itoa(*delay)), 0600); err != nil {
			os.Exit(1)
		}
		return
	}
	if _, _, err := net.SplitHostPort(*target); err != nil {
		fmt.Fprintln(os.Stderr, "invalid fixture address")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := serve(ctx, *target); err != nil {
		fmt.Fprintln(os.Stderr, "fixture bridge failed")
		os.Exit(1)
	}
}

func serve(ctx context.Context, target string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := net.Listen("tcp", ":8666")
	if err != nil {
		return err
	}
	defer listener.Close()
	var mu sync.Mutex
	active := make(map[net.Conn]struct{})
	var workers sync.WaitGroup
	var delay atomic.Int64
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f, err := os.Open("/tmp/portway-delay")
				if err != nil {
					continue
				}
				b, err := io.ReadAll(io.LimitReader(f, 16))
				f.Close()
				value, parseErr := strconv.Atoi(strings.TrimSpace(string(b)))
				if err == nil && parseErr == nil && value >= 0 && value <= 500 {
					delay.Store(int64(value))
				}
			}
		}
	}()
	stop := context.AfterFunc(ctx, func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for conn := range active {
			conn.Close()
		}
	})
	defer func() { cancel(); workers.Wait(); stop() }()
	fmt.Println("fixture_bridge_ready")
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		if ctx.Err() != nil || len(active) >= 32 {
			mu.Unlock()
			conn.Close()
			continue
		}
		active[conn] = struct{}{}
		mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { conn.Close(); mu.Lock(); delete(active, conn); mu.Unlock() }()
			remote, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
			if err != nil {
				return
			}
			defer remote.Close()
			forward(ctx, conn, remote, &delay)
		}()
	}
}

func forward(ctx context.Context, conn, remote net.Conn, delay *atomic.Int64) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closeBoth := func() { conn.Close(); remote.Close(); cancel() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	// A transport EOF terminates this TLS fixture connection in both directions;
	// retaining the other socket would hide peer crashes from the relay/agent.
	deadline := time.Now().Add(3 * time.Minute)
	_ = conn.SetDeadline(deadline)
	_ = remote.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		_, _ = io.CopyBuffer(delayedWriter{remote, ctx, delay}, conn, make([]byte, 16*1024))
		closeBoth()
		close(done)
	}()
	_, _ = io.CopyBuffer(delayedWriter{conn, ctx, delay}, remote, make([]byte, 16*1024))
	closeBoth()
	<-done
}

type delayedWriter struct {
	io.Writer
	ctx   context.Context
	delay *atomic.Int64
}

func (w delayedWriter) Write(b []byte) (int, error) {
	if delay := w.delay.Load(); delay > 0 {
		timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
		select {
		case <-w.ctx.Done():
			timer.Stop()
			return 0, w.ctx.Err()
		case <-timer.C:
		}
	}
	return w.Writer.Write(b)
}
