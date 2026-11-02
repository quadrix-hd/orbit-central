package orbit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type managementFixture struct {
	mu                sync.Mutex
	config            doc
	storage           doc
	guestType, status string
	writes            []string
	forms             []url.Values
	lose              bool
	reads             map[string]int
}

func managementServer(t *testing.T) (*Server, *managementFixture) {
	s, _ := actionServer(t)
	f := &managementFixture{config: doc{"name": "vm-one", "hostname": "vm-one", "digest": "v1", "cores": 2, "sockets": 2, "memory": 2048, "swap": 512, "scsi0": "local:disk,size=16G", "rootfs": "local:ct-disk,size=16G", "mp0": "local:ct-data,size=8G,mp=/existing", "net0": "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0,queues=4,tag=2", "agent": "enabled=1,fstrim_cloned_disks=1"}, storage: doc{"storage": "local", "type": "dir", "digest": "s1", "path": "/mnt/local", "content": "images,rootdir,iso", "password": "not-public", "keyring": "private"}, guestType: "qemu", status: "stopped", reads: map[string]int{}}
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/api2/json")
		var data any
		if r.Method != "GET" {
			raw, _ := io.ReadAll(r.Body)
			v, _ := url.ParseQuery(string(raw))
			f.writes = append(f.writes, r.Method+" "+p)
			f.forms = append(f.forms, v)
			if f.lose {
				return nil, io.ErrUnexpectedEOF
			}
			if strings.HasPrefix(p, "/storage") {
				data = doc{"storage": "local"}
			} else if r.Method == "POST" || strings.HasSuffix(p, "/resize") {
				data = "UPID:node1:test"
			}
		} else {
			f.reads[p]++
			switch {
			case p == "/cluster/resources":
				data = []any{doc{"type": f.guestType, "vmid": 100, "node": "node1", "name": "vm-one", "status": f.status, "cpu": 0.01, "mem": 1}}
			case p == "/nodes":
				data = []any{doc{"node": "node1", "status": "online"}}
			case p == "/storage":
				data = []any{doc{"storage": "local", "type": "dir"}}
			case p == "/storage/local":
				data = f.storage
			case strings.HasSuffix(p, "/config"):
				data = f.config
			case strings.HasSuffix(p, "/storage"):
				data = []any{doc{"storage": "local", "active": 1, "content": "images,rootdir,iso,backup"}, doc{"storage": "target", "active": 1, "content": "images,rootdir"}}
			case strings.HasSuffix(p, "/network"):
				data = []any{doc{"iface": "vmbr0", "type": "bridge"}, doc{"iface": "vmbr1", "type": "bridge"}}
			case strings.HasSuffix(p, "/qemu"):
				data = []any{doc{"vmid": 100, "cpu": 0.77, "mem": 123456, "status": "running", "maxmem": 2147483648}}
			case strings.HasSuffix(p, "/lxc"):
				data = []any{doc{"vmid": 200, "cpu": 0.42, "mem": 654321, "swap": 4096, "status": "running"}}
			case p == "/nodes/node1/status":
				data = doc{"cpu": 0.66, "memory": doc{"used": 4567, "total": 9999}, "swap": doc{"used": 123, "total": 4096}}
			case strings.Contains(p, "/tasks/"):
				data = doc{"status": "stopped", "exitstatus": "OK"}
			case strings.HasSuffix(p, "/content"):
				data = []any{doc{"volid": "local:z"}, doc{"volid": "local:a"}}
			default:
				data = []any{}
			}
		}
		raw, _ := json.Marshal(doc{"data": data})
		return response(200, string(raw)), nil
	})
	return s, f
}
func editInput(category string, fields doc) doc {
	return doc{"action": "guest_edit", "request_key": randomID(), "connection_id": "test", "node": "node1", "vmid": 100, "confirm_name": "vm-one", "acknowledge": true, "digest": "v1", "category": category, "fields": fields}
}
func TestGuestResourcesPreserveSocketsAndAllowMiBAndPendingChanges(t *testing.T) {
	for _, kind := range []string{"qemu", "lxc"} {
		t.Run(kind, func(t *testing.T) {
			s, f := managementServer(t)
			f.guestType = kind
			f.status = "running"
			d := editInput("resources", doc{"memory": 1536, "cores": 3})
			if kind == "lxc" {
				d["fields"].(doc)["swap"] = 256
			}
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" {
				t.Fatal(op)
			}
			if len(f.writes) != 1 || strings.Contains(f.writes[0], "status/") {
				t.Fatal("implicit lifecycle call", f.writes)
			}
			if f.forms[0].Get("memory") != "1536" || f.forms[0].Has("sockets") {
				t.Fatal("unrelated resource overwritten", f.forms)
			}
			want := "POST"
			if kind == "lxc" {
				want = "PUT"
			}
			if !strings.HasPrefix(f.writes[0], want+" /nodes/node1/"+kind+"/100/config") {
				t.Fatal(f.writes)
			}
		})
	}
}
func TestGuestRevisionAndDispatchRecheckPreventOverwrite(t *testing.T) {
	s, f := managementServer(t)
	d := editInput("resources", doc{"memory": 1024})
	d["digest"] = "old"
	if guarded(func() { s.submitOperation(context.Background(), d) }) == nil {
		t.Fatal("stale submit accepted")
	}
	d["digest"] = "v1"
	op := submit(t, s, d)
	f.config["digest"] = "v2"
	s.processOperation(context.Background(), op)
	if op["status"] != "failed" || len(f.writes) != 0 {
		t.Fatal(op, f.writes)
	}
}
func TestGuestNetworkPreservesModelMACAndUnknownOptions(t *testing.T) {
	s, f := managementServer(t)
	d := editInput("network", doc{"bridge": "vmbr1", "tag": 0})
	d["device"] = "net0"
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	value := f.forms[0].Get("net0")
	for _, v := range []string{"virtio=AA:BB:CC:DD:EE:FF", "queues=4", "bridge=vmbr1"} {
		if !strings.Contains(value, v) {
			t.Fatal(value)
		}
	}
	if strings.Contains(value, "tag=") {
		t.Fatal("VLAN not removed", value)
	}
}
func TestLXCNetworkPreservesOtherOptions(t *testing.T) {
	s, f := managementServer(t)
	f.guestType = "lxc"
	f.config["net0"] = "name=eth0,type=veth,bridge=vmbr0,ip=dhcp,hwaddr=AA:BB:CC:DD:EE:FF,firewall=1"
	d := editInput("network", doc{"ip": "192.0.2.2/24", "gw": "192.0.2.1", "ip6": "auto"})
	d["device"] = "net0"
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	value := f.forms[0].Get("net0")
	for _, v := range []string{"name=eth0", "type=veth", "ip=192.0.2.2/24", "hwaddr=AA:BB:CC:DD:EE:FF", "firewall=1", "ip6=auto"} {
		if !strings.Contains(value, v) {
			t.Fatal(value)
		}
	}
}
func TestGuestAgentTogglePreservesAgentOptions(t *testing.T) {
	s, f := managementServer(t)
	d := editInput("options", doc{"agent": false})
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	if f.forms[0].Get("agent") != "enabled=0,fstrim_cloned_disks=1" {
		t.Fatal(f.forms)
	}
}
func TestGuestDiskWorkflows(t *testing.T) {
	for _, kind := range []string{"qemu", "lxc"} {
		for _, category := range []string{"disk_add", "disk_resize", "disk_move"} {
			t.Run(kind+"/"+category, func(t *testing.T) {
				s, f := managementServer(t)
				f.guestType = kind
				fields := doc{"size": 32}
				device := "scsi0"
				if kind == "lxc" {
					device = "rootfs"
				}
				if category == "disk_add" {
					fields["storage"] = "target"
					if kind == "lxc" {
						fields["mountpoint"] = "/data"
						fields["backup"] = true
					}
				}
				if category == "disk_move" {
					fields = doc{"storage": "target"}
				}
				d := editInput(category, fields)
				d["device"] = device
				op := submit(t, s, d)
				s.processOperation(context.Background(), op)
				if op["status"] != "completed" || len(f.writes) != 1 {
					t.Fatal(op, f.writes)
				}
				args := f.forms[0]
				if category == "disk_move" && (args.Get("delete") != "0" || !strings.Contains(f.writes[0], map[bool]string{true: "move_volume", false: "move_disk"}[kind == "lxc"])) {
					t.Fatal(f.writes, args)
				}
				if category == "disk_resize" && (args.Get("size") != "32G" || args.Get("digest") != "v1") {
					t.Fatal(args)
				}
				if kind == "lxc" && category == "disk_add" && args.Get("mp1") != "target:32,mp=/data,backup=1" {
					t.Fatal(args)
				}
			})
		}
	}
}
func TestGuestEditsRejectDangerousOrUnmodelledValues(t *testing.T) {
	cases := []doc{editInput("resources", doc{"memory": 0}), editInput("resources", doc{"memory": 2048, "balloon": 4096}), editInput("resources", doc{"memory": 1.5}), editInput("options", doc{"hookscript": "local:evil"}), editInput("network", doc{"ip": "wrong"}), editInput("network", doc{"bridge": "vmbr0,script=bad"}), editInput("disk_resize", doc{"size": 8})}
	for i, d := range cases {
		s, f := managementServer(t)
		d["device"] = "scsi0"
		if d["category"] == "network" {
			d["device"] = "net0"
			f.guestType = "lxc"
		}
		if guarded(func() { s.submitOperation(context.Background(), d) }) == nil {
			t.Fatalf("case %d accepted", i)
		}
		if len(f.writes) > 0 {
			t.Fatal(f.writes)
		}
	}
	s, f := managementServer(t)
	f.guestType = "lxc"
	d := editInput("disk_add", doc{"storage": "local", "size": 8, "mountpoint": "/data,mp=/etc", "backup": true})
	if guarded(func() { s.submitOperation(context.Background(), d) }) == nil {
		t.Fatal("property injection accepted")
	}
}
func TestUncertainGuestWriteNeverRetries(t *testing.T) {
	s, f := managementServer(t)
	op := submit(t, s, editInput("resources", doc{"memory": 1024}))
	f.lose = true
	s.processOperation(context.Background(), op)
	if op["status"] != "needs_attention" {
		t.Fatal(op)
	}
	s.processOperation(context.Background(), op)
	if len(f.writes) != 1 {
		t.Fatal("write retried", f.writes)
	}
}
func storageInput(intent string, fields doc) doc {
	return doc{"action": "storage_config", "request_key": randomID(), "connection_id": "test", "intent": intent, "storage": "local", "confirm_storage": "local", "digest": "s1", "acknowledge": true, "fields": fields}
}
func TestStoragePartialUpdateAndSecretRedaction(t *testing.T) {
	s, f := managementServer(t)
	op := submit(t, s, storageInput("update", doc{"content": "images,rootdir,iso,import", "nodes": "node1", "disable": false}))
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 1 {
		t.Fatal(op, f.writes)
	}
	args := f.forms[0]
	if args.Get("digest") != "s1" || args.Has("path") || args.Has("password") {
		t.Fatal(args)
	}
	raw, _ := json.Marshal(publicStorage(f.storage))
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "not-public") {
		t.Fatal(string(raw))
	}
}
func TestStorageCreationAndClearNodeRestriction(t *testing.T) {
	s, f := managementServer(t)
	op := submit(t, s, storageInput("update", doc{"nodes": ""}))
	s.processOperation(context.Background(), op)
	if f.forms[0].Get("delete") != "nodes" {
		t.Fatal(f.forms)
	}
	d := storageInput("create", doc{"type": "nfs", "server": "192.0.2.10", "export": "/volumes", "content": "images,rootdir"})
	d["storage"] = "new-store"
	d["confirm_storage"] = "new-store"
	op = submit(t, s, d)
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || f.writes[1] != "POST /storage" {
		t.Fatal(op, f.writes)
	}
}
func TestStoragePreconditionsRejectWithoutWrites(t *testing.T) {
	for _, which := range []string{"digest", "confirmation", "unknown", "fixed", "nodes"} {
		t.Run(which, func(t *testing.T) {
			s, f := managementServer(t)
			d := storageInput("update", doc{"disable": true})
			switch which {
			case "digest":
				d["digest"] = "stale"
			case "confirmation":
				d["confirm_storage"] = "other"
			case "unknown":
				d["fields"] = doc{"password": "bad"}
			case "fixed":
				d["fields"] = doc{"path": "/new"}
			case "nodes":
				d["fields"] = doc{"nodes": "missing"}
			}
			if guarded(func() { s.submitOperation(context.Background(), d) }) == nil {
				t.Fatal("accepted")
			}
			if len(f.writes) > 0 {
				t.Fatal(f.writes)
			}
		})
	}
}
func TestStorageDispatchChecksRevisionAndUncertainWrites(t *testing.T) {
	s, f := managementServer(t)
	op := submit(t, s, storageInput("update", doc{"shared": true}))
	f.storage["digest"] = "s2"
	s.processOperation(context.Background(), op)
	if op["status"] != "failed" || len(f.writes) > 0 {
		t.Fatal(op)
	}
	f.storage["digest"] = "s1"
	op = submit(t, s, storageInput("update", doc{"shared": true}))
	f.lose = true
	s.processOperation(context.Background(), op)
	if op["status"] != "needs_attention" {
		t.Fatal(op)
	}
	s.processOperation(context.Background(), op)
	if len(f.writes) != 1 {
		t.Fatal(f.writes)
	}
}
func TestStorageContentSortedByVolume(t *testing.T) {
	s, _ := managementServer(t)
	rows := s.storageContent(context.Background(), s.conn("test"), "node1", "local").([]doc)
	if rows[0]["volid"] != "local:a" {
		t.Fatal(rows)
	}
}
func TestLargeMiBAndFractionalLimitsUseDecimalEncoding(t *testing.T) {
	s, f := managementServer(t)
	op := submit(t, s, editInput("resources", doc{"memory": 2097152, "cpulimit": 0.5}))
	s.processOperation(context.Background(), op)
	if f.forms[0].Get("memory") != "2097152" || f.forms[0].Get("cpulimit") != "0.5" {
		t.Fatal(f.forms)
	}
}
func TestNICDefaultMTUIsOmittedRatherThanInvalidZero(t *testing.T) {
	s, f := managementServer(t)
	f.guestType = "lxc"
	f.config["net0"] = "name=eth0,type=veth,bridge=vmbr0,mtu=1500"
	d := editInput("network", doc{"mtu": 0})
	d["device"] = "net0"
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	if strings.Contains(f.forms[0].Get("net0"), "mtu=") {
		t.Fatal(f.forms)
	}
}
func TestLXCDNSClearRestoresHostDefaults(t *testing.T) {
	s, f := managementServer(t)
	f.guestType = "lxc"
	f.config["nameserver"] = "192.0.2.1"
	d := editInput("options", doc{"nameserver": "", "searchdomain": ""})
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	args := f.forms[0]
	if args.Has("nameserver") || args.Has("searchdomain") || !contains(args.Get("delete"), "nameserver") || !contains(args.Get("delete"), "searchdomain") {
		t.Fatal(args)
	}
}
