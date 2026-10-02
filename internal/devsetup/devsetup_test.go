package devsetup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/transport"
)

func TestDevelopmentCredentialsArePrivateAndIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := Ensure(dir, false); err != nil {
		t.Fatal(err)
	}
	first, err := auth.ReadTokenFile(filepath.Join(dir, "agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.LoadVerifier(filepath.Join(dir, "relay-credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	records, err := os.ReadFile(filepath.Join(dir, "relay-credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(records, []byte(first)) {
		t.Fatal("relay credential file stores a raw token")
	}
	for _, name := range filenames {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("generated file permissions are not private")
		}
	}
	if _, err := transport.ServerConfig(filepath.Join(dir, "relay-cert.pem"), filepath.Join(dir, "relay-key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dir, false); err != nil {
		t.Fatal(err)
	}
	second, err := auth.ReadTokenFile(filepath.Join(dir, "agent-token"))
	if err != nil || second != first {
		t.Fatal("setup replaced an existing credential")
	}
	if err := Ensure(dir, true); err != nil {
		t.Fatal(err)
	}
	rotated, err := auth.ReadTokenFile(filepath.Join(dir, "agent-token"))
	if err != nil || rotated == first {
		t.Fatal("explicit rotation did not replace credential")
	}
}
func TestPartialSetupRequiresExplicitRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-token"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dir, false); err == nil {
		t.Fatal("partial setup silently replaced files")
	}
	token, err := os.ReadFile(filepath.Join(dir, "agent-token"))
	if err != nil || string(token) != "preserve" {
		t.Fatal("partial setup changed existing file")
	}
	if err := Ensure(dir, true); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeExistingDirectoryIsRejectedWithoutChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission boundary")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []func(string, bool) error{Ensure, EnsurePublic} {
		for _, force := range []bool{false, true} {
			if err := ensure(dir, force); err != ErrSetup {
				t.Fatal("unsafe existing directory was accepted", err)
			}
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("existing directory permissions were changed", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve" {
		t.Fatal("existing credential was changed", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "agent-token" {
		t.Fatal("unsafe setup created files", err)
	}
}
