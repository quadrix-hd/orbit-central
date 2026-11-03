package orbit

import (
	"context"
	"strings"
	"testing"
)

func TestRestoreTestLifecycle(t *testing.T) {
	s, f := actionServer(t)
	ctx := context.Background()
	rt := s.createRestoreTest(ctx, doc{"connection_id": "test", "node": "node1", "backup_storage": "local", "storage": "local", "source_vmid": 100}, "admin")
	if rt["state"] != "queued" || rt["archive"] != "local:backup/vzdump-qemu-100.vma.zst" {
		t.Fatalf("%v", rt)
	}
	if err := guarded(func() {
		s.createRestoreTest(ctx, doc{"connection_id": "test", "node": "node1", "backup_storage": "local", "storage": "local", "source_vmid": 555}, "admin")
	}); err == nil {
		t.Fatal("guest without backup accepted")
	}
	step := func() doc {
		s.advanceMaintenance(ctx)
		return s.maintDoc(str(rt["id"]))
	}
	rt = step()
	if rt["state"] != "restoring" || rt["vmid"] != float64(101) || !strings.HasPrefix(f.writes[0], "POST /nodes/node1/qemu ") || !strings.Contains(f.writes[0], "archive=local%3Abackup") {
		t.Fatalf("restore not started: %v %v", rt["state"], f.writes)
	}
	f.config = doc{"name": "vm-one", "digest": "x", "net0": "virtio=AA:BB,bridge=vmbr0"}
	rt = step()
	if rt["state"] != "booting" {
		t.Fatalf("state %v events %v", rt["state"], rt["events"])
	}
	joined := strings.Join(f.writes, "\n")
	if !strings.Contains(joined, "link_down%3D1") || !strings.Contains(joined, "name=restoretest-101") || !strings.Contains(joined, "POST /nodes/node1/qemu/101/status/start") {
		t.Fatalf("network must be cut and VM started: %s", joined)
	}
	rt["boot_at"] = now() - 25
	s.saveMaint(rt)
	f.resources = append(f.resources, doc{"type": "qemu", "vmid": 101, "node": "node1", "name": "restoretest-101", "status": "running"})
	f.writes = nil
	rt = step()
	if rt["state"] != "passed" {
		t.Fatalf("state %v %v", rt["state"], rt["result"])
	}
	joined = strings.Join(f.writes, "\n")
	if !strings.Contains(joined, "POST /nodes/node1/qemu/101/status/stop") || !strings.Contains(joined, "DELETE /nodes/node1/qemu/101") {
		t.Fatalf("temporary VM must be removed: %s", joined)
	}
}

func TestRestoreTestNeverDeletesForeignVM(t *testing.T) {
	s, f := actionServer(t)
	f.resources = append(f.resources, doc{"type": "qemu", "vmid": 101, "node": "node1", "name": "production-db", "status": "running"})
	rt := doc{"id": "restore:x", "kind": "restoretest", "connection_id": "test", "node": "node1", "vmid": 101, "state": "booting", "started": now(), "by": "admin"}
	s.saveMaint(rt)
	s.finishRestoreTest(context.Background(), rt, true, "ok")
	if rt["state"] != "failed" || !strings.Contains(str(rt["result"]), "Nicht gelöscht") {
		t.Fatalf("%v %v", rt["state"], rt["result"])
	}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "DELETE") || strings.Contains(w, "/status/stop") {
			t.Fatalf("foreign VM touched: %v", f.writes)
		}
	}
}
