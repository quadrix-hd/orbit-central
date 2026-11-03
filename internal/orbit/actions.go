package orbit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type doc = map[string]any

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		n, _ := strconv.ParseFloat(x, 64)
		return n
	}
	return 0
}
func truth(v any) bool    { return v == true || num(v) == 1 }
func fail(message string) { panic(problem(message, 400)) }
func must[T any](v T, e error) T {
	if e != nil {
		panic(e)
	}
	return v
}

// Every internal workflow is bounded by guarded; recovered errors are handled by
// the HTTP boundary or persisted as a task failure, never discarded.
func guarded(f func()) (err error) {
	defer func() {
		if p := recover(); p != nil {
			if e, ok := p.(error); ok {
				err = e
			} else {
				err = errors.New("internal workflow failure")
			}
		}
	}()
	f()
	return nil
}
func field(d doc, k string, max int, pattern string) string { return must(text(d, k, max, pattern)) }
func integer(d doc, k string, lo, hi int) int {
	v, ok := d[k]
	n := num(v)
	_, numeric := v.(float64)
	if _, yes := v.(int); yes {
		numeric = true
	}
	if !ok || !numeric || n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		fail("Ungültiger Wert: " + k)
	}
	return int(n)
}
func boolean(d doc, k string) bool {
	v, ok := d[k].(bool)
	if !ok {
		fail("Option fehlt: " + k)
	}
	return v
}
func values(d doc) url.Values {
	v := url.Values{}
	for k, x := range d {
		if x != nil {
			if n, ok := x.(float64); ok {
				v.Set(k, strconv.FormatFloat(n, 'f', -1, 64))
			} else {
				v.Set(k, str(x))
			}
		}
	}
	return v
}
func (s *Server) call(ctx context.Context, c Connection, method, path string, d doc) any {
	return must(s.PVE.Call(ctx, c, method, path, values(d)))
}
func (s *Server) conn(id string) Connection {
	c, e := s.Store.Connection(id)
	if errors.Is(e, sql.ErrNoRows) {
		panic(problem("Verbindung nicht gefunden.", 404))
	}
	return must(c, e)
}
func (s *Server) readDoc(table, id string) doc {
	var raw string
	e := s.Store.DB.QueryRow("SELECT data FROM "+table+" WHERE id=?", id).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		panic(problem("Eintrag nicht gefunden.", 404))
	}
	if e != nil {
		panic(e)
	}
	var d doc
	if e = json.Unmarshal([]byte(raw), &d); e != nil {
		panic(e)
	}
	return d
}
func (s *Server) requestDoc(table, key string) doc {
	var raw string
	e := s.Store.DB.QueryRow("SELECT data FROM "+table+" WHERE request_key=?", key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		panic(e)
	}
	var d doc
	if e = json.Unmarshal([]byte(raw), &d); e != nil {
		panic(e)
	}
	return d
}
func (s *Server) allDocs(table string) []doc {
	rows := must(s.Store.DB.Query("SELECT data FROM " + table + " ORDER BY created"))
	defer rows.Close()
	out := []doc{}
	for rows.Next() {
		var raw string
		if e := rows.Scan(&raw); e != nil {
			panic(e)
		}
		var d doc
		if e := json.Unmarshal([]byte(raw), &d); e != nil {
			panic(e)
		}
		out = append(out, d)
	}
	if e := rows.Err(); e != nil {
		panic(e)
	}
	return out
}
func pending(d doc) bool {
	return d["status"] == "queued" || d["status"] == "running" || d["status"] == "needs_attention"
}
func (s *Server) persist(table string, d doc, message string) {
	if message != "" {
		events, _ := d["events"].([]any)
		events = append(events, doc{"at": now(), "message": message})
		d["events"] = events
	}
	raw := must(json.Marshal(d))
	if _, e := s.Store.DB.Exec("UPDATE "+table+" SET data=? WHERE id=?", string(raw), d["id"]); e != nil {
		panic(e)
	}
}
func now() float64 { return float64(time.Now().UnixMilli()) / 1000 }

// reservations lists the VMIDs and names a task occupies: the guest it acts
// on and, for clones, the new guest it will create.
func reservations(d doc) ([]float64, []string) {
	ids, names := []float64{num(d["vmid"])}, []string{str(d["name"])}
	if d["action"] == "clone" {
		args := configOf(d["args"])
		ids = append(ids, num(args["newid"]))
		names = append(names, defaultText(str(args["name"]), str(args["hostname"])))
	}
	return ids, names
}

