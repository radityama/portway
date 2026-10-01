package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/protocol"
)

// ControlVerifier calls policy only during AUTH. Active relay sessions use the
// resulting bounded lease locally, preserving operation during API outages.
type ControlVerifier struct {
	Client             *control.Client
	RelayID, TokenFile string
}

func (v *ControlVerifier) Verify(ctx context.Context, token string) (Identity, error) {
	if !protocol.ValidToken(token) {
		return Identity{}, ErrInvalid
	}
	bearer, err := ReadTokenFile(v.TokenFile)
	if err != nil {
		return Identity{}, ErrConfig
	}
	h := sha256.Sum256([]byte(token))
	lease, err := v.Client.Verify(ctx, v.RelayID, hex.EncodeToString(h[:]), bearer)
	if err != nil {
		return Identity{}, ErrInvalid
	}
	return Identity{TunnelID: lease.TunnelID, Generation: lease.Generation, ExpiresAt: lease.ExpiresAt}, nil
}
