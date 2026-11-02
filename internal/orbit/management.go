package orbit

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var storageIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var guestDiskRE = regexp.MustCompile(`^(rootfs|mp[0-9]+|(?:scsi|virtio|sata|ide)[0-9]+)$`)
var macRE = regexp.MustCompile(`^(?:[a-fA-F0-9]{2}:){5}[a-fA-F0-9]{2}$`)

func boundedNumber(d doc, k string, min, max float64, whole bool) any {
	n := num(d[k])
	switch d[k].(type) {
	case int, float64:
	default:
		fail("Zahl benötigt: " + k)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < min || n > max || (whole && n != math.Trunc(n)) {
		fail("Ungültiger Wert: " + k)
	}
	if whole {
		return int(n)
	}
	return n
}
func plainValue(v any, max int) string {
	s, ok := v.(string)
	if !ok || len(s) > max || strings.ContainsAny(s, "\x00\r\n") {
		fail("Ungültiger Text.")
	}
	return s
}
func requireDigest(config, d doc) {
	if str(config["digest"]) == "" || d["digest"] != config["digest"] {
		panic(problem("Konfiguration inzwischen verändert. Dialog schließen und neu laden.", 409))
	}
}
func propertyParts(s string) map[string]string {
	out := map[string]string{}
	for _, p := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(p, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}
func propertyString(parts map[string]string) string {
	keys := []string{}
	for k := range parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := []string{}
	for _, k := range keys {
		pairs = append(pairs, k+"="+parts[k])
	}
	return strings.Join(pairs, ",")
}
func (s *Server) managedStorage(ctx context.Context, c Connection, id string) doc {
	if c.Kind != "pve" || !storageIDRE.MatchString(id) {
		fail("Ungültiges Proxmox-Storage.")
	}
	return configOf(s.call(ctx, c, "GET", "/storage/"+url.PathEscape(id), nil))
}
func publicStorage(config doc) doc {
	out := doc{}
	for _, k := range []string{"storage", "type", "digest", "content", "nodes", "disable", "shared", "path", "server", "export", "pool", "vgname", "thinpool", "preallocation", "is_mountpoint", "sparse", "username", "datastore", "namespace"} {
		if v, ok := config[k]; ok {
			out[k] = v
		}
	}
	return out
}
func (s *Server) storageContent(ctx context.Context, c Connection, node, id string) any {
	if c.Kind != "pve" || !storageIDRE.MatchString(id) || !fieldPattern.MatchString(node) || node == "" {
		fail("Ungültiges Storage-Ziel.")
	}
	s.online(ctx, c, node)
	rows := objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(id)+"/content", nil))
	sort.Slice(rows, func(i, j int) bool { return str(rows[i]["volid"]) < str(rows[j]["volid"]) })
	return rows
}
func (s *Server) submitStorage(ctx context.Context, d doc, key string) (doc, int) {
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	c := s.conn(cid)
	if c.Kind != "pve" {
		fail("Proxmox VE benötigt.")
	}
	id := field(d, "storage", 64, `[a-zA-Z0-9][a-zA-Z0-9_.-]*`)
	intent := field(d, "intent", 20, "")
	if !contains("create,update", intent) || d["confirm_storage"] != id || !boolean(d, "acknowledge") {
		fail("Storage und Auswirkungen bestätigen.")
	}
	f := configOf(d["fields"])
	args := doc{}
	deleted := []string{}
	typ := str(f["type"])
	if intent == "update" {
		old := s.managedStorage(ctx, c, id)
		requireDigest(old, d)
		typ = str(old["type"])
		args["digest"] = old["digest"]
	} else {
		if !contains("dir,nfs,lvmthin,zfspool", typ) {
			fail("Nicht unterstützter Storage-Typ.")
		}
		for _, r := range objects(s.call(ctx, c, "GET", "/storage", nil)) {
			if r["storage"] == id {
				panic(problem("Storage-ID bereits vorhanden.", 409))
			}
		}
		args["storage"] = id
		args["type"] = typ
	}
	for k, v := range f {
		switch k {
		case "type":
			if intent != "create" {
				fail("Storage-Typ kann nicht geändert werden.")
			}
		case "content":
			x := plainValue(v, 200)
			if x == "" {
				fail("Mindestens ein Inhaltstyp benötigt.")
			}
			for _, item := range strings.Split(x, ",") {
				if !contains("images,rootdir,vztmpl,iso,backup,snippets,import", item) {
					fail("Unbekannter Inhaltstyp.")
				}
			}
			args[k] = x
		case "nodes":
			x := plainValue(v, 1000)
			if x == "" {
				if intent == "update" {
					deleted = append(deleted, k)
				}
			} else {
				for _, n := range strings.Split(x, ",") {
					if !fieldPattern.MatchString(n) {
						fail("Ungültige Node-Liste.")
					}
				}
				known := map[string]bool{}
				for _, n := range objects(s.call(ctx, c, "GET", "/nodes", nil)) {
					known[str(n["node"])] = true
				}
				for _, n := range strings.Split(x, ",") {
					if !known[n] {
						fail("Unbekannter Storage-Node.")
					}
				}
				args[k] = x
			}
		case "disable", "shared":
			args[k] = boolInt(boolean(f, k))
		case "path", "server", "export", "vgname", "thinpool", "pool":
			if intent != "create" {
				fail("Backend-Pfade werden bei bestehenden Storages nicht umgeschrieben.")
			}
			valid := map[string]string{"dir": "path", "nfs": "server,export", "lvmthin": "vgname,thinpool", "zfspool": "pool"}
			if !contains(valid[typ], k) {
				fail("Option passt nicht zum Storage-Typ.")
			}
			x := plainValue(v, 256)
			if x == "" {
				fail("Backend-Option fehlt: " + k)
			}
			if k == "path" || k == "export" {
				if !strings.HasPrefix(x, "/") || path.Clean(x) != x || x == "/" {
					fail("Absoluter Storage-Pfad benötigt.")
				}
			} else if !regexp.MustCompile(`^[a-zA-Z0-9_.:/-]+$`).MatchString(x) {
				fail("Ungültige Backend-Option.")
			}
			args[k] = x
		default:
			fail("Unbekannte Storage-Option: " + k)
		}
	}
	if intent == "create" {
		required := map[string][]string{"dir": {"path"}, "nfs": {"server", "export"}, "lvmthin": {"vgname", "thinpool"}, "zfspool": {"pool"}}
		for _, k := range required[typ] {
			if str(args[k]) == "" {
				fail("Backend-Option fehlt: " + k)
			}
		}
	}
	if len(deleted) > 0 {
		args["delete"] = strings.Join(deleted, ",")
	}
	if len(args) == 1 && intent == "update" {
		fail("Keine Änderung angegeben.")
	}
	op := newTask(cid, "", id, "storage_config", 0, args)
	op["intent"] = intent
	op["storage"] = id
	return s.insertTask("operations", key, op)
}
func (s *Server) processStorage(ctx context.Context, op doc) {
	if op["stage"] == "submitting" {
		op["status"] = "needs_attention"
		s.persist("operations", op, "Unklarer Übermittlungszustand. Keine automatische Wiederholung.")
		return
	}
	err := guarded(func() {
		c := s.conn(str(op["connection_id"]))
		id := str(op["storage"])
		args := configOf(op["args"])
		method, endpoint := "PUT", "/storage/"+url.PathEscape(id)
		if op["intent"] == "create" {
			method = "POST"
			endpoint = "/storage"
			for _, r := range objects(s.call(ctx, c, "GET", "/storage", nil)) {
				if r["storage"] == id {
					fail("Storage wurde inzwischen angelegt.")
				}
			}
		} else {
			requireDigest(s.managedStorage(ctx, c, id), args)
		}
		op["status"] = "running"
		op["stage"] = "submitting"
		s.persist("operations", op, "Storage-Konfiguration an Proxmox übergeben.")
		s.call(ctx, c, method, endpoint, args)
		op["status"] = "completed"
		op["stage"] = "done"
		s.persist("operations", op, "Proxmox hat die Storage-Konfiguration gespeichert.")
		mustAudit := s.Store.Audit("storage.config.completed", str(op["id"]))
		if mustAudit != nil {
			panic(mustAudit)
		}
	})
	s.finishError(ctx, "operations", op, err)
}

