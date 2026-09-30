package config

import (
	"errors"
	"testing"
)

func lookup(values map[string]string) Lookup {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}
func TestEnvironmentConfiguration(t *testing.T) {
	relay, err := RelayEnvironment(lookup(nil))
	if err != nil || relay.Address != "127.0.0.1:8081" || relay.MaxConnections != 128 || relay.PublicBaseDomain != "portway.localhost" || relay.MaxTunnels != 1024 {
		t.Fatal("relay defaults invalid")
	}
	client, err := AgentEnvironment(lookup(map[string]string{"RELAY_PORT": "9443"}))
	if err != nil || client.Address != "127.0.0.1:9443" {
		t.Fatal("agent did not follow the relay development port")
	}
	client, err = AgentEnvironment(lookup(map[string]string{"PORTWAY_RELAY_ADDR": "relay.example.com:443", "PORTWAY_RELAY_CA_FILE": ""}))
	if err != nil || client.ServerName != "relay.example.com" || client.CAFile != "" {
		t.Fatal("system-root relay configuration invalid")
	}
}
func TestInvalidNetworkConfiguration(t *testing.T) {
	for key, value := range map[string]string{"PUBLIC_BASE_DOMAIN": "injected@host", "RELAY_MAX_TUNNELS": "0", "RELAY_REGISTRATION_TIMEOUT": "0s", "RELAY_PORT": "0", "RELAY_BIND_HOST": "not-an-ip", "RELAY_MAX_CONNECTIONS": "0", "RELAY_MAX_FRAME_BYTES": "4095", "RELAY_HANDSHAKE_TIMEOUT": "0s", "RELAY_IDLE_TIMEOUT": "1h", "RELAY_WRITE_TIMEOUT": "invalid", "RELAY_TLS_KEY_FILE": ""} {
		if _, err := RelayEnvironment(lookup(map[string]string{key: value})); !errors.Is(err, ErrEnvironment) {
			t.Fatalf("invalid %s was accepted", key)
		}
	}
	for key, value := range map[string]string{"PORTWAY_TUNNEL_ID": "../victim", "PORTWAY_STATE_DIR": "", "PORTWAY_GENERATION": "01", "PORTWAY_REGISTRATION_TIMEOUT": "0s"} {
		if _, err := AgentEnvironment(lookup(map[string]string{key: value})); !errors.Is(err, ErrEnvironment) {
			t.Fatalf("invalid %s accepted", key)
		}
	}
	for _, address := range []string{"", "localhost:0", "localhost:65536", "http://localhost:8081", "credential@localhost:8081", "-bad.example:443"} {
		if _, err := AgentEnvironment(lookup(map[string]string{"PORTWAY_RELAY_ADDR": address})); !errors.Is(err, ErrEnvironment) {
			t.Fatal("invalid relay address accepted")
		}
	}
	if _, err := AgentEnvironment(lookup(map[string]string{"PORTWAY_RELAY_SERVER_NAME": "secret@host"})); !errors.Is(err, ErrEnvironment) {
		t.Fatal("invalid TLS name accepted")
	}
}
