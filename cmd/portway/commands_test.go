package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagementCLIPrivateLoginConfigLogoutAndJSONErrors(t *testing.T) {
	source := strings.Repeat("a", 43)
	session := strings.Repeat("b", 43)
	revoked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session": map[string]any{"accessToken": session, "expiresAt": time.Now().Add(time.Minute)}}, "error": nil, "meta": map[string]any{}})
		case "/api/v1/auth/logout":
			if r.Header.Get("Authorization") != "Bearer "+session {
				t.Error("revoked source key")
			}
			revoked = true
			w.WriteHeader(204)
		default:
			t.Error("unexpected network call")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "key")
	os.WriteFile(tokenFile, []byte(source), 0600)
	env := func(key string) (string, bool) {
		if key == "PORTWAY_CONFIG_DIR" {
			return filepath.Join(dir, "profile"), true
		}
		return "", false
	}
	invoke := func(args []string, want int) map[string]json.RawMessage {
		t.Helper()
		args = append(args, "--json")
		var out, err bytes.Buffer
		if code := run(context.Background(), args, env, &out, &err); code != want || err.Len() != 0 {
			t.Fatalf("exit=%d stderr=%s output=%s", code, err.String(), out.String())
		}
		if strings.Contains(out.String(), source) || strings.Contains(out.String(), session) {
			t.Fatal("printed key")
		}
		var result map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result) != nil {
			t.Fatal("not one JSON result")
		}
		return result
	}
	invoke([]string{"login", "--token-file", tokenFile, "--api-url", server.URL + "/api/v1"}, 0)
	invoke([]string{"config"}, 0)
	invoke([]string{"config", "set", "api_url", "http://127.0.0.1:1/api/v1"}, 1)
	invoke([]string{"list", "--api-url", "http://127.0.0.1:1/api/v1"}, 1)
	invoke([]string{"login", "--token-file", tokenFile}, 1)
	invoke([]string{"logout"}, 0)
	if !revoked {
		t.Fatal("managed session not revoked")
	}
	invoke([]string{"version"}, 0)
	for _, args := range [][]string{{"login", "--token", "secret"}, {"version", "--bad"}, {"config", "set", "local_port"}, {"connect", "--once", "--once"}, {"version", "--limit", "1"}} {
		invoke(args, 2)
	}
}
