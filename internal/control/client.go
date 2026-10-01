// Package control implements bounded, verified control-plane calls. It never
// carries application traffic or retries a request internally.
package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

const MaxResponse = 64 * 1024

var ErrConfiguration = errors.New("invalid control-plane configuration")

type Error struct {
	Code      string
	Temporary bool
}

func (e *Error) Error() string { return "control-plane request failed: " + e.Code }
func Retryable(err error) bool { var e *Error; return errors.As(err, &e) && e.Temporary }
func Expired() error           { return &Error{Code: "CREDENTIAL_EXPIRED", Temporary: true} }
func InvalidAssignment() error { return &Error{Code: "INVALID_ASSIGNMENT"} }

type Client struct {
	base *url.URL
	http *http.Client
}

func NewClient(base, caFile string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.ForceQuery || strings.TrimSuffix(u.Path, "/") != "/api/v1" || u.Hostname() == "" || timeout <= 0 || timeout > 30*time.Second {
		return nil, ErrConfiguration
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, ErrConfiguration
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return nil, ErrConfiguration
		}
	}
	trust, err := transport.ClientConfig(caFile, u.Hostname())
	if err != nil {
		return nil, ErrConfiguration
	}
	trust.NextProtos = nil
	t := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: trust, DialContext: (&net.Dialer{Timeout: timeout}).DialContext, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 32 * 1024, MaxConnsPerHost: 32, MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second, DisableCompression: true}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return &Client{base: u, http: &http.Client{Transport: t, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) post(ctx context.Context, path, token string, input, output any) error {
	if !protocol.ValidToken(token) {
		return &Error{Code: "AUTH_INVALID"}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return InvalidAssignment()
	}
	u := *c.base
	u.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return ErrConfiguration
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var certificate *tls.CertificateVerificationError
		var record tls.RecordHeaderError
		if errors.As(err, &certificate) || errors.As(err, &record) {
			return &Error{Code: "TLS_INVALID"}
		}
		return &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 || response.StatusCode == 429 || response.StatusCode == 408 {
		return &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	if response.StatusCode != 200 {
		// Only this bounded internal error permits re-registration after Redis loss.
		if response.StatusCode == 409 {
			raw, readError := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
			var envelope struct {
				Data  json.RawMessage `json:"data"`
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				Meta json.RawMessage `json:"meta"`
			}
			if readError == nil && len(raw) <= MaxResponse && uniqueJSON(raw) && decode(raw, &envelope) == nil && envelope.Error.Code == "PRESENCE_EXPIRED" {
				return &Error{Code: "PRESENCE_EXPIRED", Temporary: true}
			}
		}
		return &Error{Code: "CONTROL_REJECTED"}
	}
	if strings.Split(response.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return InvalidAssignment()
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	if len(raw) > MaxResponse || !utf8.Valid(raw) || !uniqueJSON(raw) {
		return InvalidAssignment()
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
		Meta  json.RawMessage `json:"meta"`
	}
	if decode(raw, &envelope) != nil || string(envelope.Error) != "null" || len(envelope.Data) == 0 || string(envelope.Data) == "null" || len(envelope.Meta) == 0 {
		return InvalidAssignment()
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(envelope.Meta, &metadata) != nil || metadata == nil {
		return InvalidAssignment()
	}
	if decode(envelope.Data, output) != nil {
		return InvalidAssignment()
	}
	return nil
}

// encoding/json otherwise accepts case-insensitive aliases for tagged fields.
func exactKeys(raw []byte, shape reflect.Type) bool {
	if shape.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return true
		}
		shape = shape.Elem()
	}
	if shape.Kind() == reflect.Slice && !reflect.PointerTo(shape).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !exactKeys(value, shape.Elem()) {
				return false
			}
		}
		return true
	}
	if shape.Kind() != reflect.Struct || reflect.PointerTo(shape).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return true
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return false
	}
	names := make(map[string]reflect.Type, shape.NumField())
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			names[name] = field.Type
		}
	}
	for name, value := range values {
		kind, ok := names[name]
		if !ok || !exactKeys(value, kind) {
			return false
		}
	}
	return true
}
func decode(raw []byte, out any) error {
	if !exactKeys(raw, reflect.TypeOf(out)) {
		return InvalidAssignment()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return e
	}
	if e := d.Decode(new(any)); !errors.Is(e, io.EOF) {
		return InvalidAssignment()
	}
	return nil
}

