package orbit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Server) nodeConnection(ctx context.Context, c Connection, node string) Connection {
	for _, endpoint := range must(ValidateEndpoints(c.URL, c.Endpoints)) {
		candidate := c
		candidate.URL = endpoint
		candidate.Endpoints = nil
		data, err := s.PVE.once(ctx, candidate, endpoint, "GET", "/cluster/status", nil)
		if err != nil {
			continue
		}
		for _, row := range objects(data) {
			if row["type"] == "node" && row["name"] == node && truth(row["local"]) {
				return candidate
			}
		}
	}
	fail("Gültige HTTPS-Adresse des Ziel-Nodes als Cluster-Endpunkt hinterlegen.")
	return c
}
func (s *Server) registryTargets(ctx context.Context, c Connection, node string) doc {
	if c.Kind != "pve" {
		fail("Marketplace benötigt Proxmox VE.")
	}
	pin := s.nodeConnection(ctx, c, node)
	version := str(configOf(s.call(ctx, pin, "GET", "/version", nil))["version"])
	major, _ := strconv.Atoi(strings.Split(version, ".")[0])
	if major < 9 {
		fail("Direkter Cloud-Image-Import benötigt Proxmox VE 9 oder neuer.")
	}
	s.online(ctx, c, node)
	out := doc{"endpoint": pin.URL, "version": version, "image_storages": []any{}, "import_storages": []any{}, "iso_storages": []any{}, "bridges": []any{}}
	for _, store := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/storage", nil)) {
		if !truth(store["active"]) {
			continue
		}
		for k, content := range map[string]string{"image_storages": "images", "import_storages": "import", "iso_storages": "iso"} {
			if contains(str(store["content"]), content) {
				out[k] = append(out[k].([]any), store["storage"])
			}
		}
	}
	for _, n := range objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/network", nil)) {
		if n["type"] == "bridge" {
			out["bridges"] = append(out["bridges"].([]any), n["iface"])
		}
	}
	return out
}
func hasChoice(v any, item string) bool {
	for _, choice := range v.([]any) {
		if choice == item {
			return true
		}
	}
	return false
}
func (s *Server) submitRegistry(ctx context.Context, d doc, key string) (doc, int) {
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	p, sources := s.Catalog.Package(field(d, "package_id", 80, `[a-zA-Z0-9][a-zA-Z0-9_.-]*`))
	dep, ok := p["deployment"].(map[string]any)
	if !ok {
		fail("Dieses Paket besitzt kein Cloud-Image-Rezept.")
	}
	if d["package_version"] != p["version"] {
		panic(problem("Paketversion geändert. Konfigurator erneut öffnen.", 409))
	}
	if !boolean(d, "acknowledge") {
		fail("Bereitstellung ausdrücklich bestätigen.")
	}
	c := s.conn(cid)
	targets := s.registryTargets(ctx, c, node)
	a := doc{}
	for k, choices := range map[string]string{"storage": "image_storages", "import_storage": "import_storages", "iso_storage": "iso_storages", "bridge": "bridges"} {
		pick := field(d, k, 64, `[a-zA-Z0-9_.-]+`)
		if !hasChoice(targets[choices], pick) {
			fail("Ziel nicht verfügbar: " + k)
		}
		a[k] = pick
	}
	name := field(d, "name", 63, `[a-zA-Z][a-zA-Z0-9-]{0,62}`)
	id := integer(d, "vmid", 100, 999999999)
	username := field(d, "username", 31, `[a-z_][a-z0-9_-]{0,30}`)
	if username == "root" {
		fail("Eigenen Cloud-init-Benutzer statt root verwenden.")
	}
	a["username"] = username
	a["ssh"] = publicSSH(d["ssh"], true)
	a["start"] = boolean(d, "start")
	a["cpu"] = integer(d, "cpu", 1, 128)
	a["ram"] = integer(d, "ram", 1, 1024)
	a["disk"] = integer(d, "disk", 8, 65536)
	a["image"] = configOf(sources[str(dep["image_sha256"])])
	a["packages"] = dep["packages"]
	a["package_id"] = p["id"]
	a["package_version"] = p["version"]
	a["endpoint"] = targets["endpoint"]
	a["mode"] = s.Catalog.Mode()
	s.unused(ctx, c, id, name)
	return s.insertTask("operations", key, newTask(cid, node, name, "registry_deploy", id, a))
}
func seedPayloads(name, username, ssh, id string, packages []any) map[string][]byte {
	data := doc{"hostname": name, "manage_etc_hosts": true, "users": []any{doc{"name": username, "groups": []string{"sudo"}, "sudo": "ALL=(ALL) NOPASSWD:ALL", "shell": "/bin/bash", "lock_passwd": true, "ssh_authorized_keys": strings.Split(ssh, "\n")}}, "ssh_pwauth": false, "disable_root": true, "package_update": len(packages) > 0, "packages": packages, "growpart": doc{"mode": "auto", "devices": []string{"/"}}, "resize_rootfs": true}
	commands := []any{}
	services := map[string]string{"docker.io": "docker", "nginx": "nginx", "postgresql": "postgresql", "redis-server": "redis-server"}
	for _, p := range packages {
		if svc := services[str(p)]; svc != "" {
			commands = append(commands, []string{"systemctl", "enable", "--now", svc})
		}
	}
	data["runcmd"] = commands
	return map[string][]byte{"user-data": append([]byte("#cloud-config\n"), must(json.Marshal(data))...), "meta-data": must(json.Marshal(doc{"instance-id": id, "local-hostname": name})), "network-config": must(json.Marshal(doc{"version": 2, "ethernets": doc{"ethernet": doc{"match": doc{"name": "e*"}, "dhcp4": true}}}))}
}
func seedISO(ctx context.Context, path, name, username, ssh, id string, packages []any) {
	dir := must(os.MkdirTemp(filepath.Dir(path), "seed-data-"))
	defer os.RemoveAll(dir)
	for name, raw := range seedPayloads(name, username, ssh, id, packages) {
		if e := os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
			panic(e)
		}
	}
	command := exec.CommandContext(ctx, "xorriso", "-as", "mkisofs", "-quiet", "-volid", "CIDATA", "-joliet", "-rock", "-o", path, dir)
	if e := command.Run(); e != nil {
		fail("NoCloud-ISO konnte nicht erstellt werden. xorriso im Image prüfen.")
	}
}
func (s *Server) upload(ctx context.Context, c Connection, node, store, path, name, content string) any {
	if !assetNameRE.MatchString(name) || !contains("iso,import", content) {
		fail("Ungültiger Upload.")
	}
	f := must(os.Open(path))
	defer f.Close()
	st := must(f.Stat())
	h := sha256.New()
	must(io.Copy(h, f))
	must(f.Seek(0, io.SeekStart))
	prefix := &bytes.Buffer{}
	writer := multipart.NewWriter(prefix)
	for k, v := range map[string]string{"content": content, "checksum": hex.EncodeToString(h.Sum(nil)), "checksum-algorithm": "sha256"} {
		if e := writer.WriteField(k, v); e != nil {
			panic(e)
		}
	}
	must(writer.CreateFormFile("filename", name))
	header := append([]byte{}, prefix.Bytes()...)
	boundary := writer.Boundary()
	suffix := []byte("\r\n--" + boundary + "--\r\n")
	body := io.MultiReader(bytes.NewReader(header), f, bytes.NewReader(suffix))
	target := c.URL + "/api2/json/nodes/" + url.PathEscape(node) + "/storage/" + url.PathEscape(store) + "/upload"
	req := must(http.NewRequestWithContext(ctx, "POST", target, body))
	req.ContentLength = int64(len(header)) + st.Size() + int64(len(suffix))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "PVEAPIToken="+must(s.Store.Decrypt(c.Token)))
	client := *s.PVE.Client
	client.Timeout = 30 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		panic(&Problem{"Upload-Antwort unklar. Keine automatische Wiederholung.", 502, true})
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		panic(&Problem{"Proxmox-Upload abgelehnt. Storage und Datastore.AllocateTemplate prüfen.", 502, resp.StatusCode >= 500})
	}
	var envelope doc
	if e := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&envelope); e != nil {
		panic(&Problem{"Ungültige Upload-Antwort.", 502, true})
	}
	return envelope["data"]
}
func (s *Server) processRegistry(ctx context.Context, op doc) {
	if strings.HasSuffix(str(op["stage"]), "_submitting") {
		op["status"] = "needs_attention"
		s.persist("operations", op, "Übermittlung nach Neustart unklar. Keine automatische Wiederholung.")
		return
	}
	err := guarded(func() {
		a := configOf(op["args"])
		c := s.conn(str(op["connection_id"]))
		endpoint := str(a["endpoint"])
		valid := false
		for _, v := range must(ValidateEndpoints(c.URL, c.Endpoints)) {
			if v == endpoint {
				valid = true
			}
		}
		if !valid {
			fail("Ziel-Endpunkt wurde entfernt.")
		}
		pin := c
		pin.URL = endpoint
		pin.Endpoints = nil
		node := str(op["node"])
		base := "/nodes/" + url.PathEscape(node)
		vm := baseVM(node, "qemu", int(num(op["vmid"])))
		imageName := "orbit-img-" + str(op["id"]) + ".qcow2"
		seedName := "orbit-seed-" + str(op["id"]) + ".iso"
		op["status"] = "running"
		s.persist("operations", op, "")
		begin := func(stage string) { op["stage"] = stage + "_submitting"; s.persist("operations", op, "") }
		waiting := func(stage string, result any) {
			op["upid"] = taskID(result)
			op["stage"] = stage + "_waiting"
			s.persist("operations", op, "")
		}
		advance := func(stage string) { op["stage"] = stage; s.persist("operations", op, "") }
		if op["stage"] == "queued" {
			targets := s.registryTargets(ctx, c, node)
			for k, choices := range map[string]string{"storage": "image_storages", "import_storage": "import_storages", "iso_storage": "iso_storages", "bridge": "bridges"} {
				if !hasChoice(targets[choices], str(a[k])) {
					fail("Zielkonfiguration hat sich geändert.")
				}
			}
			s.unused(ctx, c, int(num(op["vmid"])), str(op["name"]))
			image := configOf(a["image"])
			begin("image")
			var result any
			if a["mode"] == "web" {
				result = s.call(ctx, pin, "POST", base+"/storage/"+url.PathEscape(str(a["import_storage"]))+"/download-url", doc{"content": "import", "url": image["url"], "filename": imageName, "checksum": image["sha256"], "checksum-algorithm": "sha256", "verify-certificates": 1})
			} else {
				func() {
					path, cleanup := s.Catalog.asset(ctx, image)
					defer cleanup()
					result = s.upload(ctx, pin, node, str(a["import_storage"]), path, imageName, "import")
				}()
			}
			waiting("image", result)
		}
		if op["stage"] == "image_waiting" {
			s.waitTask(ctx, c, node, str(op["upid"]))
			advance("seed")
		}
		if op["stage"] == "seed" {
			work := filepath.Join(s.Store.Dir, "registry-work")
			if e := os.MkdirAll(work, 0700); e != nil {
				panic(e)
			}
			dir := must(os.MkdirTemp(work, "seed-"))
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, seedName)
			pkgs, _ := a["packages"].([]any)
			seedISO(ctx, path, str(op["name"]), str(a["username"]), str(a["ssh"]), str(op["id"]), pkgs)
			begin("seed")
			waiting("seed", s.upload(ctx, pin, node, str(a["iso_storage"]), path, seedName, "iso"))
		}
		if op["stage"] == "seed_waiting" {
			s.waitTask(ctx, c, node, str(op["upid"]))
			advance("create")
		}
		if op["stage"] == "create" {
			s.unused(ctx, c, int(num(op["vmid"])), str(op["name"]))
			fields := doc{"vmid": op["vmid"], "name": op["name"], "cores": a["cpu"], "sockets": 1, "memory": int(num(a["ram"])) * 1024, "scsihw": "virtio-scsi-single", "scsi0": str(a["storage"]) + ":0,import-from=" + str(a["import_storage"]) + ":import/" + imageName, "ide2": str(a["iso_storage"]) + ":iso/" + seedName + ",media=cdrom", "net0": "virtio,bridge=" + str(a["bridge"]), "boot": "order=scsi0", "serial0": "socket", "ostype": "l26", "agent": "enabled=1", "onboot": 0, "description": "Orbit Registry: " + str(a["package_id"]) + " " + str(a["package_version"])}
			begin("create")
			waiting("create", s.call(ctx, c, "POST", base+"/qemu", fields))
		}
		if op["stage"] == "create_waiting" {
			s.waitTask(ctx, c, node, str(op["upid"]))
			advance("resize")
		}
		if op["stage"] == "resize" {
			config := configOf(s.call(ctx, c, "GET", vm+"/config", nil))
			if config["name"] != op["name"] {
				fail("VM-Identität stimmt nicht mehr.")
			}
			if num(a["disk"]) > diskGiB(config, "scsi0") {
				begin("resize")
				result := s.call(ctx, c, "PUT", vm+"/resize", doc{"disk": "scsi0", "size": str(a["disk"]) + "G"})
				if result != nil {
					waiting("resize", result)
				} else {
					advance("start")
				}
			} else {
				advance("start")
			}
		}
		if op["stage"] == "resize_waiting" {
			s.waitTask(ctx, c, node, str(op["upid"]))
			advance("start")
		}
		if op["stage"] == "start" {
			if a["start"] == true {
				vm := s.findVM(ctx, c, int(num(op["vmid"])), node)
				if vm["name"] != op["name"] {
					fail("VM-Identität stimmt nicht mehr.")
				}
				begin("start")
				waiting("start", s.call(ctx, c, "POST", base+"/qemu/"+str(op["vmid"])+"/status/start", nil))
			} else {
				advance("done")
			}
		}
		if op["stage"] == "start_waiting" {
			s.waitTask(ctx, c, node, str(op["upid"]))
			advance("done")
		}
		if op["stage"] == "done" {
			op["status"] = "completed"
			s.persist("operations", op, "VM-Bereitstellung abgeschlossen. Gastanwendung wird durch Cloud-init beim Start installiert; dies bestätigt noch nicht deren Bereitschaft.")
			_ = s.Store.Audit("registry.deployment.completed", str(op["id"]))
		}
	})
	s.finishError(ctx, "operations", op, err)
}
