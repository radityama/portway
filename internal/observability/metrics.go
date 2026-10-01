// Package observability collects bounded, payload-free local measurements.
package observability

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const MaxTunnels = 32
const MaxLogs = 4
const Retention = time.Hour

var Bounds = [...]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
var methods = [...]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE", "OTHER"}

type Log struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	Status     int       `json:"status"`
	DurationMS float64   `json:"durationMs"`
	BytesIn    string    `json:"bytesIn"`
	BytesOut   string    `json:"bytesOut"`
	Outcome    string    `json:"outcome"`
}
type Observation struct {
	TunnelID          string     `json:"tunnelId"`
	Generation        string     `json:"generation"`
	Requests          string     `json:"requests"`
	Errors            string     `json:"errors"`
	BytesIn           string     `json:"bytesIn"`
	BytesOut          string     `json:"bytesOut"`
	ActiveRequests    int        `json:"activeRequests"`
	LatencyBuckets    [12]string `json:"latencyBuckets"`
	LatencySumSeconds float64    `json:"latencySumSeconds"`
	Logs              []Log      `json:"logs"`
}
type totals struct {
	requests, errors, in, out uint64
	buckets                   [12]uint64
	sum                       float64
}
type entry struct {
	id         string
	generation uint64
	active     int
	last       time.Time
	totals     totals
	logs       []Log
}
type Metrics struct {
	role                          string
	now                           func() time.Time
	connections, connectionErrors atomic.Uint64
	connectionsActive             atomic.Int64
	streams, streamErrors         atomic.Uint64
	streamsActive                 atomic.Int64
	bytesIn, bytesOut             atomic.Uint64
	requestsActive                atomic.Int64
	dropped                       atomic.Uint64
	mu                            sync.Mutex
	totals                        totals
	counts                        [10][6]uint64
	entries                       map[string]*entry
}

func New(role string) *Metrics {
	if role != "relay" && role != "agent" {
		panic("invalid metrics role")
	}
	return &Metrics{role: role, now: time.Now, entries: make(map[string]*entry)}
}
func (m *Metrics) ConnectionOpened() {
	if m != nil {
		m.connections.Add(1)
		m.connectionsActive.Add(1)
	}
}
func (m *Metrics) ConnectionClosed(failed bool) {
	if m != nil {
		m.connectionsActive.Add(-1)
		if failed {
			m.connectionErrors.Add(1)
		}
	}
}
func (m *Metrics) StreamOpened() {
	if m != nil {
		m.streams.Add(1)
		m.streamsActive.Add(1)
	}
}
func (m *Metrics) StreamClosed(failed bool) {
	if m != nil {
		m.streamsActive.Add(-1)
		if failed {
			m.streamErrors.Add(1)
		}
	}
}

// Agent payload counters include HTTP wire headers carried in DATA frames.
func (m *Metrics) PayloadIn(n int) {
	if m != nil && m.role == "agent" && n > 0 {
		m.bytesIn.Add(uint64(n))
	}
}
func (m *Metrics) PayloadOut(n int) {
	if m != nil && m.role == "agent" && n > 0 {
		m.bytesOut.Add(uint64(n))
	}
}
func decimal(v uint64) string { return strconv.FormatUint(v, 10) }
func normalized(method string) int {
	for i, v := range methods[:len(methods)-1] {
		if v == method {
			return i
		}
	}
	return len(methods) - 1
}

type Request struct {
	m        *Metrics
	method   int
	start    time.Time
	entry    *entry
	in, out  atomic.Uint64
	failed   atomic.Bool
	finished atomic.Bool
}

