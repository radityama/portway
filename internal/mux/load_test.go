package mux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

func TestLoadStreamChurnPreservesIdentityAndReusesCredit(t *testing.T) {
	origin, remote := pairWithLimit(t, 32, func(s *Stream, open protocol.OpenStream) {
		if s.Accept() != nil {
			return
		}
		if strings.HasPrefix(open.Target, "/cancel/") {
			<-s.Context().Done()
			return
		}
		if _, err := io.WriteString(s, open.Target+"\n"); err != nil {
			return
		}
		_, _ = io.Copy(s, s)
		_ = s.CloseWrite()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const workers, perWorker, size = 8, 64, 128 * 1024
	errors := make(chan error, workers)
	var joined sync.WaitGroup
	for worker := range workers {
		joined.Add(1)
		go func() {
			defer joined.Done()
			payload := bytes.Repeat([]byte{byte(worker + 1)}, size)
			for index := range perWorker {
				open := request()
				open.Target = fmt.Sprintf("/echo/%d/%d", worker, index)
				if index%8 == 0 {
					open.Target = fmt.Sprintf("/cancel/%d/%d", worker, index)
				}
				if err := churnStream(ctx, origin, open, payload); err != nil {
					errors <- fmt.Errorf("worker %d stream %d: %w", worker, index, err)
					cancel()
					return
				}
			}
		}()
	}
	joined.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	waitFor(t, func() bool {
		for _, c := range []*Conn{origin, remote} {
			c.mu.Lock()
			clean := len(c.streams) == 0 && c.accepting == 0 && c.queuedBytes == 0 && c.sendWindow == protocol.InitialConnectionWindow
			c.mu.Unlock()
			if !clean {
				return false
			}
		}
		return true
	})
	for _, c := range []*Conn{origin, remote} {
		c.mu.Lock()
		pages := c.allocatedPages
		c.mu.Unlock()
		if pages > int(protocol.InitialConnectionWindow)/receivePageSize+2*c.opts.MaxStreams {
			t.Fatal("stream churn exceeded the retained page budget")
		}
	}
}

func churnStream(ctx context.Context, c *Conn, open protocol.OpenStream, payload []byte) error {
	s, err := c.Open(ctx, open)
	if err != nil {
		return err
	}
	defer s.Close()
	if strings.HasPrefix(open.Target, "/cancel/") {
		// Fill a stream window while the peer deliberately stops reading, then
		// reset it. Subsequent transfers must reuse the returned shared credit.
		_, err := s.Write(payload[:protocol.InitialStreamWindow])
		s.Reset(protocol.StreamCancelled)
		return err
	}
	written := make(chan error, 1)
	go func() {
		_, err := s.Write(payload)
		if err == nil {
			err = s.CloseWrite()
		}
		written <- err
	}()
	expected := sha256.New()
	_, _ = io.WriteString(expected, open.Target+"\n")
	_, _ = expected.Write(payload)
	actual := sha256.New()
	want := int64(len(open.Target) + 1 + len(payload))
	n, err := io.Copy(actual, io.LimitReader(s, want+1))
	if err != nil || n != want || !bytes.Equal(actual.Sum(nil), expected.Sum(nil)) {
		s.Close()
		<-written
		return fmt.Errorf("stream bytes or identity changed: length=%d want=%d error=%v", n, want, err)
	}
	return <-written
}
