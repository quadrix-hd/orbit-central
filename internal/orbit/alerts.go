package orbit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Alerting: rule evaluation against live Proxmox state, durable alert
// records with firing/acknowledged/resolved states and notifications through
// webhooks (JSON, Teams, Slack/Mattermost, Discord, ntfy) or SMTP e-mail.

const alertInterval = 60 * time.Second

type alertRule struct {
	Enabled    bool    `json:"enabled"`
	Threshold  float64 `json:"threshold,omitempty"`
	Minutes    int     `json:"minutes,omitempty"`
	BackupOnly bool    `json:"backup_only,omitempty"`
}

type alertChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`             // webhook | smtp
	Format   string `json:"format,omitempty"` // json | teams | slack | discord | ntfy
	URL      string `json:"url,omitempty"`    // encrypted
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Security string `json:"security,omitempty"` // starttls | tls | none
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"` // encrypted
	Enabled  bool   `json:"enabled"`
	Resolved bool   `json:"resolved"`
	MinLevel string `json:"min_level"` // warning | critical
}

type alertConfig struct {
	Rules    map[string]alertRule `json:"rules"`
	Channels []alertChannel       `json:"channels"`
}

var alertRuleNames = []string{"connection_error", "node_offline", "node_cpu", "node_memory", "storage_usage", "task_failed"}

func defaultAlertConfig() alertConfig {
	return alertConfig{Rules: map[string]alertRule{
		"connection_error": {Enabled: true},
		"node_offline":     {Enabled: true},
		"node_cpu":         {Enabled: true, Threshold: 90, Minutes: 5},
		"node_memory":      {Enabled: true, Threshold: 90, Minutes: 5},
		"storage_usage":    {Enabled: true, Threshold: 85},
		"task_failed":      {Enabled: true},
	}, Channels: []alertChannel{}}
}

type alertEngine struct {
	mu      sync.Mutex
	pending map[string]time.Time
	cursor  map[string]float64
	status  map[string]doc
	running bool
}

func newAlertEngine() *alertEngine {
	return &alertEngine{pending: map[string]time.Time{}, cursor: map[string]float64{}, status: map[string]doc{}}
}

func (s *Server) alertConfig() alertConfig {
	cfg := defaultAlertConfig()
	var raw string
	if err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key='alerting'").Scan(&raw); err == nil {
		var saved alertConfig
		if json.Unmarshal([]byte(raw), &saved) == nil {
			for name, rule := range saved.Rules {
				if _, known := cfg.Rules[name]; known {
					cfg.Rules[name] = rule
				}
			}
			if saved.Channels != nil {
				cfg.Channels = saved.Channels
			}
		}
	}
	return cfg
}

func (s *Server) saveAlertConfig(cfg alertConfig) {
	raw := must(json.Marshal(cfg))
	if _, err := s.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('alerting',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw)); err != nil {
		panic(err)
	}
}

// publicAlertConfig never returns stored secrets; it only reports whether one
// is present and a non-secret hint so the UI can show what is configured.
func (s *Server) publicAlertConfig(cfg alertConfig) doc {
	channels := []doc{}
	for _, c := range cfg.Channels {
		d := doc{"id": c.ID, "name": c.Name, "kind": c.Kind, "format": c.Format, "enabled": c.Enabled, "resolved": c.Resolved, "min_level": c.MinLevel, "host": c.Host, "port": c.Port, "security": c.Security, "from": c.From, "to": c.To, "username": c.Username, "has_url": c.URL != "", "has_password": c.Password != ""}
		if c.URL != "" {
			if plain, err := s.Store.Decrypt(c.URL); err == nil {
				if u, e := url.Parse(plain); e == nil {
					d["url_hint"] = u.Scheme + "://" + u.Host + "/…"
				}
			}
		}
		s.Alerting.mu.Lock()
		if st, ok := s.Alerting.status[c.ID]; ok {
			d["last"] = st
		}
		s.Alerting.mu.Unlock()
		channels = append(channels, d)
	}
	return doc{"rules": cfg.Rules, "channels": channels}
}

