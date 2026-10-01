package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

const testToken = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func assignment() Assignment {
	hash := sha256.Sum256([]byte("tnl_test"))
	now := time.Now().UTC()
	return Assignment{Relay: Relay{ID: "rel_test", Hostname: "localhost", Port: 8081, Protocol: "tls", Status: "HEALTHY"}, Credential: Credential{ID: "cred_test", TunnelID: "tnl_test", Scope: "connect", Token: testToken, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, Generation: 7, PublicHostname: "p-" + hex.EncodeToString(hash[:16]) + ".portway.localhost"}
}
func response(w http.ResponseWriter, a any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": a, "error": nil, "meta": map[string]any{}})
}
func TestConnectAndVerifyBindings(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("invalid authenticated request")
		}
		if r.URL.Path == "/api/v1/tunnels/tnl_test/connect" {
			var body struct {
				Minimum string `json:"minimumGeneration"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Minimum != "7" {
				t.Error("generation precision lost")
			}
			response(w, assignment())
		} else if r.URL.Path == "/api/v1/internal/credentials/verify" {
			var body struct{ RelayID, TokenHash string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.RelayID != "rel_test" || len(body.TokenHash) != 64 {
				t.Error("verification sent invalid scope")
			}
			response(w, Identity{TunnelID: "tnl_test", Generation: 7, ExpiresAt: time.Now().Add(time.Minute)})
		} else {
			t.Error("unexpected path")
		}
	}))
	defer s.Close()
	c, err := NewClient(s.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := c.Connect(context.Background(), "tnl_test", 7, testToken)
	if err != nil || a.Generation != 7 {
		t.Fatal(err)
	}
	i, err := c.Verify(context.Background(), "rel_test", strings.Repeat("a", 64), testToken)
	if err != nil || i.Generation != 7 {
		t.Fatal(err)
	}
}
func TestConfigurationRejectsInsecureRemoteAndURLInjection(t *testing.T) {
	for _, base := range []string{"http://example.test/api/v1", "http://127.0.0.1/api/v1?x=y", "https://user:secret@example.test/api/v1", "https://example.test/api/v1#fragment", "https://example.test/api/v1/../other", "https://example.test/api%2fv1", "https://example.test/api/v1?", "ftp://localhost/api/v1", "https://example.test:0/api/v1"} {
		if c, e := NewClient(base, "", time.Second); e == nil {
			c.Close()
			t.Fatalf("accepted invalid base: %s", base)
		}
	}
}
func TestUntrustedAssignmentsFailClosed(t *testing.T) {
	cases := map[string]func(*Assignment){
		"wrong tunnel": func(a *Assignment) { a.Credential.TunnelID = "tnl_other" }, "stale generation": func(a *Assignment) { a.Generation = 6 }, "untrusted hostname": func(a *Assignment) { a.PublicHostname = "other.example.test" }, "bad relay host": func(a *Assignment) { a.Relay.Hostname = "user@host" }, "plaintext relay": func(a *Assignment) { a.Relay.Protocol = "tcp" }, "draining relay": func(a *Assignment) { a.Relay.Status = "DRAINING" }, "expired": func(a *Assignment) { a.Credential.ExpiresAt = time.Now().Add(-time.Second) }, "long lease": func(a *Assignment) { a.Credential.ExpiresAt = a.Credential.IssuedAt.Add(time.Hour) }, "bad token": func(a *Assignment) { a.Credential.Token = "short" }, "zero port": func(a *Assignment) { a.Relay.Port = 0 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { a := assignment(); change(&a); response(w, a) }))
			defer s.Close()
			c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
			defer c.Close()
			_, e := c.Connect(context.Background(), "tnl_test", 7, testToken)
			if e == nil || Retryable(e) {
				t.Fatal("invalid assignment retried or accepted")
			}
		})
	}
}
func TestBoundedMalformedResponses(t *testing.T) {
	for _, raw := range []string{`{"data":{},"error":null,"meta":{},"data":{}}`, `{"data":{},"error":null,"meta":{},"extra":1}`, `{"Data":{},"error":null,"meta":{}}`, `{"data":{},"error":null,"meta":"bad"}`, `{"data":{},"error":null,"meta":{}}{}`, strings.Repeat(" ", MaxResponse+1), string([]byte{0xff}), `{"data":{"generation":7},"error":null,"meta":{}}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(raw))
		}))
		c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
		_, e := c.Connect(context.Background(), "tnl_test", 7, testToken)
		c.Close()
		s.Close()
		if e == nil || Retryable(e) {
			t.Fatal("malformed response accepted or retried")
		}
	}
}
func TestFailuresCancellationAndRedirectDoNotExposeBearer(t *testing.T) {
	for _, status := range []int{401, 403, 409, 429, 503, 408, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			hits := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(testToken))
			}))
			defer s.Close()
			c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
			defer c.Close()
			_, e := c.Connect(context.Background(), "tnl_test", 7, testToken)
			if e == nil || strings.Contains(e.Error(), testToken) || hits != 1 || Retryable(e) != (status == 429 || status >= 500 || status == 408) {
				t.Fatal("incorrect safe error or redirect followed")
			}
		})
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer s.Close()
	c, _ := NewClient(s.URL+"/api/v1", "", 50*time.Millisecond)
	defer c.Close()
	_, e := c.Connect(context.Background(), "tnl_test", 7, testToken)
	if !Retryable(e) {
		t.Fatal("timeout was terminal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = c.Connect(ctx, "tnl_test", 7, testToken)
	if !errors.Is(e, context.Canceled) || Retryable(e) {
		t.Fatal("cancellation was retried")
	}
}
func TestHTTPSRequiresVerifiedTrust(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { response(w, assignment()) }))
	s.EnableHTTP2 = false
	s.StartTLS()
	defer s.Close()
	c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
	_, e := c.Connect(context.Background(), "tnl_test", 7, testToken)
	c.Close()
	if e == nil || Retryable(e) {
		t.Fatal("untrusted certificate accepted or retried")
	}
	// Trust exactly the fixture certificate; native verification still checks IP.
	cert := s.Certificate()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	c, e = NewClient(s.URL+"/api/v1", path, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e = c.Connect(context.Background(), "tnl_test", protocol.Generation(7), testToken); e != nil {
		t.Fatal(e)
	}
}

func FuzzControlJSON(f *testing.F) {
	for _, seed := range []string{`{"data":{},"error":null,"meta":{}}`, `{"a":1,"a":2}`, `{"a":1,"\\u0061":2}`, `[[[[null]]]]`, `{"x":"text"}`, `{}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > MaxResponse {
			return
		}
		if uniqueJSON(raw) && !json.Valid(raw) {
			t.Fatal("invalid JSON accepted")
		}
	})
}
