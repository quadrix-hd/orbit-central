package orbit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func revisionOf(v any) string {
	var normalize func(any) any
	normalize = func(value any) any {
		switch d := value.(type) {
		case map[string]any:
			out := doc{}
			for k, v := range d {
				if !contains("active,exists,uptime", k) {
					out[k] = normalize(v)
				}
			}
			return out
		case []map[string]any:
			out := []any{}
			for _, v := range d {
				out = append(out, normalize(v))
			}
			return out
		case []any:
			out := []any{}
			for _, v := range d {
				out = append(out, normalize(v))
			}
			return out
		}
		return value
	}
	raw := must(json.Marshal(normalize(v)))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func firewallBase(scope, node, guest string, id int) string {
	switch scope {
	case "cluster":
		return "/cluster/firewall"
	case "host":
		return "/nodes/" + url.PathEscape(node) + "/firewall"
	case "vm":
		if guest != "qemu" && guest != "lxc" {
			fail("Ungültiger Gasttyp.")
		}
		if id < 100 || id > 999999999 {
			fail("Ungültige VMID.")
		}
		return baseVM(node, guest, id) + "/firewall"
	}
	fail("Ungültiger Firewall-Bereich.")
	return ""
}
func (s *Server) infraSnapshot(ctx context.Context, c Connection, d doc) doc {
	if c.Kind != "pve" {
		fail("Benötigt Proxmox VE.")
	}
	kind := field(d, "kind", 30, "")
	node := str(d["node"])
	if node != "" && !regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`).MatchString(node) {
		fail("Ungültiger Node.")
	}
	switch kind {
	case "network":
		if node == "" {
			fail("Node fehlt.")
		}
		rows := objects(s.call(ctx, c, "GET", "/nodes/"+url.PathEscape(node)+"/network", nil))
		sort.Slice(rows, func(i, j int) bool { return str(rows[i]["iface"]) < str(rows[j]["iface"]) })
		data := doc{"rows": rows, "node": node, "changes": s.PVE.NetworkChanges(c, "/nodes/"+url.PathEscape(node)+"/network")}
		data["revision"] = revisionOf(doc{"rows": rows, "changes": data["changes"]})
		return data
	case "firewall":
		scope := field(d, "scope", 20, "")
		if scope != "cluster" && node == "" {
			fail("Node fehlt.")
		}
		id := int(num(d["vmid"]))
		guest := str(d["guest_type"])
		if scope == "vm" {
			vm := s.findVM(ctx, c, id, node)
			if vm["type"] != guest {
				fail("Gasttyp hat sich geändert.")
			}
		}
		base := firewallBase(scope, node, guest, id)
		options := configOf(s.call(ctx, c, "GET", base+"/options", nil))
		rules := s.call(ctx, c, "GET", base+"/rules", nil)
		data := doc{"options": options, "rules": rules, "base": base, "scope": scope}
		data["revision"] = revisionOf(doc{"options": options, "rules": rules})
		return data
	}
	fail("Unbekannter Einstellungsbereich.")
	return nil
}
func (s *Server) infraRoutes(w http.ResponseWriter, r *http.Request) (handled bool, err error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/connections/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/connections/") || len(parts) != 2 || parts[1] != "infra" || r.Method != "GET" {
		return false, nil
	}
	handled = true
	err = guarded(func() {
		q := r.URL.Query()
		id, _ := strconv.Atoi(q.Get("vmid"))
		data := s.infraSnapshot(r.Context(), s.conn(parts[0]), doc{"kind": q.Get("kind"), "scope": q.Get("scope"), "node": q.Get("node"), "guest_type": q.Get("guest_type"), "vmid": id})
		writeJSON(w, 200, data)
	})
	return
}
func (s *Server) submitInfra(ctx context.Context, d doc, key string) (doc, int) {
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	c := s.conn(cid)
	kind := field(d, "kind", 30, "")
	intent := field(d, "intent", 30, "")
	if !contains("create,update,delete,apply,revert,options,move", intent) {
		fail("Unbekannte Änderung.")
	}
	if d["acknowledge"] != true {
		fail("Auswirkungen ausdrücklich bestätigen.")
	}
	node := str(d["node"])
	scope := str(d["scope"])
	if kind == "network" || scope != "cluster" {
		node = field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	}
	expected := field(d, "revision", 64, `[a-f0-9]{64}`)
	snapshot := s.infraSnapshot(ctx, c, d)
	if snapshot["revision"] != expected {
		panic(problem("Konfiguration inzwischen verändert. Editor neu laden.", 409))
	}
	fields := doc{}
	method, path, readPath, target := "", "", "", ""
	async := false
	switch kind {
	case "network":
		if d["confirm_target"] != node {
			fail("Node-Namen ausdrücklich bestätigen.")
		}
		readPath = "/nodes/" + url.PathEscape(node) + "/network"
		path = readPath
		target = node
		switch intent {
		case "apply":
			method = "PUT"
			async = true
		case "revert":
			method = "DELETE"
		case "create", "update", "delete":
			iface := field(d, "target", 32, `[a-zA-Z0-9_][a-zA-Z0-9_.:-]*`)
			target = node + " / " + iface
			var old doc
			for _, row := range snapshot["rows"].([]map[string]any) {
				if row["iface"] == iface {
					old = row
				}
			}
			if intent == "create" && old != nil {
				fail("Interface existiert bereits.")
			}
			if intent != "create" && old == nil {
				fail("Interface nicht gefunden.")
			}
			if intent == "delete" {
				if !contains("bridge,bond,vlan", str(old["type"])) {
					fail("Nur virtuelle Linux-Interfaces können hier entfernt werden.")
				}
				method = "DELETE"
				path += "/" + url.PathEscape(iface)
			} else {
				input := configOf(d["fields"])
				typ := field(input, "type", 20, "")
				if !contains("bridge,bond,vlan,eth", typ) || (intent == "create" && typ == "eth") {
					fail("Linux-Bridge, Bond oder VLAN wählen; physische Ports nur bearbeiten.")
				}
				if old != nil && old["type"] != typ {
					fail("Interface-Typ darf nicht geändert werden.")
				}
				fields = networkFields(input, intent == "update")
				if intent == "create" {
					method = "POST"
					fields["iface"] = iface
				} else {
					method = "PUT"
					path += "/" + url.PathEscape(iface)
				}
			}
		default:
			fail("Ungültige Netzwerk-Aktion.")
		}
	case "firewall":
		id := int(num(d["vmid"]))
		guest := str(d["guest_type"])
		base := firewallBase(scope, node, guest, id)
		target = base
		readPath = base
		input := configOf(d["fields"])
		if d["confirm_target"] != scopeTarget(scope, node, id) {
			fail("Firewall-Ziel ausdrücklich bestätigen.")
		}
		target = scopeTarget(scope, node, id)
		switch intent {
		case "options":
			method = "PUT"
			path = base + "/options"
			fields = firewallOptions(input, scope)
			fields["digest"] = configOf(snapshot["options"])["digest"]
		case "create":
			method = "POST"
			path = base + "/rules"
			fields = firewallRuleFields(input)
		case "update", "delete", "move":
			pos := integer(d, "position", 0, 10000)
			var row doc
			for _, r := range objects(snapshot["rules"]) {
				if int(num(r["pos"])) == pos {
					row = r
				}
			}
			if row == nil {
				fail("Regel nicht gefunden.")
			}
			path = base + "/rules/" + strconv.Itoa(pos)
			fields["digest"] = row["digest"]
			if row["digest"] == nil {
				fail("Regel-Digest fehlt; Änderung nicht sicher möglich.")
			}
			if intent == "delete" {
				method = "DELETE"
			} else {
				method = "PUT"
				if intent == "move" {
					fields["moveto"] = integer(input, "moveto", 0, 10000)
				} else {
					fields = firewallRuleFields(input)
					fields["digest"] = row["digest"]
					clear := []string{}
					for _, k := range []string{"source", "dest", "proto", "sport", "dport", "iface", "comment", "macro", "icmp-type", "ipversion"} {
						if _, present := input[k]; present && str(input[k]) == "" {
							clear = append(clear, k)
						}
					}
					if len(clear) > 0 {
						fields["delete"] = strings.Join(clear, ",")
					}
				}
			}
		default:
			fail("Ungültige Firewall-Aktion.")
		}
	default:
		fail("Unbekannter Einstellungsbereich.")
	}
	args := doc{"kind": kind, "intent": intent, "method": method, "path": path, "read_path": readPath, "fields": fields, "revision": expected, "scope": scope, "node": node, "vmid": d["vmid"], "guest_type": d["guest_type"], "async": async, "target": target}
	op := newTask(cid, node, target, "infrastructure", 0, args)
	return s.insertTask(ctx, "operations", key, op)
}
func scopeTarget(scope, node string, id int) string {
	if scope == "cluster" {
		return "Datacenter"
	}
	if scope == "host" {
		return node
	}
	return node + " / " + strconv.Itoa(id)
}
func networkFields(input doc, updating bool) doc {
	out := doc{"type": input["type"]}
	deletes := []string{}
	allowed := map[string]bool{"type": true, "mtu": true, "autostart": true, "bridge_vlan_aware": true, "bridge_ports": true, "bridge_vids": true, "slaves": true, "bond_mode": true, "bond_xmit_hash_policy": true, "vlan-raw-device": true, "vlan-id": true, "cidr": true, "cidr6": true, "gateway": true, "gateway6": true, "comments": true}
	for k, v := range input {
		if !allowed[k] {
			fail("Nicht unterstützte Netzwerk-Einstellung: " + k)
		}
		if k == "type" {
			continue
		}
		if str(v) == "" {
			if updating {
				deletes = append(deletes, k)
			}
			continue
		}
		switch k {
		case "autostart", "bridge_vlan_aware":
			out[k] = boolInt(boolean(input, k))
		case "mtu":
			out[k] = integer(input, k, 68, 65520)
		case "vlan-id":
			out[k] = integer(input, k, 1, 4094)
		case "cidr", "cidr6":
			prefix, e := netip.ParsePrefix(str(v))
			if e != nil || (k == "cidr" && !prefix.Addr().Is4()) || (k == "cidr6" && !prefix.Addr().Is6()) {
				fail("Ungültiges CIDR: " + k)
			}
			out[k] = str(v)
		case "gateway", "gateway6":
			address, e := netip.ParseAddr(str(v))
			if e != nil || (k == "gateway" && !address.Is4()) || (k == "gateway6" && !address.Is6()) {
				fail("Ungültiges Gateway: " + k)
			}
			out[k] = str(v)
		case "bridge_ports", "slaves", "vlan-raw-device":
			for _, name := range strings.Fields(str(v)) {
				if !regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:-]{0,31}$`).MatchString(name) {
					fail("Ungültiger Interface-Name.")
				}
			}
			out[k] = str(v)
		case "bond_mode":
			if !contains("balance-rr,active-backup,balance-xor,broadcast,802.3ad,balance-tlb,balance-alb", str(v)) {
				fail("Ungültiger Bond-Modus.")
			}
			out[k] = v
		case "bond_xmit_hash_policy":
			if !contains("layer2,layer2+3,layer3+4", str(v)) {
				fail("Ungültige Hash-Policy.")
			}
			out[k] = v
		case "bridge_vids":
			if len(str(v)) > 200 || !regexp.MustCompile(`^[0-9 ,;-]+$`).MatchString(str(v)) {
				fail("Ungültige VLAN-Liste.")
			}
			out[k] = v
		case "comments":
			if len(str(v)) > 1000 || strings.ContainsAny(str(v), "\r\n") {
				fail("Ungültiger Kommentar.")
			}
			out[k] = v
		}
	}
	if len(deletes) > 0 {
		sort.Strings(deletes)
		out["delete"] = strings.Join(deletes, ",")
	}
	return out
}
func firewallOptions(input doc, scope string) doc {
	out := doc{}
	clear := []string{}
	for k := range input {
		if contains("policy_in,policy_out,log_level_in,log_level_out", k) && input[k] == "" {
			if scope == "cluster" && strings.HasPrefix(k, "log_") {
				fail("Log-Level werden am Host bzw. Gast gesetzt.")
			}
			if scope == "host" && strings.HasPrefix(k, "policy_") {
				fail("Host-Richtlinien werden vom Datacenter geerbt.")
			}
			clear = append(clear, k)
			continue
		}
		switch k {
		case "enable", "dhcp", "ndp", "macfilter", "ipfilter":
			if k != "enable" && scope != "vm" {
				fail("Diese Option ist nur für Gäste verfügbar.")
			}
			out[k] = boolInt(boolean(input, k))
		case "policy_in", "policy_out":
			if scope == "host" {
				fail("Host-Richtlinien werden vom Datacenter geerbt.")
			}
			v := field(input, k, 10, "")
			if !contains("ACCEPT,DROP,REJECT", v) {
				fail("Ungültige Richtlinie.")
			}
			out[k] = v
		case "log_level_in", "log_level_out":
			if scope == "cluster" {
				fail("Log-Level werden am Host bzw. Gast gesetzt.")
			}
			v := field(input, k, 12, "")
			if !contains("nolog,emerg,alert,crit,err,warning,notice,info,debug", v) {
				fail("Ungültiger Log-Level.")
			}
			out[k] = v
		default:
			fail("Nicht unterstützte Firewall-Option: " + k)
		}
	}
	if len(clear) > 0 {
		sort.Strings(clear)
		out["delete"] = strings.Join(clear, ",")
	}
	return out
}
func firewallRuleFields(input doc) doc {
	out := doc{}
	for k, v := range input {
		if !contains("type,action,enable,proto,source,dest,sport,dport,iface,comment,log,macro,icmp-type,ipversion", k) {
			fail("Nicht unterstütztes Regelfeld: " + k)
		}
		if k == "ipversion" {
			if v == "" {
				continue
			}
			n := integer(input, k, 4, 6)
			if n != 4 && n != 6 {
				fail("IPv4 oder IPv6 wählen.")
			}
			out[k] = n
			continue
		}
		if k == "enable" {
			out[k] = boolInt(boolean(input, k))
			continue
		}
		text, ok := v.(string)
		if !ok || len(text) > 500 || strings.ContainsAny(text, "\r\n\x00") {
			fail("Ungültiges Regelfeld: " + k)
		}
		if text != "" {
			out[k] = text
		}
	}
	if !contains("in,out,forward", str(out["type"])) || !contains("ACCEPT,DROP,REJECT", str(out["action"])) {
		fail("Richtung und Aktion wählen.")
	}
	return out
}
func (s *Server) processInfra(ctx context.Context, op doc) {
	if op["stage"] == "submitting" {
		op["status"] = "needs_attention"
		s.persist("operations", op, "Übermittlung unklar. Netzwerk/Firewall und Proxmox-Aufgaben prüfen; keine Wiederholung.")
		return
	}
	err := guarded(func() {
		a := configOf(op["args"])
		c := s.conn(str(op["connection_id"]))
		if op["stage"] == "queued" {
			fresh := s.infraSnapshot(ctx, c, a)
			if fresh["revision"] != a["revision"] {
				panic(problem("Konfiguration wurde seit der Bestätigung verändert. Neu laden.", 409))
			}
			op["status"] = "running"
			op["stage"] = "submitting"
			s.persist("operations", op, "Bestätigte Änderung an Proxmox übergeben.")
			result := s.call(ctx, c, str(a["method"]), str(a["path"]), configOf(a["fields"]))
			if result == nil {
				op["stage"] = "done"
			} else {
				op["upid"] = taskID(result)
				op["stage"] = "waiting"
			}
			s.persist("operations", op, "")
		}
		if op["stage"] == "waiting" {
			s.waitTask(ctx, c, str(op["node"]), str(op["upid"]))
			op["stage"] = "done"
		}
		if op["stage"] == "done" {
			op["status"] = "completed"
			message := "Firewall-Änderung gespeichert."
			if a["kind"] == "network" {
				message = "Netzwerkkonfiguration vorgemerkt. Aktivierung erfolgt ausschließlich über Änderungen anwenden."
				if a["intent"] == "apply" {
					message = "Proxmox hat die Netzwerkkonfiguration angewendet."
				}
				if a["intent"] == "revert" {
					message = "Ausstehende Netzwerkkonfiguration verworfen."
				}
			}
			s.persist("operations", op, message)
			_ = s.Store.Audit("infrastructure.completed", str(op["id"]))
		}
	})
	s.finishError(ctx, "operations", op, err)
}
