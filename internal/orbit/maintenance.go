package orbit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Host maintenance: evacuate a host by live-migrating its running guests to
// the other hosts (one migration at a time, each a normal Orbit operation),
// keep it marked while it is serviced, bring the guests back afterwards,
// and rolling reboots that walk a cluster host by host.

const maintenanceInterval = 3 * time.Second
const rebootTimeout = 30 * time.Minute

func maintID(cid, node string) string { return "node:" + cid + "/" + node }
func rollingID(cid string) string     { return "rolling:" + cid }

func (s *Server) maintDocs() []doc {
	rows := must(s.Store.DB.Query("SELECT data FROM maintenance ORDER BY id"))
	defer rows.Close()
	out := []doc{}
	for rows.Next() {
		var raw string
		must(0, rows.Scan(&raw))
		var d doc
		if json.Unmarshal([]byte(raw), &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) maintDoc(id string) doc {
	var raw string
	if s.Store.DB.QueryRow("SELECT data FROM maintenance WHERE id=?", id).Scan(&raw) != nil {
		return nil
	}
	var d doc
	if json.Unmarshal([]byte(raw), &d) != nil {
		return nil
	}
	return d
}

func (s *Server) saveMaint(d doc) {
	d["updated"] = now()
	raw := must(json.Marshal(d))
	must(s.Store.DB.Exec("INSERT INTO maintenance(id,data,updated) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated=excluded.updated", d["id"], string(raw), now()))
}

func (s *Server) deleteMaint(id string) {
	must(s.Store.DB.Exec("DELETE FROM maintenance WHERE id=?", id))
}

// inMaintenance reports whether a host is marked for maintenance; alerts for
// it going offline are expected and suppressed.
func (s *Server) inMaintenance(cid, node string) bool {
	var n int
	return s.Store.DB.QueryRow("SELECT 1 FROM maintenance WHERE id=?", maintID(cid, node)).Scan(&n) == nil
}

func event(d doc, message string) {
	events := anyList(d["events"])
	events = append(events, doc{"at": now(), "message": message})
	if len(events) > 200 {
		events = events[len(events)-200:]
	}
	d["events"] = events
}

// evacuationPlan places every running guest of a host on the online host
// with the most free memory, largest guests first.
func (s *Server) evacuationPlan(ctx context.Context, c Connection, node string, lxcRestart bool) []doc {
	rows := objects(s.call(ctx, c, "GET", "/cluster/resources", nil))
	free := map[string]float64{}
	for _, r := range rows {
		if r["type"] == "node" && r["status"] == "online" && r["node"] != node && !s.inMaintenance(c.ID, str(r["node"])) {
			free[str(r["node"])] = num(r["maxmem"])*0.92 - num(r["mem"])
		}
	}
	if len(free) == 0 {
		fail("Kein anderer Host online, der die Gäste aufnehmen kann.")
	}
	guests := []doc{}
	for _, r := range rows {
		if (r["type"] == "qemu" || r["type"] == "lxc") && r["node"] == node && r["status"] == "running" && !truth(r["template"]) {
			guests = append(guests, r)
		}
	}
	sort.SliceStable(guests, func(i, j int) bool { return num(guests[i]["maxmem"]) > num(guests[j]["maxmem"]) })
	moves := []doc{}
	for _, g := range guests {
		id := int(num(g["vmid"]))
		mv := doc{"vmid": id, "name": g["name"], "type": g["type"], "from": node, "status": "pending"}
		allowed := map[string]bool{}
		if g["type"] == "lxc" && !lxcRestart {
			mv["status"], mv["message"] = "skipped", "Container läuft – Neustart-Migration nicht gewählt."
			moves = append(moves, mv)
			continue
		}
		if g["type"] == "qemu" {
			if pre, err := s.PVE.Call(ctx, c, "GET", baseVM(node, "qemu", id)+"/migrate", nil); err == nil {
				p := asDoc(pre)
				for _, n := range anyList(p["allowed_nodes"]) {
					allowed[str(n)] = true
				}
				if len(anyList(p["local_disks"])) > 0 {
					mv["local_disks"] = true
				}
			}
		}
		need := num(g["maxmem"])
		best, bestFree := "", -1.0
		for n, f := range free {
			if len(allowed) > 0 && !allowed[n] {
				continue
			}
			if f >= need && (f > bestFree || (f == bestFree && n < best)) {
				best, bestFree = n, f
			}
		}
		if best == "" {
			mv["status"], mv["message"] = "skipped", "Kein Zielhost mit genug freiem Arbeitsspeicher."
		} else {
			mv["to"] = best
			free[best] -= need
		}
		moves = append(moves, mv)
	}
	return moves
}

func (s *Server) enterMaintenance(ctx context.Context, c Connection, node, note string, lxcRestart bool, who string) doc {
	s.online(ctx, c, node)
	s.quorum(ctx, c)
	id := maintID(c.ID, node)
	if s.maintDoc(id) != nil {
		fail("Host ist bereits im Wartungsmodus.")
	}
	moves := s.evacuationPlan(ctx, c, node, lxcRestart)
	d := doc{"id": id, "kind": "node", "connection_id": c.ID, "node": node, "state": "evacuating", "since": now(), "by": who, "note": note, "lxc_restart": lxcRestart, "moves": moves}
	event(d, "Wartungsmodus gestartet, "+strconv.Itoa(len(moves))+" laufende Gäste.")
	s.saveMaint(d)
	_ = s.Store.AuditAs(who, "maintenance.enter", c.ID+"/"+node)
	return d
}

func (s *Server) exitMaintenance(ctx context.Context, c Connection, node string, returnGuests bool, who string) doc {
	id := maintID(c.ID, node)
	d := s.maintDoc(id)
	if d == nil {
		panic(problem("Host ist nicht im Wartungsmodus.", 404))
	}
	if d["state"] != "active" {
		fail("Evakuierung oder Rückführung läuft noch. Bitte warten.")
	}
	_ = s.Store.AuditAs(who, "maintenance.exit", c.ID+"/"+node)
	back := []doc{}
	if returnGuests {
		s.online(ctx, c, node)
		for _, m := range anyList(d["moves"]) {
			mv := asDoc(m)
			if mv["status"] == "done" {
				back = append(back, doc{"vmid": mv["vmid"], "name": mv["name"], "type": mv["type"], "from": mv["to"], "to": node, "status": "pending", "local_disks": mv["local_disks"]})
			}
		}
	}
	if len(back) == 0 {
		s.deleteMaint(id)
		return doc{"id": id, "state": "ended"}
	}
	d["state"], d["moves"], d["phase"] = "returning", back, "return"
	event(d, "Wartung beendet, "+strconv.Itoa(len(back))+" Gäste werden zurückverschoben.")
	s.saveMaint(d)
	return d
}

func shortHash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:6])
}