func ruleInput(input doc, name string, old alertRule) alertRule {
	raw, ok := input[name].(map[string]any)
	if !ok {
		return old
	}
	r := alertRule{Enabled: raw["enabled"] == true}
	switch name {
	case "node_cpu", "node_memory":
		r.Threshold = float64(integer(raw, "threshold", 50, 100))
		r.Minutes = integer(raw, "minutes", 1, 120)
	case "storage_usage":
		r.Threshold = float64(integer(raw, "threshold", 50, 99))
	case "task_failed":
		r.BackupOnly = raw["backup_only"] == true
	}
	return r
}

func (s *Server) channelInput(raw doc, old *alertChannel) alertChannel {
	c := alertChannel{
		ID:       defaultText(str(raw["id"]), randomID()),
		Name:     field(raw, "name", 60, ""),
		Kind:     field(raw, "kind", 10, "webhook|smtp"),
		Enabled:  raw["enabled"] == true,
		Resolved: raw["resolved"] == true,
		MinLevel: defaultText(str(raw["min_level"]), "warning"),
	}
	if !contains("warning,critical", c.MinLevel) {
		fail("Ungültige Mindeststufe.")
	}
	if c.Kind == "webhook" {
		c.Format = field(raw, "format", 10, "json|teams|slack|discord|ntfy")
		if plain := strings.TrimSpace(str(raw["url"])); plain != "" {
			u, err := url.Parse(plain)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(plain) > 2000 {
				fail("Webhook-URL muss mit http:// oder https:// beginnen.")
			}
			c.URL = must(s.Store.Encrypt(plain))
		} else if old != nil && old.Kind == "webhook" {
			c.URL = old.URL
		} else {
			fail("Webhook-URL fehlt.")
		}
		return c
	}
	c.Host = field(raw, "host", 253, `[a-zA-Z0-9.-]+`)
	c.Port = integer(raw, "port", 1, 65535)
	c.Security = field(raw, "security", 10, "starttls|tls|none")
	c.From = field(raw, "from", 254, `[^\s@<>]+@[^\s@<>]+`)
	c.To = field(raw, "to", 1000, `[^\s@<>,]+@[^\s@<>,]+(?:\s*,\s*[^\s@<>,]+@[^\s@<>,]+){0,19}`)
	c.Username = strings.TrimSpace(str(raw["username"]))
	if len(c.Username) > 254 {
		fail("Benutzername zu lang.")
	}
	if pw := str(raw["password"]); pw != "" {
		if len(pw) > 512 {
			fail("Passwort zu lang.")
		}
		c.Password = must(s.Store.Encrypt(pw))
	} else if old != nil && old.Kind == "smtp" && c.Username != "" {
		c.Password = old.Password
	}
	if c.Username != "" && c.Password == "" {
		fail("SMTP-Passwort fehlt.")
	}
	if c.Username != "" && c.Security == "none" {
		fail("SMTP-Anmeldung nur mit TLS oder STARTTLS.")
	}
	return c
}

func (s *Server) updateAlertConfig(d doc) alertConfig {
	cfg := s.alertConfig()
	if rules, ok := d["rules"].(map[string]any); ok {
		for _, name := range alertRuleNames {
			cfg.Rules[name] = ruleInput(rules, name, cfg.Rules[name])
		}
	}
	if list, ok := d["channels"].([]any); ok {
		if len(list) > 10 {
			fail("Höchstens 10 Benachrichtigungskanäle.")
		}
		old := map[string]alertChannel{}
		for _, c := range cfg.Channels {
			old[c.ID] = c
		}
		channels := []alertChannel{}
		for _, item := range list {
			raw, ok := item.(map[string]any)
			if !ok {
				fail("Ungültiger Kanal.")
			}
			var previous *alertChannel
			if c, ok := old[str(raw["id"])]; ok {
				previous = &c
			}
			channels = append(channels, s.channelInput(raw, previous))
		}
		cfg.Channels = channels
	}
	s.saveAlertConfig(cfg)
	return cfg
}

