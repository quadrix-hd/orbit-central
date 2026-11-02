package orbit

import (
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) extraRoutes(w http.ResponseWriter, r *http.Request, d doc, session *session) (handled bool, err error) {
	path := r.URL.Path
	method := r.Method
	if method == "HEAD" {
		method = "GET"
	}
	err = guarded(func() {
		switch {
		case path == "/api/capabilities" && method == "GET":
			handled = true
			writeJSON(w, 200, doc{"runtime": "go", "version": Version, "stage": "development", "supported": []string{"auth", "connections", "inventory", "monitoring", "vm_detail", "operations", "jobs", "console", "registry_deploy", "catalog_cache", "backup_restore"}, "pending": []string{}})
		case path == "/api/operations" && method == "POST":
			handled = true
			result, status := s.submitOperation(r.Context(), d)
			writeJSON(w, status, result)
		case strings.HasPrefix(path, "/api/operations/") && strings.HasSuffix(path, "/review") && method == "POST":
			handled = true
			parts := strings.Split(path, "/")
			if len(parts) != 5 {
				fail("Ungültige Aktion.")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			op := s.readDoc("operations", parts[3])
			if op["status"] != "needs_attention" {
				panic(problem("Aktion benötigt keine manuelle Prüfung.", 409))
			}
			if d["confirm_name"] != op["name"] || !boolean(d, "acknowledge") {
				fail("VM-Name und Prüfung bestätigen.")
			}
			note := field(d, "note", 500, "")
			if len(note) < 10 {
				fail("Nachvollziehbare Prüfnotiz benötigt.")
			}
			op["status"] = "reviewed"
			s.persist("operations", op, "Manuell geprüft: "+note)
			_ = s.Store.Audit("vm.operation.reviewed", str(op["id"]))
			writeJSON(w, 200, op)
		case path == "/api/jobs" && method == "POST":
			handled = true
			result, status := s.submitJob(r.Context(), d)
			clean := doc{}
			for k, v := range result {
				if k != "ssh" {
					clean[k] = v
				}
			}
			writeJSON(w, status, clean)
		case path == "/api/blueprints" && method == "POST":
			handled = true
			writeJSON(w, 201, s.blueprint(r.Context(), d))
		case strings.HasPrefix(path, "/api/blueprints/") && method == "DELETE":
			handled = true
			id := strings.TrimPrefix(path, "/api/blueprints/")
			if strings.Contains(id, "/") {
				fail("Ungültige Blueprint-ID.")
			}
			if _, e := s.Store.DB.Exec("DELETE FROM blueprints WHERE id=?", id); e != nil {
				panic(e)
			}
			_ = s.Store.Audit("blueprint.deleted", id)
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/consoles" && method == "POST":
			handled = true
			writeJSON(w, 201, s.createConsole(r.Context(), d, session))
		case strings.HasPrefix(path, "/api/consoles/") && method == "GET":
			handled = true
			if e := s.proxyConsole(w, r, strings.TrimPrefix(path, "/api/consoles/"), session); e != nil {
				panic(e)
			}
		case path == "/api/catalog" && method == "GET":
			handled = true
			writeJSON(w, 200, s.Catalog.Status())
		case path == "/api/catalog/sync" && method == "POST":
			handled = true
			if s.Catalog.URL == "" {
				fail("Zentrale Katalogquelle fehlt.")
			}
			s.Catalog.Start(s.ctx)
			writeJSON(w, 202, doc{"ok": true})
		case path == "/api/catalog/config" && method == "POST":
			handled = true
			if e := s.Catalog.SetMode(str(d["mode"])); e != nil {
				panic(e)
			}
			s.Catalog.Start(s.ctx)
			writeJSON(w, 200, doc{"ok": true, "mode": s.Catalog.Mode()})
		case path == "/api/catalog/targets" && method == "GET":
			handled = true
			cid := field(doc{"id": r.URL.Query().Get("connection_id")}, "id", 64, `[a-zA-Z0-9-]+`)
			c := s.conn(cid)
			nodes := objects(s.call(r.Context(), c, "GET", "/nodes", nil))
			node := r.URL.Query().Get("node")
			if node == "" {
				for _, n := range objects(s.call(r.Context(), c, "GET", "/cluster/status", nil)) {
					if n["type"] == "node" && truth(n["local"]) {
						node = str(n["name"])
					}
				}
			}
			node = field(doc{"node": node}, "node", 64, `[a-zA-Z0-9_.-]+`)
			targets := s.registryTargets(r.Context(), c, node)
			targets["nodes"] = nodes
			targets["node"] = node
			targets["nextid"] = s.call(r.Context(), c, "GET", "/cluster/nextid", nil)
			writeJSON(w, 200, targets)
		case strings.HasPrefix(path, "/api/catalog/assets/") && method == "GET":
			handled = true
			sha := strings.TrimPrefix(path, "/api/catalog/assets/")
			if !shaRE.MatchString(sha) {
				fail("Ungültige Prüfsumme.")
			}
			snapshot := s.Catalog.Snapshot()
			a := configOf(configOf(snapshot["sources"])[sha])
			file, cleanup := s.Catalog.asset(r.Context(), a)
			defer cleanup()
			w.Header().Set("Content-Disposition", `attachment; filename="`+str(a["name"])+`"`)
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeFile(w, r, file)
		case strings.HasPrefix(path, "/api/connections/"):
			parts := strings.Split(strings.TrimPrefix(path, "/api/connections/"), "/")
			if len(parts) != 2 {
				return
			}
			c := s.conn(parts[0])
			if parts[1] == "catalog" && method == "GET" {
				handled = true
				node := field(doc{"node": r.URL.Query().Get("node")}, "node", 64, `[a-zA-Z0-9_.-]+`)
				writeJSON(w, 200, s.workspaceCatalog(r.Context(), c, node))
			}
			if parts[1] == "storage-content" && method == "POST" {
				handled = true
				store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
				if c.Kind != "pve" || d["confirm_storage"] != store {
					fail("Storage-Namen ausdrücklich bestätigen.")
				}
				config := configOf(s.call(r.Context(), c, "GET", "/storage/"+url.PathEscape(store), nil))
				if !contains("dir,nfs,cifs,cephfs,glusterfs", str(config["type"])) {
					fail("Import benötigt Datei-Storage.")
				}
				content := str(config["content"])
				if !contains(content, "import") {
					s.call(r.Context(), c, "PUT", "/storage/"+url.PathEscape(store), doc{"content": strings.Trim(content+",import", ","), "digest": config["digest"]})
				}
				_ = s.Store.Audit("storage.import.enabled", c.ID+"/"+store)
				writeJSON(w, 200, doc{"ok": true})
			}
		}
	})
	return
}
