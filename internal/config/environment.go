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
	PublicManifest                                     string
	TLSReloadInterval                                  time.Duration
	APIURL, APICAFile, APITokenFile, RelayID           string
	APITimeout                                         time.Duration
	ReportInterval                                     time.Duration
	ReportURL, ReportCAFile, ReportTokenFile, ReportID string
	ShutdownTimeout                                    time.Duration
	PublicBaseDomain                                   string
	MaxTunnels                                         int
	RegistrationTimeout                                time.Duration
	Address                                            string
	CertFile                                           string
	KeyFile                                            string
	CredentialsFile                                    string
	MaxConnections                                     int
	MaxFrame                                           uint32
	HandshakeTimeout                                   time.Duration
	IdleTimeout                                        time.Duration
	WriteTimeout                                       time.Duration
	PublicAddress                                      string
	PublicPort                                         int
	PublicCertFile                                     string
	PublicKeyFile                                      string
	MaxPublicConnections                               int
	MaxStreams                                         int
	StreamTimeout                                      time.Duration
}

type Agent struct {
	APIURL, APICAFile, APITokenFile string
	APITimeout                      time.Duration
	ShutdownTimeout                 time.Duration
	MaxStreams                      int
	StreamTimeout                   time.Duration
	TunnelID                        string
	StateDir                        string
	Generation                      protocol.Generation
	RegistrationTimeout             time.Duration
	Address                         string
	CAFile                          string
	ServerName                      string
	TokenFile                       string
	ConnectTimeout                  time.Duration
	HandshakeTimeout                time.Duration
	IdleTimeout                     time.Duration
	WriteTimeout                    time.Duration
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
	cfg.APIURL = value(env, "RELAY_API_URL", "")
	cfg.APITokenFile = value(env, "RELAY_API_TOKEN_FILE", "")
	cfg.APICAFile = value(env, "RELAY_API_CA_FILE", "")
	cfg.RelayID = value(env, "RELAY_ID", "")
	cfg.APITimeout, err = duration(env, "RELAY_API_TIMEOUT", "5s")
	if err != nil || cfg.APITimeout > 30*time.Second || (cfg.APIURL == "") != (cfg.APITokenFile == "") || cfg.APITokenFile != "" && !protocol.ValidTunnelID(cfg.RelayID) {
		return Relay{}, ErrEnvironment
	}
	cfg.ReportURL = value(env, "RELAY_REPORT_API_URL", cfg.APIURL)
	cfg.ReportCAFile = value(env, "RELAY_REPORT_API_CA_FILE", cfg.APICAFile)
	cfg.ReportTokenFile = value(env, "RELAY_REPORT_API_TOKEN_FILE", cfg.APITokenFile)
	cfg.ReportID = value(env, "RELAY_REPORT_ID", cfg.RelayID)
	if (cfg.ReportURL == "") != (cfg.ReportTokenFile == "") || cfg.ReportTokenFile != "" && !protocol.ValidTunnelID(cfg.ReportID) {
		return Relay{}, ErrEnvironment
	}
	cfg.PublicManifest = value(env, "PUBLIC_TLS_MANIFEST_FILE", "")
	cfg.TLSReloadInterval, err = duration(env, "PUBLIC_TLS_RELOAD_INTERVAL", "30s")
	if err != nil || cfg.TLSReloadInterval < 100*time.Millisecond || cfg.TLSReloadInterval > 5*time.Minute {
		return Relay{}, ErrEnvironment
	}
	if cfg.PublicManifest != "" && cfg.ReportTokenFile == "" {
		return Relay{}, ErrEnvironment
	}
	cfg.ReportInterval, err = duration(env, "RELAY_API_REPORT_INTERVAL", "2s")
	if err != nil || cfg.ReportInterval < 100*time.Millisecond || cfg.ReportInterval > 5*time.Second {
		return Relay{}, ErrEnvironment
	}
	cfg.ShutdownTimeout, err = duration(env, "RELAY_SHUTDOWN_TIMEOUT", "10s")
	if err != nil || cfg.ShutdownTimeout > time.Minute {
		return Relay{}, ErrEnvironment
	}
	maxConnections, err := strconv.Atoi(value(env, "RELAY_MAX_CONNECTIONS", "128"))
	if err != nil || maxConnections < 1 || maxConnections > 10000 {
		return Relay{}, ErrEnvironment
	}
	cfg.MaxConnections = maxConnections
	cfg.PublicPort, err = strconv.Atoi(value(env, "PUBLIC_PORT", "8443"))
	publicHost := value(env, "PUBLIC_BIND_HOST", "127.0.0.1")
	if err != nil || cfg.PublicPort < 1 || cfg.PublicPort > 65535 || strconv.Itoa(cfg.PublicPort) == p || net.ParseIP(publicHost) == nil {
		return Relay{}, ErrEnvironment
	}
	cfg.PublicAddress = net.JoinHostPort(publicHost, strconv.Itoa(cfg.PublicPort))
	cfg.PublicCertFile = value(env, "PUBLIC_TLS_CERT_FILE", ".tmp/dev/public-cert.pem")
	cfg.PublicKeyFile = value(env, "PUBLIC_TLS_KEY_FILE", ".tmp/dev/public-key.pem")
	if cfg.PublicCertFile == "" || cfg.PublicKeyFile == "" {
		return Relay{}, ErrEnvironment
	}
	cfg.MaxPublicConnections, err = strconv.Atoi(value(env, "PUBLIC_MAX_CONNECTIONS", "128"))
	if err != nil || cfg.MaxPublicConnections < 1 || cfg.MaxPublicConnections > 10000 {
		return Relay{}, ErrEnvironment
	}
	cfg.MaxStreams, err = strconv.Atoi(value(env, "RELAY_MAX_STREAMS", "32"))
	if err != nil || cfg.MaxStreams < 1 || cfg.MaxStreams > 1024 {
		return Relay{}, ErrEnvironment
	}
	cfg.StreamTimeout, err = duration(env, "RELAY_STREAM_TIMEOUT", "30s")
	if err != nil {
		return Relay{}, err
	}
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
	cfg.APIURL = value(env, "PORTWAY_API_URL", "http://localhost:8080/api/v1")
	cfg.APITokenFile = value(env, "PORTWAY_API_TOKEN_FILE", "")
	cfg.APICAFile = value(env, "PORTWAY_API_CA_FILE", "")
	cfg.APITimeout, err = duration(env, "PORTWAY_API_TIMEOUT", "5s")
	if err != nil || cfg.APITimeout > 30*time.Second {
		return Agent{}, ErrEnvironment
	}
	cfg.ShutdownTimeout, err = duration(env, "PORTWAY_SHUTDOWN_TIMEOUT", "10s")
	if err != nil || cfg.ShutdownTimeout > time.Minute {
		return Agent{}, ErrEnvironment
	}
	cfg.MaxStreams, err = strconv.Atoi(value(env, "PORTWAY_MAX_STREAMS", "32"))
	if err != nil || cfg.MaxStreams < 1 || cfg.MaxStreams > 1024 {
		return Agent{}, ErrEnvironment
	}
	cfg.StreamTimeout, err = duration(env, "PORTWAY_STREAM_TIMEOUT", "30s")
	if err != nil {
		return Agent{}, err
	}
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
