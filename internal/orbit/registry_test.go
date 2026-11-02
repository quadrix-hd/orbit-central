package orbit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedISOHasReadableNoCloudFiles(t *testing.T) {
	if _, e := exec.LookPath("xorriso"); e != nil {
		t.Skip("xorriso not installed")
	}
	path := filepath.Join(t.TempDir(), "seed.iso")
	if e := guarded(func() {
		seedISO(context.Background(), path, "vm", "orbit", "ssh-ed25519 AAAATEST", "test-instance", []any{"nginx"})
	}); e != nil {
		t.Fatal(e)
	}
	raw := must(os.ReadFile(path))
	if !strings.Contains(string(raw), "CD001") || !strings.Contains(string(raw), "CIDATA") {
		t.Fatal("not an ISO9660 CIDATA volume")
	}
	out, e := exec.Command("xorriso", "-indev", path, "-find", "/", "-type", "f", "-exec", "lsdl", "--").CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
	for _, name := range []string{"user-data", "meta-data", "network-config"} {
		if !strings.Contains(string(out), name) {
			t.Fatal("NoCloud file missing", name)
		}
	}
}
func TestRegistryDeploymentWebAndCacheWorkflow(t *testing.T) {
	if _, e := exec.LookPath("xorriso"); e != nil {
		t.Skip("xorriso not installed")
	}
	for _, mode := range []string{"web", "cache"} {
		t.Run(mode, func(t *testing.T) {
			s, f := actionServer(t)
			c, _, _ := catalogFixture(t, mode)
			s.Catalog = c
			if e := c.Sync(context.Background()); e != nil {
				t.Fatal(e)
			}
			f.config["name"] = "registry-vm"
			d := doc{"request_key": randomID(), "connection_id": "test", "node": "node1", "action": "registry_deploy", "package_id": "example", "package_version": "1", "acknowledge": true, "vmid": 101, "name": "registry-vm", "username": "orbit", "ssh": "ssh-ed25519 AAAATEST", "start": false, "storage": "local", "import_storage": "local", "iso_storage": "local", "bridge": "vmbr0", "cpu": 2, "ram": 4, "disk": 16}
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" || len(f.writes) != 3 {
				t.Fatalf("status %v events %v writes=%d", op["status"], op["events"], len(f.writes))
			}
			joined := strings.Join(f.writes, "\n")
			if strings.Contains(joined, "/status/start") {
				t.Fatal("registry started VM without selection")
			}
			if mode == "web" && !strings.Contains(f.writes[0], "/download-url") {
				t.Fatal("web image not downloaded by Proxmox")
			}
			if mode == "cache" && !strings.Contains(f.writes[0], "/upload") {
				t.Fatal("cached image not uploaded")
			}
			if !strings.Contains(joined, "CIDATA") {
				t.Fatal("no cloud-init ISO uploaded")
			}
		})
	}
}
func TestRegistryCrashMarkersNeverRepeat(t *testing.T) {
	for _, stage := range []string{"image_submitting", "seed_submitting", "create_submitting", "resize_submitting", "start_submitting"} {
		s, f := actionServer(t)
		op := newTask("test", "node1", "registry-vm", "registry_deploy", 101, doc{})
		op["stage"] = stage
		_ = guarded(func() { s.insertTask("operations", randomID(), op) })
		s.processOperation(context.Background(), op)
		if op["status"] != "needs_attention" || len(f.writes) != 0 {
			t.Fatal(stage, op)
		}
	}
}
