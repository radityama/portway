package observability

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMeasurementsGenerationRetentionAndPrivacy(t *testing.T) {
	m := New("relay")
	now := time.Now()
	m.now = func() time.Time { return now }
	old := m.Begin("POST")
	old.Bind("tnl_a", 1)
	old.AddIn(3)
	fresh := m.Begin("CUSTOM-secret")
	fresh.Bind("tnl_a", 2)
	fresh.AddOut(4)
	now = now.Add(20 * time.Millisecond)
	old.Finish(500, true, false)
	fresh.Finish(200, false, false)
	o := m.Observations(map[string]uint64{"tnl_a": 2})
	if len(o) != 1 || o[0].Requests != "1" || o[0].Errors != "0" || o[0].BytesIn != "0" || o[0].BytesOut != "4" || o[0].Logs[0].Method != "OTHER" || o[0].LatencyBuckets[2] != "1" {
		t.Fatalf("incorrect observation: %+v", o)
	}
	stale := m.Begin("GET")
	stale.Bind("tnl_a", 1)
	stale.Finish(503, true, false)
	for range 6 {
		r := m.Begin("GET")
		r.Bind("tnl_a", 2)
		now = now.Add(time.Second)
		r.Finish(200, false, false)
	}
	o = m.Observations(map[string]uint64{"tnl_a": 2})
	if len(o[0].Logs) != MaxLogs || o[0].Requests != "7" {
		t.Fatalf("ring: %+v", o)
	}
	var text bytes.Buffer
	m.WritePrometheus(&text)
	for _, secret := range []string{"tnl_a", "CUSTOM-secret", "generation"} {
		if strings.Contains(text.String(), secret) {
			t.Fatalf("secret/high cardinality label: %s", secret)
		}
	}
	if !strings.Contains(text.String(), "portway_relay_request_duration_seconds_count 9") {
		t.Fatal(text.String())
	}
	now = now.Add(Retention)
	if len(m.Observations(map[string]uint64{"tnl_a": 2})) != 0 {
		t.Fatal("expired observation retained")
	}
}
func TestBoundedCollectionDoesNotDropGlobalRequests(t *testing.T) {
	m := New("relay")
	active := make([]*Request, 0, MaxTunnels)
	current := map[string]uint64{}
	for i := range MaxTunnels {
		id := "tnl_" + strconv.Itoa(i)
		r := m.Begin("GET")
		r.Bind(id, 1)
		active = append(active, r)
		current[id] = 1
	}
	skipped := m.Begin("GET")
	skipped.Bind("tnl_overflow", 1)
	skipped.Finish(200, false, false)
	if m.dropped.Load() != 1 || len(m.entries) != MaxTunnels {
		t.Fatal("observation admission is not bounded")
	}
	for _, r := range active {
		r.Finish(200, false, false)
	}
	if m.totals.requests != 33 || len(m.Observations(current)) != 32 {
		t.Fatal("global counters lost requests")
	}
	r := m.Begin("GET")
	r.Bind("tnl_next", 2)
	r.Finish(200, false, false)
	if len(m.entries) != MaxTunnels || m.entries["tnl_next"] == nil {
		t.Fatal("idle cache entry not evicted")
	}
}
func TestConcurrentCountersAndMaxReportSize(t *testing.T) {
	m := New("relay")
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			r := m.Begin("PUT")
			r.Bind("tnl_concurrent", 1)
			r.AddIn(100)
			r.AddOut(200)
			r.Finish(200, false, false)
		})
	}
	workers.Wait()
	o := m.Observations(map[string]uint64{"tnl_concurrent": 1})[0]
	if o.Requests != "64" || o.BytesIn != "6400" || o.BytesOut != "12800" || o.ActiveRequests != 0 {
		t.Fatalf("racy counters: %+v", o)
	}
	max := "18446744073709551615"
	worst := make([]Observation, MaxTunnels)
	for i := range worst {
		v := &worst[i]
		v.TunnelID = strings.Repeat("x", 128)
		v.Generation = max
		v.Requests = max
		v.Errors = max
		v.BytesIn = max
		v.BytesOut = max
		v.ActiveRequests = 10000
		v.LatencySumSeconds = 1e29
		for j := range v.LatencyBuckets {
			v.LatencyBuckets[j] = max
		}
		for range MaxLogs {
			v.Logs = append(v.Logs, Log{ID: strings.Repeat("a", 32), Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 999999999, time.UTC), Method: "CONNECT", Status: 599, DurationMS: 9223372036855, BytesIn: max, BytesOut: max, Outcome: "canceled"})
		}
	}
	encoded, err := json.Marshal(worst)
	if err != nil || len(encoded)+1024 > 65536 {
		t.Fatalf("report exceeds bounded request budget: %d %v", len(encoded), err)
	}
}

