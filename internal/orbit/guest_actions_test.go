package orbit

import (
	"context"
	"strings"
	"testing"
)

func submitErr(s *Server, d doc) error {
	return guarded(func() { s.submitOperation(context.Background(), d) })
}

func TestPowerActionsDispatchToStatusEndpoints(t *testing.T) {
	for _, tc := range []struct {
		action string
		extra  doc
	}{
		{"stop", doc{"acknowledge": true}}, {"reset", doc{}}, {"suspend", doc{}}, {"resume", doc{}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			s, f := actionServer(t)
			f.resources[0].(doc)["status"] = "running"
			d := operationInput(tc.action)
			for k, v := range tc.extra {
				d[k] = v
			}
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" || len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "POST /nodes/node1/qemu/100/status/"+tc.action) {
				t.Fatalf("state %v writes %v", op["status"], f.writes)
			}
		})
	}
}

func TestPowerActionPreconditions(t *testing.T) {
	s, f := actionServer(t)
	if err := submitErr(s, operationInput("reset")); err == nil {
		t.Fatal("reset of a stopped guest must be rejected")
	}
	if err := submitErr(s, operationInput("stop")); err == nil {
		t.Fatal("stop of a stopped guest must be rejected")
	}
	f.resources[0].(doc)["status"] = "running"
	if err := submitErr(s, operationInput("stop")); err == nil || !strings.Contains(err.Error(), "acknowledge") {
		t.Fatalf("hard stop needs acknowledgement, got %v", err)
	}
	f.resources[0].(doc)["type"] = "lxc"
	if err := submitErr(s, operationInput("reset")); err == nil {
		t.Fatal("reset is QEMU only")
	}
	if len(f.writes) != 0 {
		t.Fatalf("rejected actions must not write: %v", f.writes)
	}
}

func TestCloneFullAndLinked(t *testing.T) {
	s, f := actionServer(t)
	d := operationInput("clone")
	d["newid"], d["new_name"], d["full"], d["target"], d["storage"] = 150, "vm-copy", true, "node2", "local"
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 1 || !strings.Contains(f.writes[0], "POST /nodes/node1/qemu/100/clone") {
		t.Fatalf("state %v writes %v", op["status"], f.writes)
	}
	for _, want := range []string{"newid=150", "name=vm-copy", "full=1", "target=node2", "storage=local"} {
		if !strings.Contains(f.writes[0], want) {
			t.Fatalf("missing %s in %s", want, f.writes[0])
		}
	}

	linked := operationInput("clone")
	linked["newid"], linked["new_name"], linked["full"] = 151, "vm-linked", false
	if err := submitErr(s, linked); err == nil || !strings.Contains(err.Error(), "Vorlagen") {
		t.Fatalf("linked clone of a normal VM must be rejected, got %v", err)
	}
	f.resources[0].(doc)["template"] = 1
	op = submit(t, s, linked)
	if num(configOf(op["args"])["full"]) != 0 {
		t.Fatalf("linked clone args %v", op["args"])
	}
	if err := submitErr(s, operationInput("start")); err == nil {
		t.Fatal("templates accept no action other than clone")
	}
}

func TestCloneRejectsTakenTargetsAndReservations(t *testing.T) {
	s, f := actionServer(t)
	f.resources = append(f.resources, doc{"type": "qemu", "vmid": 150, "node": "node1", "name": "taken", "status": "stopped"})
	d := operationInput("clone")
	d["newid"], d["new_name"], d["full"] = 150, "vm-copy", true
	if err := submitErr(s, d); err == nil {
		t.Fatal("existing VMID must be rejected")
	}
	d = operationInput("clone")
	d["newid"], d["new_name"], d["full"] = 160, "vm-copy", true
	submit(t, s, d)
	f.resources = append(f.resources, doc{"type": "qemu", "vmid": 101, "node": "node1", "name": "vm-two", "status": "stopped"})
	other := operationInput("clone")
	other["newid"], other["new_name"], other["full"] = 160, "different", true
	other["vmid"], other["confirm_name"] = 101, "vm-two"
	if err := submitErr(s, other); err == nil || !strings.Contains(err.Error(), "reserviert") {
		t.Fatalf("pending clone must reserve its new VMID, got %v", err)
	}
}

func TestConvertToTemplate(t *testing.T) {
	s, f := actionServer(t)
	if err := submitErr(s, operationInput("template")); err == nil {
		t.Fatal("template conversion requires acknowledgement")
	}
	d := operationInput("template")
	d["acknowledge"] = true
	f.result = nil
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 1 || !strings.Contains(f.writes[0], "POST /nodes/node1/qemu/100/template") {
		t.Fatalf("state %v writes %v", op["status"], f.writes)
	}
	f.resources[0].(doc)["status"] = "running"
	d["request_key"] = randomID()
	if err := submitErr(s, d); err == nil {
		t.Fatal("running guests cannot become templates")
	}
}
