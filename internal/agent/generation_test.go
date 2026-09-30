package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/radityama/portway/internal/protocol"
)

func statePath(dir, id string) string {
	hash := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(hash[:])+".generation")
}

func TestGenerationSurvivesRestartAndRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	for n := protocol.Generation(1); n <= 3; n++ {
		got, err := NextGeneration(context.Background(), dir, "tnl_fixture")
		if err != nil || got != n {
			t.Fatal("generation did not advance")
		}
	}
	if got, err := ReserveGeneration(context.Background(), dir, "tnl_fixture", 50); err != nil || got != 50 {
		t.Fatal("recovery override failed")
	}
	if got, err := NextGeneration(context.Background(), dir, "tnl_fixture"); err != nil || got != 51 {
		t.Fatal("override was not persisted")
	}
	if _, err := ReserveGeneration(context.Background(), dir, "tnl_fixture", 10); !errors.Is(err, ErrGeneration) {
		t.Fatal("override rolled state back")
	}
	if got, err := NextGeneration(context.Background(), dir, "tnl_other"); err != nil || got != 1 {
		t.Fatal("counter not scoped to tunnel")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NextGeneration(ctx, dir, "tnl_fixture"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func TestGenerationFailsClosed(t *testing.T) {
	for _, kind := range []string{"corrupt", "overflow", "oversized", "locked", "symlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			path := statePath(dir, "tnl_fixture")
			switch kind {
			case "corrupt":
				_ = os.WriteFile(path, []byte("01\n"), 0600)
			case "overflow":
				_ = os.WriteFile(path, []byte("18446744073709551615\n"), 0600)
			case "oversized":
				_ = os.WriteFile(path, make([]byte, 1024), 0600)
			case "locked":
				_ = os.WriteFile(path+".lock", nil, 0600)
			case "symlink":
				if runtime.GOOS == "windows" {
					t.Skip("POSIX symlink")
				}
				target := filepath.Join(dir, "other")
				_ = os.WriteFile(target, []byte("1\n"), 0600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if runtime.GOOS == "windows" {
					t.Skip("POSIX permissions")
				}
				_ = os.WriteFile(path, []byte("1\n"), 0644)
				_ = os.Chmod(path, 0644)
			}
			if _, err := NextGeneration(context.Background(), dir, "tnl_fixture"); !errors.Is(err, ErrGeneration) {
				t.Fatal("unsafe state silently reset")
			}
		})
	}
}
