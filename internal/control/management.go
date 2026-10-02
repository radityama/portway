package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/radityama/portway/internal/protocol"
)

// DecodeDocument validates bounded management responses and private client state.
// Callers also apply their own smaller file limits before invoking it.
func DecodeDocument(raw []byte, output any) error {
	if len(raw) > MaxResponse || !utf8.Valid(raw) || !uniqueJSON(raw) {
		return InvalidAssignment()
	}
	return decode(raw, output)
}

type Metadata struct {
	NextCursor       *string `json:"nextCursor,omitempty"`
	RetentionSeconds int     `json:"retentionSeconds,omitempty"`
	MaxEntries       int     `json:"maxEntries,omitempty"`
}

func publicErrorCode(code string) bool {
	return oneOf(code, "AUTH_INVALID", "AUTH_EXPIRED", "AUTH_REVOKED", "FORBIDDEN", "VALIDATION_ERROR", "RATE_LIMITED", "TUNNEL_NOT_FOUND", "TUNNEL_CONFLICT", "TUNNEL_REVOKED", "TUNNEL_INVALID_STATE", "RELAY_NOT_FOUND", "RELAY_UNAVAILABLE", "RELAY_OVERLOADED", "RELAY_DRAINING", "DOMAIN_NOT_FOUND", "DOMAIN_ALREADY_ASSIGNED", "DOMAIN_VERIFICATION_REQUIRED", "DOMAIN_CONFLICT", "DNS_UNAVAILABLE", "PROJECT_CONFLICT", "PROJECT_NOT_FOUND", "PROJECT_NOT_EMPTY", "PAYLOAD_TOO_LARGE", "STREAM_LIMIT_REACHED", "REQUEST_TIMEOUT", "INTERNAL_ERROR", "STORAGE_UNAVAILABLE", "PRESENCE_UNAVAILABLE", "CAPACITY_REACHED", "GENERATION_EXHAUSTED", "IDEMPOTENCY_CONFLICT", "CREDENTIAL_ALREADY_ISSUED", "NOT_FOUND")
}

func (c *Client) management(ctx context.Context, method, path, token string, query url.Values, input, output any, expected int) (Metadata, error) {
	var meta Metadata
	if !protocol.ValidToken(token) {
		return meta, &Error{Code: "AUTH_INVALID"}
	}
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil || len(payload) > MaxResponse {
			return meta, InvalidAssignment()
		}
		body = bytes.NewReader(payload)
	}
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return meta, ErrConfiguration
	}
	if path != "/auth/login" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return meta, ctx.Err()
		}
		var certificate *tls.CertificateVerificationError
		var record tls.RecordHeaderError
		if errors.As(err, &certificate) || errors.As(err, &record) {
			return meta, &Error{Code: "TLS_INVALID"}
		}
		return meta, &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	defer response.Body.Close()
	if response.StatusCode == expected && expected == http.StatusNoContent {
		return meta, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return meta, ctx.Err()
		}
		return meta, &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
		Meta  json.RawMessage `json:"meta"`
	}
	valid := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]) == "application/json" && DecodeDocument(raw, &envelope) == nil && DecodeDocument(envelope.Meta, &meta) == nil
	if response.StatusCode != expected {
		code := "CONTROL_REJECTED"
		temporary := response.StatusCode >= 500 || response.StatusCode == 429 || response.StatusCode == 408
		if temporary {
			code = "CONTROL_UNAVAILABLE"
		} else if valid {
			var failure struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if DecodeDocument(envelope.Error, &failure) == nil && publicErrorCode(failure.Code) {
				code = failure.Code
			}
		}
		return meta, &Error{Code: code, Temporary: temporary}
	}
	if !valid || string(envelope.Error) != "null" || len(envelope.Data) == 0 || string(envelope.Data) == "null" || DecodeDocument(envelope.Data, output) != nil {
		return meta, InvalidAssignment()
	}
	if meta.NextCursor != nil && (len(*meta.NextCursor) == 0 || len(*meta.NextCursor) > 2048 || strings.ContainsAny(*meta.NextCursor, "\r\n")) {
		return Metadata{}, InvalidAssignment()
	}
	return meta, nil
}