// advanceMoves runs the next migration of a maintenance document. It returns
// true when no migration is pending or running.
func (s *Server) advanceMoves(ctx context.Context, d doc) bool {
	moves := anyList(d["moves"])
	c := s.conn(str(d["connection_id"]))
	var resources map[int]doc
	lookup := func(id int) doc {
		if resources == nil {
			resources = map[int]doc{}
			for _, r := range objects(s.call(ctx, c, "GET", "/cluster/resources", doc{"type": "vm"})) {
				resources[int(num(r["vmid"]))] = r
			}
		}
		return resources[id]
	}
	for _, m := range moves {
		mv := asDoc(m)
		if mv["status"] != "running" {
			continue
		}
		op := s.readDocOrNil("operations", str(mv["operation_id"]))
		switch {
		case op == nil:
			mv["status"], mv["message"] = "failed", "Migrationsaufgabe nicht gefunden."
		case op["status"] == "completed":
			mv["status"] = "done"
			event(d, str(mv["name"])+" nach "+str(mv["to"])+" verschoben.")
		case op["status"] == "failed" || op["status"] == "needs_attention":
			msg := "Migration fehlgeschlagen."
			if ev := anyList(op["events"]); len(ev) > 0 {
				msg = str(asDoc(ev[len(ev)-1])["message"])
			}
			mv["status"], mv["message"] = "failed", msg
			event(d, str(mv["name"])+": "+msg)
		default:
			return false
		}
		s.saveMaint(d)
	}
	for _, m := range moves {
		mv := asDoc(m)
		if mv["status"] != "pending" {
			continue
		}
		id := int(num(mv["vmid"]))
		g := lookup(id)
		if g == nil || g["node"] != mv["from"] || (g["status"] != "running" && d["phase"] != "return") {
			mv["status"], mv["message"] = "skipped", "Gast nicht mehr auf "+str(mv["from"])+" oder nicht mehr laufend."
			s.saveMaint(d)
			continue
		}
		req := doc{"request_key": "maint-" + shortHash(str(d["id"])+str(d["since"])+str(d["phase"])) + "-" + strconv.Itoa(id), "action": "migrate", "connection_id": c.ID, "node": mv["from"], "vmid": id, "confirm_name": g["name"], "target": mv["to"], "with_local_disks": truth(mv["local_disks"]), "restart": g["type"] == "lxc"}
		jobCtx := withUser(ctx, &session{Username: "Wartung " + str(d["node"]), Role: "admin"})
		if e := guarded(func() {
			op, _ := s.submitOperation(jobCtx, req)
			mv["operation_id"] = op["id"]
		}); e != nil {
			mv["status"], mv["message"] = "failed", errorText(e)
			event(d, str(mv["name"])+": "+errorText(e))
		} else {
			mv["status"] = "running"
		}
		s.saveMaint(d)
		return false
	}
	return true
}

