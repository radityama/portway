package control

import (
	"context"
	"encoding/json"
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
