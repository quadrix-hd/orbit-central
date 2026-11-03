package orbit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func catalogFixture(t *testing.T, mode string) (*Catalog, doc, []byte) {
	t.Helper()
	payload := []byte("verified image")
	h := sha256.Sum256(payload)
	a := doc{"name": "image.qcow2", "url": "https://registry.test/image.qcow2", "size": float64(len(payload)), "sha256": hex.EncodeToString(h[:])}
	manifest := doc{"schema": 1, "revision": "one", "packages": []any{doc{"id": "example", "name": "Example", "version": "1", "description": "Example image", "category": "Betriebssystem", "assets": []any{a}, "deployment": doc{"kind": "cloud-image", "image_sha256": a["sha256"], "packages": []any{"qemu-guest-agent"}}}}}
	c := NewCatalog(t.TempDir(), "https://registry.test/catalog.json", mode)
	c.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "catalog.json") {
			raw, _ := json.Marshal(manifest)
			return response(200, string(raw)), nil
		}
		return response(200, string(payload)), nil
	})
	return c, manifest, payload
}
func TestCatalogWebAndCacheModes(t *testing.T) {
	for _, mode := range []string{"web", "cache"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _ := catalogFixture(t, mode)
			if e := c.Sync(context.Background()); e != nil {
				t.Fatal(e)
			}
			p, sources := c.Package("example")
			a := configOf(sources[str(configOf(p["deployment"])["image_sha256"])])
			path := filepath.Join(c.Root, "objects", str(a["sha256"]))
			if _, e := os.Stat(path); (e == nil) != (mode == "cache") {
				t.Fatal("unexpected prefetch mode")
			}
			file, cleanup := c.asset(context.Background(), a)
			if !validAsset(file, a) {
				t.Fatal("invalid downloaded image")
			}
			cleanup()
			if mode == "web" {
				if _, e := os.Stat(file); !os.IsNotExist(e) {
					t.Fatal("web download retained")
				}
			} else if !validAsset(path, a) {
				t.Fatal("cache missing")
			}
		})
	}
}
func TestCatalogRejectsTamperingAndKeepsPreviousSnapshot(t *testing.T) {
	c, manifest, _ := catalogFixture(t, "cache")
	if e := c.Sync(context.Background()); e != nil {
		t.Fatal(e)
	}
	before := must(os.ReadFile(filepath.Join(c.Root, "snapshot.json")))
	manifest["revision"] = "two"
	a := configOf(objects(manifest["packages"])[0]["assets"].([]any)[0])
	a["sha256"] = strings.Repeat("0", 64)
	configOf(objects(manifest["packages"])[0]["deployment"])["image_sha256"] = a["sha256"]
	if e := c.Sync(context.Background()); e == nil {
		t.Fatal("bad checksum accepted")
	}
	after := must(os.ReadFile(filepath.Join(c.Root, "snapshot.json")))
	if string(before) != string(after) {
		t.Fatal("previous catalog replaced")
	}
}
func TestCatalogOriginAndModeValidation(t *testing.T) {
	c, manifest, _ := catalogFixture(t, "web")
	for _, address := range []string{"http://registry.test/image", "https://attacker.test/image", "https://user:pw@registry.test/image"} {
		copyRaw := must(json.Marshal(manifest))
		var d doc
		_ = json.Unmarshal(copyRaw, &d)
		objects(objects(d["packages"])[0]["assets"])[0]["url"] = address
		if err := guarded(func() { c.Validate(d) }); err == nil {
			t.Fatal("untrusted origin accepted", address)
		}
	}
	if e := c.SetMode("bad"); e == nil {
		t.Fatal("bad mode accepted")
	}
	if e := c.SetMode("cache"); e != nil {
		t.Fatal(e)
	}
	next := NewCatalog(filepath.Dir(c.Root), c.URL, "web")
	if next.Mode() != "cache" {
		t.Fatal("mode not persisted")
	}
}
func TestSeedPayloadIsSafeNoCloudData(t *testing.T) {
	payload := seedPayloads("my-vm", "operator", "ssh-ed25519 AAAATEST", "instance", []any{"nginx"})
	var d doc
	if e := json.Unmarshal([]byte(strings.TrimPrefix(string(payload["user-data"]), "#cloud-config\n")), &d); e != nil {
		t.Fatal(e)
	}
	if d["ssh_pwauth"] != false || d["disable_root"] != true || len(d["runcmd"].([]any)) != 1 {
		t.Fatal(d)
	}
	if !strings.Contains(string(payload["network-config"]), "dhcp4") {
		t.Fatal("network seed missing")
	}
}

func TestPublishedRegistryManifestIsValid(t *testing.T) {
	raw := must(os.ReadFile("../../docs/catalog.json"))
	var d doc
	if e := json.Unmarshal(raw, &d); e != nil {
		t.Fatal(e)
	}
	c := NewCatalog(t.TempDir(), "https://quadrix-hd.github.io/orbit-central/catalog.json", "web")
	if e := guarded(func() {
		snapshot, assets := c.Validate(d)
		if len(objects(snapshot["packages"])) != 25 || len(assets) != 27 {
			t.Fatalf("missing recipes or deduplicated images: %d packages, %d assets", len(objects(snapshot["packages"])), len(assets))
		}
		withContainers := 0
		for _, p := range objects(snapshot["packages"]) {
			if len(anyList(asDoc(p["deployment"])["containers"])) > 0 {
				withContainers++
			}
		}
		if withContainers != 7 {
			t.Fatalf("container recipes: %d", withContainers)
		}
	}); e != nil {
		t.Fatal(e)
	}
}