type Session struct {
	AccessToken string    `json:"accessToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func (c *Client) Login(ctx context.Context, key string) (Session, error) {
	var output struct {
		Session Session `json:"session"`
	}
	_, err := c.management(ctx, "POST", "/auth/login", key, nil, struct {
		Token string `json:"token"`
	}{key}, &output, 200)
	if err != nil {
		return Session{}, err
	}
	now := time.Now()
	if !protocol.ValidToken(output.Session.AccessToken) || !output.Session.ExpiresAt.After(now) || output.Session.ExpiresAt.After(now.Add(time.Hour+30*time.Second)) {
		return Session{}, InvalidAssignment()
	}
	return output.Session, nil
}

func (c *Client) Logout(ctx context.Context, token string) error {
	_, err := c.management(ctx, "POST", "/auth/logout", token, nil, struct{}{}, nil, 204)
	return err
}

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName *string   `json:"displayName,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}
type Profile struct {
	User         User         `json:"user"`
	Organization Organization `json:"organization"`
	Role         string       `json:"role"`
}

func (c *Client) Me(ctx context.Context, token string) (Profile, error) {
	var output Profile
	_, err := c.management(ctx, "GET", "/me", token, nil, nil, &output, 200)
	if err == nil && (!protocol.ValidTunnelID(output.User.ID) || !protocol.ValidTunnelID(output.Organization.ID) || !oneOf(output.Role, "OWNER", "ADMIN", "MEMBER", "VIEWER")) {
		err = InvalidAssignment()
	}
	return output, err
}

type Project struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Management counters may be zero; protocol registration generations may not.
// Keep the entire unsigned range as a canonical decimal string in CLI JSON.
type Counter string

func (c *Counter) UnmarshalJSON(raw []byte) error {
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) == 0 || len(value) > 20 || len(value) > 1 && value[0] == '0' {
		return InvalidAssignment()
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return InvalidAssignment()
		}
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return InvalidAssignment()
	}
	*c = Counter(value)
	return nil
}

type Tunnel struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"projectId"`
	Name            string     `json:"name"`
	Slug            string     `json:"slug"`
	Type            string     `json:"type"`
	Status          string     `json:"status"`
	Protocol        string     `json:"protocol"`
	LocalHost       string     `json:"localHost"`
	LocalPort       *int       `json:"localPort"`
	PublicHostname  *string    `json:"publicHostname"`
	RelayID         *string    `json:"relayId"`
	Generation      Counter    `json:"generation"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	LastConnectedAt *time.Time `json:"lastConnectedAt"`
}

func (t Tunnel) valid() bool {
	return protocol.ValidTunnelID(t.ID) && protocol.ValidTunnelID(t.ProjectID) && t.Generation != "" && !t.CreatedAt.IsZero() && !t.UpdatedAt.IsZero() && oneOf(t.Type, "EPHEMERAL", "PERSISTENT") && oneOf(t.Status, "CREATED", "CONNECTING", "CONNECTED", "DISCONNECTED", "DRAINING", "REVOKED") && t.Protocol == "http" && net.ParseIP(t.LocalHost).IsLoopback() && (t.LocalPort == nil || *t.LocalPort >= 1 && *t.LocalPort <= 65535) && (t.PublicHostname == nil || protocol.ValidHostname(*t.PublicHostname)) && (t.RelayID == nil || protocol.ValidTunnelID(*t.RelayID))
}
func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

