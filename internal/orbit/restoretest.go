package orbit

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Restore tests prove that a backup can actually be restored: Orbit
// restores the newest backup of a guest into a temporary VM with its
// network links down, starts it, waits until it runs (and the guest agent
// answers when configured), then stops and deletes the temporary VM. The
// result is kept for reporting. One test runs per cluster at a time.

const restoreBootTimeout = 5 * time.Minute
const restorePrefix = "restoretest-"

var netKeyRE = regexp.MustCompile(`^net\d+$`)

func restoreTestID() string { return "restore:" + randomID()[:16] }

// latestBackup returns the newest QEMU backup of a guest on a storage.
func (s *Server) latestBackup(ctx context.Context, c Connection, node, store string, vmid int) doc {
	var best doc
	for _, v := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(store)+"/content", doc{"content": "backup"})) {
		volid := str(v["volid"])
		if int(num(v["vmid"])) != vmid || !(strings.Contains(volid, ":backup/vm/") || strings.Contains(volid, "vzdump-qemu-")) {
			continue
		}
		if best == nil || num(v["ctime"]) > num(best["ctime"]) {
			best = v
		}
	}
	if best == nil {
		fail("Kein QEMU-Backup von " + strconv.Itoa(vmid) + " auf " + store + " gefunden.")
	}
	return best
}

func (s *Server) freeVMID(ctx context.Context, c Connection) int {
	id := int(num(s.call(ctx, c, "GET", "/cluster/nextid", nil)))
	if id < 100 {
		fail("Keine freie VMID verfügbar.")
	}
	return id
}

func (s *Server) createRestoreTest(ctx context.Context, d doc, who string) doc {
	c := s.conn(field(d, "connection_id", 64, `[a-zA-Z0-9-]+`))
	if c.Kind != "pve" {
		fail("Proxmox VE benötigt.")
	}
	node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	backupStore := field(d, "backup_storage", 64, `[a-zA-Z0-9_.-]+`)
	target := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
	source := integer(d, "source_vmid", 100, 999999999)
	s.online(ctx, c, node)
	s.storeTarget(ctx, c, node, target, "images")
	backup := s.latestBackup(ctx, c, node, backupStore, source)
	rt := doc{"id": restoreTestID(), "kind": "restoretest", "connection_id": c.ID, "node": node, "source_vmid": source, "source_name": d["source_name"], "archive": backup["volid"], "backup_time": backup["ctime"], "backup_storage": backupStore, "storage": target, "state": "queued", "since": now(), "by": who}
	event(rt, "Restore-Test eingeplant: "+str(backup["volid"]))
	s.saveMaint(rt)
	_ = s.Store.AuditAs(who, "restoretest.create", c.ID+" "+strconv.Itoa(source))
	return rt
}

func (s *Server) restoreTests() []doc {
	out := []doc{}
	for _, d := range s.maintDocs() {
		if d["kind"] == "restoretest" {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return num(out[i]["since"]) > num(out[j]["since"]) })
	return out
}

func (s *Server) finishRestoreTest(ctx context.Context, rt doc, passed bool, message string) {
	c := s.conn(str(rt["connection_id"]))
	if id := int(num(rt["vmid"])); id > 0 {
		cleanup := guarded(func() { s.removeRestoreVM(ctx, c, rt) })
		if cleanup != nil {
			passed = false
			message += " Aufräumen fehlgeschlagen: " + errorText(cleanup) + " Temporäre VM " + strconv.Itoa(id) + " manuell prüfen."
		}
	}
	rt["state"], rt["finished"], rt["result"] = "failed", now(), message
	if passed {
		rt["state"] = "passed"
	}
	rt["duration"] = now() - num(rt["started"])
	event(rt, message)
	s.saveMaint(rt)
	_ = s.Store.AuditAs(str(rt["by"]), "restoretest."+str(rt["state"]), str(rt["source_vmid"])+" "+message)
	// Keep the 50 newest results.
	if all := s.restoreTests(); len(all) > 50 {
		for _, old := range all[50:] {
			if old["state"] == "passed" || old["state"] == "failed" {
				s.deleteMaint(str(old["id"]))
			}
		}
	}
}

// removeRestoreVM stops and deletes the temporary VM, but only after
// re-checking that it is the VM this test created.
func (s *Server) removeRestoreVM(ctx context.Context, c Connection, rt doc) {
	id := int(num(rt["vmid"]))
	name := restorePrefix + strconv.Itoa(id)
	var vm doc
	for _, r := range objects(s.call(ctx, c, "GET", "/cluster/resources", doc{"type": "vm"})) {
		if int(num(r["vmid"])) == id {
			vm = r
		}
	}
	if vm == nil {
		return
	}
	if vm["name"] != name {
		fail("VM " + strconv.Itoa(id) + " heißt nicht " + name + ". Nicht gelöscht.")
	}
	base := baseVM(str(vm["node"]), "qemu", id)
	if vm["status"] == "running" {
		s.waitTask(ctx, c, str(vm["node"]), taskID(s.call(ctx, c, "POST", base+"/status/stop", doc{"skiplock": 1})))
	}
	s.waitTask(ctx, c, str(vm["node"]), taskID(s.call(ctx, c, "DELETE", base, doc{"purge": 1, "destroy-unreferenced-disks": 1})))
	event(rt, "Temporäre VM "+strconv.Itoa(id)+" gelöscht.")
}

