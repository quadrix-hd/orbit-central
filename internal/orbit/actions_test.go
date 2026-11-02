package orbit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fakePVE struct {
	writes    []string
	method    string
	config    doc
	resources []any
	result    any
	status    string
	lose      bool
}

func actionServer(t *testing.T) (*Server, *fakePVE) {
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.Endpoints = nil
	if err := store.SaveConnection(c, true); err != nil {
		t.Fatal(err)
	}
	f := &fakePVE{config: doc{"name": "vm-one", "digest": "v1", "scsi0": "local:disk,size=16G", "ide2": "local:cloudinit", "net0": "virtio=AA:BB,bridge=vmbr0", "boot": "order=scsi0"}, resources: []any{doc{"type": "qemu", "vmid": 100, "node": "node1", "name": "vm-one", "status": "stopped"}}, result: "UPID:node1:1", status: "stopped"}
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		var data any
		if r.Method != "GET" {
			var raw []byte
			if r.Body != nil {
				raw, _ = io.ReadAll(r.Body)
			}
			f.writes = append(f.writes, r.Method+" "+path+" "+string(raw))
			if f.lose {
				return nil, io.ErrUnexpectedEOF
			}
			data = f.result
		} else {
			switch {
			case path == "/cluster/resources":
				data = f.resources
			case path == "/version":
				data = doc{"version": "9.0"}
			case path == "/cluster/status":
				data = []any{doc{"type": "cluster", "name": "cluster", "quorate": 1}, doc{"type": "node", "name": "node1", "local": 1}}
			case path == "/nodes":
				data = []any{doc{"node": "node1", "status": "online"}, doc{"node": "node2", "status": "online"}}
			case strings.HasSuffix(path, "/storage"):
				data = []any{doc{"storage": "local", "active": 1, "content": "images,iso,backup,import"}}
			case strings.HasSuffix(path, "/network"):
				data = []any{doc{"iface": "vmbr0", "type": "bridge"}}
			case strings.HasSuffix(path, "/config"):
				data = f.config
			case strings.Contains(path, "/tasks/"):
				data = doc{"status": "stopped", "exitstatus": "OK"}
			case strings.HasSuffix(path, "/snapshot"):
				data = []any{doc{"name": "snap1"}}
			case path == "/cluster/ha/resources":
				data = []any{}
			case path == "/cluster/nextid":
				data = "101"
			case strings.HasSuffix(path, "/content"):
				data = []any{doc{"volid": "local:iso/test.iso"}, doc{"volid": "local:backup/vzdump-qemu-100.vma.zst"}}
			default:
				data = []any{}
			}
		}
		raw, _ := json.Marshal(doc{"data": data})
		return response(200, string(raw)), nil
	})
	return s, f
}
func operationInput(action string) doc {
	return doc{"request_key": randomID(), "connection_id": "test", "node": "node1", "vmid": 100, "confirm_name": "vm-one", "action": action}
}
func submit(t *testing.T, s *Server, d doc) doc {
	t.Helper()
	var op doc
	err := guarded(func() {
		var status int
		op, status = s.submitOperation(context.Background(), d)
		if status != 202 {
			t.Fatalf("submit status %d", status)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func TestOperationDispatchesSupportedActions(t *testing.T) {
	cases := []struct {
		action string
		args   doc
		path   string
		sync   bool
	}{
		{"start", doc{}, "/status/start", false}, {"configure", doc{"cpu": 2, "ram": 4}, "/config", true}, {"snapshot", doc{"snapshot_name": "snap1"}, "/snapshot", false}, {"backup", doc{"storage": "local"}, "/vzdump", false}, {"migrate", doc{"target": "node2"}, "/migrate", false}, {"disk_add", doc{"storage": "local", "disk": 8}, "/config", false}, {"disk_resize", doc{"device": "scsi0", "disk": 24}, "/resize", true}, {"nic", doc{"device": "net0", "bridge": "vmbr0", "vlan": 12, "firewall": true}, "/config", true}, {"metadata", doc{"new_name": "renamed", "description": "note", "onboot": true}, "/config", true}, {"iso", doc{"iso": "local:iso/test.iso"}, "/config", true}, {"rollback", doc{"snapshot_name": "snap1", "acknowledge": true}, "/snapshot/snap1/rollback", false}, {"snapshot_delete", doc{"snapshot_name": "snap1", "acknowledge": true}, "/snapshot/snap1", false}, {"delete", doc{"confirm_vmid": 100, "acknowledge": true}, "/qemu/100", false}, {"ha", doc{"ha_state": "started"}, "/cluster/ha/resources", true},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			s, f := actionServer(t)
			f.config["ide2"] = "none,media=cdrom"
			d := operationInput(tc.action)
			for k, v := range tc.args {
				d[k] = v
			}
			if tc.sync {
				f.result = nil
			}
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" || len(f.writes) != 1 || !strings.Contains(f.writes[0], tc.path) {
				t.Fatalf("state %+v writes %v", op, f.writes)
			}
		})
	}
}
func TestOperationIdentityRaceDoesNotWrite(t *testing.T) {
	s, f := actionServer(t)
	op := submit(t, s, operationInput("start"))
	f.resources[0].(doc)["name"] = "other-vm"
	s.processOperation(context.Background(), op)
	if len(f.writes) != 0 || op["status"] != "failed" {
		t.Fatal(op, f.writes)
	}
}
func TestUncertainDispatchNeverRepeats(t *testing.T) {
	s, f := actionServer(t)
	op := submit(t, s, operationInput("start"))
	f.lose = true
	s.processOperation(context.Background(), op)
	if op["status"] != "needs_attention" || len(f.writes) != 1 {
		t.Fatal(op, f.writes)
	}
	op["status"] = "running"
	s.processOperation(context.Background(), op)
	if len(f.writes) != 1 {
		t.Fatal("write repeated")
	}
}
func TestIdempotentRequestsAndCrossQueueReservation(t *testing.T) {
	s, _ := actionServer(t)
	d := operationInput("start")
	op := submit(t, s, d)
	var again doc
	err := guarded(func() {
		var status int
		again, status = s.submitOperation(context.Background(), d)
		if status != 200 {
			t.Fatal(status)
		}
	})
	if err != nil || again["id"] != op["id"] {
		t.Fatal(err)
	}
	err = guarded(func() { s.submitOperation(context.Background(), operationInput("snapshot")) })
	if err == nil {
		t.Fatal("missing snapshot input accepted")
	}
	j := newTask("test", "node1", "another", "clone", 100, nil)
	if err = guarded(func() { s.insertTask("jobs", randomID(), j) }); err == nil {
		t.Fatal("cross-queue conflict accepted")
	}
}
func TestDestructiveInputsAndRunningHardwareRejected(t *testing.T) {
	for _, action := range []string{"delete", "rollback", "snapshot_delete", "disk_resize", "ha"} {
		t.Run(action, func(t *testing.T) {
			s, f := actionServer(t)
			d := operationInput(action)
			d["disk"] = 8
			d["device"] = "scsi0"
			d["snapshot_name"] = "snap1"
			d["ha_state"] = "invalid"
			err := guarded(func() { s.submitOperation(context.Background(), d) })
			if err == nil || len(f.writes) != 0 {
				t.Fatal("unconfirmed write accepted")
			}
		})
	}
	s, f := actionServer(t)
	f.resources[0].(doc)["status"] = "running"
	d := operationInput("configure")
	d["cpu"] = 2
	d["ram"] = 4
	if err := guarded(func() { s.submitOperation(context.Background(), d) }); err == nil {
		t.Fatal("running hardware accepted")
	}
}
func TestCreateAndRestoreDoNotOverwrite(t *testing.T) {
	for _, action := range []string{"create", "restore"} {
		t.Run(action, func(t *testing.T) {
			s, f := actionServer(t)
			d := operationInput(action)
			d["name"] = "new-vm"
			d["storage"] = "local"
			d["cpu"] = 2
			d["ram"] = 4
			d["disk"] = 16
			d["bridge"] = "vmbr0"
			d["iso"] = ""
			d["archive"] = "local:backup/vzdump-qemu-100.vma.zst"
			if err := guarded(func() { s.submitOperation(context.Background(), d) }); err == nil {
				t.Fatal("existing VMID accepted")
			}
			d["vmid"] = 101
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" || len(f.writes) != 1 {
				t.Fatal(op, f.writes)
			}
		})
	}
}
func TestJobCrashMarkersNeverDispatch(t *testing.T) {
	for _, stage := range []string{"clone_submitting", "configure_submitting", "resize_submitting", "start_submitting"} {
		s, f := actionServer(t)
		j := newTask("test", "node1", "vm-one", "clone", 100, nil)
		j["stage"] = stage
		_ = guarded(func() { s.insertTask("jobs", randomID(), j) })
		s.processJob(context.Background(), j)
		if j["status"] != "needs_attention" || len(f.writes) != 0 {
			t.Fatal(stage, j)
		}
	}
}