type hijacker struct {
	http.ResponseWriter
	conn net.Conn
	rw   *bufio.ReadWriter
}

func (h *hijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) { return h.conn, h.rw, nil }
func TestResponseControllerAndHijackedByteAccounting(t *testing.T) {
	m := New("relay")
	r := m.Begin("GET")
	r.Bind("tnl_a", 1)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	reader := bufio.NewReader(strings.NewReader("early"))
	_, _ = reader.Peek(5)
	h := &hijacker{ResponseWriter: httptest.NewRecorder(), conn: a, rw: bufio.NewReadWriter(reader, bufio.NewWriter(a))}
	w := &Writer{ResponseWriter: h, Request: r}
	if !errors.Is(http.NewResponseController(w).SetWriteDeadline(time.Now()), http.ErrNotSupported) {
		t.Fatal("deadline controller behavior changed")
	}
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(io.LimitReader(buffered.Reader, 5))
	if string(data) != "early" {
		t.Fatal("buffered WebSocket data lost")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = b.Write([]byte("next"))
		buf := make([]byte, 3)
		_, _ = io.ReadFull(b, buf)
	}()
	buf := make([]byte, 4)
	_, _ = io.ReadFull(conn, buf)
	_, _ = conn.Write([]byte("out"))
	<-done
	r.Finish(w.Status, false, false)
	o := m.Observations(map[string]uint64{"tnl_a": 1})[0]
	if o.BytesIn != "9" || o.BytesOut != "3" || o.Logs[0].Status != 101 {
		t.Fatalf("hijack counters: %+v", o)
	}
}
func TestFlushInformationalHeadersAndBodyCounts(t *testing.T) {
	m := New("relay")
	finished := make(chan Log, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		r := m.Begin(request.Method)
		body := &Body{ReadCloser: request.Body, Request: r}
		_, _ = io.Copy(io.Discard, body)
		w := &Writer{ResponseWriter: writer, Request: r}
		w.WriteHeader(103)
		w.WriteHeader(201)
		_, _ = w.Write([]byte("reply"))
		if http.NewResponseController(w).Flush() != nil {
			r.Fail()
		}
		finished <- r.Finish(w.Status, false, false)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "text/plain", strings.NewReader("upload"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	record := <-finished
	if record.BytesIn != "6" || record.BytesOut != "5" || record.Status != 201 || response.StatusCode != 201 || string(data) != "reply" || record.Outcome != "complete" {
		t.Fatalf("body/flush semantics changed: %+v, %d %q", record, response.StatusCode, data)
	}
}

func TestExpiredLogsAreRemovedWhileRequestsRemainActive(t *testing.T) {
	m := New("relay")
	now := time.Now()
	m.now = func() time.Time { return now }
	r := m.Begin("GET")
	r.Bind("tnl_a", 1)
	r.Finish(200, false, false)
	active := m.Begin("GET")
	active.Bind("tnl_a", 1)
	now = now.Add(Retention)
	o := m.Observations(map[string]uint64{"tnl_a": 1})
	if len(o) != 1 || len(o[0].Logs) != 0 || len(m.entries["tnl_a"].logs) != 0 {
		t.Fatal("expired logs retained with active traffic")
	}
	active.Finish(200, false, false)
}
func TestUnusualHTTPStatusCannotInvalidateReports(t *testing.T) {
	m := New("relay")
	r := m.Begin("GET")
	r.Bind("tnl_a", 1)
	record := r.Finish(999, false, false)
	o := m.Observations(map[string]uint64{"tnl_a": 1})[0]
	if record.Status != 0 || record.Outcome != "error" || o.Errors != "1" {
		t.Fatal("unsupported metadata status escaped normalization")
	}
}
