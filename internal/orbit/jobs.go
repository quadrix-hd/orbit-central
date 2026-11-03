package orbit

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func (s *Server) blueprint(ctx context.Context, d doc) doc {
	b := doc{}
	for k, max := range map[string]int{"name": 80, "description": 500, "connection_id": 64, "node": 64, "storage": 64, "bridge": 64, "username": 32, "category": 40} {
		pattern := ""
		if contains("node,storage,bridge,username", k) {
			pattern = `[a-zA-Z0-9_.:-]+`
		}
		b[k] = field(d, k, max, pattern)
	}
	if !contains("Betriebssystem,Container,Datenbanken,Anwendungen", str(b["category"])) {
		fail("Ungültige Kategorie.")
	}
	for k, bounds := range map[string][2]int{"template_vmid": {100, 999999999}, "cpu": {1, 128}, "ram": {1, 1024}, "disk": {8, 65536}} {
		b[k] = integer(d, k, bounds[0], bounds[1])
	}
	b["disk_device"] = field(d, "disk_device", 16, `(?:scsi|virtio|sata)\d+`)
	c := s.conn(str(b["connection_id"]))
	if c.Kind != "pve" {
		fail("Blueprint benötigt Proxmox VE.")
	}
	config := configOf(s.call(ctx, c, "GET", baseVM(str(b["node"]), "qemu", int(num(b["template_vmid"])))+"/config", nil))
	b["min_disk"] = checkTemplate(config, str(b["disk_device"]), int(num(b["disk"])))
	b["id"] = randomID()
	raw := must(json.Marshal(b))
	if _, err := s.Store.DB.Exec("INSERT INTO blueprints VALUES(?,?)", b["id"], string(raw)); err != nil {
		panic(err)
	}
	_ = s.Store.Audit("blueprint.created", str(b["id"]))
	return b
}

