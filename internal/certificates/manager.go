// Package certificates owns bounded local certificate deployment outside TLS callbacks.
package certificates

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/radityama/portway/internal/protocol"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

var ErrCertificate = errors.New("invalid certificate deployment")

const MaxDomains = 128
const MaxFile = 65536

type Entry struct {
	Hostname string `json:"hostname"`
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}
type Manifest struct {
	Default *Entry  `json:"default,omitempty"`
	Domains []Entry `json:"domains"`
}
type cache struct {
	primary *tls.Certificate
	domains map[string]*tls.Certificate
}
type Manager struct {
	CertFile, KeyFile, ManifestFile, BaseDomain string
	Allowed                                     func(string) bool
	Now                                         func() time.Time
	current                                     atomic.Pointer[cache]
}

func ReadFile(path string, private bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxFile || (private && runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, ErrCertificate
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrCertificate
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrCertificate
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil || len(b) > MaxFile {
		return nil, ErrCertificate
	}
	return b, nil
}
func Pair(certFile, keyFile, host string, now time.Time) (*tls.Certificate, error) {
	certPEM, err := ReadFile(certFile, false)
	if err != nil {
		return nil, err
	}
	keyPEM, err := ReadFile(keyFile, true)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 || len(pair.Certificate) > 8 {
		return nil, ErrCertificate
	}
	for _, der := range pair.Certificate {
		c, e := x509.ParseCertificate(der)
		if e != nil || now.Before(c.NotBefore) || !now.Before(c.NotAfter) {
			return nil, ErrCertificate
		}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || leaf.VerifyHostname(host) != nil {
		return nil, ErrCertificate
	}
	usage := len(leaf.ExtKeyUsage) == 0
	for _, u := range leaf.ExtKeyUsage {
		usage = usage || u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny
	}
	if !usage {
		return nil, ErrCertificate
	}
	pair.Leaf = leaf
	return &pair, nil
}
func validName(host string) bool {
	return protocol.ValidHostname(host) && len(host) <= 220 && strings.Contains(host, ".")
}
func DecodeManifest(raw []byte) (Manifest, error) {
	var value Manifest
	if len(raw) > MaxFile {
		return value, ErrCertificate
	}
	// Local manifests reject duplicate/case-alias keys before typed decoding.
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 4 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] || (key != "default" && key != "domains" && key != "hostname" && key != "certFile" && key != "keyFile") {
					return false
				}
				seen[key] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim(']')
		}
		return false
	}
	if !walk(0) {
		return value, ErrCertificate
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return value, ErrCertificate
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if typed.Decode(&value) != nil || value.Domains == nil || len(value.Domains) > MaxDomains {
		return value, ErrCertificate
	}
	seen := map[string]bool{}
	entries := value.Domains
	if value.Default != nil {
		e := *value.Default
		if !validName(e.Hostname) || e.CertFile == "" || e.KeyFile == "" || len(e.CertFile) > 4096 || len(e.KeyFile) > 4096 {
			return value, ErrCertificate
		}
	}
	for _, e := range entries {
		if !validName(e.Hostname) || seen[e.Hostname] || e.CertFile == "" || e.KeyFile == "" || len(e.CertFile) > 4096 || len(e.KeyFile) > 4096 {
			return value, ErrCertificate
		}
		seen[e.Hostname] = true
	}
	return value, nil
}
func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
func (m *Manager) Reload() error {
	if !protocol.ValidHostname(m.BaseDomain) {
		return ErrCertificate
	}
	manifest := Manifest{Domains: []Entry{}}
	if m.ManifestFile != "" {
		raw, e := ReadFile(m.ManifestFile, true)
		if e != nil {
			return e
		}
		manifest, e = DecodeManifest(raw)
		if e != nil {
			return e
		}
	}
	resolve := func(path string) string {
		if m.ManifestFile == "" || filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(filepath.Dir(m.ManifestFile), path)
	}
	certFile, keyFile := m.CertFile, m.KeyFile
	if manifest.Default != nil {
		certFile, keyFile = resolve(manifest.Default.CertFile), resolve(manifest.Default.KeyFile)
	}
	primary, err := Pair(certFile, keyFile, "p-test."+m.BaseDomain, m.now())
	if err != nil {
		return err
	}
	next := &cache{primary: primary, domains: map[string]*tls.Certificate{}}
	for _, entry := range manifest.Domains {
		if entry.Hostname == m.BaseDomain || strings.HasSuffix(entry.Hostname, "."+m.BaseDomain) {
			return ErrCertificate
		}
		pair, e := Pair(resolve(entry.CertFile), resolve(entry.KeyFile), entry.Hostname, m.now())
		if e != nil {
			return e
		}
		next.domains[entry.Hostname] = pair
	}
	m.current.Store(next)
	return nil
}
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	value := m.current.Load()
	if value == nil {
		return nil, ErrCertificate
	}
	host := strings.ToLower(hello.ServerName)
	cert := value.primary
	if host != "" && host != "localhost" && host != m.BaseDomain && !strings.HasSuffix(host, "."+m.BaseDomain) {
		if !validName(host) || m.Allowed == nil || !m.Allowed(host) {
			return nil, ErrCertificate
		}
		cert = value.domains[host]
	}
	if cert == nil || cert.Leaf == nil || m.now().Before(cert.Leaf.NotBefore) || !m.now().Before(cert.Leaf.NotAfter) || (host != "" && cert.Leaf.VerifyHostname(host) != nil) {
		return nil, ErrCertificate
	}
	return cert, nil
}
func (m *Manager) Config() (*tls.Config, error) {
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: m.GetCertificate, SessionTicketsDisabled: true}, nil
}
func (m *Manager) Run(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := m.Reload(); err != nil {
				logger.Warn("public_certificate_reload_failed")
			}
		}
	}
}
