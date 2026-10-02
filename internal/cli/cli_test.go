package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radityama/portway/internal/control"
)

func emptyEnv(string) (string, bool) { return "", false }
func TestSavedSessionDestinationExpiryAndPrivateState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	store, err := NewStore(emptyEnv, dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, exists, err := store.Settings()
	if err != nil || exists {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 43)
	if err = store.SaveSession(settings, control.Session{AccessToken: token, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "session.json", "session.token"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe %s", name)
		}
	}
	settings, exists, err = store.Settings()
	if err != nil || !exists {
		t.Fatal(err)
	}
	lookup, err := store.Environment(emptyEnv, settings, exists, nil)
	if err != nil {
		t.Fatal(err)
	}
	if file, _ := lookup("PORTWAY_API_TOKEN_FILE"); file != store.TokenPath() {
		t.Fatal("no cached credential")
	}
	if _, err = store.Environment(emptyEnv, settings, true, map[string]string{"PORTWAY_API_URL": "http://127.0.0.1:9999/api/v1"}); !errors.Is(err, ErrSession) {
		t.Fatal("forwarded cached session to another API", err)
	}
	changed := settings
	changed.APIURL = "http://127.0.0.1:9999/api/v1"
	if !errors.Is(store.Save(changed), ErrSession) {
		t.Fatal("changed bound server")
	}
	info, _, _ := store.Session()
	info.ExpiresAt = time.Now().Add(-time.Second)
	b, _ := json.Marshal(info)
	if err = WritePrivate(filepath.Join(dir, "session.json"), b); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Environment(emptyEnv, settings, true, nil); !errors.Is(err, ErrSession) {
		t.Fatal("accepted expired session", err)
	}
	if info, value, err := store.ClearSession(); err != nil || info == nil || value != token {
		t.Fatal("could not clear expired session", err)
	}
	if _, err = os.Stat(store.TokenPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("token survived logout")
	}
	if err = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"version":1,"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Settings(); !errors.Is(err, ErrState) {
		t.Fatal("accepted duplicate config", err)
	}
}
func TestPrivateSymlinkPermissionsBoundsAndLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(path, 100); !errors.Is(err, ErrState) {
		t.Fatal("accepted public file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(path, 3); !errors.Is(err, ErrState) {
		t.Fatal("accepted oversized file")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(link, []byte("new")); !errors.Is(err, ErrState) {
		t.Fatal("replaced symlink")
	}
	if err := WithLock(dir, "test", func() error {
		if !errors.Is(WithLock(dir, "test", func() error { return nil }), ErrBusy) {
			t.Fatal("concurrent mutation allowed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(dir, "test", func() error { return nil }); err != nil {
		t.Fatal("lock leaked")
	}
}
func listener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { l.Close(); <-done })
	return l.Addr().(*net.TCPAddr).Port
}
func TestDiscoveryManagersPrecedenceAmbiguityAndCancellation(t *testing.T) {
	first, second := listener(t), listener(t)
	for _, manager := range []string{"npm", "pnpm", "yarn", "bun"} {
		t.Run(manager, func(t *testing.T) {
			dir := t.TempDir()
			b := fmt.Sprintf(`{"packageManager":"%s@1","scripts":{"dev":"vite --port %d"},"devDependencies":{"vite":"1"}}`, manager, first)
			os.WriteFile(filepath.Join(dir, "package.json"), []byte(b), 0600)
			result, err := Detect(context.Background(), emptyEnv, dir)
			if err != nil || result.Port != first || result.PackageManager != manager || result.Source != "dev_script" {
				t.Fatal(result, err)
			}
			env := func(key string) (string, bool) {
				if key == "PORTWAY_LOCAL_PORT" {
					return strconv.Itoa(second), true
				}
				return "", false
			}
			result, err = Detect(context.Background(), env, dir)
			if err != nil || result.Port != second || result.Source != "configuration" {
				t.Fatal(result, err)
			}
		})
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "dev.log")
	os.WriteFile(log, []byte(fmt.Sprintf("https://evil.invalid:1234\nhttp://localhost:%d/\nhttp://127.0.0.1:%d/\n", first, second)), 0600)
	env := func(key string) (string, bool) {
		if key == "PORTWAY_DEV_LOG_FILE" {
			return log, true
		}
		return "", false
	}
	if _, err := Detect(context.Background(), env, dir); !errors.Is(err, ErrAmbiguous) {
		t.Fatal("chose an ambiguous service", err)
	}
	os.WriteFile(log, []byte(fmt.Sprintf("http://localhost:%d/\n", first)), 0600)
	result, err := Detect(context.Background(), env, dir)
	if err != nil || result.Port != first || result.Source != "dev_output" {
		t.Fatal(result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Detect(ctx, env, dir); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored canceled discovery", err)
	}
	if err = os.Symlink(log, filepath.Join(dir, "package.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = Detect(context.Background(), env, dir); err == nil {
		t.Fatal("followed project symlink")
	}
}
func TestRuntimeOwnershipStopStaleAndJoinedShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := StartRuntime(ctx, dir, "tnl_test", 3000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Update("ready", "https://test.portway.localhost")
	status, err := LocalStatus(ctx, dir, "tnl_test")
	if err != nil || status.State != "ready" {
		t.Fatal(status, err)
	}
	if _, err = StartRuntime(ctx, dir, "tnl_test", 3000, func() {}); !errors.Is(err, ErrRunning) {
		t.Fatal("stole live ownership", err)
	}
	record, err := runtimeRecord(dir, "tnl_test")
	if err != nil {
		t.Fatal(err)
	}
	wrong := record
	wrong.Secret = strings.Repeat("a", 64)
	if _, err = runtimeCall(ctx, wrong, "POST", "/stop"); !errors.Is(err, ErrRuntime) || ctx.Err() != nil {
		t.Fatal("unauthorized stop", err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 5 {
				r.Update("ready", "")
				_, _ = LocalStatus(context.Background(), dir, "tnl_test")
			}
		}()
	}
	workers.Wait()
	if err = StopLocal(context.Background(), dir, "tnl_test"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("stop did not cancel lifecycle")
	}
	r.Update("ready", "")
	status, err = LocalStatus(context.Background(), dir, "tnl_test")
	if err != nil || status.State != "draining" {
		t.Fatal("revived draining agent", status, err)
	}
	r.Close()
	if _, err = runtimeRecord(dir, "tnl_test"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime leaked", err)
	}
	// Reclaim a refused endpoint, but refuse a live unrelated listener.
	b, _ := json.Marshal(record)
	WritePrivate(runtimePath(dir, "tnl_test"), b)
	r, err = StartRuntime(context.Background(), dir, "tnl_test", 3000, func() {})
	if err != nil {
		t.Fatal("refused stale endpoint not reclaimed", err)
	}
	r.Close()
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) })}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); server.Serve(l) }()
	defer func() { server.Close(); <-done }()
	record.Port = l.Addr().(*net.TCPAddr).Port
	b, _ = json.Marshal(record)
	WritePrivate(runtimePath(dir, "tnl_test"), b)
	if _, err = StartRuntime(context.Background(), dir, "tnl_test", 3000, func() {}); !errors.Is(err, ErrRuntime) {
		t.Fatal("reclaimed unrelated endpoint", err)
	}
}
func TestRuntimeSlowPeerAndAdmissionRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	r, err := StartRuntime(context.Background(), dir, "tnl_slow", 3000, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	record, _ := runtimeRecord(dir, "tnl_slow")
	var peers []net.Conn
	for range 8 {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(record.Port)), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, c)
		fmt.Fprint(c, "GET /status HTTP/1.1\r\n")
	}
	defer func() {
		for _, c := range peers {
			c.Close()
		}
	}()
	for _, c := range peers {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [4096]byte
		c.Read(b[:])
	}
	if _, err = LocalStatus(context.Background(), dir, "tnl_slow"); err != nil {
		t.Fatal("slow peers retained capacity", err)
	}
}
