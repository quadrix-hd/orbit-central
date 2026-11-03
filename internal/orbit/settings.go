package orbit

import (
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func defaultPreferences() doc {
	return doc{"theme": "cloud", "density": "comfortable", "font_size": "normal", "refresh_seconds": 30, "default_cpu": 2, "default_ram": 4, "default_disk": 32}
}
func (s *Server) preferences() doc {
	d := defaultPreferences()
	var raw string
	err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key='preferences'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return d
	}
	if err != nil {
		panic(err)
	}
	var saved doc
	if err = json.Unmarshal([]byte(raw), &saved); err != nil {
		panic(err)
	}
	for k := range d {
		if v, ok := saved[k]; ok {
			d[k] = v
		}
	}
	return d
}
func (s *Server) settingsRoutes(w http.ResponseWriter, r *http.Request, d doc, current *session) (handled bool, err error) {
	path, method := r.URL.Path, r.Method
	if method == "HEAD" {
		method = "GET"
	}
	err = guarded(func() {
		switch {
		case path == "/api/settings" && method == "GET":
			handled = true
			writeJSON(w, 200, s.preferences())
		case path == "/api/settings" && method == "POST":
			handled = true
			clean := doc{}
			for k, allowed := range map[string]string{"theme": "cloud,ocean,dusk", "density": "comfortable,compact", "font_size": "normal,large"} {
				v := field(d, k, 30, "")
				if !contains(allowed, v) {
					fail("Ungültige Einstellung: " + k)
				}
				clean[k] = v
			}
			seconds := integer(d, "refresh_seconds", 0, 120)
			if seconds != 0 && seconds != 15 && seconds != 30 && seconds != 60 && seconds != 120 {
				fail("Ungültiges Aktualisierungsintervall.")
			}
			clean["refresh_seconds"] = seconds
			clean["default_cpu"] = integer(d, "default_cpu", 1, 128)
			clean["default_ram"] = integer(d, "default_ram", 1, 1024)
			clean["default_disk"] = integer(d, "default_disk", 8, 65536)
			raw := must(json.Marshal(clean))
			if _, e := s.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('preferences',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw)); e != nil {
				panic(e)
			}
			_ = s.Store.Audit("settings.updated", "")
			writeJSON(w, 200, clean)
		case path == "/api/account/password" && method == "POST":
			handled = true
			if e := s.limitLogin(r); e != nil {
				panic(e)
			}
			old, valid := d["current_password"].(string)
			if !valid || len([]rune(old)) > 256 {
				fail("Aktuelles Passwort fehlt.")
			}
			password, ok := d["new_password"].(string)
			if !ok || len([]rune(password)) < 12 || len([]rune(password)) > 256 {
				fail("Neues Passwort muss 12–256 Zeichen enthalten.")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			var saved string
			if e := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&saved); e != nil {
				panic(e)
			}
			if !CheckPassword(old, saved) {
				panic(problem("Aktuelles Passwort ist nicht korrekt.", 403))
			}
			hash := must(PasswordHash(password, ""))
			tx := must(s.Store.DB.Begin())
			defer tx.Rollback()
			if _, e := tx.Exec("UPDATE settings SET value=? WHERE key='password'", hash); e != nil {
				panic(e)
			}
			if _, e := tx.Exec("DELETE FROM sessions WHERE id!=?", current.ID); e != nil {
				panic(e)
			}
			if e := tx.Commit(); e != nil {
				panic(e)
			}
			_ = s.Store.Audit("account.password.changed", "")
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/account/sessions" && method == "GET":
			handled = true
			rows := must(s.Store.DB.Query("SELECT id,expires FROM sessions WHERE expires>? ORDER BY expires DESC", now()))
			defer rows.Close()
			out := []any{}
			for rows.Next() {
				var id string
				var expires float64
				if e := rows.Scan(&id, &expires); e != nil {
					panic(e)
				}
				out = append(out, doc{"current": hmac.Equal([]byte(id), []byte(current.ID)), "expires": expires})
			}
			if e := rows.Err(); e != nil {
				panic(e)
			}
			writeJSON(w, 200, out)
		case path == "/api/account/sessions/revoke" && method == "POST":
			handled = true
			if d["acknowledge"] != true {
				fail("Abmelden anderer Sitzungen bestätigen.")
			}
			if _, e := s.Store.DB.Exec("DELETE FROM sessions WHERE id!=?", current.ID); e != nil {
				panic(e)
			}
			_ = s.Store.Audit("account.sessions.revoked", "")
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/catalog/source" && method == "POST":
			handled = true
			source := field(d, "url", 2000, "")
			catalogOrigin(source)
			if _, e := s.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('catalog_source',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", source); e != nil {
				panic(e)
			}
			_ = s.Store.Audit("catalog.source.saved", "")
			writeJSON(w, 200, doc{"ok": true, "restart_required": source != s.Catalog.URL})
		case strings.HasPrefix(path, "/api/connections/") && strings.HasSuffix(path, "/delete") && method == "POST":
			handled = true
			parts := strings.Split(path, "/")
			if len(parts) != 5 {
				fail("Ungültige Verbindung.")
			}
			c := s.conn(parts[3])
			if d["confirm_name"] != c.Name {
				fail("Verbindungsnamen bestätigen.")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, table := range []string{"jobs", "operations"} {
				for _, op := range s.allDocs(table) {
					if op["connection_id"] == c.ID && pending(op) {
						panic(problem("Offene Aufgaben zuerst abschließen oder prüfen.", 409))
					}
				}
			}
			rows := must(s.Store.Documents("blueprints"))
			for _, b := range rows {
				if b["connection_id"] == c.ID {
					panic(problem("Zugehörige Blueprints zuerst entfernen.", 409))
				}
			}
			if _, e := s.Store.DB.Exec("DELETE FROM connections WHERE id=?", c.ID); e != nil {
				panic(e)
			}
			_ = s.Store.Audit("connection.deleted", c.ID)
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/system/status" && method == "GET":
			handled = true
			var connections int
			if e := s.Store.DB.QueryRow("SELECT count(*) FROM connections").Scan(&connections); e != nil {
				panic(e)
			}
			var active int
			if e := s.Store.DB.QueryRow("SELECT count(*) FROM sessions WHERE expires>?", now()).Scan(&active); e != nil {
				panic(e)
			}
			ca := os.Getenv("ORBIT_CA_FILE")
			caState := "system"
			if ca != "" {
				caState = "configured"
				if _, e := os.Stat(ca); e != nil {
					caState = "unreadable"
				}
			}
			_, maintenance := os.Stat(filepath.Join(s.Store.Dir, "maintenance"))
			writeJSON(w, 200, doc{"version": Version, "runtime": "go", "public_origin": s.Origin, "ca": caState, "connections": connections, "active_sessions": active, "maintenance": maintenance == nil, "server_time": time.Now().UTC().Format(time.RFC3339), "catalog_source": s.Catalog.URL, "saved_catalog_source": s.catalogSource(), "catalog_interval_hours": 6})
		case path == "/api/system/backup" && method == "POST":
			handled = true
			path := must(Backup(s.Store.Dir))
			_ = s.Store.Audit("backup.created", "")
			w.Header().Set("Content-Disposition", attachment(filepath.Base(path)))
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, path)
		}
	})
	return
}

func (s *Server) catalogSource() string {
	var source string
	err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key='catalog_source'").Scan(&source)
	if err == nil {
		return source
	}
	return os.Getenv("ORBIT_CATALOG_URL")
}
