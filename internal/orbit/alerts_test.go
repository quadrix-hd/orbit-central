package orbit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type alertFake struct {
	mu        sync.Mutex
	resources []any
	tasks     []any
	down      bool
}

func alertServer(t *testing.T) (*Server, *alertFake) {
	t.Helper()
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.Endpoints = nil
	if err := store.SaveConnection(c, true); err != nil {
		t.Fatal(err)
	}
	f := &alertFake{}
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			return nil, io.ErrUnexpectedEOF
		}
		var data any = []any{}
		switch strings.TrimPrefix(r.URL.Path, "/api2/json") {
		case "/cluster/resources":
			data = f.resources
		case "/cluster/tasks":
			data = f.tasks
		}
		raw, _ := json.Marshal(doc{"data": data})
		return response(200, string(raw)), nil
	})
	return s, f
}

func alertsByRule(s *Server) map[string]doc {
	out := map[string]doc{}
	for _, a := range s.listAlerts() {
		out[str(a["rule"])+"|"+str(a["object"])] = a
	}
	return out
}

func TestAlertRulesFireHoldAndResolve(t *testing.T) {
	s, f := alertServer(t)
	start := time.Now()
	f.resources = []any{
		doc{"type": "node", "node": "pve-01", "status": "online", "cpu": 0.97, "mem": 10.0, "maxmem": 100.0},
		doc{"type": "node", "node": "pve-02", "status": "offline"},
		doc{"type": "storage", "node": "pve-01", "storage": "local", "status": "available", "disk": 90.0, "maxdisk": 100.0},
		doc{"type": "storage", "node": "pve-01", "storage": "ceph", "shared": 1, "status": "available", "disk": 96.0, "maxdisk": 100.0},
		doc{"type": "storage", "node": "pve-03", "storage": "ceph", "shared": 1, "status": "available", "disk": 96.0, "maxdisk": 100.0},
	}
	f.tasks = []any{
		doc{"upid": "UPID:a", "type": "vzdump", "id": "101", "node": "pve-01", "endtime": float64(start.Unix() - 60), "status": "ERROR: job failed"},
		doc{"upid": "UPID:b", "type": "qmstart", "node": "pve-01", "endtime": float64(start.Unix() - 30), "status": "OK"},
		doc{"upid": "UPID:old", "type": "vzdump", "node": "pve-01", "endtime": float64(start.Unix() - 7200), "status": "ERROR: ancient"},
	}
	s.evaluateAlerts(context.Background(), start)
	got := alertsByRule(s)
	if got["node_offline|pve-02"] == nil || got["storage_usage|pve-01/local"] == nil || got["task_failed|pve-01"] == nil {
		t.Fatalf("missing alerts: %v", got)
	}
	if got["node_cpu|pve-01"] != nil {
		t.Fatal("CPU alert must wait for the configured duration")
	}
	shared := 0
	for k := range got {
		if strings.HasPrefix(k, "storage_usage|ceph") {
			shared++
		}
	}
	if shared != 1 || got["storage_usage|ceph (shared)"]["severity"] != "critical" {
		t.Fatalf("shared storage must be reported once and as critical: %v", got)
	}
	if len(s.listAlerts()) != 4 {
		t.Fatalf("old tasks must not be replayed: %d alerts", len(s.listAlerts()))
	}

	s.evaluateAlerts(context.Background(), start.Add(6*time.Minute))
	if a := alertsByRule(s)["node_cpu|pve-01"]; a == nil || a["state"] != "firing" {
		t.Fatal("CPU alert expected after the hold period")
	}

	f.resources = []any{doc{"type": "node", "node": "pve-01", "status": "online", "cpu": 0.1, "mem": 10.0, "maxmem": 100.0}, doc{"type": "node", "node": "pve-02", "status": "online", "cpu": 0.1, "mem": 1.0, "maxmem": 100.0}}
	s.evaluateAlerts(context.Background(), start.Add(7*time.Minute))
	got = alertsByRule(s)
	for _, k := range []string{"node_cpu|pve-01", "node_offline|pve-02", "storage_usage|pve-01/local"} {
		if got[k]["state"] != "resolved" {
			t.Fatalf("%s should be resolved: %v", k, got[k])
		}
	}
	if got["task_failed|pve-01"]["state"] != "firing" {
		t.Fatal("task events stay open until acknowledged")
	}
	a := s.acknowledgeAlert(str(got["task_failed|pve-01"]["id"]), "Storage getauscht")
	if a["state"] != "resolved" || a["note"] != "Storage getauscht" {
		t.Fatalf("acknowledged event %v", a)
	}
}

