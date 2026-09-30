package config

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/radityama/portway/internal/protocol"
)

var ErrEnvironment = errors.New("invalid Portway connection environment")

type Lookup func(string) (string, bool)

type Relay struct {
	PublicBaseDomain    string
	MaxTunnels          int
	RegistrationTimeout time.Duration
	Address             string
	CertFile            string
	KeyFile             string
	CredentialsFile     string
	MaxConnections      int
	MaxFrame            uint32
	HandshakeTimeout    time.Duration
	IdleTimeout         time.Duration
	WriteTimeout        time.Duration
}

type Agent struct {
	TunnelID            string
	StateDir            string
	Generation          protocol.Generation
	RegistrationTimeout time.Duration
	Address             string
	CAFile              string
	ServerName          string
	TokenFile           string
	ConnectTimeout      time.Duration
	HandshakeTimeout    time.Duration
	IdleTimeout         time.Duration
	WriteTimeout        time.Duration
}

func value(env Lookup, key, fallback string) string {
	if v, ok := env(key); ok {
		return v
	}
	return fallback
}
func duration(env Lookup, key, fallback string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value(env, key, fallback))
	if err != nil || parsed <= 0 || parsed > 5*time.Minute {
		return 0, ErrEnvironment
	}
	return parsed, nil
}
func port(env Lookup) (string, error) {
	raw := value(env, "RELAY_PORT", "8081")
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > 65535 {
		return "", ErrEnvironment
	}
	return strconv.Itoa(parsed), nil
}

func RelayEnvironment(env Lookup) (Relay, error) {
	p, err := port(env)
	if err != nil {
		return Relay{}, err
	}
	host := value(env, "RELAY_BIND_HOST", "127.0.0.1")
	if net.ParseIP(host) == nil {
		return Relay{}, ErrEnvironment
	}
	cfg := Relay{Address: net.JoinHostPort(host, p), CertFile: value(env, "RELAY_TLS_CERT_FILE", ".tmp/dev/relay-cert.pem"), KeyFile: value(env, "RELAY_TLS_KEY_FILE", ".tmp/dev/relay-key.pem"), CredentialsFile: value(env, "RELAY_CREDENTIALS_FILE", ".tmp/dev/relay-credentials.json")}
	maxConnections, err := strconv.Atoi(value(env, "RELAY_MAX_CONNECTIONS", "128"))
	if err != nil || maxConnections < 1 || maxConnections > 10000 {
		return Relay{}, ErrEnvironment
	}
	cfg.MaxConnections = maxConnections
	cfg.PublicBaseDomain = value(env, "PUBLIC_BASE_DOMAIN", "portway.localhost")
	if !protocol.ValidHostname(cfg.PublicBaseDomain) || len(cfg.PublicBaseDomain) > 218 {
		return Relay{}, ErrEnvironment
	}
	cfg.MaxTunnels, err = strconv.Atoi(value(env, "RELAY_MAX_TUNNELS", "1024"))
	if err != nil || cfg.MaxTunnels < 1 || cfg.MaxTunnels > 100000 {
		return Relay{}, ErrEnvironment
	}
	if cfg.RegistrationTimeout, err = duration(env, "RELAY_REGISTRATION_TIMEOUT", "10s"); err != nil {
		return Relay{}, err
	}
	frame, err := strconv.ParseUint(value(env, "RELAY_MAX_FRAME_BYTES", "4194304"), 10, 32)
	if err != nil || frame < protocol.MaxHandshakePayloadSize || frame > uint64(protocol.MaxPayloadSize) {
		return Relay{}, ErrEnvironment
	}
	cfg.MaxFrame = uint32(frame)
	if cfg.CertFile == "" || cfg.KeyFile == "" || cfg.CredentialsFile == "" {
		return Relay{}, ErrEnvironment
	}
	if cfg.HandshakeTimeout, err = duration(env, "RELAY_HANDSHAKE_TIMEOUT", "10s"); err != nil {
		return Relay{}, err
	}
	if cfg.IdleTimeout, err = duration(env, "RELAY_IDLE_TIMEOUT", "120s"); err != nil {
		return Relay{}, err
	}
	if cfg.WriteTimeout, err = duration(env, "RELAY_WRITE_TIMEOUT", "5s"); err != nil {
		return Relay{}, err
	}
	return cfg, nil
}

func AgentEnvironment(env Lookup) (Agent, error) {
	p, err := port(env)
	if err != nil {
		return Agent{}, err
	}
	cfg := Agent{Address: value(env, "PORTWAY_RELAY_ADDR", net.JoinHostPort("127.0.0.1", p)), CAFile: value(env, "PORTWAY_RELAY_CA_FILE", ".tmp/dev/ca.pem"), TokenFile: value(env, "PORTWAY_TOKEN_FILE", ".tmp/dev/agent-token")}
	cfg.TunnelID = value(env, "PORTWAY_TUNNEL_ID", "tnl_local_dev")
	cfg.StateDir = value(env, "PORTWAY_STATE_DIR", ".tmp/agent-state")
	if !protocol.ValidTunnelID(cfg.TunnelID) || cfg.StateDir == "" {
		return Agent{}, ErrEnvironment
	}
	if raw, ok := env("PORTWAY_GENERATION"); ok {
		cfg.Generation, err = protocol.ParseGeneration(raw)
		if err != nil {
			return Agent{}, ErrEnvironment
		}
	}
	if cfg.RegistrationTimeout, err = duration(env, "PORTWAY_REGISTRATION_TIMEOUT", "10s"); err != nil {
		return Agent{}, err
	}
	host, port, err := net.SplitHostPort(cfg.Address)
	parsed, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || !validHost(host) || parsed < 1 || parsed > 65535 || cfg.TokenFile == "" {
		return Agent{}, ErrEnvironment
	}
	cfg.ServerName = value(env, "PORTWAY_RELAY_SERVER_NAME", host)
	if !validHost(cfg.ServerName) {
		return Agent{}, ErrEnvironment
	}
	if cfg.ConnectTimeout, err = duration(env, "PORTWAY_CONNECT_TIMEOUT", "10s"); err != nil {
		return Agent{}, err
	}
	if cfg.HandshakeTimeout, err = duration(env, "PORTWAY_HANDSHAKE_TIMEOUT", "10s"); err != nil {
		return Agent{}, err
	}
	if cfg.IdleTimeout, err = duration(env, "PORTWAY_IDLE_TIMEOUT", "120s"); err != nil {
		return Agent{}, err
	}
	if cfg.WriteTimeout, err = duration(env, "PORTWAY_WRITE_TIMEOUT", "5s"); err != nil {
		return Agent{}, err
	}
	return cfg, nil
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
