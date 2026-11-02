package orbit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (s *Server) liveSnapshot(r *http.Request, section string, tick int) doc {
	result := doc{"at": now(), "interval_seconds": 2}
	connections, err := s.Store.Connections()
	if err != nil {
		panic(err)
	}
	resources := []any{}
	issues := []string{}
	failed := []string{}
	modules := []any{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, connection := range connections {
		c := connection
		c.ActiveEndpoint = s.PVE.Active(c)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			defer func() { <-slots }()
			_ = guarded(func() {
				local := []any{}
				var problemText string
				if c.Kind == "pve" {
					data, e := s.PVE.Call(r.Context(), c, "GET", "/cluster/resources", nil)
					if e != nil {
						problemText = c.Name + ": Datenabruf fehlgeschlagen. Erreichbarkeit und Rechte prüfen."
					} else {
						for _, row := range objects(data) {
							row["connection_id"] = c.ID
							local = append(local, row)
						}
					}
				}
				var module any
				if section != "" && (c.Kind == "pve" || section == "protection") {
					data, e := s.moduleData(r, c, section)
					if e != nil {
						module = doc{"connection": c, "error": "Moduldaten konnten nicht gelesen werden."}
					} else {
						module = doc{"connection": c, "data": data}
					}
				}
				mu.Lock()
				defer mu.Unlock()
				resources = append(resources, local...)
				if problemText != "" {
					issues = append(issues, problemText)
					failed = append(failed, c.ID)
				}
				if module != nil {
					modules = append(modules, module)
				}
			})
		}()
	}
	wg.Wait()
	sort.SliceStable(resources, func(i, j int) bool {
		a, b := configOf(resources[i]), configOf(resources[j])
		for _, key := range []string{"connection_id", "type"} {
			if str(a[key]) != str(b[key]) {
				return str(a[key]) < str(b[key])
			}
		}
		if num(a["vmid"]) != num(b["vmid"]) {
			return num(a["vmid"]) < num(b["vmid"])
		}
		return str(a["node"])+str(a["id"]) < str(b["node"])+str(b["id"])
	})
	sort.SliceStable(modules, func(i, j int) bool {
		return configOf(modules[i])["connection"].(Connection).ID < configOf(modules[j])["connection"].(Connection).ID
	})
	for i := range connections {
		connections[i].ActiveEndpoint = s.PVE.Active(connections[i])
	}
	result["connections"] = connections
	result["resources"] = resources
	result["errors"] = issues
	result["failed_connections"] = failed
	result["jobs"] = must(s.Store.Documents("jobs"))
	result["operations"] = must(s.Store.Documents("operations"))
	if section != "" {
		result["section"] = section
		result["modules"] = modules
	}
	if tick%3 == 0 {
		result["blueprints"] = must(s.Store.Documents("blueprints"))
		result["catalog"] = s.Catalog.Status()
		rows := must(s.Store.DB.Query("SELECT at,action,details FROM audit ORDER BY id DESC LIMIT 100"))
		audit := []any{}
		for rows.Next() {
			var at float64
			var action, details string
			if e := rows.Scan(&at, &action, &details); e != nil {
				rows.Close()
				panic(e)
			}
			audit = append(audit, doc{"at": at, "action": action, "details": details})
		}
		rows.Close()
		result["audit"] = audit
	}
	if cid := r.URL.Query().Get("connection_id"); cid != "" {
		id, e := strconv.Atoi(r.URL.Query().Get("vmid"))
		if e == nil && id >= 100 && id <= 999999999 {
			c, e := s.Store.Connection(cid)
			if e == nil {
				vm := s.safeRead(r, c, baseVM(r.URL.Query().Get("node"), defaultText(r.URL.Query().Get("guest_type"), "qemu"), id)+"/status/current", nil)
				result["guest_status"] = vm
				result["guest_connection_id"] = cid
				if tick%3 == 0 {
					detail, e := s.vmDetailData(r, c, id)
					if e == nil {
						result["guest_detail"] = detail
					}
				}
			}
		}
	}
	return result
}
func (s *Server) events(w http.ResponseWriter, r *http.Request, current *session) error {
	if r.Method != "GET" {
		return problem("GET erwartet.", 405)
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.Origin {
		return problem("Fremder Ursprung nicht erlaubt.", 403)
	}
	section := r.URL.Query().Get("section")
	if section != "" && !contains("network,security,storage,protection,lifecycle", section) {
		return problem("Unbekanntes Live-Modul.", 400)
	}
	node := r.URL.Query().Get("node")
	if node != "" && !fieldPattern.MatchString(node) {
		return problem("Ungültiger Node.", 400)
	}
	guestType := r.URL.Query().Get("guest_type")
	if guestType != "" && guestType != "qemu" && guestType != "lxc" {
		return problem("Ungültiger Gasttyp.", 400)
	}
	s.mu.Lock()
	if s.liveClients >= 8 {
		s.mu.Unlock()
		return problem("Zu viele Live-Verbindungen.", 429)
	}
	s.liveClients++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.liveClients--; s.mu.Unlock() }()
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	send := func(event string, data any) error {
		raw, e := json.Marshal(data)
		if e != nil {
			return e
		}
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); e != nil {
			return e
		}
		return controller.Flush()
	}
	_, _ = fmt.Fprint(w, "retry: 1000\n\n")
	if e := send("ready", doc{"interval_seconds": 2}); e != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	frames := make(chan doc, 1)
	go func() {
		tick := 0
		for {
			pollCtx, stop := context.WithTimeout(ctx, 12*time.Second)
			var snapshot doc
			e := guarded(func() { snapshot = s.liveSnapshot(r.WithContext(pollCtx), section, tick) })
			stop()
			if e != nil {
				snapshot = doc{"error": "Live-Abruf fehlgeschlagen.", "at": now()}
			}
			select {
			case frames <- snapshot:
			case <-ctx.Done():
				return
			}
			tick++
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	auth := time.NewTicker(2 * time.Second)
	defer auth.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.ctx.Done():
			return nil
		case <-auth.C:
			var expires float64
			if e := s.Store.DB.QueryRow("SELECT expires FROM sessions WHERE id=? AND expires>?", current.ID, now()).Scan(&expires); e != nil {
				_ = send("auth_expired", doc{"error": "Bitte erneut anmelden."})
				return nil
			}
		case <-heartbeat.C:
			if e := send("heartbeat", doc{"at": now()}); e != nil {
				return nil
			}
		case frame := <-frames:
			event := "snapshot"
			if frame["error"] != nil {
				event = "live_error"
			}
			if e := send(event, frame); e != nil {
				return nil
			}
		}
	}
}
