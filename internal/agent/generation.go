package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/radityama/portway/internal/protocol"
)

var ErrGeneration = errors.New("cannot reserve a tunnel generation; check local state or concurrent starts")

// NextGeneration reserves a number before network registration. Exclusive locks
// reject concurrent starts without hidden waiting. Corrupt state fails closed;
// missing state starts at one and must still pass the relay's generation check.
// State is local client metadata, never a credential or relay routing database.
func NextGeneration(ctx context.Context, dir, tunnelID string) (protocol.Generation, error) {
	return ReserveGeneration(ctx, dir, tunnelID, 0)
}

// ReserveGeneration persists an explicit recovery generation, or increments
// the counter when requested is zero. Overrides never roll local state back.
func ReserveGeneration(ctx context.Context, dir, tunnelID string, requested protocol.Generation) (protocol.Generation, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if dir == "" || !protocol.ValidTunnelID(tunnelID) {
		return 0, ErrGeneration
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, ErrGeneration
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return 0, ErrGeneration
	}
	hash := sha256.Sum256([]byte(tunnelID))
	path := filepath.Join(dir, hex.EncodeToString(hash[:])+".generation")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, ErrGeneration
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(path + ".lock")
		return 0, ErrGeneration
	}
	defer os.Remove(path + ".lock")
	var previous protocol.Generation
	info, err = os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > 21 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return 0, ErrGeneration
		}
		file, err := os.Open(path)
		if err != nil {
			return 0, ErrGeneration
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 22))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > 21 {
			return 0, ErrGeneration
		}
		previous, err = protocol.ParseGeneration(strings.TrimSuffix(string(data), "\n"))
		if err != nil {
			return 0, ErrGeneration
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, ErrGeneration
	}
	if previous == protocol.Generation(^uint64(0)) {
		return 0, ErrGeneration
	}
	next := previous + 1
	if requested != 0 {
		if requested <= previous {
			return 0, ErrGeneration
		}
		next = requested
	}
	file, err := os.CreateTemp(dir, ".generation-*")
	if err != nil {
		return 0, ErrGeneration
	}
	defer os.Remove(file.Name())
	if _, err := io.WriteString(file, next.String()+"\n"); err != nil {
		file.Close()
		return 0, ErrGeneration
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return 0, ErrGeneration
	}
	if err := file.Close(); err != nil {
		return 0, ErrGeneration
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return 0, ErrGeneration
	}
	return next, nil
}
