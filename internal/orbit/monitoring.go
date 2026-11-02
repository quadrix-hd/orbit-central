package orbit

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

func (s *Server) module(w http.ResponseWriter, r *http.Request, c Connection, section string) error {
	if c.Kind == "pbs" {
		if section != "protection" {
			return problem("Dieses Modul benötigt Proxmox VE.", 400)
		}
		return jsonData(w, map[string]any{"datastores": s.safeRead(r, c, "/status/datastore-usage", nil), "pbs_tasks": s.safeRead(r, c, "/nodes/localhost/tasks", url.Values{"limit": {"30"}})}, nil)
	}
	sections := map[string]map[string]string{
		"protection": {"backup_jobs": "/cluster/backup", "replication": "/cluster/replication", "ha": "/cluster/ha/status/current"},
		"security":   {"firewall_options": "/cluster/firewall/options", "firewall_rules": "/cluster/firewall/rules", "vnets": "/cluster/sdn/vnets"},
		"storage":    {"storage_config": "/storage", "storage_resources": "/cluster/resources"},
	}
	if paths, ok := sections[section]; ok {
		result := map[string]any{}
		for key, path := range paths {
			value := s.safeRead(r, c, path, nil)
			if key == "storage_resources" && value["ok"] == true {
				rows := []map[string]any{}
				for _, row := range objects(value["data"]) {
					if row["type"] == "storage" {
						rows = append(rows, row)
					}
				}
				value["data"] = rows
			}
			result[key] = value
		}
		return jsonData(w, result, nil)
	}
	if section == "network" || section == "lifecycle" {
		data, err := s.PVE.Call(r.Context(), c, "GET", "/nodes", nil)
		if err != nil {
			return err
		}
		result := []map[string]any{}
		for _, node := range objects(data) {
			if node["status"] != "online" {
				continue
			}
			name, _ := node["node"].(string)
			base := "/nodes/" + url.PathEscape(name)
			entry := map[string]any{"node": name}
			if section == "network" {
				entry["interfaces"] = s.safeRead(r, c, base+"/network", nil)
			} else {
				entry["updates"] = s.safeRead(r, c, base+"/apt/update", nil)
				entry["versions"] = s.safeRead(r, c, base+"/apt/versions", nil)
			}
			result = append(result, entry)
		}
		return jsonData(w, map[string]any{"nodes": result}, nil)
	}
	return problem("Unbekanntes Modul.", 404)
}

var diskPattern = regexp.MustCompile(`^(scsi|virtio|sata|ide)\d+$`)
var sizePattern = regexp.MustCompile(`(?:^|,)size=([^,]+)`)
var networkPattern = regexp.MustCompile(`^net\d+$`)

func (s *Server) vmDetail(w http.ResponseWriter, r *http.Request, c Connection, vmid int) error {
	if c.Kind != "pve" {
		return problem("VM-Verwaltung benötigt Proxmox VE.", 400)
	}
	data, err := s.PVE.Call(r.Context(), c, "GET", "/cluster/resources", url.Values{"type": {"vm"}})
	if err != nil {
		return err
	}
	var vm map[string]any
	for _, row := range objects(data) {
		if (row["type"] == "qemu" || row["type"] == "lxc") && row["vmid"] == float64(vmid) {
			vm = row
			break
		}
	}
	if vm == nil {
		return problem("VM oder Container nicht gefunden.", 404)
	}
	node, _ := vm["node"].(string)
	if requested := r.URL.Query().Get("node"); requested != "" && requested != node {
		return problem("Die VM befindet sich inzwischen auf einem anderen Node. Inventar aktualisieren.", 409)
	}
	base := "/nodes/" + url.PathEscape(node) + "/" + fmt.Sprint(vm["type"]) + "/" + fmt.Sprint(vmid)
	data, err = s.PVE.Call(r.Context(), c, "GET", base+"/config", nil)
	if err != nil {
		return err
	}
	config, ok := data.(map[string]any)
	if !ok {
		return problem("Ungültige VM-Konfiguration.", 502)
	}
	clean := map[string]any{}
	for _, key := range []string{"name", "cores", "sockets", "memory", "ostype", "agent", "onboot", "tags", "description", "template", "lock", "boot"} {
		if value, exists := config[key]; exists {
			clean[key] = value
		}
	}
	if vm["type"] == "lxc" {
		clean["name"] = config["hostname"]
	}
	disks := []map[string]any{}
	networks := []map[string]any{}
	keys := []string{}
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fmt.Sprint(config[key])
		if diskPattern.MatchString(key) && !strings.Contains(value, "cloudinit") && !strings.Contains(value, "media=cdrom") {
			size := "—"
			if match := sizePattern.FindStringSubmatch(value); len(match) > 1 {
				size = match[1]
			}
			disks = append(disks, map[string]any{"device": key, "size": size})
		}
		if networkPattern.MatchString(key) {
			parts := map[string]string{}
			for _, pair := range strings.Split(value, ",") {
				k, v, ok := strings.Cut(pair, "=")
				if ok {
					parts[k] = v
				}
			}
			vlan, fw := parts["tag"], parts["firewall"]
			if vlan == "" {
				vlan = "0"
			}
			if fw == "" {
				fw = "0"
			}
			mac := ""
			for _, model := range []string{"virtio", "e1000", "rtl8139", "vmxnet3"} {
				if parts[model] != "" {
					mac = parts[model]
					break
				}
			}
			networks = append(networks, map[string]any{"device": key, "bridge": parts["bridge"], "vlan": vlan, "firewall": fw, "mac": mac})
		}
	}
	return jsonData(w, map[string]any{"vm": vm, "config": clean, "disks": disks, "networks": networks, "snapshots": s.safeRead(r, c, base+"/snapshot", nil), "metrics": s.safeRead(r, c, base+"/rrddata", url.Values{"timeframe": {"hour"}, "cf": {"AVERAGE"}})}, nil)
}
