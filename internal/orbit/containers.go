package orbit

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

var lxcArchiveRE = regexp.MustCompile(`(?:^|/)vzdump-lxc-[0-9]+-[^/]+\.tar(?:\.(?:zst|gz|lzo))?$`)

func lxcBackup(volume string) bool {
	return strings.Contains(volume, ":backup/ct/") || lxcArchiveRE.MatchString(volume)
}
func (s *Server) submitContainer(ctx context.Context, d doc, key string) (doc, int) {
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	name := field(d, "name", 63, `[a-zA-Z][a-zA-Z0-9-]{0,62}`)
	id := integer(d, "vmid", 100, 999999999)
	store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
	action := str(d["action"])
	if d["confirm_name"] != name || !boolean(d, "acknowledge") {
		fail("Neuen Containernamen und Auswirkungen bestätigen.")
	}
	c := s.conn(cid)
	if c.Kind != "pve" {
		fail("Proxmox VE benötigt.")
	}
	s.online(ctx, c, node)
	s.storeTarget(ctx, c, node, store, "rootdir")
	s.unused(ctx, c, id, name)
	a := doc{"vmid": id, "hostname": name, "storage": store, "start": 0, "onboot": 0, "force": 0}
	if action == "lxc_restore" {
		archive := field(d, "archive", 512, "")
		if !lxcBackup(archive) {
			fail("LXC-Backup benötigt. QEMU-Backups sind nicht kompatibel.")
		}
		s.volume(ctx, c, node, archive, "backup")
		a["ostemplate"] = archive
		a["restore"] = 1
		a["unique"] = 1
	} else {
		template := field(d, "ostemplate", 512, "")
		s.volume(ctx, c, node, template, "vztmpl")
		bridge := field(d, "bridge", 64, `[a-zA-Z0-9_.-]+`)
		s.bridgeTarget(ctx, c, node, bridge)
		ssh := publicSSH(d["ssh"], true)
		a["ostemplate"] = template
		a["cores"] = integer(d, "cores", 1, 128)
		a["memory"] = integer(d, "memory", 16, 4194304)
		a["swap"] = integer(d, "swap", 0, 4194304)
		a["rootfs"] = store + ":" + strconv.Itoa(integer(d, "disk", 1, 65536))
		a["unprivileged"] = boolInt(boolean(d, "unprivileged"))
		a["ssh-public-keys"] = ssh
		a["net0"] = "name=eth0,type=veth,bridge=" + bridge + ",ip=dhcp,ip6=manual,firewall=1"
		vlan := integer(d, "vlan", 0, 4094)
		if vlan > 0 {
			a["net0"] = str(a["net0"]) + ",tag=" + strconv.Itoa(vlan)
		}
	}
	op := newTask(cid, node, name, action, id, a)
	op["guest_type"] = "lxc"
	return s.insertTask("operations", key, op)
}
func (s *Server) containerPreflight(ctx context.Context, c Connection, op doc) {
	a := configOf(op["args"])
	node := str(op["node"])
	s.online(ctx, c, node)
	s.storeTarget(ctx, c, node, str(a["storage"]), "rootdir")
	content := "vztmpl"
	if op["action"] == "lxc_restore" {
		content = "backup"
	}
	s.volume(ctx, c, node, str(a["ostemplate"]), content)
}