func (s *Server) readDocOrNil(table, id string) (d doc) {
	_ = guarded(func() { d = s.readDoc(table, id) })
	return d
}

func (s *Server) advanceNode(ctx context.Context, d doc) {
	switch d["state"] {
	case "evacuating":
		if s.advanceMoves(ctx, d) {
			failed := 0
			for _, m := range anyList(d["moves"]) {
				if asDoc(m)["status"] == "failed" {
					failed++
				}
			}
			d["state"] = "active"
			d["evacuated_at"] = now()
			if failed > 0 {
				event(d, "Evakuierung abgeschlossen, "+strconv.Itoa(failed)+" Migration(en) fehlgeschlagen.")
			} else {
				event(d, "Evakuierung abgeschlossen. Host ist bereit für die Wartung.")
			}
			s.saveMaint(d)
		}
	case "returning":
		if s.advanceMoves(ctx, d) {
			s.deleteMaint(str(d["id"]))
		}
	}
}

func (s *Server) pauseRolling(d doc, message string) {
	d["state"], d["error"] = "paused", message
	event(d, "Angehalten: "+message)
	s.saveMaint(d)
}

func (s *Server) advanceRolling(ctx context.Context, d doc) {
	if d["state"] != "running" {
		return
	}
	c := s.conn(str(d["connection_id"]))
	nodes := anyList(d["nodes"])
	index := int(num(d["index"]))
	if index >= len(nodes) {
		d["state"] = "done"
		event(d, "Rolling-Neustart abgeschlossen.")
		s.saveMaint(d)
		_ = s.Store.AuditAs(str(d["by"]), "maintenance.rolling.done", c.ID)
		return
	}
	node := str(nodes[index])
	md := s.maintDoc(maintID(c.ID, node))
	switch d["step"] {
	case "evacuate":
		if md == nil {
			if e := guarded(func() { s.enterMaintenance(ctx, c, node, "Rolling-Neustart", truth(d["lxc_restart"]), str(d["by"])) }); e != nil {
				s.pauseRolling(d, node+": "+errorText(e))
				return
			}
			event(d, node+": Wartungsmodus, Gäste werden verschoben.")
			s.saveMaint(d)
			return
		}
		if md["state"] != "active" {
			return
		}
		for _, m := range anyList(md["moves"]) {
			if mv := asDoc(m); mv["status"] == "failed" {
				s.pauseRolling(d, node+": "+str(mv["name"])+" konnte nicht verschoben werden. Manuell verschieben und fortsetzen.")
				return
			}
		}
		running := []string{}
		for _, r := range objects(s.call(ctx, c, "GET", "/cluster/resources", doc{"type": "vm"})) {
			if r["node"] == node && r["status"] == "running" {
				running = append(running, str(r["name"]))
			}
		}
		if len(running) > 0 {
			s.pauseRolling(d, node+": Es laufen noch Gäste ("+strings.Join(running, ", ")+"). Verschieben oder stoppen und fortsetzen.")
			return
		}
		d["step"] = "reboot"
		s.saveMaint(d)
	case "reboot":
		s.quorum(ctx, c)
		s.call(ctx, c, "POST", "/nodes/"+url.PathEscape(node)+"/status", doc{"command": "reboot"})
		d["step"], d["reboot_at"] = "wait", now()
		event(d, node+": Neustart ausgelöst.")
		s.saveMaint(d)
	case "wait":
		since := now() - num(d["reboot_at"])
		if since > rebootTimeout.Seconds() {
			s.pauseRolling(d, node+" ist nach 30 Minuten nicht zurück. Host prüfen und fortsetzen.")
			return
		}
		if since < 10 {
			return
		}
		up := false
		_ = guarded(func() {
			for _, n := range objects(s.call(ctx, c, "GET", "/nodes", nil)) {
				if n["node"] == node && n["status"] == "online" {
					st := asDoc(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/status", nil))
					up = num(st["uptime"]) > 0 && num(st["uptime"]) < since+5
				}
			}
			if up {
				for _, r := range objects(s.call(ctx, c, "GET", "/cluster/status", nil)) {
					if r["type"] == "cluster" && !truth(r["quorate"]) {
						up = false
					}
				}
			}
		})
		if !up {
			return
		}
		d["step"] = "return"
		event(d, node+": wieder online.")
		s.saveMaint(d)
	case "return":
		if md == nil {
			d["index"], d["step"] = index+1, "evacuate"
			event(d, node+": fertig.")
			s.saveMaint(d)
			return
		}
		if md["state"] == "active" {
			if e := guarded(func() { s.exitMaintenance(ctx, c, node, truth(d["return_guests"]), str(d["by"])) }); e != nil {
				s.pauseRolling(d, node+": "+errorText(e))
			}
		}
	}
}

func (s *Server) advanceMaintenance(ctx context.Context) {
	for _, d := range s.maintDocs() {
		if ctx.Err() != nil {
			return
		}
		if e := guarded(func() {
			if d["kind"] == "rolling" {
				s.advanceRolling(ctx, d)
			} else if d["kind"] == "restoretest" {
				if e := guarded(func() { s.advanceRestoreTest(ctx, d) }); e != nil && ctx.Err() == nil && contains("queued,restoring,booting", str(d["state"])) {
					s.finishRestoreTest(ctx, d, false, "Restore-Test abgebrochen: "+errorText(e))
				}
			} else {
				s.advanceNode(ctx, d)
			}
		}); e != nil && ctx.Err() == nil {
			log.Print("maintenance_step_failed id=" + str(d["id"]))
			if d["kind"] == "rolling" && d["state"] == "running" {
				s.pauseRolling(d, errorText(e))
			}
		}
	}
}

func (s *Server) RunMaintenance(ctx context.Context) {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(maintenanceInterval):
			}
			s.maintMu.Lock()
			_ = guarded(func() { s.advanceMaintenance(ctx) })
			s.maintMu.Unlock()
		}
	}()
}

