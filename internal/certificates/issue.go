package certificates

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func privateDirectory(dir string) error {
	if os.MkdirAll(dir, 0700) != nil {
		return ErrCertificate
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return ErrCertificate
	}
	return nil
}
func lockDirectory(dir string) (func(), error) {
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".issue.lock")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, ErrCertificate
	}
	return func() { f.Close(); os.Remove(path) }, nil
}
func serial() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }
func writeAtomic(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".cert-")
	if err != nil {
		return ErrCertificate
	}
	defer os.Remove(f.Name())
	if f.Chmod(0600) != nil {
		f.Close()
		return ErrCertificate
	}
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return ErrCertificate
	}
	if f.Sync() != nil {
		f.Close()
		return ErrCertificate
	}
	if f.Close() != nil {
		return ErrCertificate
	}
	if os.Rename(f.Name(), path) != nil {
		return ErrCertificate
	}
	return nil
}
func ca(dir string, now time.Time) (*x509.Certificate, crypto.Signer, error) {
	certPEM, e := ReadFile(filepath.Join(dir, "ca.pem"), false)
	if e != nil {
		return nil, nil, e
	}
	keyPEM, e := ReadFile(filepath.Join(dir, "ca-key.pem"), true)
	if e != nil {
		return nil, nil, e
	}
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil || len(pair.Certificate) != 1 {
		return nil, nil, ErrCertificate
	}
	certificate, e := x509.ParseCertificate(pair.Certificate[0])
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if e != nil || !ok || !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, nil, ErrCertificate
	}
	return certificate, signer, nil
}

// InitCA is create-only. A partial/existing invalid root is never silently replaced.
func InitCA(dir string, now time.Time) error {
	unlock, err := lockDirectory(dir)
	if err != nil {
		return err
	}
	defer unlock()
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		if _, e := os.Lstat(filepath.Join(dir, name)); e == nil {
			_, _, err = ca(dir, now)
			return err
		} else if !os.IsNotExist(e) {
			return ErrCertificate
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ErrCertificate
	}
	id, err := serial()
	if err != nil {
		return ErrCertificate
	}
	cert := &x509.Certificate{SerialNumber: id, Subject: pkix.Name{CommonName: "Portway local development CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return ErrCertificate
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return ErrCertificate
	}
	if writeAtomic(filepath.Join(dir, "ca-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})) != nil {
		return ErrCertificate
	}
	return writeAtomic(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// Issue creates an immutable pair and atomically publishes a private pointer file.
// A valid pair outside its renewal window is reused; the CA remains stable.
func Issue(dir string, hosts []string, days int, renewBefore time.Duration, now time.Time) (Entry, error) {
	var result Entry
	if len(hosts) < 1 || len(hosts) > 32 || days < 1 || days > 90 || renewBefore < 0 || renewBefore >= time.Duration(days)*24*time.Hour {
		return result, ErrCertificate
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		h := strings.TrimPrefix(host, "*.")
		if !validName(h) || seen[host] || (strings.Contains(host, "*") && !strings.HasPrefix(host, "*.")) {
			return result, ErrCertificate
		}
		seen[host] = true
	}
	unlock, err := lockDirectory(dir)
	if err != nil {
		return result, err
	}
	defer unlock()
	root, key, err := ca(dir, now)
	if err != nil {
		return result, err
	}
	pointer := filepath.Join(dir, "certificate.json")
	if raw, e := ReadFile(pointer, true); e == nil {
		manifest, e := DecodeManifest(raw)
		if e != nil || manifest.Default == nil {
			return result, ErrCertificate
		}
		entry := *manifest.Default
		pair, e := Pair(entry.CertFile, entry.KeyFile, strings.Replace(hosts[0], "*.", "p-test.", 1), now)
		if e == nil && pair.Leaf.CheckSignatureFrom(root) == nil && pair.Leaf.NotAfter.Sub(now) > renewBefore && len(pair.Leaf.DNSNames) == len(hosts) {
			all := true
			for _, host := range hosts {
				all = all && seen[host]
				found := false
				for _, n := range pair.Leaf.DNSNames {
					found = found || n == host
				}
				all = all && found
			}
			if all {
				return entry, nil
			}
		}
	} else if _, statErr := os.Lstat(pointer); !os.IsNotExist(statErr) {
		return result, ErrCertificate
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return result, ErrCertificate
	}
	id, err := serial()
	if err != nil {
		return result, ErrCertificate
	}
	end := now.Add(time.Duration(days) * 24 * time.Hour)
	if end.After(root.NotAfter) {
		return result, ErrCertificate
	}
	leaf := &x509.Certificate{SerialNumber: id, Subject: pkix.Name{CommonName: hosts[0]}, DNSNames: hosts, NotBefore: now.Add(-time.Minute), NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, key)
	if err != nil {
		return result, ErrCertificate
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return result, ErrCertificate
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, ErrCertificate
	}
	bundle := filepath.Join(dir, "bundles", hex.EncodeToString(nonce[:]))
	if privateDirectory(bundle) != nil {
		return result, ErrCertificate
	}
	bundle, err = filepath.Abs(bundle)
	if err != nil {
		return result, ErrCertificate
	}
	result = Entry{Hostname: strings.TrimPrefix(hosts[0], "*."), CertFile: filepath.Join(bundle, "cert.pem"), KeyFile: filepath.Join(bundle, "key.pem")}
	if writeAtomic(result.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})) != nil || writeAtomic(result.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) != nil {
		return Entry{}, ErrCertificate
	}
	if _, err := Pair(result.CertFile, result.KeyFile, strings.Replace(hosts[0], "*.", "p-test.", 1), now); err != nil {
		return Entry{}, err
	}
	manifest := Manifest{Default: &result, Domains: []Entry{}}
	for _, host := range hosts {
		if !strings.Contains(host, "*") {
			entry := result
			entry.Hostname = host
			manifest.Domains = append(manifest.Domains, entry)
		}
	}
	raw, err := jsonManifest(manifest)
	if err != nil || writeAtomic(pointer, raw) != nil {
		return Entry{}, ErrCertificate
	}
	return result, nil
}

func jsonManifest(value Manifest) ([]byte, error) { return json.Marshal(value) }
