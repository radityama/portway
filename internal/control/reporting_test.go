package control

import (
	"context"
	"encoding/json"
	"github.com/radityama/portway/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReportFencingBindings(t *testing.T) {
	report := RelayReport{Capacity: Capacity{MaxConnections: 2, MaxTunnels: 4, MaxStreams: 2}, InstanceID: strings.Repeat("a", 32), Status: "HEALTHY"}
	ack := ReportAcknowledgement{RelayID: "rel_a", InstanceID: report.InstanceID, LeaseID: strings.Repeat("b", 32), ExpiresAt: time.Now().Add(15 * time.Second)}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad payload")
		}
		if body["activeConnections"] != float64(0) || body["instanceId"] != report.InstanceID {
			t.Error("report was not flat")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": ack, "error": nil, "meta": map[string]any{}})
	}))
	defer api.Close()
	client, err := NewClient(api.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	first, err := client.ReportRelay(context.Background(), "rel_a", strings.Repeat("r", 43), report, nil)
	if err != nil {
		t.Fatal(err)
	}
	ack.Sequence = 1
	if _, err = client.ReportRelay(context.Background(), "rel_a", strings.Repeat("r", 43), report, &first); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(){func() { ack.LeaseID = strings.Repeat("c", 32) }, func() { ack.RelayID = "rel_other" }, func() { ack.ExpiresAt = time.Now().Add(time.Hour) }, func() { ack.InstanceID = strings.Repeat("d", 32) }} {
		saved := ack
		change()
		if _, err = client.ReportRelay(context.Background(), "rel_a", strings.Repeat("r", 43), report, &first); err == nil {
			t.Fatal("invalid acknowledgement accepted")
		}
		ack = saved
	}
}

func TestDomainSnapshotValidation(t *testing.T) {
	report := RelayReport{Capacity: Capacity{MaxConnections: 2, MaxTunnels: 4, MaxStreams: 2}, InstanceID: strings.Repeat("a", 32), Status: "HEALTHY"}
	var routes any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"relayId": "rel_a", "instanceId": report.InstanceID, "leaseId": strings.Repeat("b", 32), "sequence": 0, "expiresAt": time.Now().Add(15 * time.Second), "drainRequested": false, "routes": routes,
		}, "error": nil, "meta": map[string]any{}})
	}))
	defer api.Close()
	client, err := NewClient(api.URL+"/api/v1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	call := func() (ReportAcknowledgement, error) {
		return client.ReportRelay(context.Background(), "rel_a", strings.Repeat("r", 43), report, nil)
	}
	valid := func() map[string]any {
		return map[string]any{"hostname": "app.example.test", "tunnelId": "tnl_alias", "generation": "18446744073709551615", "expiresAt": time.Now().Add(time.Minute)}
	}
	routes = []any{valid()}
	ack, err := call()
	if err != nil || len(ack.Routes) != 1 || ack.Routes[0].Generation != protocol.Generation(^uint64(0)) {
		t.Fatal("exact generation snapshot rejected", err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any) any
	}{
		{"unknown nested key", func(r map[string]any) any { r["destination"] = "http://remote.test"; return []any{r} }},
		{"case alias", func(r map[string]any) any { r["Hostname"] = r["hostname"]; delete(r, "hostname"); return []any{r} }},
		{"generation overflow", func(r map[string]any) any { r["generation"] = "18446744073709551616"; return []any{r} }},
		{"zero generation", func(r map[string]any) any { r["generation"] = "0"; return []any{r} }},
		{"expired", func(r map[string]any) any { r["expiresAt"] = time.Now().Add(-time.Second); return []any{r} }},
		{"excessive lease", func(r map[string]any) any { r["expiresAt"] = time.Now().Add(time.Hour); return []any{r} }},
		{"invalid hostname", func(r map[string]any) any { r["hostname"] = "APP.example.test"; return []any{r} }},
		{"duplicate hostname", func(r map[string]any) any { return []any{r, r} }},
		{"too many routes", func(r map[string]any) any {
			result := make([]any, 129)
			for i := range result {
				result[i] = r
			}
			return result
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes = tc.change(valid())
			if _, err := call(); err == nil {
				t.Fatal("invalid domain snapshot accepted")
			}
		})
	}
}