func (s *Server) maintenanceRoutes(w http.ResponseWriter, r *http.Request, d doc) (handled bool, err error) {
	if !strings.HasPrefix(r.URL.Path, "/api/maintenance") {
		return false, nil
	}
	handled = true
	err = guarded(func() {
		who := requestUser(r.Context())
		action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/maintenance"), "/")
		if r.Method == "GET" && action == "" {
			writeJSON(w, 200, doc{"items": s.maintDocs()})
			return
		}
		if r.Method != "POST" {
			panic(problem("Nicht gefunden.", 404))
		}
		c := s.conn(field(d, "connection_id", 64, `[a-zA-Z0-9-]+`))
		if c.Kind != "pve" {
			fail("Proxmox VE benötigt.")
		}
		s.maintMu.Lock()
		defer s.maintMu.Unlock()
		node := func() string { return field(d, "node", 64, `[a-zA-Z0-9_.-]+`) }
		switch action {
		case "plan":
			n := node()
			writeJSON(w, 200, doc{"moves": s.evacuationPlan(r.Context(), c, n, d["lxc_restart"] == true)})
		case "enter":
			note, _ := d["note"].(string)
			if len(note) > 200 {
				fail("Notiz zu lang.")
			}
			writeJSON(w, 200, s.enterMaintenance(r.Context(), c, node(), strings.TrimSpace(note), d["lxc_restart"] == true, who))
		case "exit":
			writeJSON(w, 200, s.exitMaintenance(r.Context(), c, node(), d["return_guests"] == true, who))
		case "rolling":
			id := rollingID(c.ID)
			if old := s.maintDoc(id); old != nil && (old["state"] == "running" || old["state"] == "paused") {
				fail("Für diesen Cluster läuft bereits ein Rolling-Neustart.")
			}
			online := map[string]bool{}
			for _, n := range objects(s.call(r.Context(), c, "GET", "/nodes", nil)) {
				if n["status"] == "online" {
					online[str(n["node"])] = true
				}
			}
			list := []any{}
			seen := map[string]bool{}
			for _, v := range anyList(d["nodes"]) {
				n := str(v)
				if !online[n] || seen[n] {
					fail("Host " + n + " ist nicht online oder doppelt.")
				}
				seen[n] = true
				list = append(list, n)
			}
			if len(list) == 0 {
				fail("Mindestens einen Host wählen.")
			}
			if len(online) < 2 {
				fail("Ein Rolling-Neustart benötigt mindestens zwei Hosts.")
			}
			if !boolean(d, "acknowledge") {
				fail("Neustart der Hosts ausdrücklich bestätigen.")
			}
			s.quorum(r.Context(), c)
			rd := doc{"id": id, "kind": "rolling", "connection_id": c.ID, "nodes": list, "index": 0, "step": "evacuate", "state": "running", "since": now(), "by": who, "return_guests": d["return_guests"] == true, "lxc_restart": d["lxc_restart"] == true}
			event(rd, "Rolling-Neustart gestartet: "+strconv.Itoa(len(list))+" Host(s).")
			s.saveMaint(rd)
			_ = s.Store.AuditAs(who, "maintenance.rolling.start", c.ID)
			writeJSON(w, 200, rd)
		case "rolling/cancel", "rolling/resume", "rolling/dismiss":
			rd := s.maintDoc(rollingID(c.ID))
			if rd == nil {
				panic(problem("Kein Rolling-Neustart für diesen Cluster.", 404))
			}
			switch action {
			case "rolling/cancel":
				rd["state"] = "cancelled"
				event(rd, "Abgebrochen von "+who+". Hosts im Wartungsmodus bleiben markiert.")
			case "rolling/resume":
				if rd["state"] != "paused" {
					fail("Nur ein angehaltener Ablauf kann fortgesetzt werden.")
				}
				rd["state"], rd["error"] = "running", ""
				event(rd, "Fortgesetzt von "+who+".")
			case "rolling/dismiss":
				if rd["state"] == "running" || rd["state"] == "paused" {
					fail("Ablauf zuerst abbrechen.")
				}
				s.deleteMaint(str(rd["id"]))
				writeJSON(w, 200, doc{"ok": true})
				return
			}
			s.saveMaint(rd)
			_ = s.Store.AuditAs(who, "maintenance."+strings.ReplaceAll(action, "/", "."), c.ID)
			writeJSON(w, 200, rd)
		case "apt-refresh":
			n := node()
			s.online(r.Context(), c, n)
			result := s.call(r.Context(), c, "POST", "/nodes/"+url.PathEscape(n)+"/apt/update", nil)
			_ = s.Store.AuditAs(who, "node.apt.refresh", c.ID+"/"+n)
			writeJSON(w, 200, doc{"upid": taskID(result)})
		default:
			panic(problem("Nicht gefunden.", 404))
		}
	})
	return
}

// nodeUpdates lists pending package updates of a host.
func (s *Server) nodeUpdates(r *http.Request, c Connection) (any, error) {
	var out []doc
	err := guarded(func() {
		node := field(doc{"v": r.URL.Query().Get("node")}, "v", 64, `[a-zA-Z0-9_.-]+`)
		out = []doc{}
		for _, p := range objects(s.call(r.Context(), c, "GET", "/nodes/"+url.PathEscape(node)+"/apt/update", nil)) {
			out = append(out, doc{"package": p["Package"], "old": p["OldVersion"], "new": p["Version"], "priority": p["Priority"], "title": p["Title"]})
		}
	})
	return out, err
}
