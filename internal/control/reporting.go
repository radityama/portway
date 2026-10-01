package control

import (
	"context"
	"encoding/hex"
	"github.com/radityama/portway/internal/protocol"
	"time"
)

type Capacity struct {
	ActiveConnections int `json:"activeConnections"`
	ActiveTunnels     int `json:"activeTunnels"`
	RetainedTunnels   int `json:"retainedTunnels"`
	ActiveStreams     int `json:"activeStreams"`
	MaxConnections    int `json:"maxConnections"`
	MaxTunnels        int `json:"maxTunnels"`
	MaxStreams        int `json:"maxStreams"`
}
type RelayReport struct {
	Capacity
	InstanceID string `json:"instanceId"`
	Status     string `json:"status"`
}
type DomainRoute struct {
	Hostname   string              `json:"hostname"`
	TunnelID   string              `json:"tunnelId"`
	Generation protocol.Generation `json:"generation"`
	ExpiresAt  time.Time           `json:"expiresAt"`
}
type ReportAcknowledgement struct {
	Routes         []DomainRoute `json:"routes,omitempty"`
	RelayID        string        `json:"relayId"`
	InstanceID     string        `json:"instanceId"`
	LeaseID        string        `json:"leaseId"`
	Sequence       int           `json:"sequence"`
	ExpiresAt      time.Time     `json:"expiresAt"`
	DrainRequested bool          `json:"drainRequested"`
}

func validIncarnation(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}
func (c Capacity) valid() bool {
	return c.MaxConnections > 0 && c.MaxConnections <= 10000 && c.MaxTunnels > 0 && c.MaxTunnels <= 100000 && c.MaxStreams > 0 && c.MaxStreams <= 1024 && c.ActiveConnections >= 0 && c.ActiveConnections <= c.MaxConnections && c.ActiveTunnels >= 0 && c.ActiveTunnels <= c.ActiveConnections && c.RetainedTunnels >= c.ActiveTunnels && c.RetainedTunnels <= c.MaxTunnels && c.ActiveStreams >= 0 && c.ActiveStreams <= c.ActiveTunnels*c.MaxStreams
}
func (c *Client) ReportRelay(ctx context.Context, relay, bearer string, report RelayReport, prior *ReportAcknowledgement) (ReportAcknowledgement, error) {
	var ack ReportAcknowledgement
	if !protocol.ValidTunnelID(relay) || !validIncarnation(report.InstanceID) || !report.Capacity.valid() || (report.Status != "HEALTHY" && report.Status != "DEGRADED" && report.Status != "DRAINING") {
		return ack, InvalidAssignment()
	}
	path := "/internal/relays/" + relay + "/register"
	var input any = report
	expected := 0
	if prior != nil {
		if prior.RelayID != relay || prior.InstanceID != report.InstanceID || !validIncarnation(prior.LeaseID) || prior.Sequence < 0 || prior.Sequence >= 2147483647 {
			return ack, InvalidAssignment()
		}
		expected = prior.Sequence + 1
		path = "/internal/relays/" + relay + "/report"
		input = struct {
			RelayReport
			LeaseID  string `json:"leaseId"`
			Sequence int    `json:"sequence"`
		}{report, prior.LeaseID, expected}
	}
	if err := c.post(ctx, path, bearer, input, &ack); err != nil {
		return ack, err
	}
	now := time.Now()
	if ack.RelayID != relay || ack.InstanceID != report.InstanceID || !validIncarnation(ack.LeaseID) || ack.Sequence < 0 || ack.Sequence > 2147483647 || !ack.ExpiresAt.After(now) || ack.ExpiresAt.After(now.Add(18*time.Second)) || prior != nil && (ack.LeaseID != prior.LeaseID || ack.Sequence != expected) {
		return ReportAcknowledgement{}, InvalidAssignment()
	}
	if len(ack.Routes) > 128 {
		return ReportAcknowledgement{}, InvalidAssignment()
	}
	seen := map[string]bool{}
	for _, r := range ack.Routes {
		if !protocol.ValidHostname(r.Hostname) || len(r.Hostname) > 220 || !protocol.ValidTunnelID(r.TunnelID) || r.Generation == 0 || !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(15*time.Minute+30*time.Second)) || seen[r.Hostname] {
			return ReportAcknowledgement{}, InvalidAssignment()
		}
		seen[r.Hostname] = true
	}
	return ack, nil
}
func (c *Client) RelayPolicy(ctx context.Context, relay, bearer string, drain bool) error {
	if !protocol.ValidTunnelID(relay) {
		return InvalidAssignment()
	}
	action := "activate"
	if drain {
		action = "drain"
	}
	var result struct {
		Relay Relay `json:"relay"`
	}
	if err := c.post(ctx, "/internal/relays/"+relay+"/"+action, bearer, struct{}{}, &result); err != nil {
		return err
	}
	if result.Relay.ID != relay || drain && result.Relay.Status != "DRAINING" {
		return InvalidAssignment()
	}
	return nil
}
