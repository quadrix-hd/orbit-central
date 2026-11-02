package orbit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const Version = "0.12.0"

type Server struct {
	Store          *Store
	PVE            *Proxmox
	Origin, Static string
	mu             sync.Mutex
	attempts       map[string][]time.Time
	Catalog        *Catalog
	Console        *Console
	ctx            context.Context
	workers        sync.WaitGroup
	liveClients    int
	metricsMu      sync.Mutex
	metricsCache   map[string]metricCache
	metricsRunning map[string]chan struct{}
}

func NewServer(s *Store, p *Proxmox, origin, static string) *Server {
	server := &Server{Store: s, PVE: p, Origin: strings.TrimRight(origin, "/"), Static: static, attempts: map[string][]time.Time{}, Catalog: NewCatalog(s.Dir, os.Getenv("ORBIT_CATALOG_URL"), os.Getenv("ORBIT_CATALOG_MODE")), Console: newConsole(), ctx: context.Background()}
	server.Catalog = NewCatalog(s.Dir, server.catalogSource(), os.Getenv("ORBIT_CATALOG_MODE"))
	return server
}

type session struct{ ID, CSRF string }

func hashToken(token string) string {
	b := sha256.Sum256([]byte(token))
	return hex.EncodeToString(b[:])
}
func (s *Server) session(r *http.Request) (*session, error) {
	cookie, err := r.Cookie("orbit_session")
	if err != nil {
		return nil, nil
	}
	var csrf string
	id := hashToken(cookie.Value)
	err = s.Store.DB.QueryRow("SELECT csrf FROM sessions WHERE id=? AND expires>?", id, float64(time.Now().UnixMilli())/1000).Scan(&csrf)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &session{id, csrf}, nil
}
func (s *Server) newSession(w http.ResponseWriter) (string, error) {
	token, csrf := randomToken(32), randomToken(32)
	_, err := s.Store.DB.Exec("INSERT INTO sessions VALUES(?,?,?)", hashToken(token), csrf, float64(time.Now().Unix()+12*3600))
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: "orbit_session", Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	return csrf, nil
}
func (s *Server) limitLogin(r *http.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	for ip, attempts := range s.attempts {
		recent := []time.Time{}
		for _, at := range attempts {
			if now.Sub(at) < 5*time.Minute {
				recent = append(recent, at)
			}
		}
		if len(recent) == 0 {
			delete(s.attempts, ip)
		} else {
			s.attempts[ip] = recent
		}
	}
	if len(s.attempts[host]) >= 10 {
		return problem("Zu viele Anmeldeversuche. Bitte fünf Minuten warten.", 429)
	}
	s.attempts[host] = append(s.attempts[host], now)
	return nil
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (s *Server) ServeHTTP(writer http.ResponseWriter, r *http.Request) {
	started := time.Now()
	id := randomToken(12)
	w := &statusWriter{ResponseWriter: writer}
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("request_id=%s internal_panic", id)
			if w.status == 0 {
				writeJSON(w, 500, map[string]any{"error": "Interner Fehler.", "request_id": id})
			}
		}
		log.Printf("request_id=%s method=%s path=%s status=%d duration_ms=%d", id, r.Method, r.URL.EscapedPath(), w.status, time.Since(started).Milliseconds())
	}()
	if err := s.route(w, r); err != nil {
		var pr *Problem
		status, message := 500, "Interner Fehler. Anfrage-ID im Protokoll prüfen."
		if errors.As(err, &pr) {
			status, message = pr.Status, pr.Message
		} else {
			log.Printf("request_id=%s internal_error", id)
		}
		writeJSON(w, status, map[string]any{"error": message, "request_id": id})
	}
}
func readBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return nil, problem("JSON erwartet.", 415)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			return nil, problem("Anfrage zu groß.", 413)
		}
		return nil, problem("Ungültiges JSON.", 400)
	}
	if data == nil {
		return nil, problem("JSON-Objekt erwartet.", 400)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, problem("Ungültiges JSON.", 400)
	}
	return data, nil
}
func text(data map[string]any, key string, max int, pattern string) (string, error) {
	v, ok := data[key].(string)
	v = strings.TrimSpace(v)
	if !ok || v == "" || utf8.RuneCountInString(v) > max || (pattern != "" && !regexp.MustCompile("^(?:"+pattern+")$").MatchString(v)) {
		return "", problem("Ungültiges Feld: "+key, 400)
	}
	return v, nil
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	method := r.Method
	if method == "HEAD" {
		method = "GET"
	}
	if !strings.HasPrefix(path, "/api/") {
		if method != "GET" {
			return problem("Nicht erlaubt.", 405)
		}
		if strings.HasPrefix(path, "/vendor/novnc/") {
			return s.novnc(w, r)
		}
		return s.static(w, r)
	}
	if method != "GET" && method != "POST" && method != "DELETE" {
		return problem("Nicht erlaubt.", 405)
	}
	mutating := method == "POST" || method == "DELETE"
	if mutating && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.Origin {
		return problem("Fremder Ursprung nicht erlaubt.", 403)
	}
	data := map[string]any{}
	var err error
	if method == "POST" {
		data, err = readBody(w, r)
		if err != nil {
			return err
		}
	}
	if path == "/api/health" && method == "GET" {
		if err = s.Store.DB.PingContext(r.Context()); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"ok": true, "version": Version, "mode": "live", "runtime": "go"})
		return nil
	}
	if path == "/api/auth" && method == "GET" {
		session, err := s.session(r)
		if err != nil {
			return err
		}
		var csrf any
		if session != nil {
			csrf = session.CSRF
		}
		writeJSON(w, 200, map[string]any{"configured": s.Store.Configured(), "authenticated": session != nil, "csrf": csrf, "version": Version, "runtime": "go"})
		return nil
	}
	if path == "/api/setup" && method == "POST" {
		if err = s.limitLogin(r); err != nil {
			return err
		}
		password, ok := data["password"].(string)
		if !ok || utf8.RuneCountInString(password) < 12 || utf8.RuneCountInString(password) > 256 {
			return problem("Passwort muss 12–256 Zeichen enthalten.", 400)
		}
		// Serialize the one-time check and insertion with other setup calls.
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.Store.Configured() {
			return problem("Bereits eingerichtet.", 409)
		}
		expected, err := os.ReadFile(filepath.Join(s.Store.Dir, "setup-token"))
		token, _ := data["token"].(string)
		if err != nil || !hmac.Equal([]byte(token), []byte(strings.TrimSpace(string(expected)))) {
			return problem("Einrichtungscode ungültig.", 403)
		}
		hashed, err := PasswordHash(password, "")
		if err != nil {
			return err
		}
		if _, err = s.Store.DB.Exec("INSERT INTO settings VALUES('password',?)", hashed); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(s.Store.Dir, "setup-token")); err != nil {
			log.Print("setup_token_cleanup_failed")
		}
		csrf, err := s.newSession(w)
		if err != nil {
			return err
		}
		if err = s.Store.Audit("setup.completed", ""); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"csrf": csrf})
		return nil
	}
	if path == "/api/login" && method == "POST" {
		if err = s.limitLogin(r); err != nil {
			return err
		}
		password, ok := data["password"].(string)
		if !ok || utf8.RuneCountInString(password) > 256 {
			return problem("Anmeldung fehlgeschlagen.", 401)
		}
		var saved string
		err = s.Store.DB.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&saved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if saved == "" {
			saved, _ = PasswordHash("unconfigured", "00112233445566778899aabbccddeeff")
		}
		if !CheckPassword(password, saved) || !s.Store.Configured() {
			return problem("Anmeldung fehlgeschlagen.", 401)
		}
		csrf, err := s.newSession(w)
		if err != nil {
			return err
		}
		if err = s.Store.Audit("auth.login", ""); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"csrf": csrf})
		return nil
	}
	if mutating {
		if _, err = os.Stat(filepath.Join(s.Store.Dir, "maintenance")); err == nil {
			return problem("Wartung läuft. Bitte später erneut versuchen.", 503)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	session, err := s.session(r)
	if err != nil {
		return err
	}
	if session == nil {
		return problem("Bitte anmelden.", 401)
	}
	if mutating && !hmac.Equal([]byte(r.Header.Get("X-Orbit-CSRF")), []byte(session.CSRF)) {
		return problem("Sitzung ungültig. Bitte neu anmelden.", 403)
	}
	if path == "/api/logout" && method == "POST" {
		if _, err = s.Store.DB.Exec("DELETE FROM sessions WHERE id=?", session.ID); err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{Name: "orbit_session", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
		return nil
	}
	if path == "/api/events" && method == "GET" {
		return s.events(w, r, session)
	}
	if handled, err := s.infraRoutes(w, r); handled || err != nil {
		return err
	}
	if handled, err := s.settingsRoutes(w, r, data, session); handled || err != nil {
		return err
	}
	if handled, err := s.extraRoutes(w, r, data, session); handled || err != nil {
		return err
	}
	if path == "/api/audit" && method == "GET" {
		rows, err := s.Store.DB.Query("SELECT id,at,action,details FROM audit ORDER BY id DESC LIMIT 200")
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int
			var at float64
			var action, details string
			if err = rows.Scan(&id, &at, &action, &details); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "at": at, "action": action, "details": details})
		}
		if err = rows.Err(); err != nil {
			return err
		}
		writeJSON(w, 200, out)
		return nil
	}
	if path == "/api/connections" {
		if method == "GET" {
			rows, err := s.Store.Connections()
			if err != nil {
				return err
			}
			for i := range rows {
				rows[i].ActiveEndpoint = s.PVE.Active(rows[i])
			}
			writeJSON(w, 200, rows)
			return nil
		}
		if method == "POST" {
			return s.saveConnection(w, r, data, "")
		}
	}
	if strings.HasPrefix(path, "/api/connections/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/connections/"), "/")
		if len(parts) == 1 && method == "POST" {
			return s.saveConnection(w, r, data, parts[0])
		}
		conn, err := s.Store.Connection(parts[0])
		if errors.Is(err, sql.ErrNoRows) {
			return problem("Verbindung nicht gefunden.", 404)
		}
		if err != nil {
			return err
		}
		if method == "GET" {
			return s.connectionRead(w, r, conn, parts[1:])
		}
	}
	if method == "GET" && (path == "/api/jobs" || path == "/api/operations" || path == "/api/blueprints") {
		docs, err := s.Store.Documents(strings.TrimPrefix(path, "/api/"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, docs)
		return nil
	}
	return problem("Nicht gefunden.", 404)
}