func pageQuery(limit int, cursor string) (url.Values, error) {
	if limit < 1 || limit > 100 || len(cursor) > 2048 || strings.ContainsAny(cursor, "\r\n") {
		return nil, ErrConfiguration
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q, nil
}
func (c *Client) Projects(ctx context.Context, token string) (Page[Project], error) {
	var output struct {
		Projects []Project `json:"projects"`
	}
	meta, err := c.management(ctx, "GET", "/projects", token, url.Values{"limit": {"2"}}, nil, &output, 200)
	if err == nil && (output.Projects == nil || len(output.Projects) > 2) {
		err = InvalidAssignment()
	}
	for _, project := range output.Projects {
		if !protocol.ValidTunnelID(project.ID) || !protocol.ValidTunnelID(project.OrganizationID) {
			err = InvalidAssignment()
		}
	}
	return Page[Project]{output.Projects, meta.NextCursor}, err
}
func (c *Client) Tunnels(ctx context.Context, token, project string, limit int, cursor string) (Page[Tunnel], error) {
	q, err := pageQuery(limit, cursor)
	if err != nil || project != "" && !protocol.ValidTunnelID(project) {
		return Page[Tunnel]{}, ErrConfiguration
	}
	if project != "" {
		q.Set("projectId", project)
	}
	var output struct {
		Tunnels []Tunnel `json:"tunnels"`
	}
	meta, err := c.management(ctx, "GET", "/tunnels", token, q, nil, &output, 200)
	if err == nil && (output.Tunnels == nil || len(output.Tunnels) > limit) {
		err = InvalidAssignment()
	}
	for _, tunnel := range output.Tunnels {
		if !tunnel.valid() || project != "" && tunnel.ProjectID != project {
			err = InvalidAssignment()
		}
	}
	return Page[Tunnel]{output.Tunnels, meta.NextCursor}, err
}
func (c *Client) Tunnel(ctx context.Context, token, id string) (Tunnel, error) {
	var output struct {
		Tunnel Tunnel `json:"tunnel"`
	}
	if !protocol.ValidTunnelID(id) {
		return Tunnel{}, ErrConfiguration
	}
	_, err := c.management(ctx, "GET", "/tunnels/"+id, token, nil, nil, &output, 200)
	if err == nil && (!output.Tunnel.valid() || output.Tunnel.ID != id) {
		err = InvalidAssignment()
	}
	return output.Tunnel, err
}

type CreateTunnel struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Protocol  string `json:"protocol"`
	LocalHost string `json:"localHost"`
	LocalPort int    `json:"localPort"`
}

func (c *Client) CreateTunnel(ctx context.Context, token string, input CreateTunnel) (Tunnel, error) {
	var output struct {
		Tunnel Tunnel `json:"tunnel"`
	}
	if !protocol.ValidTunnelID(input.ProjectID) || len(input.Name) < 1 || len(input.Name) > 100 || !utf8.ValidString(input.Name) || !oneOf(input.Type, "EPHEMERAL", "PERSISTENT") || input.Protocol != "http" || !net.ParseIP(input.LocalHost).IsLoopback() || input.LocalPort < 1 || input.LocalPort > 65535 {
		return Tunnel{}, ErrConfiguration
	}
	_, err := c.management(ctx, "POST", "/tunnels", token, nil, input, &output, 201)
	if err == nil && (!output.Tunnel.valid() || output.Tunnel.ProjectID != input.ProjectID || output.Tunnel.Type != input.Type || output.Tunnel.LocalPort == nil || *output.Tunnel.LocalPort != input.LocalPort) {
		err = InvalidAssignment()
	}
	return output.Tunnel, err
}
func (c *Client) DeleteTunnel(ctx context.Context, token, id string) error {
	if !protocol.ValidTunnelID(id) {
		return ErrConfiguration
	}
	_, err := c.management(ctx, "DELETE", "/tunnels/"+id, token, nil, struct{}{}, nil, 204)
	return err
}

type Domain struct {
	ID                    string     `json:"id"`
	TunnelID              *string    `json:"tunnelId"`
	Hostname              string     `json:"hostname"`
	Status                string     `json:"status"`
	VerifiedAt            *time.Time `json:"verifiedAt"`
	VerificationExpiresAt *time.Time `json:"verificationExpiresAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}
type DomainResult struct {
	Domain       Domain `json:"domain"`
	Verification *struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"verification,omitempty"`
}

func (d DomainResult) valid() bool {
	return protocol.ValidTunnelID(d.Domain.ID) && (d.Domain.TunnelID == nil || protocol.ValidTunnelID(*d.Domain.TunnelID)) && protocol.ValidHostname(d.Domain.Hostname) && oneOf(d.Domain.Status, "PENDING_VERIFICATION", "VERIFIED", "ACTIVE", "DISABLED") && (d.Verification == nil || d.Verification.Type == "TXT" && d.Verification.Name == "_portway-challenge."+d.Domain.Hostname && strings.HasPrefix(d.Verification.Value, "portway-verification=") && protocol.ValidToken(strings.TrimPrefix(d.Verification.Value, "portway-verification=")))
}
func (c *Client) AddDomain(ctx context.Context, token, tunnel, hostname string) (DomainResult, error) {
	var output DomainResult
	if !protocol.ValidTunnelID(tunnel) || !protocol.ValidHostname(hostname) {
		return output, ErrConfiguration
	}
	_, err := c.management(ctx, "POST", "/domains", token, nil, struct {
		Hostname string `json:"hostname"`
		TunnelID string `json:"tunnelId"`
	}{hostname, tunnel}, &output, 201)
	if err == nil && (!output.valid() || output.Verification == nil || output.Domain.Hostname != hostname || output.Domain.TunnelID == nil || *output.Domain.TunnelID != tunnel) {
		err = InvalidAssignment()
	}
	return output, err
}
func (c *Client) DomainAction(ctx context.Context, token, id, action string) (DomainResult, error) {
	var output DomainResult
	if !protocol.ValidTunnelID(id) || !oneOf(action, "verify", "activate", "challenge", "remove") {
		return output, ErrConfiguration
	}
	method, path, expected := "POST", "/domains/"+id+"/"+action, 200
	var target any = &output
	if action == "remove" {
		method, path, expected, target = "DELETE", "/domains/"+id, 204, nil
	}
	_, err := c.management(ctx, method, path, token, nil, struct{}{}, target, expected)
	if err == nil && action != "remove" && (!output.valid() || output.Domain.ID != id) {
		err = InvalidAssignment()
	}
	return output, err
}