func TestConnectionOutageKeepsNodeAlerts(t *testing.T) {
	s, f := alertServer(t)
	at := time.Now()
	f.resources = []any{doc{"type": "node", "node": "pve-02", "status": "offline"}}
	s.evaluateAlerts(context.Background(), at)
	f.down = true
	s.evaluateAlerts(context.Background(), at.Add(time.Minute))
	got := alertsByRule(s)
	if got["node_offline|pve-02"]["state"] != "firing" {
		t.Fatal("an unreachable cluster must not mark node alerts resolved")
	}
	if got["connection_error|Test"]["state"] != "firing" {
		t.Fatalf("connection alert expected: %v", got)
	}
}

func TestWebhookNotificationFormatsAndSecrets(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]string{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = string(raw)
		mu.Unlock()
		if r.URL.Path == "/fail" {
			w.WriteHeader(500)
		}
	}))
	defer hook.Close()
	s, f := alertServer(t)
	cfg := s.updateAlertConfig(doc{"channels": []any{
		doc{"name": "Teams", "kind": "webhook", "format": "teams", "url": hook.URL + "/teams?sig=SECRET", "enabled": true, "resolved": true},
		doc{"name": "JSON", "kind": "webhook", "format": "json", "url": hook.URL + "/json", "enabled": true, "resolved": false, "min_level": "critical"},
	}})
	public, _ := json.Marshal(s.publicAlertConfig(cfg))
	if strings.Contains(string(public), "SECRET") || strings.Contains(string(public), "/teams") {
		t.Fatalf("webhook secrets leaked: %s", public)
	}
	f.resources = []any{doc{"type": "node", "node": "pve-02", "status": "offline"}}
	s.evaluateAlerts(context.Background(), time.Now())
	s.Alerting.sending.Wait()
	mu.Lock()
	teams, plain := bodies["/teams"], bodies["/json"]
	mu.Unlock()
	if !strings.Contains(teams, "AdaptiveCard") || !strings.Contains(teams, "KRITISCH") || !strings.Contains(plain, `"rule":"node_offline"`) {
		t.Fatalf("payloads teams=%s json=%s", teams, plain)
	}

	// Keeping the stored URL when the form leaves it empty.
	ids := s.publicAlertConfig(s.alertConfig())["channels"].([]doc)
	cfg = s.updateAlertConfig(doc{"channels": []any{doc{"id": ids[0]["id"], "name": "Teams neu", "kind": "webhook", "format": "teams", "url": "", "enabled": true, "resolved": true}}})
	if len(cfg.Channels) != 1 || cfg.Channels[0].URL == "" || cfg.Channels[0].Name != "Teams neu" {
		t.Fatalf("existing secret must be kept: %+v", cfg.Channels)
	}

	bad := alertChannel{ID: "x", Kind: "webhook", Format: "json", URL: must(s.Store.Encrypt(hook.URL + "/fail")), Enabled: true}
	if err := s.notify(bad, doc{"title": "t", "state": "firing"}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("HTTP errors must be reported, got %v", err)
	}
}

