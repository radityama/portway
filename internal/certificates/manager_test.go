package certificates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStableRootRenewalAndAtomicReload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	now := time.Now()
	hosts := []string{"*.portway.localhost", "app.example.test"}
	if err := InitCA(dir, now); err != nil {
		t.Fatal(err)
	}
	root, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	first, err := Issue(dir, hosts, 2, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Issue(dir, hosts, 2, 24*time.Hour, now)
	if err != nil || again != first {
		t.Fatal("valid pair was rotated", err)
	}
	allowed := false
	m := &Manager{CertFile: first.CertFile, KeyFile: first.KeyFile, ManifestFile: filepath.Join(dir, "certificate.json"), BaseDomain: "portway.localhost", Allowed: func(string) bool { return allowed }, Now: func() time.Time { return now }}
	config, err := m.Config()
	if err != nil {
		t.Fatal(err)
	}
	primary, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: "p-test.portway.localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.test"}); err == nil {
		t.Fatal("certificate alone authorized alias")
	}
	allowed = true
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.test"}); err == nil {
		t.Fatal("unknown SNI allowed")
	}
	now = now.Add(30 * time.Hour)
	renewed, err := Issue(dir, hosts, 2, 24*time.Hour, now)
	if err != nil || renewed == first {
		t.Fatal("renewal did not rotate leaf", err)
	}
	if err = m.Reload(); err != nil {
		t.Fatal(err)
	}
	current, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.test"})
	if err != nil || current.Leaf.SerialNumber.Cmp(primary.Leaf.SerialNumber) == 0 {
		t.Fatal("renewal was not loaded", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(root)
	if _, err = current.Leaf.Verify(x509.VerifyOptions{DNSName: "app.example.test", Roots: roots, CurrentTime: now}); err != nil {
		t.Fatal("renewal changed root trust", err)
	}
	if err := InitCA(dir, now); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if string(after) != string(root) {
		t.Fatal("root rotated")
	}
	if err = os.Chmod(renewed.KeyFile, 0644); err != nil {
		t.Fatal(err)
	}
	if m.Reload() == nil {
		t.Fatal("public private key accepted")
	}
	saved, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.test"})
	if err != nil || saved != current {
		t.Fatal("failed reload replaced good pair")
	}
	now = now.Add(3 * 24 * time.Hour)
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.test"}); err == nil {
		t.Fatal("expired leaf served")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload worker leaked")
	}
}
func TestManifestAndPartialRootFailClosed(t *testing.T) {
	for _, raw := range []string{`{}`, `{"domains":null}`, `{"domains":[],"domains":[]}`, `{"Domains":[]}`, `{"domains":[],"unexpected":true}`, `{"domains":[{"hostname":"app.test","certFile":"a","keyFile":"b"},{"hostname":"app.test","certFile":"c","keyFile":"d"}]}`, `{"domains":[{"hostname":"*.test","certFile":"a","keyFile":"b"}]}`} {
		if _, err := DecodeManifest([]byte(raw)); err == nil {
			t.Fatalf("unsafe manifest accepted %s", raw)
		}
	}
	dir := filepath.Join(t.TempDir(), "ca")
	if err := privateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if InitCA(dir, time.Now()) == nil {
		t.Fatal("partial root replaced")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if string(raw) != "preserve" {
		t.Fatal("partial root overwritten")
	}
}
