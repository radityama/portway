package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

var (
	ErrInvalid = errors.New("invalid credential")
	ErrExpired = errors.New("expired credential")
	ErrRevoked = errors.New("revoked credential")
	ErrConfig  = errors.New("invalid credential file configuration")
)

const MaxCredentials = 1024

type Identity struct {
	TunnelID  string
	ExpiresAt time.Time
}

type Verifier interface {
	Verify(context.Context, string) (Identity, error)
}

// Record uses existing TunnelCredential meanings; no raw token is retained.
type Record struct {
	TunnelID  string     `json:"tunnelId"`
	TokenHash string     `json:"tokenHash"`
	Scope     string     `json:"scope"`
	ExpiresAt time.Time  `json:"expiresAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type storedRecord struct {
	record Record
	hash   [sha256.Size]byte
}

type FileVerifier struct {
	records []storedRecord
}

func NewVerifier(records []Record) (*FileVerifier, error) {
	if len(records) == 0 || len(records) > MaxCredentials {
		return nil, ErrConfig
	}
	verifier := &FileVerifier{}
	seen := make(map[[sha256.Size]byte]bool)
	for _, record := range records {
		digest, err := hex.DecodeString(record.TokenHash)
		if err != nil || len(digest) != sha256.Size || record.ExpiresAt.IsZero() || record.Scope != "connect" || !validTunnelID(record.TunnelID) {
			return nil, ErrConfig
		}
		var hash [sha256.Size]byte
		copy(hash[:], digest)
		if seen[hash] {
			return nil, ErrConfig
		}
		seen[hash] = true
		// Copy optional pointer data so caller mutation cannot change policy.
		if record.RevokedAt != nil {
			revoked := *record.RevokedAt
			record.RevokedAt = &revoked
		}
		verifier.records = append(verifier.records, storedRecord{record: record, hash: hash})
	}
	return verifier, nil
}

func (v *FileVerifier) Verify(ctx context.Context, token string) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if !protocol.ValidToken(token) {
		return Identity{}, ErrInvalid
	}
	digest := sha256.Sum256([]byte(token))
	match := -1
	for i, entry := range v.records {
		if subtle.ConstantTimeCompare(digest[:], entry.hash[:]) == 1 {
			match = i
		}
	}
	if match < 0 {
		return Identity{}, ErrInvalid
	}
	record := v.records[match].record
	if record.RevokedAt != nil {
		return Identity{}, ErrRevoked
	}
	if !time.Now().Before(record.ExpiresAt) {
		return Identity{}, ErrExpired
	}
	return Identity{TunnelID: record.TunnelID, ExpiresAt: record.ExpiresAt}, nil
}

func LoadVerifier(path string) (*FileVerifier, error) {
	file, err := openPrivateFile(path, 1024*1024)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []Record
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&records); err != nil {
		return nil, ErrConfig
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrConfig
	}
	return NewVerifier(records)
}

func ReadTokenFile(path string) (string, error) {
	file, err := openPrivateFile(path, protocol.MaxTokenSize+2)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxTokenSize+3))
	if err != nil {
		return "", ErrConfig
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if !protocol.ValidToken(token) {
		return "", ErrConfig
	}
	return token, nil
}

func openPrivateFile(path string, maxSize int64) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrConfig
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		_ = file.Close()
		return nil, ErrConfig
	}
	return file, nil
}

func validTunnelID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