func TestAlertConfigValidation(t *testing.T) {
	s, _ := alertServer(t)
	for _, bad := range []doc{
		{"channels": []any{doc{"name": "x", "kind": "webhook", "format": "json", "url": "ftp://host/x", "enabled": true}}},
		{"channels": []any{doc{"name": "x", "kind": "webhook", "format": "json", "url": "", "enabled": true}}},
		{"channels": []any{doc{"name": "x", "kind": "smtp", "host": "mail", "port": 25, "security": "none", "from": "a@b", "to": "c@d", "username": "u", "password": "p", "enabled": true}}},
		{"rules": doc{"node_cpu": doc{"enabled": true, "threshold": 20, "minutes": 5}}},
	} {
		if err := guarded(func() { s.updateAlertConfig(bad) }); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	cfg := s.updateAlertConfig(doc{"rules": doc{"node_cpu": doc{"enabled": true, "threshold": 80, "minutes": 10}, "task_failed": doc{"enabled": true, "backup_only": true}}})
	if cfg.Rules["node_cpu"].Threshold != 80 || !cfg.Rules["task_failed"].BackupOnly || !cfg.Rules["node_offline"].Enabled {
		t.Fatalf("rules %+v", cfg.Rules)
	}
}

func TestAlertRoutesRequireAuthAndCSRF(t *testing.T) {
	s, store := testServer(t)
	if w := request(s, "GET", "/api/alerts", "", nil, "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	cookie, csrf := setup(t, s, store)
	if w := request(s, "GET", "/api/alerts", "", cookie, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"rules"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/alerts/config", `{"rules":{}}`, cookie, "", s.Origin); w.Code != 403 {
		t.Fatalf("missing CSRF must be rejected: %d", w.Code)
	}
	if w := request(s, "POST", "/api/alerts/config", `{"rules":{}}`, cookie, csrf, s.Origin); w.Code != 200 {
		t.Fatalf("config: %d %s", w.Code, w.Body.String())
	}
}

func TestAlertsStayOpenWhenStateIsUnknown(t *testing.T) {
	s, f := alertServer(t)
	s.updateAlertConfig(doc{"rules": doc{"connection_error": doc{"enabled": false}}})
	at := time.Now()
	f.resources = []any{
		doc{"type": "node", "node": "pve-02", "status": "offline"},
		doc{"type": "storage", "node": "pve-01", "storage": "local", "status": "available", "disk": 95.0, "maxdisk": 100.0},
	}
	s.evaluateAlerts(context.Background(), at)
	// Outage with the connection rule disabled: nothing may be resolved.
	f.down = true
	s.evaluateAlerts(context.Background(), at.Add(time.Minute))
	got := alertsByRule(s)
	if got["node_offline|pve-02"]["state"] != "firing" || got["storage_usage|pve-01/local"]["state"] != "firing" {
		t.Fatalf("unreachable cluster must not resolve alerts: %v", got)
	}
	// Storage of an offline node: usage unknown, alert stays.
	f.down = false
	f.resources = []any{doc{"type": "node", "node": "pve-02", "status": "offline"}, doc{"type": "storage", "node": "pve-01", "storage": "local", "status": "unknown"}}
	s.evaluateAlerts(context.Background(), at.Add(2*time.Minute))
	if alertsByRule(s)["storage_usage|pve-01/local"]["state"] != "firing" {
		t.Fatal("unknown storage state must not resolve the alert")
	}
}

func TestAlertsOfRemovedConnectionResolve(t *testing.T) {
	s, f := alertServer(t)
	f.resources = []any{doc{"type": "node", "node": "pve-02", "status": "offline"}}
	s.evaluateAlerts(context.Background(), time.Now())
	if _, err := s.Store.DB.Exec("DELETE FROM connections"); err != nil {
		t.Fatal(err)
	}
	s.evaluateAlerts(context.Background(), time.Now().Add(time.Minute))
	a := alertsByRule(s)["node_offline|pve-02"]
	if a["state"] != "resolved" || !strings.Contains(str(a["message"]), "Verbindung entfernt") {
		t.Fatalf("alert of removed connection must close: %v", a)
	}
}