type RequestLog struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	Status     int       `json:"status"`
	DurationMS float64   `json:"durationMs"`
	BytesIn    Counter   `json:"bytesIn"`
	BytesOut   Counter   `json:"bytesOut"`
	Outcome    string    `json:"outcome"`
}
type Logs struct {
	Available  bool         `json:"available"`
	ObservedAt *time.Time   `json:"observedAt"`
	Logs       []RequestLog `json:"logs"`
}

func (c *Client) Logs(ctx context.Context, token, id string, limit int) (Logs, error) {
	var output Logs
	if !protocol.ValidTunnelID(id) || limit < 1 || limit > 4 {
		return output, ErrConfiguration
	}
	_, err := c.management(ctx, "GET", "/tunnels/"+id+"/logs", token, url.Values{"limit": {strconv.Itoa(limit)}}, nil, &output, 200)
	if err == nil && (output.Logs == nil || len(output.Logs) > limit || output.Available != (output.ObservedAt != nil)) {
		err = InvalidAssignment()
	}
	for _, item := range output.Logs {
		if item.Timestamp.IsZero() || item.BytesIn == "" || item.BytesOut == "" || len(item.ID) == 0 || len(item.ID) > 128 || item.Status < 0 || item.Status > 599 || item.DurationMS < 0 || !oneOf(item.Outcome, "complete", "error", "canceled") || !oneOf(item.Method, "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD", "OTHER") {
			err = InvalidAssignment()
		}
	}
	return output, err
}

func (c *Client) Readiness(ctx context.Context, token string) error {
	// /ready belongs to the same already validated origin, outside /api/v1.
	u := *c.base
	u.Path = "/ready"
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return ErrConfiguration
	}
	response, err := c.http.Do(req)
	if err != nil {
		return &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return &Error{Code: "CONTROL_UNAVAILABLE", Temporary: true}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	var envelope struct {
		Data struct {
			Status  string `json:"status"`
			Storage string `json:"storage"`
		} `json:"data"`
		Error json.RawMessage `json:"error"`
		Meta  Metadata        `json:"meta"`
	}
	if err != nil || len(raw) > 4096 || DecodeDocument(raw, &envelope) != nil || envelope.Data.Status != "ready" || string(envelope.Error) != "null" {
		return InvalidAssignment()
	}
	return nil
}

func (c *Client) Relays(ctx context.Context, token string) (Page[Relay], error) {
	var output struct {
		Relays []Relay `json:"relays"`
	}
	meta, err := c.management(ctx, "GET", "/relays", token, url.Values{"limit": {"50"}}, nil, &output, 200)
	if err == nil && (output.Relays == nil || len(output.Relays) > 50) {
		err = InvalidAssignment()
	}
	for _, r := range output.Relays {
		if !protocol.ValidTunnelID(r.ID) || r.Port < 1 || r.Port > 65535 || r.Protocol != "tls" || (net.ParseIP(r.Hostname) == nil && r.Hostname != "localhost" && !protocol.ValidHostname(r.Hostname)) || r.Capacity != nil && !r.Capacity.valid() || !oneOf(r.Status, "HEALTHY", "DEGRADED", "DRAINING", "OFFLINE") {
			err = InvalidAssignment()
		}
	}
	return Page[Relay]{output.Relays, meta.NextCursor}, err
}