func (s *Server) advanceRestoreTest(ctx context.Context, rt doc) {
	c := s.conn(str(rt["connection_id"]))
	node := str(rt["node"])
	switch rt["state"] {
	case "queued":
		for _, other := range s.restoreTests() {
			if other["connection_id"] == c.ID && other["id"] != rt["id"] && contains("restoring,booting", str(other["state"])) {
				return
			}
		}
		id := s.freeVMID(ctx, c)
		rt["vmid"], rt["started"], rt["state"] = id, now(), "restoring"
		event(rt, "Wiederherstellung in temporäre VM "+strconv.Itoa(id)+" auf "+node+".")
		s.saveMaint(rt)
		upid := taskID(s.call(ctx, c, "POST", "/nodes/"+url.PathEscape(node)+"/qemu", doc{"vmid": id, "archive": rt["archive"], "storage": rt["storage"], "unique": 1, "start": 0}))
		rt["upid"] = upid
		s.saveMaint(rt)
	case "restoring":
		st := asDoc(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(str(rt["upid"]))+"/status", nil))
		if st["status"] != "stopped" {
			if now()-num(rt["started"]) > 6*3600 {
				s.finishRestoreTest(ctx, rt, false, "Wiederherstellung dauert länger als 6 Stunden.")
			}
			return
		}
		if st["exitstatus"] != "OK" && !strings.HasPrefix(str(st["exitstatus"]), "WARNINGS") {
			s.finishRestoreTest(ctx, rt, false, "Wiederherstellung fehlgeschlagen: "+str(st["exitstatus"]))
			return
		}
		id := int(num(rt["vmid"]))
		base := baseVM(node, "qemu", id)
		cfg := asDoc(s.call(ctx, c, "GET", base+"/config", nil))
		update := doc{"name": restorePrefix + strconv.Itoa(id), "onboot": 0, "digest": cfg["digest"]}
		for k, v := range cfg {
			if netKeyRE.MatchString(k) && !strings.Contains(str(v), "link_down=1") {
				update[k] = str(v) + ",link_down=1"
			}
		}
		// Detach HA, replication and backups from the copy by name; the
		// description marks it as a test machine.
		update["description"] = "Orbit Restore-Test von VM " + str(rt["source_vmid"]) + " (" + str(rt["archive"]) + "). Wird automatisch gelöscht."
		s.call(ctx, c, "PUT", base+"/config", update)
		rt["restored_at"] = now()
		event(rt, "Wiederhergestellt in "+strconv.Itoa(int(now()-num(rt["started"])))+" s. Netzwerk getrennt, VM wird gestartet.")
		s.waitTask(ctx, c, node, taskID(s.call(ctx, c, "POST", base+"/status/start", nil)))
		rt["state"], rt["boot_at"], rt["agent"] = "booting", now(), truth(strings.SplitN(str(cfg["agent"]), ",", 2)[0])
		s.saveMaint(rt)
	case "booting":
		id := int(num(rt["vmid"]))
		base := baseVM(node, "qemu", id)
		cur := asDoc(s.call(ctx, c, "GET", base+"/status/current", nil))
		since := now() - num(rt["boot_at"])
		if cur["status"] != "running" {
			if since > 30 {
				s.finishRestoreTest(ctx, rt, false, "VM startet nicht (Status "+str(cur["status"])+").")
			}
			return
		}
		if truth(rt["agent"]) {
			if _, err := s.PVE.Call(ctx, c, "POST", base+"/agent/ping", nil); err != nil {
				if since > restoreBootTimeout.Seconds() {
					s.finishRestoreTest(ctx, rt, false, "VM läuft, aber der QEMU-Gastagent antwortet nach 5 Minuten nicht.")
				}
				return
			}
			s.finishRestoreTest(ctx, rt, true, "Erfolgreich: VM gestartet und Gastagent antwortet nach "+strconv.Itoa(int(since))+" s.")
			return
		}
		if since < 20 {
			return
		}
		s.finishRestoreTest(ctx, rt, true, "Erfolgreich: VM wiederhergestellt und läuft stabil (kein Gastagent konfiguriert).")
	}
}

func (s *Server) restoreTestRoutes(w http.ResponseWriter, r *http.Request, d doc) (handled bool, err error) {
	if r.URL.Path != "/api/restore-tests" {
		return false, nil
	}
	handled = true
	err = guarded(func() {
		switch r.Method {
		case "GET":
			writeJSON(w, 200, doc{"items": s.restoreTests()})
		case "POST":
			s.maintMu.Lock()
			defer s.maintMu.Unlock()
			writeJSON(w, 202, s.createRestoreTest(r.Context(), d, requestUser(r.Context())))
		default:
			panic(problem("Nicht erlaubt.", 405))
		}
	})
	return
}
