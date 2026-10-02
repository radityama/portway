package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fixtureToken = "fixture_credential_only_for_tests"

func record() Record {
	digest := sha256.Sum256([]byte(fixtureToken))
	return Record{TunnelID: "tnl_fixture", TokenHash: hex.EncodeToString(digest[:]), Scope: "connect", ExpiresAt: time.Now().Add(time.Hour)}
}
func TestVerifierPolicyAndImmutableSnapshot(t *testing.T) {
	for _, kind := range []string{"valid", "invalid", "expired", "revoked", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			entry := record()
			if kind == "expired" {
				entry.ExpiresAt = time.Now().Add(-time.Hour)
			}
			if kind == "revoked" {
				now := time.Now()
				entry.RevokedAt = &now
			}
			verifier, err := NewVerifier([]Record{entry})
			if err != nil {
				t.Fatal(err)
			}
			// Copying the caller-owned record prevents policy mutation after construction.
			if entry.RevokedAt != nil {
				*entry.RevokedAt = time.Time{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			token := fixtureToken
			if kind == "invalid" {
				token = strings.Repeat("x", 32)
			}
			identity, err := verifier.Verify(ctx, token)
			want := map[string]error{"invalid": ErrInvalid, "expired": ErrExpired, "revoked": ErrRevoked, "cancelled": context.Canceled}[kind]
			if want == nil {
				if err != nil || identity.TunnelID != "tnl_fixture" {
					t.Fatalf("valid credential failed: %v", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
		})
	}
}
func TestInvalidCredentialConfiguration(t *testing.T) {
	for _, mutate := range []func(*Record){
		func(r *Record) { r.TokenHash = "invalid" }, func(r *Record) { r.Scope = "admin" }, func(r *Record) { r.ExpiresAt = time.Time{} }, func(r *Record) { r.TunnelID = "" }, func(r *Record) { r.TunnelID = "bad/id" },
	} {
		entry := record()
		mutate(&entry)
		if _, err := NewVerifier([]Record{entry}); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid record accepted")
		}
	}
	if _, err := NewVerifier(nil); !errors.Is(err, ErrConfig) {
		t.Fatal("empty credential store accepted")
	}
	if _, err := NewVerifier([]Record{record(), record()}); !errors.Is(err, ErrConfig) {
		t.Fatal("duplicate token hashes accepted")
	}
	if _, err := NewVerifier(make([]Record, MaxCredentials+1)); !errors.Is(err, ErrConfig) {
		t.Fatal("oversized store accepted")
	}
}
func TestPrivateBoundedCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	recordsPath := filepath.Join(dir, "records.json")
	if err := os.WriteFile(tokenPath, []byte(fixtureToken+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if token, err := ReadTokenFile(tokenPath); err != nil || token != fixtureToken {
		t.Fatal("valid private token file rejected")
	}
	data, err := json.Marshal([]Record{record()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordsPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	verifier, err := LoadVerifier(recordsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), fixtureToken); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", strings.Repeat("x", 513), "bad token", fixtureToken + "\n\n"} {
		if err := os.WriteFile(tokenPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadTokenFile(tokenPath); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid token file accepted")
		}
	}
	for _, content := range []string{"[]", string(data) + " {}", "[{}]", strings.Repeat("x", 1024*1024+1)} {
		if err := os.WriteFile(recordsPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadVerifier(recordsPath); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid verifier file accepted")
		}
	}
	if _, err := ReadTokenFile(dir); !errors.Is(err, ErrConfig) {
		t.Fatal("directory accepted as token file")
	}
	if _, err := ReadTokenFile(filepath.Join(dir, "missing")); !errors.Is(err, ErrConfig) {
		t.Fatal("missing token file accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tokenPath, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadTokenFile(tokenPath); !errors.Is(err, ErrConfig) {
			t.Fatal("publicly readable token file accepted")
		}
	}
}

func TestCredentialFilesRejectSymlinks(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	recordsPath := filepath.Join(dir, "records.json")
	if err := os.WriteFile(tokenPath, []byte(fixtureToken), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]Record{record()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordsPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name, target string
		load         func(string) error
	}{
		{"token", tokenPath, func(path string) error { _, err := ReadTokenFile(path); return err }},
		{"policy", recordsPath, func(path string) error { _, err := LoadVerifier(path); return err }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			link := filepath.Join(dir, entry.name+"-link")
			if err := os.Symlink(entry.target, link); err != nil {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation is unavailable")
				}
				t.Fatal(err)
			}
			if err := entry.load(link); !errors.Is(err, ErrConfig) {
				t.Fatal("symlink credential file was accepted")
			}
		})
	}
}