func (m *Metrics) Begin(method string) *Request {
	m.requestsActive.Add(1)
	return &Request{m: m, method: normalized(method), start: m.now()}
}
func (r *Request) Bind(id string, generation uint64) {
	m := r.m
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	e := m.entries[id]
	if e != nil && e.generation > generation {
		m.dropped.Add(1)
		return
	}
	if e == nil || e.generation != generation || e.active == 0 && now.Sub(e.last) >= Retention {
		if e == nil && len(m.entries) >= MaxTunnels {
			var oldest *entry
			for _, v := range m.entries {
				if v.active == 0 && (oldest == nil || v.last.Before(oldest.last)) {
					oldest = v
				}
			}
			if oldest == nil {
				m.dropped.Add(1)
				return
			}
			delete(m.entries, oldest.id)
		}
		e = &entry{id: id, generation: generation, last: now, logs: make([]Log, 0, MaxLogs)}
		m.entries[id] = e
	}
	e.active++
	e.last = now
	r.entry = e
}
func (r *Request) AddIn(n int) {
	if n > 0 {
		r.in.Add(uint64(n))
		r.m.bytesIn.Add(uint64(n))
	}
}
func (r *Request) AddOut(n int) {
	if n > 0 {
		r.out.Add(uint64(n))
		r.m.bytesOut.Add(uint64(n))
	}
}
func (r *Request) Fail() { r.failed.Store(true) }
func accumulate(t *totals, elapsed float64, in, out uint64, failed bool) {
	t.requests++
	if failed {
		t.errors++
	}
	t.in += in
	t.out += out
	t.sum += elapsed
	for i, b := range Bounds {
		if elapsed <= b {
			t.buckets[i]++
		}
	}
	t.buckets[11]++
}
func (r *Request) Finish(status int, failed, canceled bool) Log {
	if !r.finished.CompareAndSwap(false, true) {
		return Log{}
	}
	m := r.m
	now := m.now()
	elapsed := now.Sub(r.start).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	failed = failed || r.failed.Load() || status >= 500
	if status < 0 || status > 599 || status > 0 && status < 100 {
		status = 0
		failed = true
	}
	outcome := "complete"
	if failed {
		outcome = "error"
	}
	if canceled {
		outcome = "canceled"
		failed = true
	}
	var random [16]byte
	// On entropy failure omit the scoped record rather than emit a duplicate ID.
	_, randomErr := rand.Read(random[:])
	log := Log{ID: hex.EncodeToString(random[:]), Timestamp: now.UTC(), Method: methods[r.method], Status: status, DurationMS: elapsed * 1000, BytesIn: decimal(r.in.Load()), BytesOut: decimal(r.out.Load()), Outcome: outcome}
	m.requestsActive.Add(-1)
	m.mu.Lock()
	defer m.mu.Unlock()
	accumulate(&m.totals, elapsed, r.in.Load(), r.out.Load(), failed)
	class := status / 100
	if class < 0 || class > 5 {
		class = 0
	}
	m.counts[r.method][class]++
	if e := r.entry; e != nil {
		e.active--
		e.last = now
		accumulate(&e.totals, elapsed, r.in.Load(), r.out.Load(), failed)
		if randomErr == nil {
			if len(e.logs) == MaxLogs {
				copy(e.logs, e.logs[1:])
				e.logs = e.logs[:MaxLogs-1]
			}
			e.logs = append(e.logs, log)
		}
	}
	return log
}
func (m *Metrics) Observations(current map[string]uint64) []Observation {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	result := make([]Observation, 0, len(m.entries))
	for id, e := range m.entries {
		if e.active == 0 && now.Sub(e.last) >= Retention {
			delete(m.entries, id)
			continue
		}
		if current[id] != e.generation {
			continue
		}
		live := e.logs[:0]
		for _, record := range e.logs {
			if now.Sub(record.Timestamp) < Retention {
				live = append(live, record)
			}
		}
		clear(e.logs[len(live):])
		e.logs = live
		t := e.totals
		o := Observation{TunnelID: id, Generation: decimal(e.generation), Requests: decimal(t.requests), Errors: decimal(t.errors), BytesIn: decimal(t.in), BytesOut: decimal(t.out), ActiveRequests: e.active, LatencySumSeconds: t.sum, Logs: make([]Log, 0, MaxLogs)}
		for i, v := range t.buckets {
			o.LatencyBuckets[i] = decimal(v)
		}
		for i := len(e.logs) - 1; i >= 0; i-- {
			if now.Sub(e.logs[i].Timestamp) < Retention {
				o.Logs = append(o.Logs, e.logs[i])
			}
		}
		result = append(result, o)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TunnelID < result[j].TunnelID })
	return result
}
func (m *Metrics) WritePrometheus(w io.Writer) {
	p := "portway_" + m.role + "_"
	metric := func(name, kind string, value any) {
		fmt.Fprintf(w, "# TYPE %s%s %s\n%s%s %v\n", p, name, kind, p, name, value)
	}
	metric("connections_total", "counter", m.connections.Load())
	metric("connections_active", "gauge", m.connectionsActive.Load())
	metric("connection_errors_total", "counter", m.connectionErrors.Load())
	metric("streams_total", "counter", m.streams.Load())
	metric("streams_active", "gauge", m.streamsActive.Load())
	metric("stream_errors_total", "counter", m.streamErrors.Load())
	metric("bytes_in_total", "counter", m.bytesIn.Load())
	metric("bytes_out_total", "counter", m.bytesOut.Load())
	if m.role == "relay" {
		metric("requests_active", "gauge", m.requestsActive.Load())
		metric("observation_drops_total", "counter", m.dropped.Load())
		m.mu.Lock()
		t := m.totals
		counts := m.counts
		m.mu.Unlock()
		metric("request_errors_total", "counter", t.errors)
		fmt.Fprintf(w, "# TYPE %srequests_total counter\n", p)
		for method, classes := range counts {
			for class, count := range classes {
				if count > 0 {
					fmt.Fprintf(w, "%srequests_total{method=%q,status_class=%q} %d\n", p, methods[method], strconv.Itoa(class)+"xx", count)
				}
			}
		}
		fmt.Fprintf(w, "# TYPE %srequest_duration_seconds histogram\n", p)
		for i, count := range t.buckets {
			bound := "+Inf"
			if i < len(Bounds) {
				bound = strconv.FormatFloat(Bounds[i], 'g', -1, 64)
			}
			fmt.Fprintf(w, "%srequest_duration_seconds_bucket{le=%q} %d\n", p, bound, count)
		}
		fmt.Fprintf(w, "%srequest_duration_seconds_sum %g\n%srequest_duration_seconds_count %d\n", p, t.sum, p, t.requests)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	metric("go_heap_bytes", "gauge", mem.HeapAlloc)
	metric("go_goroutines", "gauge", runtime.NumGoroutine())
	samples := []metrics.Sample{{Name: "/cpu/classes/user:cpu-seconds"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}, {Name: "/cpu/classes/scavenge/total:cpu-seconds"}}
	metrics.Read(samples)
	fmt.Fprintf(w, "# TYPE %sgo_cpu_class_seconds_total counter\n", p)
	for i, v := range samples {
		if v.Value.Kind() == metrics.KindFloat64 {
			fmt.Fprintf(w, "%sgo_cpu_class_seconds_total{class=%q} %g\n", p, []string{"user", "gc", "scavenge"}[i], v.Value.Float64())
		}
	}

}