var tokenPattern = regexp.MustCompile(`^[^\s=]+![^\s=]+=[^\s]+$`)

func endpointsInput(v any) ([]string, error) {
	if v == nil {
		return []string{}, nil
	}
	if text, ok := v.(string); ok {
		out := []string{}
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
		return out, nil
	}
	if list, ok := v.([]any); ok {
		out := []string{}
		for _, item := range list {
			value, ok := item.(string)
			if !ok {
				return nil, problem("Ungültige Cluster-Adresse.", 400)
			}
			out = append(out, value)
		}
		return out, nil
	}
	return nil, problem("Ungültige Cluster-Adressen.", 400)
}
func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request, data map[string]any, id string) error {
	create := id == ""
	conn := Connection{ID: randomID(), Kind: "pve", Created: float64(time.Now().UnixMilli()) / 1000, Endpoints: []string{}}
	var err error
	if !create {
		conn, err = s.Store.Connection(id)
		if errors.Is(err, sql.ErrNoRows) {
			return problem("Verbindung nicht gefunden.", 404)
		}
		if err != nil {
			return err
		}
	}
	conn.Name, err = text(data, "name", 80, "")
	if err != nil {
		return err
	}
	primary := conn.URL
	if v, exists := data["url"]; exists {
		var ok bool
		primary, ok = v.(string)
		if !ok {
			return problem("Ungültige Adresse.", 400)
		}
	}
	extra := conn.Endpoints
	if v, exists := data["endpoints"]; exists {
		extra, err = endpointsInput(v)
		if err != nil {
			return err
		}
	}
	all, err := ValidateEndpoints(primary, extra)
	if err != nil {
		return err
	}
	conn.URL, conn.Endpoints = all[0], all[1:]
	if create {
		if v, exists := data["kind"]; exists {
			conn.Kind, _ = v.(string)
		}
		if conn.Kind != "pve" && conn.Kind != "pbs" {
			return problem("Verbindungstyp muss pve oder pbs sein.", 400)
		}
	}
	token := ""
	if v, exists := data["token"]; exists {
		var ok bool
		token, ok = v.(string)
		if !ok {
			return problem("Ungültiges Token-Format.", 400)
		}
	}
	if token != "" || create {
		if len(token) > 500 || !tokenPattern.MatchString(token) {
			return problem("Ungültiges Token-Format.", 400)
		}
		conn.Token, err = s.Store.Encrypt(token)
		if err != nil {
			return err
		}
	}
	if err = s.PVE.VerifyEndpoints(r.Context(), conn); err != nil {
		return err
	}
	version, err := s.PVE.Call(r.Context(), conn, "GET", "/version", nil)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var duplicate int
	err = s.Store.DB.QueryRow("SELECT 1 FROM connections WHERE url=? AND id!=?", conn.URL, conn.ID).Scan(&duplicate)
	if err == nil {
		return problem("Adresse bereits verbunden.", 409)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = s.Store.SaveConnection(conn, create); err != nil {
		return err
	}
	action := "connection.updated"
	status := 200
	if create {
		action = "connection.created"
		status = 201
	}
	if err = s.Store.Audit(action, conn.ID); err != nil {
		return err
	}
	ver, _ := version.(map[string]any)
	writeJSON(w, status, map[string]any{"ok": true, "id": conn.ID, "name": conn.Name, "url": conn.URL, "kind": conn.Kind, "version": ver["version"]})
	return nil
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) error {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	allowed := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "management.js": "text/javascript; charset=utf-8", "live.js": "text/javascript; charset=utf-8", "infrastructure.js": "text/javascript; charset=utf-8", "vm-workspace.js": "text/javascript; charset=utf-8", "bootstrap.js": "text/javascript; charset=utf-8", "app.css": "text/css; charset=utf-8", "orbit-sans-regular.woff": "font/woff", "orbit-sans-bold.woff": "font/woff"}
	mime, ok := allowed[name]
	if !ok {
		return problem("Nicht gefunden.", 404)
	}
	raw, err := os.ReadFile(filepath.Join(s.Static, name))
	if errors.Is(err, os.ErrNotExist) {
		return problem("Nicht gefunden.", 404)
	}
	if err != nil {
		return err
	}
	if name == "index.html" {
		html := strings.ReplaceAll(string(raw), `name="orbit-mode" content="demo"`, `name="orbit-mode" content="live"`)
		raw = []byte(html)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(200)
	if r.Method != "HEAD" {
		_, err = w.Write(raw)
	}
	return err
}
func (s *Server) safeRead(r *http.Request, c Connection, path string, fields url.Values) map[string]any {
	data, err := s.PVE.Call(r.Context(), c, "GET", path, fields)
	if err != nil {
		var pr *Problem
		message := "Daten konnten nicht gelesen werden."
		if errors.As(err, &pr) {
			message = pr.Message
		}
		return map[string]any{"ok": false, "error": message}
	}
	return map[string]any{"ok": true, "data": data}
}
func jsonData(w http.ResponseWriter, data any, err error) error {
	if err != nil {
		return err
	}
	writeJSON(w, 200, data)
	return nil
}
func (s *Server) connectionRead(w http.ResponseWriter, r *http.Request, c Connection, parts []string) error {
	if len(parts) == 1 {
		section := parts[0]
		if section == "inventory" || section == "templates" {
			if c.Kind == "pbs" {
				return problem("Diese Funktion benötigt Proxmox VE.", 400)
			}
			data, err := s.PVE.Call(r.Context(), c, "GET", "/cluster/resources", nil)
			if err != nil {
				return err
			}
			if section == "templates" {
				out := []map[string]any{}
				for _, row := range objects(data) {
					if row["type"] == "qemu" && row["template"] == float64(1) {
						out = append(out, row)
					}
				}
				data = out
			}
			return jsonData(w, data, nil)
		}
		if section == "nodes" {
			data, err := s.PVE.Call(r.Context(), c, "GET", "/nodes", nil)
			return jsonData(w, data, err)
		}
		if section == "targets" {
			if c.Kind == "pbs" {
				return problem("Proxmox VE benötigt.", 400)
			}
			data, err := s.PVE.Call(r.Context(), c, "GET", "/nodes", nil)
			if err != nil {
				return err
			}
			out := []map[string]any{}
			for _, node := range objects(data) {
				if node["status"] != "online" {
					continue
				}
				name, _ := node["node"].(string)
				stores, err := s.PVE.Call(r.Context(), c, "GET", "/nodes/"+url.PathEscape(name)+"/storage", nil)
				if err != nil {
					return err
				}
				networks, err := s.PVE.Call(r.Context(), c, "GET", "/nodes/"+url.PathEscape(name)+"/network", nil)
				if err != nil {
					return err
				}
				storage := []string{}
				bridges := []string{}
				for _, row := range objects(stores) {
					content, _ := row["content"].(string)
					if row["active"] == float64(1) && strings.Contains(content, "images") {
						storage = append(storage, fmt.Sprint(row["storage"]))
					}
				}
				for _, row := range objects(networks) {
					if row["type"] == "bridge" {
						bridges = append(bridges, fmt.Sprint(row["iface"]))
					}
				}
				out = append(out, map[string]any{"node": name, "storages": storage, "bridges": bridges})
			}
			return jsonData(w, out, nil)
		}
	}
	if len(parts) == 2 && parts[0] == "modules" {
		return s.module(w, r, c, parts[1])
	}
	if len(parts) == 2 && parts[0] == "vms" {
		id, err := strconv.Atoi(parts[1])
		if err != nil || id < 100 || id > 999999999 {
			return problem("Ungültige VM-ID.", 400)
		}
		return s.vmDetail(w, r, c, id)
	}
	return problem("Nicht gefunden.", 404)
}