func reservationsOverlap(a, b doc) bool {
	aIDs, aNames := reservations(a)
	bIDs, bNames := reservations(b)
	for _, x := range aIDs {
		for _, y := range bIDs {
			if x == y {
				return true
			}
		}
	}
	for _, x := range aNames {
		for _, y := range bNames {
			if x != "" && x == y {
				return true
			}
		}
	}
	return false
}

func (s *Server) insertTask(table, key string, d doc) (doc, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.requestDoc(table, key); old != nil {
		return old, 200
	}
	for _, t := range []string{"jobs", "operations"} {
		for _, other := range s.allDocs(t) {
			if pending(other) && other["connection_id"] == d["connection_id"] && reservationsOverlap(other, d) {
				panic(problem("VMID oder Name ist durch eine andere Aufgabe reserviert.", 409))
			}
		}
	}
	raw := must(json.Marshal(d))
	if _, e := s.Store.DB.Exec("INSERT INTO "+table+" VALUES(?,?,?,?)", d["id"], key, string(raw), d["created"]); e != nil {
		panic(e)
	}
	if e := s.Store.Audit("task.queued", str(d["id"])); e != nil {
		panic(e)
	}
	return d, 202
}
func newTask(cid, node, name, action string, vmid int, args doc) doc {
	return doc{"id": randomID(), "connection_id": cid, "node": node, "name": name, "vmid": vmid, "guest_type": "qemu", "action": action, "args": args, "status": "queued", "stage": "queued", "created": now(), "events": []any{doc{"at": now(), "message": "Aktion eingeplant."}}}
}
func (s *Server) findVM(ctx context.Context, c Connection, id int, node string) doc {
	if c.Kind != "pve" {
		fail("Proxmox VE benötigt.")
	}
	for _, v := range objects(s.call(ctx, c, "GET", "/cluster/resources", doc{"type": "vm"})) {
		if int(num(v["vmid"])) == id && (v["type"] == "qemu" || v["type"] == "lxc") {
			if node != "" && v["node"] != node {
				panic(problem("Gast befindet sich inzwischen auf einem anderen Node.", 409))
			}
			return v
		}
	}
	panic(problem("VM oder Container nicht gefunden.", 404))
}
func baseVM(node, kind string, id int) string {
	return "/nodes/" + url.PathEscape(node) + "/" + kind + "/" + strconv.Itoa(id)
}
func configOf(v any) doc {
	d, ok := v.(map[string]any)
	if !ok {
		panic(&Problem{"Ungültige Proxmox-Konfiguration.", 502, true})
	}
	return d
}
func (s *Server) storeTarget(ctx context.Context, c Connection, node, store, content string) {
	for _, r := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage", nil)) {
		if r["storage"] == store && truth(r["active"]) && contains(str(r["content"]), content) {
			return
		}
	}
	fail("Aktives Storage mit Inhaltstyp " + content + " benötigt.")
}
func contains(list, item string) bool {
	for _, v := range strings.Split(list, ",") {
		if v == item {
			return true
		}
	}
	return false
}
func (s *Server) bridgeTarget(ctx context.Context, c Connection, node, bridge string) {
	for _, r := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/network", nil)) {
		if r["iface"] == bridge && r["type"] == "bridge" {
			return
		}
	}
	fail("Bridge nicht gefunden.")
}
func (s *Server) online(ctx context.Context, c Connection, node string) {
	for _, r := range objects(s.call(ctx, c, "GET", "/nodes", nil)) {
		if r["node"] == node && r["status"] == "online" {
			return
		}
	}
	fail("Online-Node benötigt.")
}
func (s *Server) unused(ctx context.Context, c Connection, id int, name string) {
	for _, r := range objects(s.call(ctx, c, "GET", "/cluster/resources", nil)) {
		if num(r["vmid"]) == float64(id) || r["name"] == name {
			panic(problem("VMID oder Name bereits vorhanden. Kein Überschreiben.", 409))
		}
	}
}
func (s *Server) volume(ctx context.Context, c Connection, node, volume, content string) {
	store, _, ok := strings.Cut(volume, ":")
	if !ok || len(volume) > 512 || !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(store) {
		fail("Ungültiges Volume.")
	}
	for _, r := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(store)+"/content", doc{"content": content})) {
		if r["volid"] == volume {
			return
		}
	}
	fail("Volume nicht verfügbar.")
}

