package devsetup

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicFixturesMigrateWithoutRotatingCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir, false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsurePublic(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePublic(dir, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "agent-token"))
	if string(before) != string(after) {
		t.Fatal("public setup rotated tunnel credential")
	}
	data, err := os.ReadFile(filepath.Join(dir, "public-cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("p-abc.portway.localhost"); err != nil {
		t.Fatal("public certificate lacks wildcard hostname")
	}
	if err := os.Remove(filepath.Join(dir, "public-key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePublic(dir, false); err == nil {
		t.Fatal("partial public fixtures silently replaced")
	}
}
