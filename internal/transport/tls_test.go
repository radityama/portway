package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInvalidTLSConfigurationFailsBeforeDial(t *testing.T) {
	for _, config := range []*tls.Config{nil, {MinVersion: tls.VersionTLS12}, {MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}, {MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS12}} {
		dialer := &TLSDialer{Config: config, Timeout: time.Second}
		if _, err := dialer.Dial(context.Background(), "127.0.0.1:1"); !errors.Is(err, ErrTLSConfig) {
			t.Fatal("unsafe TLS configuration dialed")
		}
	}
	if _, err := (&TLSDialer{Config: &tls.Config{MinVersion: tls.VersionTLS13}}).Dial(context.Background(), "127.0.0.1:1"); !errors.Is(err, ErrTLSConfig) {
		t.Fatal("unbounded dial timeout accepted")
	}
}
func TestInvalidTrustFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if _, err := ClientConfig(path, "localhost"); !errors.Is(err, ErrTLSConfig) {
		t.Fatal("missing CA file accepted")
	}
	for _, content := range [][]byte{[]byte("invalid PEM"), make([]byte, 1024*1024+1)} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ClientConfig(path, "localhost"); !errors.Is(err, ErrTLSConfig) {
			t.Fatal("invalid/big CA file accepted")
		}
	}
	if _, err := ServerConfig("missing", "missing"); !errors.Is(err, ErrTLSConfig) {
		t.Fatal("missing server certificate accepted")
	}
}