var diskSizeRE = regexp.MustCompile(`(?:^|,)size=(\d+(?:\.\d+)?)([KMGT])(?:,|$)`)

func diskGiB(c doc, device string) float64 {
	m := diskSizeRE.FindStringSubmatch(str(c[device]))
	if len(m) != 3 {
		fail("Laufwerksgröße konnte nicht ermittelt werden.")
	}
	return num(m[1]) * map[string]float64{"K": 1.0 / (1024 * 1024), "M": 1.0 / 1024, "G": 1, "T": 1024}[m[2]]
}
func checkTemplate(c doc, device string, size int) int {
	if !truth(c["template"]) {
		fail("Quelle ist keine Proxmox-Vorlage.")
	}
	networks, cloud := 0, false
	for k, v := range c {
		if networkPattern.MatchString(k) {
			networks++
		}
		if diskPattern.MatchString(k) && strings.Contains(str(v), "cloudinit") {
			cloud = true
		}
	}
	if networks > 1 || !cloud {
		fail("Vorlage benötigt Cloud-init und maximal eine Netzwerkkarte.")
	}
	minimum := int(math.Ceil(diskGiB(c, device)))
	if size < minimum {
		fail("Zielgröße ist kleiner als das Vorlagenlaufwerk.")
	}
	return minimum
}
func taskID(v any) string {
	x, ok := v.(string)
	if !ok || !strings.HasPrefix(x, "UPID:") {
		panic(&Problem{"Keine gültige Aufgaben-ID. Proxmox-Zustand manuell prüfen.", 502, true})
	}
	return x
}
func (s *Server) waitTask(ctx context.Context, c Connection, node, id string) {
	deadline := time.NewTimer(30 * time.Minute)
	defer deadline.Stop()
	for {
		d := configOf(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(id)+"/status", nil))
		if d["status"] == "stopped" {
			if d["exitstatus"] != "OK" {
				fail("Proxmox-Aufgabe fehlgeschlagen. Proxmox-Protokoll prüfen.")
			}
			return
		}
		select {
		case <-ctx.Done():
			panic(ctx.Err())
		case <-deadline.C:
			panic(&Problem{"Proxmox-Aufgabe nach 30 Minuten noch aktiv.", 502, true})
		case <-time.After(time.Second):
		}
	}
}
func (s *Server) quorum(ctx context.Context, c Connection) {
	for _, r := range objects(s.call(ctx, c, "GET", "/cluster/status", nil)) {
		if r["type"] == "cluster" && truth(r["quorate"]) {
			return
		}
	}
	fail("HA-Änderungen benötigen Cluster-Quorum.")
}