// Alert records ---------------------------------------------------------------

func alertID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:10])
}

func (s *Server) readAlert(id string) doc {
	var raw string
	err := s.Store.DB.QueryRow("SELECT data FROM alerts WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	var d doc
	must(0, json.Unmarshal([]byte(raw), &d))
	return d
}

func (s *Server) writeAlert(d doc) {
	if _, err := s.Store.DB.Exec("INSERT INTO alerts(id,data,updated) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated=excluded.updated", d["id"], string(must(json.Marshal(d))), now()); err != nil {
		panic(err)
	}
}

func (s *Server) listAlerts() []doc {
	rows, err := s.Store.DB.Query("SELECT data FROM alerts ORDER BY updated DESC LIMIT 500")
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	out := []doc{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			panic(err)
		}
		var d doc
		if json.Unmarshal([]byte(raw), &d) == nil {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		rank := func(d doc) int {
			if d["state"] != "firing" {
				return 2
			}
			if d["severity"] == "critical" {
				return 0
			}
			return 1
		}
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return num(out[i]["last_seen"]) > num(out[j]["last_seen"])
	})
	return out
}

func (s *Server) acknowledgeAlert(id, note string) doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.readAlert(id)
	if a == nil {
		panic(problem("Alarm nicht gefunden.", 404))
	}
	a["acknowledged"] = true
	a["acknowledged_at"] = now()
	a["note"] = note
	if a["kind"] == "event" && a["state"] == "firing" {
		a["state"] = "resolved"
		a["resolved_at"] = now()
	}
	s.writeAlert(a)
	_ = s.Store.Audit("alert.acknowledged", id)
	return a
}

// Evaluation ------------------------------------------------------------------

type finding struct {
	id, rule, severity, kind, connection, object, title, message string
	value                                                        float64
}

func (s *Server) evaluateAlerts(ctx context.Context, at time.Time) {
	cfg := s.alertConfig()
	conns, err := s.Store.Connections()
	if err != nil {
		panic(err)
	}
	active := map[string]finding{}
	evaluated := map[string]bool{}
	for _, c := range conns {
		if c.Kind != "pve" {
			continue
		}
		evaluated[c.ID] = true
		for _, f := range s.connectionFindings(ctx, c, cfg, at) {
			active[f.id] = f
		}
	}
	s.applyFindings(cfg, active, evaluated, at)
}

