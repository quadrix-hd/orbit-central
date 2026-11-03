package orbit

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func containerServer(t *testing.T) (*Server, *managementFixture) {
	s, f := managementServer(t)
	original := s.PVE.Client.Transport
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/content") {
			return response(200, `{"data":[{"volid":"local:vztmpl/debian.tar.zst","content":"vztmpl"},{"volid":"local:backup/vzdump-lxc-200-2026_11_03-01_00_00.tar.zst","content":"backup"},{"volid":"pbs:backup/ct/200/2026-11-03T01:00:00Z","content":"backup"},{"volid":"local:backup/vzdump-qemu-100.vma.zst","content":"backup"}]}`), nil
		}
		return original.RoundTrip(r)
	})
	return s, f
}
func containerInput(action string) doc {
	return doc{"action": action, "request_key": randomID(), "connection_id": "test", "node": "node1", "vmid": 300, "name": "new-container", "confirm_name": "new-container", "acknowledge": true, "storage": "local", "ostemplate": "local:vztmpl/debian.tar.zst", "archive": "local:backup/vzdump-lxc-200-2026_11_03-01_00_00.tar.zst", "cores": 2, "memory": 1536, "swap": 256, "disk": 8, "bridge": "vmbr0", "vlan": 42, "unprivileged": true, "ssh": "ssh-ed25519 AAAA example"}
}
func TestContainerCreationNeverStartsGuestAndUsesMiB(t *testing.T) {
	s, f := containerServer(t)
	d := containerInput("lxc_create")
	op := submit(t, s, d)
	again, status := s.submitOperation(context.Background(), d)
	if status != 200 || again["id"] != op["id"] {
		t.Fatal("request not idempotent")
	}
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 1 || f.writes[0] != "POST /nodes/node1/lxc" {
		t.Fatal(op, f.writes)
	}
	a := f.forms[0]
	for k, want := range map[string]string{"vmid": "300", "memory": "1536", "swap": "256", "rootfs": "local:8", "unprivileged": "1", "start": "0", "onboot": "0", "force": "0"} {
		if a.Get(k) != want {
			t.Fatalf("%s = %s", k, a.Get(k))
		}
	}
	if a.Has("password") || !strings.Contains(a.Get("net0"), "tag=42") || a.Get("ssh-public-keys") == "" {
		t.Fatal(a)
	}
}
func TestContainerRestoreUsesNewIDAndBackupConfiguration(t *testing.T) {
	for _, archive := range []string{"local:backup/vzdump-lxc-200-2026_11_03-01_00_00.tar.zst", "pbs:backup/ct/200/2026-11-03T01:00:00Z"} {
		t.Run(archive, func(t *testing.T) {
			s, f := containerServer(t)
			d := containerInput("lxc_restore")
			d["archive"] = archive
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			a := f.forms[0]
			if op["status"] != "completed" || a.Get("ostemplate") != archive || a.Get("restore") != "1" || a.Get("unique") != "1" || a.Get("start") != "0" || a.Get("force") != "0" {
				t.Fatal(op, a)
			}
			if a.Has("unprivileged") || a.Has("memory") || a.Has("rootfs") {
				t.Fatal("backup mode/resources overwritten", a)
			}
		})
	}
}
func TestContainerCreationRejectsOccupiedIDWrongMediaAndMissingSSH(t *testing.T) {
	for _, which := range []string{"occupied", "key", "private-key", "confirmation", "source", "qemu-backup", "mode"} {
		t.Run(which, func(t *testing.T) {
			s, f := containerServer(t)
			d := containerInput("lxc_create")
			switch which {
			case "occupied":
				d["vmid"] = 100
			case "key":
				d["ssh"] = ""
			case "private-key":
				d["ssh"] = "-----BEGIN OPENSSH PRIVATE KEY-----"
			case "confirmation":
				d["confirm_name"] = "wrong"
			case "source":
				d["ostemplate"] = "local:vztmpl/missing.tar.zst"
			case "qemu-backup":
				d["action"] = "lxc_restore"
				d["archive"] = "local:backup/vzdump-qemu-100.vma.zst"
			case "mode":
				d["unprivileged"] = "on"
			}
			if guarded(func() { s.submitOperation(context.Background(), d) }) == nil {
				t.Fatal("unsafe/invalid input accepted")
			}
			if len(f.writes) > 0 {
				t.Fatal(f.writes)
			}
		})
	}
}
func TestContainerWorkerRechecksSourceAndDoesNotRepeatUnknownWrite(t *testing.T) {
	s, f := containerServer(t)
	op := submit(t, s, containerInput("lxc_create"))
	original := s.PVE.Client.Transport
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			return response(200, `{"data":[]}`), nil
		}
		return original.RoundTrip(r)
	})
	s.processOperation(context.Background(), op)
	if op["status"] != "failed" || len(f.writes) > 0 {
		t.Fatal(op, f.writes)
	}
	s.PVE.Client.Transport = original
	op = submit(t, s, containerInput("lxc_create"))
	f.lose = true
	s.processOperation(context.Background(), op)
	if op["status"] != "needs_attention" {
		t.Fatal(op)
	}
	s.processOperation(context.Background(), op)
	if len(f.writes) != 1 {
		t.Fatal("write repeated", f.writes)
	}
}
func TestContainerFeaturesMergeInsteadOfDroppingExistingFlags(t *testing.T) {
	s, f := managementServer(t)
	f.guestType = "lxc"
	f.config["features"] = "fuse=1,mknod=1,nesting=0"
	op := submit(t, s, editInput("options", doc{"feature_nesting": true, "feature_keyctl": true}))
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" {
		t.Fatal(op)
	}
	parts := propertyParts(f.forms[0].Get("features"))
	if parts["fuse"] != "1" || parts["mknod"] != "1" || parts["nesting"] != "1" || parts["keyctl"] != "1" {
		t.Fatal(parts)
	}
}
