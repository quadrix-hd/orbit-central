package orbit

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) alertRoutes(w http.ResponseWriter, r *http.Request, d doc) (handled bool, err error) {
	path, method := r.URL.Path, r.Method
	if method == "HEAD" {
		method = "GET"
	}
	if !strings.HasPrefix(path, "/api/alerts") {
		return false, nil
	}
	err = guarded(func() {
		switch {
		case path == "/api/alerts" && method == "GET":
			handled = true
			alerts := s.listAlerts()
			firing := 0
			for _, a := range alerts {
				if a["state"] == "firing" && a["acknowledged"] != true {
					firing++
				}
			}
			writeJSON(w, 200, doc{"alerts": alerts, "open": firing, "config": s.publicAlertConfig(s.alertConfig()), "interval_seconds": int(alertInterval / time.Second)})
		case path == "/api/alerts/config" && method == "POST":
			handled = true
			cfg := s.updateAlertConfig(d)
			_ = s.Store.Audit("alerts.config.updated", "")
			writeJSON(w, 200, s.publicAlertConfig(cfg))
		case path == "/api/alerts/evaluate" && method == "POST":
			handled = true
			s.evaluateAlerts(r.Context(), time.Now())
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/alerts/test" && method == "POST":
			handled = true
			id := field(d, "channel_id", 64, `[a-zA-Z0-9_-]+`)
			for _, c := range s.alertConfig().Channels {
				if c.ID == id {
					sample := doc{"id": "test", "rule": "test", "state": "firing", "severity": "warning", "title": "Testbenachrichtigung", "message": "Dieser Kanal ist korrekt eingerichtet.", "object": "Orbit Central", "connection_id": "", "last_seen": now()}
					if e := s.notify(c, sample); e != nil {
						panic(problem(e.Error(), 502))
					}
					_ = s.Store.Audit("alerts.test.sent", id)
					writeJSON(w, 200, doc{"ok": true})
					return
				}
			}
			panic(problem("Kanal nicht gefunden.", 404))
		case strings.HasSuffix(path, "/ack") && method == "POST":
			handled = true
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/alerts/"), "/ack")
			if !anchored(`[0-9a-f]{20}`).MatchString(id) {
				fail("Ungültige Alarm-ID.")
			}
			note := strings.TrimSpace(str(d["note"]))
			if len([]rune(note)) > 300 {
				fail("Notiz zu lang.")
			}
			writeJSON(w, 200, s.acknowledgeAlert(id, note))
		}
	})
	return
}