func (s *Server) connectionFindings(ctx context.Context, c Connection, cfg alertConfig, at time.Time) []finding {
	out := []finding{}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := s.PVE.Call(reqCtx, c, "GET", "/cluster/resources", nil)
	if err != nil {
		if cfg.Rules["connection_error"].Enabled {
			msg := "Proxmox-API nicht erreichbar."
			var pr *Problem
			if errors.As(err, &pr) {
				msg = pr.Message
			}
			out = append(out, finding{id: alertID("connection_error", c.ID), rule: "connection_error", severity: "critical", kind: "state", connection: c.ID, object: c.Name, title: "Cluster nicht erreichbar: " + c.Name, message: msg})
		}
		return out
	}
	hold := func(f finding, minutes int) {
		s.Alerting.mu.Lock()
		defer s.Alerting.mu.Unlock()
		since, ok := s.Alerting.pending[f.id]
		if !ok {
			s.Alerting.pending[f.id] = at
			since = at
		}
		if at.Sub(since) >= time.Duration(minutes)*time.Minute {
			out = append(out, f)
		}
	}
	seenShared := map[string]bool{}
	for _, r := range objects(data) {
		switch r["type"] {
		case "node":
			name := str(r["node"])
			if r["status"] != "online" {
				if cfg.Rules["node_offline"].Enabled {
					out = append(out, finding{id: alertID("node_offline", c.ID, name), rule: "node_offline", severity: "critical", kind: "state", connection: c.ID, object: name, title: "Host offline: " + name, message: "Proxmox meldet den Host als " + defaultText(str(r["status"]), "unbekannt") + "."})
				}
				continue
			}
			if rule := cfg.Rules["node_cpu"]; rule.Enabled {
				cpu := math.Round(num(r["cpu"]) * 100)
				if cpu >= rule.Threshold {
					hold(finding{id: alertID("node_cpu", c.ID, name), rule: "node_cpu", severity: severityFor(cpu, rule.Threshold), kind: "state", connection: c.ID, object: name, value: cpu, title: fmt.Sprintf("CPU-Last %s: %.0f %%", name, cpu), message: fmt.Sprintf("CPU-Auslastung seit mindestens %d min über %.0f %%.", rule.Minutes, rule.Threshold)}, rule.Minutes)
				} else {
					s.clearPending(alertID("node_cpu", c.ID, name))
				}
			}
			if rule := cfg.Rules["node_memory"]; rule.Enabled && num(r["maxmem"]) > 0 {
				mem := math.Round(num(r["mem"]) / num(r["maxmem"]) * 100)
				if mem >= rule.Threshold {
					hold(finding{id: alertID("node_memory", c.ID, name), rule: "node_memory", severity: severityFor(mem, rule.Threshold), kind: "state", connection: c.ID, object: name, value: mem, title: fmt.Sprintf("Arbeitsspeicher %s: %.0f %%", name, mem), message: fmt.Sprintf("RAM-Belegung seit mindestens %d min über %.0f %%.", rule.Minutes, rule.Threshold)}, rule.Minutes)
				} else {
					s.clearPending(alertID("node_memory", c.ID, name))
				}
			}
		case "storage":
			rule := cfg.Rules["storage_usage"]
			if !rule.Enabled || r["status"] != "available" || num(r["maxdisk"]) <= 0 {
				continue
			}
			name, node := str(r["storage"]), str(r["node"])
			object := node + "/" + name
			if truth(r["shared"]) {
				if seenShared[name] {
					continue
				}
				seenShared[name] = true
				object = name + " (shared)"
			}
			used := math.Round(num(r["disk"]) / num(r["maxdisk"]) * 100)
			if used >= rule.Threshold {
				out = append(out, finding{id: alertID("storage_usage", c.ID, object), rule: "storage_usage", severity: severityFor(used, rule.Threshold), kind: "state", connection: c.ID, object: object, value: used, title: fmt.Sprintf("Storage %s: %.0f %% belegt", object, used), message: fmt.Sprintf("Belegung über %.0f %%. Verbleibend %s.", rule.Threshold, bytesText(num(r["maxdisk"])-num(r["disk"])))})
			}
		}
	}
	if rule := cfg.Rules["task_failed"]; rule.Enabled {
		out = append(out, s.taskFindings(reqCtx, c, rule, at)...)
	}
	return out
}

func (s *Server) clearPending(id string) {
	s.Alerting.mu.Lock()
	delete(s.Alerting.pending, id)
	s.Alerting.mu.Unlock()
}

func severityFor(value, threshold float64) string {
	if value >= 95 || value >= threshold+((100-threshold)/2) {
		return "critical"
	}
	return "warning"
}

