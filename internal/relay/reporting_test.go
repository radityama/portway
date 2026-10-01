package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/radityama/portway/internal/control"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReporterRecoversMissingPresenceAndJoinsOnDrain(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("r", 43)), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	registrations, reports := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InstanceID string `json:"instanceId"`
			LeaseID    string `json:"leaseId"`
			Sequence   int    `json:"sequence"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid body")
		}
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "register") {
			registrations++
		} else {
			reports++
			if reports == 1 {
				w.WriteHeader(409)
				io.WriteString(w, `{"data":null,"error":{"code":"PRESENCE_EXPIRED","message":"expired"},"meta":{}}`)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": control.ReportAcknowledgement{RelayID: "rel_a", InstanceID: body.InstanceID, LeaseID: strings.Repeat("b", 32), Sequence: body.Sequence, ExpiresAt: time.Now().Add(15 * time.Second), DrainRequested: reports == 2}, "error": nil, "meta": map[string]any{}})
	}))
	defer api.Close()
	client, err := control.NewClient(api.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	drained := make(chan struct{}, 1)
	r := Reporter{Client: client, RelayID: "rel_a", TokenFile: tokenFile, Interval: time.Millisecond, Snapshot: NewServer(nil).Snapshot, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), OnDrain: func() { drained <- struct{}{} }}
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("reporter did not stop")
	}
	select {
	case <-drained:
	default:
		t.Fatal("drain command was ignored")
	}
	mu.Lock()
	defer mu.Unlock()
	if registrations != 2 || reports != 2 {
		t.Fatalf("unexpected report lifecycle %d/%d", registrations, reports)
	}
}

func TestReporterSkipsSequenceAfterAmbiguousResponse(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("r", 43)), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	sequences := []int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InstanceID string `json:"instanceId"`
			Sequence   int    `json:"sequence"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "report") {
			sequences = append(sequences, body.Sequence)
			if len(sequences) == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"data":null,"error":{"code":"PRESENCE_UNAVAILABLE","message":"unknown commit"},"meta":{}}`)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": control.ReportAcknowledgement{RelayID: "rel_a", InstanceID: body.InstanceID, LeaseID: strings.Repeat("b", 32), Sequence: body.Sequence, ExpiresAt: time.Now().Add(15 * time.Second), DrainRequested: len(sequences) == 2}, "error": nil, "meta": map[string]any{}})
	}))
	defer api.Close()
	client, err := control.NewClient(api.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := Reporter{Client: client, RelayID: "rel_a", TokenFile: tokenFile, Interval: time.Millisecond, Snapshot: NewServer(nil).Snapshot, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), OnDrain: func() {}}
	r.Run(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 {
		t.Fatalf("ambiguous response reused sequence: %v", sequences)
	}
}

func TestReporterDrainSkipsCanceledReportSequence(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("r", 43)), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sequences []int
	var statuses []string
	inFlight := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InstanceID string `json:"instanceId"`
			Sequence   int    `json:"sequence"`
			Status     string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if strings.HasSuffix(r.URL.Path, "report") {
			mu.Lock()
			sequences = append(sequences, body.Sequence)
			statuses = append(statuses, body.Status)
			first := len(sequences) == 1
			mu.Unlock()
			if first {
				close(inFlight)
				<-r.Context().Done()
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": control.ReportAcknowledgement{RelayID: "rel_a", InstanceID: body.InstanceID, LeaseID: strings.Repeat("b", 32), Sequence: body.Sequence, ExpiresAt: time.Now().Add(15 * time.Second)}, "error": nil, "meta": map[string]any{}})
	}))
	defer api.Close()
	client, err := control.NewClient(api.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := Reporter{Client: client, RelayID: "rel_a", TokenFile: tokenFile, Interval: time.Millisecond, Snapshot: NewServer(nil).Snapshot, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), OnDrain: func() {}}
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	select {
	case <-inFlight:
	case <-ctx.Done():
		t.Fatal("report did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled reporter did not join")
	}
	drain, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := r.Drain(drain); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 || statuses[1] != "DRAINING" {
		t.Fatalf("drain reused canceled report: sequences=%v statuses=%v", sequences, statuses)
	}
}