func (s *Server) submitOperation(ctx context.Context, d doc) (doc, int) {
	key := field(d, "request_key", 80, `[a-zA-Z0-9-]{16,80}`)
	if old := s.requestDoc("operations", key); old != nil {
		return old, 200
	}
	action := field(d, "action", 20, "")
	if action == "lxc_create" || action == "lxc_restore" {
		return s.submitContainer(ctx, d, key)
	}
	if action == "storage_config" {
		return s.submitStorage(ctx, d, key)
	}
	if action == "guest_edit" {
		return s.submitGuestEdit(ctx, d, key)
	}
	if action == "infrastructure" {
		return s.submitInfra(ctx, d, key)
	}
	if action == "registry_deploy" {
		return s.submitRegistry(ctx, d, key)
	}
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	c := s.conn(cid)
	node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	id := integer(d, "vmid", 100, 999999999)
	if action == "create" || action == "restore" {
		name := field(d, "name", 63, `[a-zA-Z][a-zA-Z0-9-]{0,62}`)
		store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
		if c.Kind != "pve" {
			fail("Proxmox VE benötigt.")
		}
		s.online(ctx, c, node)
		s.storeTarget(ctx, c, node, store, "images")
		s.unused(ctx, c, id, name)
		a := doc{"vmid": id, "name": name}
		if action == "restore" {
			archive := field(d, "archive", 512, "")
			s.volume(ctx, c, node, archive, "backup")
			if !strings.Contains(archive, ":backup/vm/") && !regexp.MustCompile(`\.vma(?:\.(?:zst|gz|lzo))?$`).MatchString(archive) {
				fail("QEMU-Backup benötigt.")
			}
			a["archive"] = archive
			a["storage"] = store
			a["force"] = 0
		} else {
			bridge := field(d, "bridge", 64, `[a-zA-Z0-9_.-]+`)
			s.bridgeTarget(ctx, c, node, bridge)
			iso := str(d["iso"])
			if iso != "" {
				s.volume(ctx, c, node, iso, "iso")
			}
			a = doc{"vmid": id, "name": name, "cores": integer(d, "cpu", 1, 128), "memory": integer(d, "ram", 1, 1024) * 1024, "sockets": 1, "scsihw": "virtio-scsi-single", "scsi0": store + ":" + strconv.Itoa(integer(d, "disk", 8, 65536)), "net0": "virtio,bridge=" + bridge, "ide2": defaultText(iso, "none") + ",media=cdrom", "ostype": "l26", "boot": "order=scsi0", "onboot": 0}
			if iso != "" {
				a["boot"] = "order=ide2;scsi0"
			}
		}
		return s.insertTask("operations", key, newTask(cid, node, name, action, id, a))
	}
	vm := s.findVM(ctx, c, id, node)
	name := field(d, "confirm_name", 100, "")
	if name != vm["name"] || (truth(vm["template"]) && action != "clone") {
		panic(problem("VM-Name stimmt nicht überein oder Gast ist eine Vorlage.", 409))
	}
	kind := str(vm["type"])
	if kind == "lxc" && !contains("start,shutdown,reboot,stop,snapshot,backup,configure,migrate,ha,clone,template", action) {
		fail("Diese Aktion wird für LXC nicht unterstützt.")
	}
	a := doc{}
	switch action {
	case "ha":
		desired := field(d, "ha_state", 16, "")
		if !contains("started,stopped,disabled,ignored", desired) {
			fail("Ungültiger HA-Zustand.")
		}
		s.quorum(ctx, c)
		a["state"] = desired
	case "snapshot":
		n := field(d, "snapshot_name", 40, `[a-zA-Z][a-zA-Z0-9_-]{0,39}`)
		if n == "current" {
			fail("Snapshot-Name ist reserviert.")
		}
		a = doc{"snapname": n, "vmstate": 0}
	case "backup":
		store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
		s.storeTarget(ctx, c, node, store, "backup")
		a = doc{"vmid": id, "storage": store, "mode": "snapshot"}
	case "migrate":
		target := field(d, "target", 64, `[a-zA-Z0-9_.-]+`)
		if target == node {
			fail("Ziel- und Quell-Node sind identisch.")
		}
		s.online(ctx, c, target)
		a["target"] = target
		if kind == "qemu" {
			a["online"] = boolInt(vm["status"] == "running")
		} else if vm["status"] != "stopped" {
			fail("LXC-Migration benötigt einen gestoppten Container.")
		}
	case "configure":
		if vm["status"] != "stopped" {
			fail("CPU/RAM-Änderung benötigt einen gestoppten Gast.")
		}
		a = doc{"cores": integer(d, "cpu", 1, 128), "memory": integer(d, "ram", 1, 1024) * 1024}
		if kind == "qemu" {
			a["sockets"] = 1
		}
	case "start":
		if vm["status"] == "running" {
			fail("Gast läuft bereits.")
		}
	case "shutdown", "reboot", "reset", "suspend":
		if vm["status"] != "running" {
			fail("Gast läuft nicht.")
		}
	case "stop", "resume":
		// A paused QEMU guest is still reported as running by /cluster/resources.
		if vm["status"] == "stopped" {
			fail("Gast ist bereits gestoppt.")
		}
		if action == "stop" && !boolean(d, "acknowledge") {
			fail("Hartes Ausschalten ausdrücklich bestätigen.")
		}
	case "template":
		if vm["status"] != "stopped" {
			fail("Nur ein gestoppter Gast kann in eine Vorlage umgewandelt werden.")
		}
		if !boolean(d, "acknowledge") {
			fail("Umwandlung in eine Vorlage ausdrücklich bestätigen. Sie ist nicht umkehrbar.")
		}
	case "clone":
		newID := integer(d, "newid", 100, 999999999)
		newName := field(d, "new_name", 63, `[a-zA-Z][a-zA-Z0-9-]{0,62}`)
		full := boolean(d, "full")
		if !full && !truth(vm["template"]) {
			fail("Verknüpfte Klone sind nur von Vorlagen möglich.")
		}
		s.unused(ctx, c, newID, newName)
		nameKey := "name"
		if kind == "lxc" {
			nameKey = "hostname"
		}
		a = doc{"newid": newID, nameKey: newName, "full": boolInt(full)}
		if target := str(d["target"]); target != "" && target != node {
			target = field(d, "target", 64, `[a-zA-Z0-9_.-]+`)
			s.online(ctx, c, target)
			a["target"] = target
		}
		if store := str(d["storage"]); store != "" {
			if !full {
				fail("Ziel-Storage ist nur für vollständige Klone wählbar.")
			}
			store = field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
			s.storeTarget(ctx, c, defaultText(str(a["target"]), node), store, "images")
			a["storage"] = store
		}
	case "disk_add", "disk_resize", "nic", "metadata", "iso", "rollback", "snapshot_delete", "delete":
		a = s.hardwareArgs(ctx, c, vm, d, action)
	default:
		fail("Aktion wird nicht unterstützt.")
	}
	op := newTask(cid, node, name, action, id, a)
	op["guest_type"] = kind
	return s.insertTask("operations", key, op)
}
func defaultText(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Server) hardwareArgs(ctx context.Context, c Connection, vm, d doc, action string) doc {
	node := str(vm["node"])
	base := baseVM(node, "qemu", int(num(vm["vmid"])))
	config := configOf(s.call(ctx, c, "GET", base+"/config", nil))
	if config["name"] != vm["name"] {
		fail("VM-Identität hat sich geändert.")
	}
	if action != "metadata" && vm["status"] != "stopped" {
		fail("Änderung benötigt eine gestoppte VM.")
	}
	a := doc{"digest": config["digest"]}
	switch action {
	case "disk_add":
		store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
		s.storeTarget(ctx, c, node, store, "images")
		device := ""
		for i := 0; i < 31; i++ {
			candidate := "scsi" + strconv.Itoa(i)
			if _, ok := config[candidate]; !ok {
				device = candidate
				break
			}
		}
		if device == "" {
			fail("Kein freier SCSI-Anschluss.")
		}
		a[device] = store + ":" + strconv.Itoa(integer(d, "disk", 1, 65536))
	case "disk_resize":
		device := field(d, "device", 16, `(?:scsi|virtio|sata)\d+`)
		size := integer(d, "disk", 1, 65536)
		if float64(size) <= diskGiB(config, device) {
			fail("Neue Größe muss größer als das vorhandene Laufwerk sein.")
		}
		a = doc{"disk": device, "size": strconv.Itoa(size) + "G"}
	case "nic":
		device := field(d, "device", 4, `net[0-9]`)
		bridge := field(d, "bridge", 64, `[a-zA-Z0-9_.-]+`)
		s.bridgeTarget(ctx, c, node, bridge)
		parts := map[string]string{}
		for _, pair := range strings.Split(str(config[device]), ",") {
			k, v, ok := strings.Cut(pair, "=")
			if ok {
				parts[k] = v
			}
		}
		for _, model := range []string{"e1000", "rtl8139", "vmxnet3"} {
			if mac := parts[model]; mac != "" {
				delete(parts, model)
				parts["virtio"] = mac
			}
		}
		parts["bridge"] = bridge
		vlan := integer(d, "vlan", 0, 4094)
		delete(parts, "tag")
		if vlan > 0 {
			parts["tag"] = strconv.Itoa(vlan)
		}
		parts["firewall"] = strconv.Itoa(boolInt(boolean(d, "firewall")))
		keys := []string{}
		for k := range parts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := []string{}
		if parts["virtio"] == "" {
			pairs = append(pairs, "virtio")
		}
		for _, k := range keys {
			pairs = append(pairs, k+"="+parts[k])
		}
		a[device] = strings.Join(pairs, ",")
	case "metadata":
		a["name"] = field(d, "new_name", 63, `[a-zA-Z][a-zA-Z0-9-]{0,62}`)
		note, ok := d["description"].(string)
		if !ok || len(note) > 4000 {
			fail("Ungültige Beschreibung.")
		}
		a["description"] = note
		a["onboot"] = boolInt(boolean(d, "onboot"))
	case "iso":
		iso := str(d["iso"])
		old := str(config["ide2"])
		if strings.Contains(old, "cloudinit") || (old != "" && !strings.Contains(old, "media=cdrom")) {
			fail("ide2 ist kein austauschbares CD-ROM-Laufwerk.")
		}
		if iso != "" {
			s.volume(ctx, c, node, iso, "iso")
		}
		a["ide2"] = defaultText(iso, "none") + ",media=cdrom"
		boot := defaultText(str(config["boot"]), "order=scsi0")
		if iso != "" && strings.HasPrefix(boot, "order=") {
			devices := []string{"ide2"}
			for _, device := range strings.Split(strings.TrimPrefix(boot, "order="), ";") {
				if device != "ide2" && regexp.MustCompile(`^(scsi|virtio|sata|ide|net)\d+$`).MatchString(device) {
					devices = append(devices, device)
				}
			}
			boot = "order=" + strings.Join(devices, ";")
		}
		a["boot"] = boot
	case "rollback", "snapshot_delete":
		name := field(d, "snapshot_name", 40, `[a-zA-Z][a-zA-Z0-9_-]{0,39}`)
		exists := false
		for _, r := range objects(s.call(ctx, c, "GET", base+"/snapshot", nil)) {
			if r["name"] == name {
				exists = true
			}
		}
		if name == "current" || !exists || !boolean(d, "acknowledge") {
			fail("Snapshot und Auswirkungen bestätigen.")
		}
		a = doc{"snapname": name}
	case "delete":
		if !boolean(d, "acknowledge") || num(d["confirm_vmid"]) != num(vm["vmid"]) {
			fail("Löschen mit VMID und Datenverlustbestätigung bestätigen.")
		}
		a = doc{"purge": 1, "destroy-unreferenced-disks": 0}
	}
	return a
}