func bytesText(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// taskFindings reports Proxmox tasks that finished with an error since the
// previous evaluation. The first evaluation only looks back ten minutes so a
// restart does not replay a day of history as new alerts.
func (s *Server) taskFindings(ctx context.Context, c Connection, rule alertRule, at time.Time) []finding {
	data, err := s.PVE.Call(ctx, c, "GET", "/cluster/tasks", nil)
	if err != nil {
		return nil
	}
	s.Alerting.mu.Lock()
	cursor, ok := s.Alerting.cursor[c.ID]
	if !ok {
		cursor = float64(at.Add(-10 * time.Minute).Unix())
	}
	s.Alerting.mu.Unlock()
	newest := cursor
	out := []finding{}
	for _, t := range objects(data) {
		end := num(t["endtime"])
		if end <= cursor {
			continue
		}
		if end > newest {
			newest = end
		}
		status := str(t["status"])
		if status == "OK" || status == "" || strings.HasPrefix(status, "WARNINGS") {
			continue
		}
		kind := str(t["type"])
		if rule.BackupOnly && kind != "vzdump" {
			continue
		}
		label := map[string]string{"vzdump": "Backup", "qmigrate": "Migration", "vzmigrate": "Migration", "qmclone": "Klon", "qmstart": "VM-Start", "vzstart": "CT-Start", "qmsnapshot": "Snapshot", "hamigrate": "HA-Migration"}[kind]
		if label == "" {
			label = "Task " + kind
		}
		target := str(t["id"])
		title := label + " fehlgeschlagen"
		if target != "" {
			title += ": " + target
		}
		out = append(out, finding{id: alertID("task_failed", c.ID, str(t["upid"])), rule: "task_failed", severity: map[bool]string{true: "critical", false: "warning"}[kind == "vzdump"], kind: "event", connection: c.ID, object: str(t["node"]), title: title + " (" + str(t["node"]) + ")", message: status})
	}
	s.Alerting.mu.Lock()
	s.Alerting.cursor[c.ID] = newest
	s.Alerting.mu.Unlock()
	return out
}

func (s *Server) applyFindings(cfg alertConfig, active map[string]finding, evaluated map[string]bool, at time.Time) {
	ts := float64(at.UnixMilli()) / 1000
	notify := []doc{}
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		notify = s.recordFindings(active, evaluated, ts)
	}()
	for _, a := range notify {
		s.notifyAll(cfg, a)
	}
}

func (s *Server) recordFindings(active map[string]finding, evaluated map[string]bool, ts float64) []doc {
	notify := []doc{}
	existing := s.listAlerts()
	known := map[string]doc{}
	for _, a := range existing {
		known[str(a["id"])] = a
	}
	for id, f := range active {
		a := known[id]
		if a == nil || a["state"] == "resolved" {
			a = doc{"id": id, "rule": f.rule, "kind": f.kind, "connection_id": f.connection, "object": f.object, "first_seen": ts, "acknowledged": false}
			a["state"] = "firing"
			notify = append(notify, a)
		}
		a["severity"], a["title"], a["message"], a["value"], a["last_seen"] = f.severity, f.title, f.message, f.value, ts
		s.writeAlert(a)
	}
	for _, a := range existing {
		id := str(a["id"])
		if a["state"] != "firing" || a["kind"] == "event" {
			continue
		}
		if _, still := active[id]; still || !evaluated[str(a["connection_id"])] {
			continue
		}
		// A connection outage hides node and storage state; keep those alerts
		// as they are instead of reporting them resolved.
		if a["rule"] != "connection_error" {
			if _, down := active[alertID("connection_error", str(a["connection_id"]))]; down {
				continue
			}
		}
		a["state"] = "resolved"
		a["resolved_at"] = ts
		s.writeAlert(a)
		notify = append(notify, a)
	}
	if _, err := s.Store.DB.Exec("DELETE FROM alerts WHERE updated < ? AND data LIKE '%\"state\":\"resolved\"%'", ts-14*86400); err != nil {
		log.Print("alert_cleanup_failed")
	}
	return notify
}

func (s *Server) RunAlerts(ctx context.Context) {
	s.Alerting.mu.Lock()
	if s.Alerting.running {
		s.Alerting.mu.Unlock()
		return
	}
	s.Alerting.running = true
	s.Alerting.mu.Unlock()
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if err := guarded(func() { s.evaluateAlerts(ctx, time.Now()) }); err != nil && ctx.Err() == nil {
				log.Print("alert_evaluation_failed")
			}
			timer.Reset(alertInterval)
		}
	}()
}
