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
	data, err := s.moduleData(r, c, section)
	return jsonData(w, data, err)
}
func (s *Server) moduleData(r *http.Request, c Connection, section string) (any, error) {
	if c.Kind == "pbs" {
		if section != "protection" {
			return nil, problem("Dieses Modul benötigt Proxmox VE.", 400)
		}
		return map[string]any{"datastores": s.safeRead(r, c, "/status/datastore-usage", nil), "pbs_tasks": s.safeRead(r, c, "/nodes/localhost/tasks", url.Values{"limit": {"30"}})}, nil
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
				sort.Slice(rows, func(i, j int) bool {
					return str(rows[i]["storage"])+"/"+str(rows[i]["node"]) < str(rows[j]["storage"])+"/"+str(rows[j]["node"])
				})
				value["data"] = rows
			}
			result[key] = value
		}
		return result, nil
	}
	if section == "network" || section == "lifecycle" {
		data, err := s.PVE.Call(r.Context(), c, "GET", "/nodes", nil)
		if err != nil {
			return nil, err
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
		return map[string]any{"nodes": result}, nil
	}
	return nil, problem("Unbekanntes Modul.", 404)
}

var diskPattern = regexp.MustCompile(`^(scsi|virtio|sata|ide)\d+$`)
var sizePattern = regexp.MustCompile(`(?:^|,)size=([^,]+)`)
var networkPattern = regexp.MustCompile(`^net\d+$`)

func (s *Server) vmDetail(w http.ResponseWriter, r *http.Request, c Connection, id int) error {
	data, err := s.vmDetailData(r, c, id)
	return jsonData(w, data, err)
}
func (s *Server) vmDetailData(r *http.Request, c Connection, vmid int) (doc, error) {
	if c.Kind != "pve" {
		return nil, problem("VM-Verwaltung benötigt Proxmox VE.", 400)
	}
	data, err := s.PVE.Call(r.Context(), c, "GET", "/cluster/resources", url.Values{"type": {"vm"}})
	if err != nil {
		return nil, err
	}
	var vm map[string]any
	for _, row := range objects(data) {
		if (row["type"] == "qemu" || row["type"] == "lxc") && row["vmid"] == float64(vmid) {
			vm = row
			break
		}
	}
	if vm == nil {
		return nil, problem("VM oder Container nicht gefunden.", 404)
	}
	vm["connection_id"] = c.ID
	node, _ := vm["node"].(string)
	if requested := r.URL.Query().Get("node"); requested != "" && requested != node {
		return nil, problem("Die VM befindet sich inzwischen auf einem anderen Node. Inventar aktualisieren.", 409)
	}
	base := "/nodes/" + url.PathEscape(node) + "/" + fmt.Sprint(vm["type"]) + "/" + fmt.Sprint(vmid)
	data, err = s.PVE.Call(r.Context(), c, "GET", base+"/config", nil)
	if err != nil {
		return nil, err
	}
	config, ok := data.(map[string]any)
	if !ok {
		return nil, problem("Ungültige VM-Konfiguration.", 502)
	}
	clean := map[string]any{}
	for _, key := range []string{"name", "cores", "sockets", "memory", "ostype", "agent", "onboot", "tags", "description", "template", "lock", "boot", "digest", "cpu", "cpulimit", "cpuunits", "balloon", "swap", "hostname", "protection", "bios", "machine", "nameserver", "searchdomain", "features", "unprivileged"} {
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
		if (diskPattern.MatchString(key) || key == "rootfs" || regexp.MustCompile(`^mp[0-9]+$`).MatchString(key)) && !strings.Contains(value, "cloudinit") && !strings.Contains(value, "media=cdrom") {
			size := "—"
			if match := sizePattern.FindStringSubmatch(value); len(match) > 1 {
				size = match[1]
			}
			disks = append(disks, map[string]any{"device": key, "size": size, "volume": strings.Split(value, ",")[0], "mountpoint": propertyParts(value)["mp"]})
		}
		if networkPattern.MatchString(key) {
			parts := map[string]string{}
			for _, pair := range strings.Split(value, ",") {
				k, v, ok := strings.Cut(pair, "=")
				if ok {
					parts[k] = v
				}
			}
			clean[key] = value
			vlan, fw := parts["tag"], parts["firewall"]
			if vlan == "" {
				vlan = "0"
			}
			if fw == "" {
				fw = "0"
			}
			mac := ""
			for _, model := range []string{"virtio", "e1000", "rtl8139", "vmxnet3", "e1000e"} {
				if parts[model] != "" {
					mac = parts[model]
					break
				}
			}
			networks = append(networks, map[string]any{"device": key, "bridge": parts["bridge"], "vlan": vlan, "firewall": fw, "mac": defaultText(mac, parts["hwaddr"]), "name": parts["name"], "ip": parts["ip"], "ip6": parts["ip6"], "gw": parts["gw"], "gw6": parts["gw6"], "mtu": parts["mtu"], "rate": parts["rate"]})
		}
	}
	return map[string]any{"vm": vm, "config": clean, "disks": disks, "networks": networks, "snapshots": s.safeRead(r, c, base+"/snapshot", nil), "metrics": s.safeRead(r, c, base+"/rrddata", url.Values{"timeframe": {"hour"}, "cf": {"AVERAGE"}})}, nil
}
