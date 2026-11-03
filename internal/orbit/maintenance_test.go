package orbit

import (
	"context"
	"strings"
	"testing"
)

const testGiB = 1 << 30

func maintServer(t *testing.T) (*Server, *fakePVE) {
	s, f := actionServer(t)
	f.resources = []any{
		doc{"type": "node", "node": "node1", "status": "online", "maxmem": float64(64 * testGiB), "mem": float64(20 * testGiB)},
		doc{"type": "node", "node": "node2", "status": "online", "maxmem": float64(64 * testGiB), "mem": float64(10 * testGiB)},
		doc{"type": "qemu", "vmid": 100, "node": "node1", "name": "vm-one", "status": "running", "maxmem": float64(8 * testGiB)},
		doc{"type": "qemu", "vmid": 101, "node": "node1", "name": "vm-two", "status": "stopped", "maxmem": float64(8 * testGiB)},
		doc{"type": "lxc", "vmid": 200, "node": "node1", "name": "ct-one", "status": "running", "maxmem": float64(1 * testGiB)},
	}
	return s, f
}

func TestEvacuationPlan(t *testing.T) {
	s, f := maintServer(t)
	moves := s.evacuationPlan(context.Background(), s.conn("test"), "node1", false)
	if len(moves) != 2 {
		t.Fatalf("only running guests are moved: %v", moves)
	}
	if moves[0]["vmid"] != 100 || moves[0]["to"] != "node2" || moves[0]["status"] != "pending" {
		t.Fatalf("largest guest first to node2: %v", moves[0])
	}
	if moves[1]["vmid"] != 200 || moves[1]["status"] != "skipped" {
		t.Fatalf("running container needs restart migration: %v", moves[1])
	}
	f.resources[1].(doc)["mem"] = float64(60 * testGiB)
	moves = s.evacuationPlan(context.Background(), s.conn("test"), "node1", true)
	if moves[0]["status"] != "skipped" || !strings.Contains(str(moves[0]["message"]), "Arbeitsspeicher") {
		t.Fatalf("no capacity must skip: %v", moves[0])
	}
	f.resources[1].(doc)["status"] = "offline"
	if err := guarded(func() { s.evacuationPlan(context.Background(), s.conn("test"), "node1", false) }); err == nil {
		t.Fatal("evacuation without a target host must fail")
	}
}

func TestMaintenanceEvacuateAndReturn(t *testing.T) {
	s, f := maintServer(t)
	ctx := context.Background()
	c := s.conn("test")
	d := s.enterMaintenance(ctx, c, "node1", "RAM-Tausch", true, "admin")
	if d["state"] != "evacuating" || !s.inMaintenance("test", "node1") {
		t.Fatalf("state %v", d["state"])
	}
	if err := guarded(func() { s.enterMaintenance(ctx, c, "node1", "", true, "admin") }); err == nil {
		t.Fatal("double maintenance accepted")
	}
	// Run all migrations: each pass submits one, the worker completes it.
	for i := 0; i < 10; i++ {
		s.advanceMaintenance(ctx)
		for _, op := range s.allDocs("operations") {
			if op["status"] == "queued" {
				if op["action"] != "migrate" || configOf(op["args"])["target"] != "node2" {
					t.Fatalf("unexpected op %v", op)
				}
				s.processOperation(ctx, op)
				vm := int(num(op["vmid"]))
				for _, r := range f.resources {
					if int(num(r.(doc)["vmid"])) == vm {
						r.(doc)["node"] = "node2"
					}
				}
			}
		}
	}
	d = s.maintDoc(maintID("test", "node1"))
	if d["state"] != "active" {
		t.Fatalf("evacuation not finished: %v %v", d["state"], d["moves"])
	}
	ops := s.allDocs("operations")
	if len(ops) != 2 {
		t.Fatalf("expected two migrations, got %d", len(ops))
	}
	for _, op := range ops {
		if op["vmid"] == float64(200) && configOf(op["args"])["restart"] != float64(1) {
			t.Fatalf("container must use restart migration: %v", op["args"])
		}
	}
	if err := guarded(func() { s.exitMaintenance(ctx, c, "node1", true, "admin") }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		s.advanceMaintenance(ctx)
		for _, op := range s.allDocs("operations") {
			if op["status"] == "queued" {
				s.processOperation(ctx, op)
				for _, r := range f.resources {
					if num(r.(doc)["vmid"]) == num(op["vmid"]) {
						r.(doc)["node"] = "node1"
					}
				}
			}
		}
	}
	if s.inMaintenance("test", "node1") || len(s.allDocs("operations")) != 4 {
		t.Fatalf("guests must return and maintenance end: %d ops", len(s.allDocs("operations")))
	}
}

func TestRollingRebootPausesWhenGuestsRemain(t *testing.T) {
	s, f := maintServer(t)
	ctx := context.Background()
	rd := doc{"id": rollingID("test"), "kind": "rolling", "connection_id": "test", "nodes": []any{"node1"}, "index": 0, "step": "evacuate", "state": "running", "by": "admin", "return_guests": true, "lxc_restart": false}
	s.saveMaint(rd)
	for i := 0; i < 10; i++ {
		s.advanceMaintenance(ctx)
		for _, op := range s.allDocs("operations") {
			if op["status"] == "queued" {
				s.processOperation(ctx, op)
				for _, r := range f.resources {
					if num(r.(doc)["vmid"]) == num(op["vmid"]) {
						r.(doc)["node"] = "node2"
					}
				}
			}
		}
	}
	rd = s.maintDoc(rollingID("test"))
	if rd["state"] != "paused" || !strings.Contains(str(rd["error"]), "ct-one") {
		t.Fatalf("running container must pause the rolling reboot: %v %v", rd["state"], rd["error"])
	}
	for _, w := range f.writes {
		if strings.Contains(w, "/status") && strings.Contains(w, "reboot") {
			t.Fatal("host must not reboot while guests run")
		}
	}
	f.resources[4].(doc)["status"] = "stopped"
	rd["state"] = "running"
	s.saveMaint(rd)
	s.advanceMaintenance(ctx)
	s.advanceMaintenance(ctx)
	rebooted := false
	for _, w := range f.writes {
		if strings.HasPrefix(w, "POST /nodes/node1/status") && strings.Contains(w, "reboot") {
			rebooted = true
		}
	}
	if rd = s.maintDoc(rollingID("test")); !rebooted || rd["step"] != "wait" {
		t.Fatalf("reboot expected: %v step %v", f.writes, rd["step"])
	}
}

func TestMigrateOptions(t *testing.T) {
	s, f := actionServer(t)
	f.resources[0].(doc)["status"] = "running"
	d := operationInput("migrate")
	d["target"], d["with_local_disks"] = "node2", true
	op := submit(t, s, d)
	if a := configOf(op["args"]); a["with-local-disks"] != 1 || a["online"] != 1 {
		t.Fatalf("args %v", a)
	}
	s.processOperation(context.Background(), op)
	f.resources[0].(doc)["type"] = "lxc"
	f.config["hostname"] = "vm-one"
	d = operationInput("migrate")
	d["target"] = "node2"
	if err := submitErr(s, d); err == nil {
		t.Fatal("running container migration without restart accepted")
	}
	d["request_key"], d["restart"] = randomID(), true
	op = submit(t, s, d)
	if configOf(op["args"])["restart"] != 1 {
		t.Fatalf("args %v", op["args"])
	}
}