func TestBlueprintCloneJobCompletesWithoutStarting(t *testing.T) {
	s, f := actionServer(t)
	f.config["template"] = 1
	f.config["name"] = "new-vm"
	d := doc{"name": "Base", "description": "Test base", "connection_id": "test", "node": "node1", "storage": "local", "bridge": "vmbr0", "username": "orbit", "category": "Betriebssystem", "template_vmid": 9000, "cpu": 2, "ram": 4, "disk": 16, "disk_device": "scsi0"}
	var b doc
	if e := guarded(func() { b = s.blueprint(context.Background(), d) }); e != nil {
		t.Fatal(e)
	}
	var j doc
	if e := guarded(func() {
		j, _ = s.submitJob(context.Background(), doc{"request_key": randomID(), "blueprint_id": b["id"], "name": "new-vm", "cpu": 2, "ram": 4, "disk": 16, "ssh": "", "start": false})
	}); e != nil {
		t.Fatal(e)
	}
	s.processJob(context.Background(), j)
	if j["status"] != "completed" || len(f.writes) != 2 {
		t.Fatal(j, f.writes)
	}
	for _, w := range f.writes {
		if strings.Contains(w, "/status/start") {
			t.Fatal("started without checkbox")
		}
	}
}
func TestResumeOperationPollsWithoutSubmittingAgain(t *testing.T) {
	s, f := actionServer(t)
	op := submit(t, s, operationInput("start"))
	op["stage"] = "waiting"
	op["status"] = "running"
	op["upid"] = "UPID:node1:1"
	s.persist("operations", op, "")
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 0 {
		t.Fatal(op, f.writes)
	}
}
func TestLXCAndPowerActionParity(t *testing.T) {
	for _, action := range []string{"shutdown", "reboot", "configure"} {
		t.Run(action, func(t *testing.T) {
			s, f := actionServer(t)
			f.resources[0].(doc)["type"] = "lxc"
			f.config["hostname"] = "vm-one"
			d := operationInput(action)
			if action == "configure" {
				d["cpu"] = 2
				d["ram"] = 4
				f.result = nil
			} else {
				f.resources[0].(doc)["status"] = "running"
			}
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" || len(f.writes) != 1 || !strings.Contains(f.writes[0], "/lxc/100") {
				t.Fatal(op, f.writes)
			}
		})
	}
}