func (s *Server) processOperation(ctx context.Context, op doc) {
	if op["action"] == "storage_config" {
		s.processStorage(ctx, op)
		return
	}
	if op["action"] == "guest_edit" {
		s.processGuestEdit(ctx, op)
		return
	}
	if op["action"] == "infrastructure" {
		s.processInfra(ctx, op)
		return
	}
	if op["action"] == "registry_deploy" {
		s.processRegistry(ctx, op)
		return
	}
	stage := str(op["stage"])
	if stage == "submitting" {
		op["status"] = "needs_attention"
		s.persist("operations", op, "Unklarer Übermittlungszustand nach Neustart. Keine Wiederholung.")
		return
	}
	err := guarded(func() {
		c := s.conn(str(op["connection_id"]))
		node := str(op["node"])
		kind := defaultText(str(op["guest_type"]), "qemu")
		base := baseVM(node, kind, int(num(op["vmid"])))
		op["status"] = "running"
		s.persist("operations", op, "")
		action := str(op["action"])
		args := configOf(op["args"])
		if op["stage"] == "queued" {
			creating := contains("create,restore,lxc_create,lxc_restore", action)
			if creating {
				s.unused(ctx, c, int(num(op["vmid"])), str(op["name"]))
				if action == "lxc_create" || action == "lxc_restore" {
					s.containerPreflight(ctx, c, op)
				}
			} else {
				vm := s.findVM(ctx, c, int(num(op["vmid"])), node)
				if vm["name"] != op["name"] || vm["type"] != kind || (truth(vm["template"]) && action != "clone") {
					fail("Gastidentität hat sich geändert.")
				}
				if action == "clone" {
					s.unused(ctx, c, int(num(args["newid"])), defaultText(str(args["name"]), str(args["hostname"])))
				}
				if contains("configure,disk_add,disk_resize,nic,iso,rollback,snapshot_delete,delete,template", action) && vm["status"] != "stopped" {
					fail("Gast ist inzwischen gestartet. Änderung abgebrochen.")
				}
			}
			method, path, synchronous := "POST", base+"/status/"+action, false
			switch action {
			case "ha":
				s.quorum(ctx, c)
				sid := "vm:" + str(op["vmid"])
				if kind == "lxc" {
					sid = "ct:" + str(op["vmid"])
				}
				exists := false
				for _, r := range objects(s.call(ctx, c, "GET", "/cluster/ha/resources", nil)) {
					if r["sid"] == sid {
						exists = true
					}
				}
				path = "/cluster/ha/resources"
				if exists {
					method = "PUT"
					path += "/" + url.PathEscape(sid)
				} else {
					args["sid"] = sid
				}
				synchronous = true
			case "create", "restore", "lxc_create", "lxc_restore":
				path = "/nodes/" + url.PathEscape(node) + "/" + kind
			case "configure", "metadata", "nic", "iso":
				method = "PUT"
				path = base + "/config"
				synchronous = true
				config := configOf(s.call(ctx, c, "GET", path, nil))
				nameKey := "name"
				if kind == "lxc" {
					nameKey = "hostname"
				}
				if config[nameKey] != op["name"] {
					fail("Gastidentität stimmt nicht mehr.")
				}
				if old := args["digest"]; old != nil && old != config["digest"] {
					fail("Konfiguration inzwischen verändert.")
				}
				args["digest"] = config["digest"]
			case "disk_add":
				path = base + "/config"
			case "disk_resize":
				method = "PUT"
				path = base + "/resize"
				synchronous = true
			case "rollback":
				path = base + "/snapshot/" + url.PathEscape(str(args["snapname"])) + "/rollback"
				args = doc{"start": 0}
			case "snapshot_delete":
				method = "DELETE"
				path = base + "/snapshot/" + url.PathEscape(str(args["snapname"]))
				args = doc{}
			case "delete":
				method = "DELETE"
				path = base
			case "snapshot", "migrate", "clone":
				path = base + "/" + action
			case "template":
				path = base + "/template"
				synchronous = true
			case "backup":
				path = "/nodes/" + url.PathEscape(node) + "/vzdump"
			}
			op["stage"] = "submitting"
			s.persist("operations", op, "Aktion an Proxmox übergeben.")
			result := s.call(ctx, c, method, path, args)
			if synchronous && result == nil {
				op["stage"] = "done"
			} else {
				op["upid"] = taskID(result)
				op["stage"] = "waiting"
			}
			s.persist("operations", op, "")
		}
		if op["stage"] == "waiting" {
			s.waitTask(ctx, c, node, str(op["upid"]))
			op["stage"] = "done"
		}
		op["status"] = "completed"
		s.persist("operations", op, "Proxmox hat die Aktion erfolgreich abgeschlossen.")
		if e := s.Store.Audit("vm."+action+".completed", str(op["id"])); e != nil {
			panic(e)
		}
	})
	s.finishError(ctx, "operations", op, err)
}
func (s *Server) finishError(ctx context.Context, table string, d doc, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	status, message := "failed", "Interner Fehler. Proxmox-Zustand manuell prüfen."
	var p *Problem
	if errors.As(err, &p) {
		message = p.Message
	}
	stage := str(d["stage"])
	if (strings.Contains(stage, "submitting") || strings.Contains(stage, "wait")) && (p == nil || p.Uncertain) {
		status = "needs_attention"
	}
	d["status"] = status
	_ = guarded(func() { s.persist(table, d, message); _ = s.Store.Audit("task."+status, str(d["id"])) })
}
func (s *Server) RunWorkers(ctx context.Context) {
	s.ctx = ctx
	s.workers.Add(2)
	go func() {
		defer s.workers.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			if _, err := os.Stat(filepath.Join(s.Store.Dir, "maintenance")); errors.Is(err, os.ErrNotExist) {
				_ = guarded(func() {
					for _, table := range []string{"operations", "jobs"} {
						for _, d := range s.allDocs(table) {
							if d["status"] == "queued" || d["status"] == "running" {
								if table == "operations" {
									s.processOperation(ctx, d)
								} else {
									s.processJob(ctx, d)
								}
								if ctx.Err() != nil {
									return
								}
							}
						}
					}
				})
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	go func() { defer s.workers.Done(); s.Catalog.Run(ctx) }()
	s.RunAlerts(ctx)
}

func (s *Server) WaitWorkers() { s.workers.Wait() }
