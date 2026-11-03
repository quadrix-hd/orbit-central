package orbit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// clusterFake answers /cluster/status as the node behind the requested host.
func clusterFake(t *testing.T, reachable map[string]string) (*Server, Connection, *[]string) {
	t.Helper()
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.URL, c.Endpoints = "https://pve1.example.lan:8006", nil
	hosts := []string{}
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		local, ok := reachable[r.URL.Host]
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		rows := []any{doc{"type": "cluster", "name": "dc", "quorate": 1}}
		for name, ip := range map[string]string{"pve1": "10.0.0.11", "pve4": "10.0.0.14"} {
			rows = append(rows, doc{"type": "node", "name": name, "ip": ip, "local": boolInt(name == local), "online": 1})
		}
		raw, _ := json.Marshal(doc{"data": rows})
		return response(200, string(raw)), nil
	})
	return s, c, &hosts
}

func TestNodeConnectionDiscoversNodeAddress(t *testing.T) {
	s, c, _ := clusterFake(t, map[string]string{"pve1.example.lan:8006": "pve1", "10.0.0.14:8006": "pve4"})
	if got := s.nodeConnection(context.Background(), c, "pve4").URL; got != "https://10.0.0.14:8006" {
		t.Fatalf("expected discovered node IP, got %s", got)
	}
	if got := s.nodeConnection(context.Background(), c, "pve1").URL; got != "https://pve1.example.lan:8006" {
		t.Fatalf("configured endpoint must be preferred, got %s", got)
	}
}

func TestNodeConnectionFallsBackToHostnameScheme(t *testing.T) {
	s, c, _ := clusterFake(t, map[string]string{"pve1.example.lan:8006": "pve1", "pve4.example.lan:8006": "pve4"})
	if got := s.nodeConnection(context.Background(), c, "pve4").URL; got != "https://pve4.example.lan:8006" {
		t.Fatalf("expected hostname candidate, got %s", got)
	}
}

func TestNodeConnectionRejectsWrongNodeAndExplains(t *testing.T) {
	// The IP answers, but as a different node: it must not be used.
	s, c, _ := clusterFake(t, map[string]string{"pve1.example.lan:8006": "pve1", "10.0.0.14:8006": "pve1"})
	err := guarded(func() { s.nodeConnection(context.Background(), c, "pve4") })
	if err == nil || !strings.Contains(err.Error(), "https://10.0.0.14:8006") || !strings.Contains(err.Error(), "zusätzlichen Endpunkt") {
		t.Fatalf("expected explanatory failure, got %v", err)
	}
}