var sshRE = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp\d+) [A-Za-z0-9+/=]+(?: [^\r\n]+)?$`)

func publicSSH(v any, required bool) string {
	x, ok := v.(string)
	if v == nil {
		x = ""
		ok = true
	}
	x = strings.TrimSpace(x)
	if !ok || len(x) > 16384 || (required && x == "") {
		fail("Gültigen öffentlichen SSH-Schlüssel angeben.")
	}
	if x != "" {
		for _, line := range strings.Split(x, "\n") {
			if !sshRE.MatchString(strings.TrimSuffix(line, "\r")) {
				fail("Ungültiger öffentlicher SSH-Schlüssel.")
			}
		}
	}
	return x
}
func (s *Server) submitJob(ctx context.Context, d doc) (doc, int) {
	key := field(d, "request_key", 80, `[a-zA-Z0-9-]{16,80}`)
	if old := s.requestDoc("jobs", key); old != nil {
		return old, 200
	}
	b := s.readDoc("blueprints", field(d, "blueprint_id", 64, `[a-zA-Z0-9-]+`))
	name := field(d, "name", 63, `[a-zA-Z][a-zA-Z0-9-]{0,62}`)
	cpu, ram, disk := integer(d, "cpu", 1, 128), integer(d, "ram", 1, 1024), integer(d, "disk", int(num(b["min_disk"])), 65536)
	start := boolean(d, "start")
	ssh := publicSSH(d["ssh"], false)
	c := s.conn(str(b["connection_id"]))
	node := str(b["node"])
	fresh := configOf(s.call(ctx, c, "GET", baseVM(node, "qemu", int(num(b["template_vmid"])))+"/config", nil))
	checkTemplate(fresh, str(b["disk_device"]), disk)
	s.storeTarget(ctx, c, node, str(b["storage"]), "images")
	s.bridgeTarget(ctx, c, node, str(b["bridge"]))
	s.online(ctx, c, node)
	resources := objects(s.call(ctx, c, "GET", "/cluster/resources", nil))
	used := map[int]bool{}
	for _, r := range resources {
		used[int(num(r["vmid"]))] = true
		if r["name"] == name {
			fail("VM-Name wird bereits verwendet.")
		}
	}
	for _, table := range []string{"jobs", "operations"} {
		for _, j := range s.allDocs(table) {
			if j["connection_id"] == b["connection_id"] {
				used[int(num(j["vmid"]))] = true
				if pending(j) && j["name"] == name {
					fail("Name wird bereits bereitgestellt.")
				}
			}
		}
	}
	id := int(num(s.call(ctx, c, "GET", "/cluster/nextid", nil)))
	for used[id] {
		id++
	}
	if id < 100 || id > 999999999 {
		fail("Keine freie VMID.")
	}
	j := newTask(c.ID, node, name, "clone", id, nil)
	for _, k := range []string{"template_vmid", "storage", "bridge", "username", "disk_device"} {
		j[k] = b[k]
	}
	j["source_node"] = node
	j["blueprint"] = b["name"]
	j["cpu"] = cpu
	j["ram"] = ram
	j["disk"] = disk
	j["ssh"] = ssh
	j["start"] = start
	return s.insertTask(ctx, "jobs", key, j)
}
func (s *Server) processJob(ctx context.Context, j doc) {
	stage := str(j["stage"])
	if strings.HasSuffix(stage, "_submitting") {
		j["status"] = "needs_attention"
		s.persist("jobs", j, "Unklarer Zustand nach Neustart. Keine automatische Wiederholung.")
		return
	}
	err := guarded(func() {
		c := s.conn(str(j["connection_id"]))
		node := str(j["node"])
		source := str(j["source_node"])
		base := baseVM(node, "qemu", int(num(j["vmid"])))
		j["status"] = "running"
		s.persist("jobs", j, "")
		if j["stage"] == "queued" {
			config := configOf(s.call(ctx, c, "GET", baseVM(source, "qemu", int(num(j["template_vmid"])))+"/config", nil))
			checkTemplate(config, str(j["disk_device"]), int(num(j["disk"])))
			s.unused(ctx, c, int(num(j["vmid"])), str(j["name"]))
			j["stage"] = "clone_submitting"
			s.persist("jobs", j, "Vorlage wird vollständig geklont.")
			j["upid"] = taskID(s.call(ctx, c, "POST", baseVM(source, "qemu", int(num(j["template_vmid"])))+"/clone", doc{"newid": j["vmid"], "name": j["name"], "full": 1, "target": node, "storage": j["storage"]}))
			j["stage"] = "clone_wait"
			s.persist("jobs", j, "")
		}
		if j["stage"] == "clone_wait" {
			s.waitTask(ctx, c, source, str(j["upid"]))
			j["stage"] = "configure"
			s.persist("jobs", j, "Klon erstellt.")
		}
		if j["stage"] == "configure" {
			config := s.guestConfig(ctx, c, base, "name", str(j["name"]))
			cloud := false
			for k, v := range config {
				if diskPattern.MatchString(k) && strings.Contains(str(v), "cloudinit") {
					cloud = true
				}
			}
			if !cloud {
				fail("Vorlage hat kein Cloud-init-Laufwerk.")
			}
			tags := map[string]bool{"orbit": true}
			for _, t := range strings.Split(str(config["tags"]), ";") {
				if t != "" {
					tags[t] = true
				}
			}
			list := []string{}
			for t := range tags {
				list = append(list, t)
			}
			sort.Strings(list)
			args := doc{"cores": j["cpu"], "sockets": 1, "memory": int(num(j["ram"])) * 1024, "ciuser": j["username"], "ipconfig0": "ip=dhcp", "net0": "virtio,bridge=" + str(j["bridge"]), "onboot": 0, "tags": strings.Join(list, ";"), "digest": config["digest"]}
			if str(j["ssh"]) != "" {
				args["sshkeys"] = url.QueryEscape(str(j["ssh"]))
			}
			j["stage"] = "configure_submitting"
			s.persist("jobs", j, "")
			result := s.call(ctx, c, "PUT", base+"/config", args)
			j["stage"] = "resize"
			if result != nil {
				j["upid"] = taskID(result)
				j["stage"] = "configure_wait"
			}
			s.persist("jobs", j, "")
		}
		if j["stage"] == "configure_wait" {
			s.waitTask(ctx, c, node, str(j["upid"]))
			j["stage"] = "resize"
			s.persist("jobs", j, "")
		}
		if j["stage"] == "resize" {
			config := s.guestConfig(ctx, c, base, "name", str(j["name"]))
			size := diskGiB(config, str(j["disk_device"]))
			if num(j["disk"]) < size {
				fail("Keine Verkleinerung eines Laufwerks möglich.")
			}
			if num(j["disk"]) > size {
				j["stage"] = "resize_submitting"
				s.persist("jobs", j, "")
				result := s.call(ctx, c, "PUT", base+"/resize", doc{"disk": j["disk_device"], "size": strconv.Itoa(int(num(j["disk"]))) + "G"})
				j["stage"] = "start"
				if result != nil {
					j["upid"] = taskID(result)
					j["stage"] = "resize_wait"
				}
			} else {
				j["stage"] = "start"
			}
			s.persist("jobs", j, "")
		}
		if j["stage"] == "resize_wait" {
			s.waitTask(ctx, c, node, str(j["upid"]))
			j["stage"] = "start"
			s.persist("jobs", j, "")
		}
		if j["stage"] == "start" {
			if j["start"] == true {
				s.guestConfig(ctx, c, base, "name", str(j["name"]))
				j["stage"] = "start_submitting"
				s.persist("jobs", j, "")
				j["upid"] = taskID(s.call(ctx, c, "POST", base+"/status/start", nil))
				j["stage"] = "start_wait"
			} else {
				j["stage"] = "done"
			}
			s.persist("jobs", j, "")
		}
		if j["stage"] == "start_wait" {
			s.waitTask(ctx, c, node, str(j["upid"]))
			j["stage"] = "done"
		}
		if j["stage"] == "done" {
			j["status"] = "completed"
			s.persist("jobs", j, "Bereitstellung erfolgreich. Software stammt aus der verknüpften Vorlage.")
			_ = s.Store.Audit("deployment.completed", str(j["id"]))
		}
	})
	s.finishError(ctx, "jobs", j, err)
}
func (s *Server) workspaceCatalog(ctx context.Context, c Connection, node string) doc {
	s.online(ctx, c, node)
	stores := objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage", nil))
	networks := objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/network", nil))
	media := []any{}
	issues := []string{}
	active := []any{}
	for _, store := range stores {
		if !truth(store["active"]) {
			continue
		}
		active = append(active, store)
		for _, content := range []string{"iso", "backup", "vztmpl"} {
			if !contains(str(store["content"]), content) {
				continue
			}
			err := guarded(func() {
				for i, row := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(str(store["storage"]))+"/content", doc{"content": content})) {
					if i >= 300 {
						break
					}
					if row["volid"] != nil {
						media = append(media, doc{"storage": store["storage"], "content": content, "volid": row["volid"], "size": row["size"], "vmid": row["vmid"]})
					}
				}
			})
			if err != nil {
				issues = append(issues, str(store["storage"])+": Medien nicht lesbar.")
			}
		}
	}
	bridges := []string{}
	for _, n := range networks {
		if n["type"] == "bridge" {
			bridges = append(bridges, str(n["iface"]))
		}
	}
	id := int(num(s.call(ctx, c, "GET", "/cluster/nextid", nil)))
	reserved := map[int]bool{}
	for _, table := range []string{"jobs", "operations"} {
		for _, j := range s.allDocs(table) {
			if j["connection_id"] == c.ID && pending(j) {
				reserved[int(num(j["vmid"]))] = true
			}
		}
	}
	for reserved[id] {
		id++
	}
	return doc{"nodes": s.call(ctx, c, "GET", "/nodes", nil), "node": node, "nextid": id, "storages": active, "bridges": bridges, "media": media, "errors": issues}
}
