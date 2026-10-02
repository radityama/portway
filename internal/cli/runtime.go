package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/protocol"
)

var ErrRunning = errors.New("a local agent already owns this tunnel; stop it before starting another")
var ErrRuntime = errors.New("cannot verify local agent ownership; check private runtime state")

type RuntimeRecord struct {
	TunnelID string `json:"tunnel_id"`
	Instance string `json:"instance"`
	Port     int    `json:"port"`
	Secret   string `json:"secret"`
}
type RuntimeStatus struct {
	TunnelID  string `json:"tunnel_id"`
	Instance  string `json:"instance"`
	LocalPort int    `json:"local_port"`
	State     string `json:"state"`
	PublicURL string `json:"public_url,omitempty"`
}

func runtimePath(dir, id string) string {
	hash := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(hash[:])+".runtime.json")
}
func runtimeRecord(dir, id string) (RuntimeRecord, error) {
	var record RuntimeRecord
	if !protocol.ValidTunnelID(id) {
		return record, ErrRuntime
	}
	if err := privateDirectory(dir, false); err != nil {
		return record, err
	}
	raw, err := ReadPrivate(runtimePath(dir, id), 4096)
	if err != nil {
		return record, err
	}
	if control.DecodeDocument(raw, &record) != nil || record.TunnelID != id || record.Port < 1 || record.Port > 65535 || len(record.Instance) != 32 || !protocol.ValidToken(record.Secret) {
		return RuntimeRecord{}, ErrRuntime
	}
	if decoded, err := hex.DecodeString(record.Instance); err != nil || len(decoded) != 16 || record.Instance != strings.ToLower(record.Instance) {
		return RuntimeRecord{}, ErrRuntime
	}
	return record, nil
}
func runtimeCall(ctx context.Context, record RuntimeRecord, method, path string) (RuntimeStatus, error) {
	var output RuntimeStatus
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, MaxConnsPerHost: 1, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 4096}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	url := "http://127.0.0.1:" + strconv.Itoa(record.Port) + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return output, ErrRuntime
	}
	req.Header.Set("Authorization", "Bearer "+record.Secret)
	response, err := client.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return output, os.ErrNotExist
		}
		return output, ErrRuntime
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || response.StatusCode != 200 || len(raw) > 4096 || control.DecodeDocument(raw, &output) != nil || output.TunnelID != record.TunnelID || output.Instance != record.Instance || output.LocalPort < 1 || output.LocalPort > 65535 || !oneRuntimeState(output.State) {
		return RuntimeStatus{}, ErrRuntime
	}
	return output, nil
}
func oneRuntimeState(state string) bool {
	return state == "starting" || state == "ready" || state == "reconnecting" || state == "draining"
}
func LocalStatus(ctx context.Context, dir, id string) (RuntimeStatus, error) {
	record, err := runtimeRecord(dir, id)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return runtimeCall(ctx, record, "GET", "/status")
}
func StopLocal(ctx context.Context, dir, id string) error {
	record, err := runtimeRecord(dir, id)
	if err != nil {
		return err
	}
	if _, err = runtimeCall(ctx, record, "GET", "/status"); err != nil {
		return err
	}
	_, err = runtimeCall(ctx, record, "POST", "/stop")
	return err
}

type Runtime struct {
	mu     sync.Mutex
	status RuntimeStatus
	record RuntimeRecord
	close  func()
}

func (r *Runtime) Update(state, publicURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.State == "draining" && state != "draining" {
		return
	}
	r.status.State = state
	r.status.PublicURL = publicURL
}
func (r *Runtime) Close() { r.close() }
func StartRuntime(ctx context.Context, dir, id string, localPort int, cancel context.CancelFunc) (*Runtime, error) {
	if !protocol.ValidTunnelID(id) || localPort < 1 || localPort > 65535 {
		return nil, ErrRuntime
	}
	var result *Runtime
	err := WithLock(dir, "runtime-"+filepath.Base(runtimePath(dir, id)), func() error {
		if old, err := runtimeRecord(dir, id); err == nil {
			if _, err = runtimeCall(ctx, old, "GET", "/status"); err == nil {
				return ErrRunning
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return ErrRuntime
		}
		var nonce [48]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			listener.Close()
			return ErrRuntime
		}
		record := RuntimeRecord{id, hex.EncodeToString(nonce[:16]), listener.Addr().(*net.TCPAddr).Port, hex.EncodeToString(nonce[16:])}
		r := &Runtime{status: RuntimeStatus{TunnelID: id, Instance: record.Instance, LocalPort: localPort, State: "starting"}, record: record}
		server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096, ErrorLog: log.New(io.Discard, "", 0)}
		var handlers sync.WaitGroup
		var admission sync.Mutex
		closing := false
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			admission.Lock()
			if closing {
				admission.Unlock()
				w.WriteHeader(503)
				return
			}
			handlers.Add(1)
			admission.Unlock()
			defer handlers.Done()
			w.Header().Set("Cache-Control", "no-store")
			if req.Host != "127.0.0.1:"+strconv.Itoa(record.Port) || req.URL.RawQuery != "" || req.Header.Get("Origin") != "" || len(req.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+record.Secret)) != 1 {
				w.WriteHeader(403)
				return
			}
			if req.ContentLength != 0 || len(req.TransferEncoding) != 0 {
				w.WriteHeader(400)
				return
			}
			if !(req.Method == "GET" && req.URL.Path == "/status" || req.Method == "POST" && req.URL.Path == "/stop") {
				w.WriteHeader(404)
				return
			}
			r.mu.Lock()
			if req.Method == "POST" {
				r.status.State = "draining"
			}
			status := r.status
			r.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(status)
			if req.Method == "POST" {
				cancel()
			}
		})
		b, _ := json.Marshal(record)
		if err = WritePrivate(runtimePath(dir, id), append(b, '\n')); err != nil {
			listener.Close()
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = server.Serve(&runtimeListener{Listener: listener, slots: make(chan struct{}, 8)})
		}()
		var once sync.Once
		r.close = func() {
			once.Do(func() {
				admission.Lock()
				closing = true
				admission.Unlock()
				shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
				_ = server.Shutdown(shutdown)
				cancelShutdown()
				_ = server.Close()
				<-done
				handlers.Wait()
				_ = WithLock(dir, "runtime-"+filepath.Base(runtimePath(dir, id)), func() error {
					current, err := runtimeRecord(dir, id)
					if err == nil && current.Instance == record.Instance && current.Secret == record.Secret {
						return os.Remove(runtimePath(dir, id))
					}
					return nil
				})
			})
		}
		result = r
		return nil
	})
	return result, err
}

type runtimeListener struct {
	net.Listener
	slots chan struct{}
}

func (l *runtimeListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &runtimeConn{Conn: conn, slots: l.slots}, nil
		default:
			conn.Close()
		}
	}
}

type runtimeConn struct {
	net.Conn
	slots chan struct{}
	once  sync.Once
}

func (c *runtimeConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { <-c.slots })
	return err
}