func (s *Server) guestEditArgs(ctx context.Context, c Connection, vm, d doc) doc {
	kind, node := str(vm["type"]), str(vm["node"])
	config := configOf(s.call(ctx, c, "GET", baseVM(node, kind, int(num(vm["vmid"])))+"/config", nil))
	requireDigest(config, d)
	nameKey := "name"
	if kind == "lxc" {
		nameKey = "hostname"
	}
	if config[nameKey] != vm["name"] {
		panic(problem("Gastname hat sich geändert. Inventar neu laden.", 409))
	}
	category := field(d, "category", 20, "")
	f := configOf(d["fields"])
	a := doc{"digest": config["digest"]}
	if strings.HasPrefix(category, "disk_") && vm["status"] != "stopped" {
		fail("Diese Konfigurationsänderung benötigt einen gestoppten Gast. Orbit stoppt ihn nicht automatisch.")
	}
	if strings.HasPrefix(category, "disk_") {
		allowed := "size"
		if category == "disk_move" {
			allowed = "storage"
		}
		if category == "disk_add" {
			allowed = "storage,size"
			if kind == "lxc" {
				allowed += ",mountpoint,backup"
			}
		}
		for k := range f {
			if !contains(allowed, k) {
				fail("Unbekannte Datenträgeroption: " + k)
			}
		}
	}
	switch category {
	case "resources", "options":
		for k, v := range f {
			switch k {
			case "cores", "sockets", "memory", "swap", "balloon", "cpulimit", "cpuunits":
				if category != "resources" {
					fail("Ressourcen gehören in den Ressourcen-Editor.")
				}
				bounds := map[string][2]float64{"cores": {1, 8192}, "sockets": {1, 16}, "memory": {16, 4194304}, "swap": {0, 4194304}, "balloon": {0, 4194304}, "cpulimit": {0, 8192}, "cpuunits": {1, 500000}}
				if (kind == "qemu" && k == "swap") || (kind == "lxc" && (k == "sockets" || k == "balloon")) {
					fail("Option passt nicht zum Gasttyp.")
				}
				b := bounds[k]
				a[k] = boundedNumber(f, k, b[0], b[1], k != "cpulimit")
			case "cpu":
				if kind != "qemu" || category != "resources" {
					fail("CPU-Modell nur für QEMU.")
				}
				x := plainValue(v, 512)
				if !regexp.MustCompile(`^[a-zA-Z0-9_+;=.,:-]+$`).MatchString(x) {
					fail("Ungültiges CPU-Modell.")
				}
				a[k] = x
			case "name", "hostname":
				if category != "options" || k != map[bool]string{true: "hostname", false: "name"}[kind == "lxc"] {
					fail("Unpassender Gastname.")
				}
				a[k] = field(f, k, 100, `[a-zA-Z0-9][a-zA-Z0-9_.-]*`)
			case "description":
				if category != "options" {
					fail("Unpassende Option.")
				}
				x, ok := v.(string)
				if !ok || len(x) > 4000 || strings.ContainsRune(x, 0) {
					fail("Ungültige Beschreibung.")
				}
				a[k] = x
			case "onboot", "protection":
				if category != "options" {
					fail("Unpassende Option.")
				}
				a[k] = boolInt(boolean(f, k))
			case "agent":
				if category != "options" || kind != "qemu" {
					fail("Guest Agent nur für QEMU.")
				}
				parts := propertyParts(str(config[k]))
				parts["enabled"] = strconv.Itoa(boolInt(boolean(f, k)))
				a[k] = propertyString(parts)
			case "tags":
				if category != "options" {
					fail("Unpassende Option.")
				}
				x := plainValue(v, 512)
				if x != "" && !regexp.MustCompile(`^[a-zA-Z0-9_.+-]+(?:;[a-zA-Z0-9_.+-]+)*$`).MatchString(x) {
					fail("Ungültige Tags.")
				}
				a[k] = x
			case "bios", "machine", "boot", "ostype":
				if kind != "qemu" || category != "options" {
					fail("QEMU-Option benötigt.")
				}
				if vm["status"] != "stopped" {
					fail("Boot- und Hardwareoptionen benötigen eine gestoppte VM.")
				}
				x := plainValue(v, 512)
				if !regexp.MustCompile(`^[a-zA-Z0-9_.=;,:+-]+$`).MatchString(x) {
					fail("Ungültige VM-Option.")
				}
				if k == "bios" && !contains("seabios,ovmf", x) {
					fail("Ungültiges BIOS.")
				}
				a[k] = x
			case "nameserver", "searchdomain":
				if kind != "lxc" || category != "options" {
					fail("Container-DNS-Option benötigt.")
				}
				x := plainValue(v, 512)
				for _, part := range strings.Fields(x) {
					if k == "nameserver" {
						if _, err := netip.ParseAddr(part); err != nil {
							fail("Ungültiger DNS-Server.")
						}
					} else if !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(part) {
						fail("Ungültige DNS-Suchdomäne.")
					}
				}
				a[k] = x
			default:
				fail("Nicht unterstützte Gastoption: " + k)
			}
		}
		if value, ok := a["balloon"]; ok {
			mem := num(config["memory"])
			if m, ok := a["memory"]; ok {
				mem = num(m)
			}
			if num(value) > mem {
				fail("Balloon-Minimum darf RAM nicht übersteigen.")
			}
		} else if value, ok := a["memory"]; ok && num(config["balloon"]) > num(value) {
			fail("RAM wäre kleiner als das bestehende Balloon-Minimum.")
		}
	case "network":
		device := field(d, "device", 8, `net[0-9]{1,2}`)
		parts := propertyParts(str(config[device]))
		model := "virtio"
		if kind == "qemu" {
			first := strings.Split(str(config[device]), ",")[0]
			if k, mac, ok := strings.Cut(first, "="); ok && macRE.MatchString(mac) {
				model = k
				parts["mac"] = mac
				delete(parts, k)
			} else if first != "" && !strings.Contains(first, "=") {
				model = first
			}
		} else if parts["type"] == "" {
			parts["type"] = "veth"
		}
		for k, v := range f {
			switch k {
			case "bridge":
				x := field(f, k, 64, `[a-zA-Z0-9_.-]+`)
				s.bridgeTarget(ctx, c, node, x)
				parts[k] = x
			case "tag":
				n := int(num(boundedNumber(f, k, 0, 4094, true)))
				if n == 0 {
					delete(parts, k)
				} else {
					parts[k] = strconv.Itoa(n)
				}
			case "firewall", "link_down":
				parts[k] = strconv.Itoa(boolInt(boolean(f, k)))
			case "rate":
				parts[k] = str(boundedNumber(f, k, 0, 1000000, false))
			case "mtu":
				n := int(num(boundedNumber(f, k, 0, 65520, true)))
				if n == 0 {
					delete(parts, k)
				} else if n < 68 {
					fail("MTU muss mindestens 68 sein.")
				} else {
					parts[k] = strconv.Itoa(n)
				}
			case "mac", "hwaddr":
				x := plainValue(v, 32)
				if !macRE.MatchString(x) {
					fail("Ungültige MAC-Adresse.")
				}
				if kind == "qemu" {
					parts["mac"] = x
				} else {
					parts["hwaddr"] = x
				}
			case "model":
				if kind != "qemu" {
					fail("NIC-Modell nur für QEMU.")
				}
				x := plainValue(v, 20)
				if !contains("virtio,e1000,e1000e,rtl8139,vmxnet3", x) {
					fail("Ungültiges NIC-Modell.")
				}
				model = x
			case "name":
				if kind != "lxc" {
					fail("Interface-Name nur für LXC.")
				}
				parts[k] = field(f, k, 15, `[a-zA-Z][a-zA-Z0-9_.-]*`)
			case "ip", "ip6":
				if kind != "lxc" {
					fail("Gast-IP-Adressen nur für LXC.")
				}
				x := plainValue(v, 100)
				valid := x == "dhcp" || x == "manual" || (k == "ip6" && x == "auto")
				if !valid {
					prefix, err := netip.ParsePrefix(x)
					if err != nil || (k == "ip" && !prefix.Addr().Is4()) || (k == "ip6" && !prefix.Addr().Is6()) {
						fail("Ungültige IP/CIDR.")
					}
				}
				parts[k] = x
			case "gw", "gw6":
				if kind != "lxc" {
					fail("Gast-Gateway nur für LXC.")
				}
				x := plainValue(v, 100)
				if x == "" {
					delete(parts, k)
				} else {
					addr, err := netip.ParseAddr(x)
					if err != nil || (k == "gw" && !addr.Is4()) || (k == "gw6" && !addr.Is6()) {
						fail("Ungültiges Gateway.")
					}
					parts[k] = x
				}
			default:
				fail("Unbekannte Netzwerkoption: " + k)
			}
		}
		if parts["bridge"] == "" {
			fail("Bridge fehlt.")
		}
		if kind == "qemu" {
			mac := parts["mac"]
			delete(parts, "mac")
			a[device] = model
			if mac != "" {
				a[device] = model + "=" + mac
			}
			if tail := propertyString(parts); tail != "" {
				a[device] = str(a[device]) + "," + tail
			}
		} else {
			if parts["name"] == "" {
				fail("Container-Interface-Name fehlt.")
			}
			a[device] = propertyString(parts)
		}
	case "disk_add":
		store := field(f, "storage", 64, `[a-zA-Z0-9_.-]+`)
		content := "images"
		if kind == "lxc" {
			content = "rootdir"
		}
		s.storeTarget(ctx, c, node, store, content)
		size := integer(f, "size", 1, 65536)
		device := ""
		prefix := "scsi"
		limit := 31
		if kind == "lxc" {
			prefix = "mp"
			limit = 256
		}
		for i := 0; i < limit; i++ {
			candidate := prefix + strconv.Itoa(i)
			if _, ok := config[candidate]; !ok {
				device = candidate
				break
			}
		}
		if device == "" {
			fail("Kein freier Anschluss.")
		}
		a[device] = fmt.Sprintf("%s:%d", store, size)
		if kind == "lxc" {
			mp := field(f, "mountpoint", 200, "")
			if !strings.HasPrefix(mp, "/") || path.Clean(mp) != mp || strings.ContainsAny(mp, ",\r\n\x00") || mp == "/" || contains("/proc,/sys,/dev", mp) {
				fail("Absoluter Mountpoint im Container benötigt.")
			}
			for k, v := range config {
				if strings.HasPrefix(k, "mp") && propertyParts(str(v))["mp"] == mp {
					fail("Mountpoint bereits belegt.")
				}
			}
			a[device] = str(a[device]) + ",mp=" + mp + ",backup=" + strconv.Itoa(boolInt(boolean(f, "backup")))
		}
	case "disk_resize", "disk_move":
		device := field(d, "device", 16, "")
		if !guestDiskRE.MatchString(device) || str(config[device]) == "" {
			fail("Vorhandenes Laufwerk benötigt.")
		}
		if (kind == "lxc") != (device == "rootfs" || strings.HasPrefix(device, "mp")) {
			fail("Laufwerk passt nicht zum Gasttyp.")
		}
		raw := str(config[device])
		if strings.Contains(raw, "media=cdrom") || strings.Contains(raw, "cloudinit") || !strings.Contains(strings.Split(raw, ",")[0], ":") {
			fail("Nur verwaltete Datenvolumes unterstützt.")
		}
		a = doc{"digest": config["digest"]}
		if category == "disk_resize" {
			size := integer(f, "size", 1, 65536)
			if float64(size) <= diskGiB(config, device) {
				fail("Neue Gesamtgröße muss größer sein.")
			}
			a["disk"] = device
			a["size"] = strconv.Itoa(size) + "G"
		} else {
			store := field(f, "storage", 64, `[a-zA-Z0-9_.-]+`)
			if strings.HasPrefix(raw, store+":") {
				fail("Volume liegt bereits auf diesem Storage.")
			}
			content := "images"
			if kind == "lxc" {
				content = "rootdir"
			}
			s.storeTarget(ctx, c, node, store, content)
			a["storage"] = store
			a["delete"] = 0
			if kind == "qemu" {
				a["disk"] = device
			} else {
				a["volume"] = device
			}
		}
	default:
		fail("Unbekannter Bearbeitungsbereich.")
	}
	if len(a) <= 1 {
		fail("Keine Änderungen angegeben.")
	}
	return a
}
func (s *Server) submitGuestEdit(ctx context.Context, d doc, key string) (doc, int) {
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	c := s.conn(cid)
	node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	id := integer(d, "vmid", 100, 999999999)
	vm := s.findVM(ctx, c, id, node)
	if d["confirm_name"] != vm["name"] || !boolean(d, "acknowledge") || truth(vm["template"]) {
		fail("Gastname und Auswirkungen bestätigen.")
	}
	args := s.guestEditArgs(ctx, c, vm, d)
	op := newTask(cid, node, str(vm["name"]), "guest_edit", id, args)
	op["guest_type"] = vm["type"]
	op["category"] = d["category"]
	return s.insertTask("operations", key, op)
}
func (s *Server) processGuestEdit(ctx context.Context, op doc) {
	if op["stage"] == "submitting" {
		op["status"] = "needs_attention"
		s.persist("operations", op, "Unklarer Übermittlungszustand. Keine automatische Wiederholung.")
		return
	}
	err := guarded(func() {
		c := s.conn(str(op["connection_id"]))
		node, kind := str(op["node"]), str(op["guest_type"])
		category := str(op["category"])
		id := int(num(op["vmid"]))
		base := baseVM(node, kind, id)
		a := configOf(op["args"])
		if op["stage"] == "queued" {
			vm := s.findVM(ctx, c, id, node)
			if vm["type"] != kind || vm["name"] != op["name"] || truth(vm["template"]) {
				fail("Gastidentität hat sich geändert.")
			}
			if strings.HasPrefix(category, "disk_") && vm["status"] != "stopped" {
				fail("Gast inzwischen gestartet. Aktion abgebrochen.")
			}
			for _, k := range []string{"bios", "machine", "boot", "ostype"} {
				if _, ok := a[k]; ok && vm["status"] != "stopped" {
					fail("Gast inzwischen gestartet.")
				}
			}
			config := configOf(s.call(ctx, c, "GET", base+"/config", nil))
			requireDigest(config, a)
			nameKey := "name"
			if kind == "lxc" {
				nameKey = "hostname"
			}
			if config[nameKey] != op["name"] {
				fail("Gastname inzwischen verändert.")
			}
			method, endpoint := "PUT", base+"/config"
			synchronous := true
			if kind == "qemu" {
				method = "POST"
				synchronous = false
			}
			switch category {
			case "disk_add":
				if kind == "qemu" {
					method = "POST"
					synchronous = false
				}
			case "disk_resize":
				method = "PUT"
				synchronous = false
				endpoint = base + "/resize"
			case "disk_move":
				endpoint = base + "/move_disk"
				if kind == "lxc" {
					endpoint = base + "/move_volume"
				}
				method = "POST"
				synchronous = false
			}
			op["stage"] = "submitting"
			op["status"] = "running"
			s.persist("operations", op, "Konfigurationsänderung an Proxmox übergeben.")
			result := s.call(ctx, c, method, endpoint, a)
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
		s.persist("operations", op, "Proxmox hat die Änderung erfolgreich abgeschlossen. Es wurde kein Gast automatisch gestartet oder gestoppt.")
		if err := s.Store.Audit("guest.edit.completed", str(op["id"])); err != nil {
			panic(err)
		}
	})
	s.finishError(ctx, "operations", op, err)
}
