package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func managementTunnel() Tunnel {
	port := 3000
	return Tunnel{ID: "tnl_test", ProjectID: "prj_test", Name: "test", Slug: "test", Type: "PERSISTENT", Status: "CREATED", Protocol: "http", LocalHost: "127.0.0.1", LocalPort: &port, Generation: "0", CreatedAt: time.Now(), UpdatedAt: time.Now()}
}
func TestManagementPaginationScopeAndUnsignedCounters(t *testing.T) {
	tunnel := managementTunnel()
	cursor := "next_page"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Method != "GET" || r.URL.Query().Get("projectId") != "prj_test" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("cursor") != "page +/=" {
			t.Error("unbound management request")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"tunnels": []Tunnel{tunnel}}, "error": nil, "meta": Metadata{NextCursor: &cursor}})
	}))
	defer s.Close()
	c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
	defer c.Close()
	page, err := c.Tunnels(context.Background(), testToken, "prj_test", 1, "page +/=")
	if err != nil || len(page.Items) != 1 || page.Items[0].Generation != "0" || page.NextCursor == nil || *page.NextCursor != cursor {
		t.Fatal(page, err)
	}
	for _, value := range []string{"0", "18446744073709551615"} {
		var counter Counter
		if json.Unmarshal([]byte(`"`+value+`"`), &counter) != nil || string(counter) != value {
			t.Fatal("lost uint64 precision")
		}
	}
	for _, value := range []string{`0`, `"00"`, `"-1"`, `"18446744073709551616"`, `null`} {
		var counter Counter
		if json.Unmarshal([]byte(value), &counter) == nil {
			t.Fatal("invalid counter accepted", value)
		}
	}
}
func TestManagementMalformedResponsesAndNoMutationReplay(t *testing.T) {
	valid, _ := json.Marshal(map[string]any{"data": map[string]any{"tunnel": managementTunnel()}, "error": nil, "meta": Metadata{}})
	for _, raw := range []string{`{"data":{},"error":null,"meta":{},"data":{}}`, `{"data":{},"error":null,"meta":{},"unknown":1}`, string(valid) + `{}`, string(valid[:len(valid)-1]), strings.Replace(string(valid), `"generation":"0"`, `"generation":0`, 1), strings.Repeat(" ", MaxResponse+1)} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, raw)
		}))
		c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
		_, err := c.Tunnel(context.Background(), testToken, "tnl_test")
		c.Close()
		s.Close()
		if err == nil || Retryable(err) {
			t.Fatal("malformed response accepted or retried", err)
		}
	}
	for _, status := range []int{302, 401, 409, 429, 503} {
		var calls atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", "/other")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": strings.ToUpper(testToken), "message": testToken}, "meta": Metadata{}})
		}))
		c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
		_, err := c.CreateTunnel(context.Background(), testToken, CreateTunnel{ProjectID: "prj_test", Name: "test", Type: "PERSISTENT", Protocol: "http", LocalHost: "127.0.0.1", LocalPort: 3000})
		c.Close()
		s.Close()
		if err == nil || calls.Load() != 1 || strings.Contains(strings.ToLower(err.Error()), strings.ToLower(testToken)) {
			t.Fatal("mutation replay or credential leak", err)
		}
	}
}
func TestManagementLoginAndCancellation(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("login sent bearer header")
		}
		var body struct {
			Token string `json:"token"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Token != testToken {
			t.Error("bad login body")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session": Session{testToken, time.Now().Add(time.Minute)}}, "error": nil, "meta": Metadata{}})
	}))
	defer s.Close()
	c, _ := NewClient(s.URL+"/api/v1", "", time.Second)
	defer c.Close()
	if _, err := c.Login(context.Background(), testToken); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Login(ctx, testToken); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("canceled login hit server", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer slow.Close()
	bounded, _ := NewClient(slow.URL+"/api/v1", "", 50*time.Millisecond)
	defer bounded.Close()
	start := time.Now()
	if _, err := bounded.Me(context.Background(), testToken); !Retryable(err) || time.Since(start) > time.Second {
		t.Fatal("slow management peer not bounded", err)
	}
}
