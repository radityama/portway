// Package devsetup creates local-only, ignored TLS/authentication fixtures.
package devsetup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/radityama/portway/internal/auth"
)

var ErrSetup = errors.New("development credentials are incomplete; regenerate with dev-init --force")
var filenames = []string{"ca.pem", "relay-cert.pem", "relay-key.pem", "agent-token", "relay-credentials.json"}

func Ensure(dir string, force bool) error { return ensureFiles(dir, force, filenames, generate) }
func EnsurePublic(dir string, force bool) error {
	return ensureFiles(dir, force, []string{"public-ca.pem", "public-cert.pem", "public-key.pem"}, func() (map[string][]byte, error) {
		files, err := generate()
		if err != nil {
			return nil, err
		}
		return map[string][]byte{"public-ca.pem": files["ca.pem"], "public-cert.pem": files["relay-cert.pem"], "public-key.pem": files["relay-key.pem"]}, nil
	})
}
func ensureFiles(dir string, force bool, names []string, generateFiles func() (map[string][]byte, error)) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ErrSetup
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return ErrSetup
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".init.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrSetup
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lock.Name()) }()
	existing := 0
	for _, name := range names {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil {
			if !info.Mode().IsRegular() {
				return ErrSetup
			}
			existing++
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrSetup
		}
	}
	if !force && existing == len(names) {
		return nil
	}
	if !force && existing > 0 {
		return ErrSetup
	}
	files, err := generateFiles()
	if err != nil {
		return ErrSetup
	}
	staging, err := os.MkdirTemp(dir, ".staging-")
	if err != nil {
		return ErrSetup
	}
	defer os.RemoveAll(staging)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(staging, name), files[name], 0600); err != nil {
			return ErrSetup
		}
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(dir, name)); err != nil {
			return ErrSetup
		}
	}
	return nil
}

func generate() (map[string][]byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	relayKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	relaySerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: caSerial, Subject: pkix.Name{CommonName: "Portway development CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	leaf := &x509.Certificate{SerialNumber: relaySerial, Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost", "*.portway.localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, NotBefore: now.Add(-5 * time.Minute), NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &relayKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(relayKey)
	if err != nil {
		return nil, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(token))
	records, err := json.MarshalIndent([]auth.Record{{TunnelID: "tnl_local_dev", TokenHash: hex.EncodeToString(hash[:]), Scope: "connect", ExpiresAt: now.Add(24 * time.Hour)}}, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"ca.pem":                 pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"relay-cert.pem":         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		"relay-key.pem":          pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		"agent-token":            []byte(token + "\n"),
		"relay-credentials.json": append(records, '\n'),
	}, nil
}