// Reject duplicate keys (including escaped aliases) and excessive nesting before
// decoding typed envelopes. The response byte cap also bounds key allocations.
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 16 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		delimiter, ok := t.(json.Delim)
		if !ok {
			return true
		}
		if delimiter == '{' {
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !walk(depth + 1) {
					return false
				}
			}
			t, e = d.Token()
			return e == nil && t == json.Delim('}')
		}
		if delimiter == '[' {
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			t, e = d.Token()
			return e == nil && t == json.Delim(']')
		}
		return false
	}
	if !walk(0) {
		return false
	}
	_, e := d.Token()
	return errors.Is(e, io.EOF)
}

type Relay struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Region     string     `json:"region"`
	Hostname   string     `json:"hostname"`
	Port       int        `json:"port"`
	Protocol   string     `json:"protocol"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	Capacity   *Capacity  `json:"capacity"`
}
type Credential struct {
	ID        string    `json:"id"`
	TunnelID  string    `json:"tunnelId"`
	Scope     string    `json:"scope"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Token     string    `json:"token"`
}
type Assignment struct {
	Relay          Relay               `json:"relay"`
	Credential     Credential          `json:"credential"`
	Generation     protocol.Generation `json:"generation"`
	PublicHostname string              `json:"publicHostname"`
}

func (c *Client) Connect(ctx context.Context, tunnel string, minimum protocol.Generation, token string) (Assignment, error) {
	return c.ConnectAvoid(ctx, tunnel, minimum, token, "")
}
func (c *Client) ConnectAvoid(ctx context.Context, tunnel string, minimum protocol.Generation, token, avoid string) (Assignment, error) {
	var a Assignment
	if !protocol.ValidTunnelID(tunnel) || minimum == 0 || avoid != "" && !protocol.ValidTunnelID(avoid) {
		return a, InvalidAssignment()
	}
	err := c.post(ctx, "/tunnels/"+tunnel+"/connect", token, struct {
		Minimum protocol.Generation `json:"minimumGeneration"`
		Avoid   string              `json:"avoidRelayId,omitempty"`
	}{minimum, avoid}, &a)
	if err != nil {
		return Assignment{}, err
	}
	h := sha256.Sum256([]byte(tunnel))
	prefix := "p-" + hex.EncodeToString(h[:16]) + "."
	host := a.Relay.Hostname
	validHost := net.ParseIP(host) != nil || host == "localhost" || protocol.ValidHostname(host)
	now := time.Now()
	if !protocol.ValidTunnelID(a.Relay.ID) || !validHost || a.Relay.Port < 1 || a.Relay.Port > 65535 || a.Relay.Protocol != "tls" || a.Relay.Status != "HEALTHY" || a.Relay.Capacity != nil && !a.Relay.Capacity.valid() || !protocol.ValidTunnelID(a.Credential.ID) || a.Credential.TunnelID != tunnel || a.Credential.Scope != "connect" || !protocol.ValidToken(a.Credential.Token) || a.Credential.IssuedAt.IsZero() || a.Credential.IssuedAt.After(now.Add(30*time.Second)) || !a.Credential.ExpiresAt.After(now) || !a.Credential.ExpiresAt.After(a.Credential.IssuedAt) || a.Credential.ExpiresAt.Sub(a.Credential.IssuedAt) > 15*time.Minute || a.Credential.ExpiresAt.After(now.Add(15*time.Minute+30*time.Second)) || a.Generation < minimum || !protocol.ValidHostname(a.PublicHostname) || !strings.HasPrefix(a.PublicHostname, prefix) {
		return Assignment{}, InvalidAssignment()
	}
	return a, nil
}

type Identity struct {
	TunnelID   string              `json:"tunnelId"`
	Generation protocol.Generation `json:"generation"`
	ExpiresAt  time.Time           `json:"expiresAt"`
}

func (c *Client) Verify(ctx context.Context, relay, tokenHash, bearer string) (Identity, error) {
	var i Identity
	hashBytes, hashError := hex.DecodeString(tokenHash)
	if !protocol.ValidTunnelID(relay) || len(hashBytes) != sha256.Size || hashError != nil || tokenHash != strings.ToLower(tokenHash) {
		return i, InvalidAssignment()
	}
	err := c.post(ctx, "/internal/credentials/verify", bearer, struct {
		RelayID   string `json:"relayId"`
		TokenHash string `json:"tokenHash"`
	}{relay, tokenHash}, &i)
	if err != nil {
		return i, err
	}
	now := time.Now()
	if !protocol.ValidTunnelID(i.TunnelID) || i.Generation == 0 || !i.ExpiresAt.After(now) || i.ExpiresAt.After(now.Add(15*time.Minute+30*time.Second)) {
		return Identity{}, InvalidAssignment()
	}
	return i, nil
}
