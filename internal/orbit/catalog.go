package orbit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Catalog struct {
	Root, URL string
	mu        sync.Mutex
	assetMu   sync.Mutex
	mode      string
	busy      bool
	Client    *http.Client
}

func NewCatalog(dir, source, mode string) *Catalog {
	if mode != "cache" {
		mode = "web"
	}
	c := &Catalog{Root: filepath.Join(dir, "catalog"), URL: source, mode: mode, Client: &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var d doc
	if raw, e := os.ReadFile(filepath.Join(c.Root, "preferences.json")); e == nil && json.Unmarshal(raw, &d) == nil && (d["mode"] == "cache" || d["mode"] == "web") {
		c.mode = str(d["mode"])
	}
	return c
}
func (c *Catalog) Mode() string { c.mu.Lock(); defer c.mu.Unlock(); return c.mode }
func (c *Catalog) atomic(name string, d any) {
	if e := os.MkdirAll(c.Root, 0700); e != nil {
		panic(e)
	}
	f := must(os.CreateTemp(c.Root, ".catalog-"))
	defer os.Remove(f.Name())
	raw := must(json.Marshal(d))
	if _, e := f.Write(raw); e != nil {
		f.Close()
		panic(e)
	}
	if e := f.Sync(); e != nil {
		f.Close()
		panic(e)
	}
	if e := f.Close(); e != nil {
		panic(e)
	}
	if e := os.Rename(f.Name(), filepath.Join(c.Root, name)); e != nil {
		panic(e)
	}
}
func (c *Catalog) SetMode(mode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode != "web" && mode != "cache" {
		return problem("Modus muss web oder cache sein.", 400)
	}
	if c.busy {
		return problem("Katalog wird gerade aktualisiert.", 409)
	}
	if err := guarded(func() { c.atomic("preferences.json", doc{"mode": mode}) }); err != nil {
		return err
	}
	c.mode = mode
	return nil
}
func (c *Catalog) Snapshot() doc {
	raw, e := os.ReadFile(filepath.Join(c.Root, "snapshot.json"))
	if errors.Is(e, os.ErrNotExist) {
		return doc{"packages": []any{}, "sources": doc{}}
	}
	if e != nil {
		panic(e)
	}
	var d doc
	if e = json.Unmarshal(raw, &d); e != nil {
		panic(problem("Lokaler Katalog ist beschädigt.", 500))
	}
	return d
}
func (c *Catalog) Status() doc {
	d := c.Snapshot()
	delete(d, "sources")
	c.mu.Lock()
	d["busy"] = c.busy
	d["mode"] = c.mode
	c.mu.Unlock()
	d["configured"] = c.URL != ""
	var status doc
	if raw, e := os.ReadFile(filepath.Join(c.Root, "status.json")); e == nil && json.Unmarshal(raw, &status) == nil {
		for k, v := range status {
			d[k] = v
		}
	}
	for _, p := range objects(d["packages"]) {
		cached := true
		for _, a := range objects(p["assets"]) {
			st, e := os.Stat(filepath.Join(c.Root, "objects", str(a["sha256"])))
			if e != nil || st.Size() != int64(num(a["size"])) {
				cached = false
			}
		}
		p["cached"] = cached
	}
	return d
}
func catalogOrigin(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		fail("Katalog benötigt HTTPS ohne Zugangsdaten, Query oder Fragment.")
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}
func (c *Catalog) allowed(raw string) bool {
	origin := catalogOrigin(raw)
	return origin == catalogOrigin(c.URL) || contains("https://cloud-images.ubuntu.com,https://cloud.debian.org,https://saimei.ftp.acc.umu.se", origin)
}
func (c *Catalog) fetch(ctx context.Context, raw string) *http.Response {
	if !c.allowed(raw) {
		fail("Downloadquelle ist nicht freigegeben.")
	}
	r := must(http.NewRequestWithContext(ctx, "GET", raw, nil))
	r.Header.Set("User-Agent", "Orbit-Go-Catalog/1")
	response := must(c.Client.Do(r))
	if response.StatusCode != 200 {
		response.Body.Close()
		fail("Katalogquelle meldet keinen erfolgreichen Download.")
	}
	return response
}

var shaRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
var assetNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,159}$`)

func (c *Catalog) Validate(d doc) (doc, doc) {
	if num(d["schema"]) != 1 {
		fail("Nicht unterstütztes Katalogformat.")
	}
	revision := field(d, "revision", 100, "")
	packages, ok := d["packages"].([]any)
	if !ok || len(packages) > 500 {
		fail("Ungültige Paketliste.")
	}
	ids := map[string]bool{}
	assets := doc{}
	out := []any{}
	const limit = 20 * 1024 * 1024 * 1024
	for _, row := range packages {
		p := configOf(row)
		q := doc{}
		for k, max := range map[string]int{"id": 80, "name": 100, "version": 80, "description": 2000, "category": 60} {
			q[k] = field(p, k, max, "")
		}
		id := str(q["id"])
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(id) || ids[id] {
			fail("Ungültige oder doppelte Paket-ID.")
		}
		ids[id] = true
		files, ok := p["assets"].([]any)
		if !ok || len(files) < 1 || len(files) > 20 {
			fail("Jedes Paket benötigt 1 bis 20 Dateien.")
		}
		clean := []any{}
		images := map[string]bool{}
		for _, file := range files {
			a := configOf(file)
			sha := str(a["sha256"])
			name := str(a["name"])
			size := num(a["size"])
			if !shaRE.MatchString(sha) || !assetNameRE.MatchString(name) || size <= 0 || size > limit || size != float64(int64(size)) {
				fail("Ungültige Paketdatei.")
			}
			raw := field(a, "url", 2000, "")
			base := must(url.Parse(c.URL))
			relative := must(url.Parse(raw))
			target := base.ResolveReference(relative).String()
			if !c.allowed(target) {
				fail("Paketquelle nicht freigegeben.")
			}
			if prior, ok := assets[sha].(map[string]any); ok && num(prior["size"]) != size {
				fail("Widersprüchliche Dateigrößen.")
			}
			assets[sha] = doc{"sha256": sha, "size": size, "name": name, "url": target}
			clean = append(clean, doc{"sha256": sha, "size": size, "name": name})
			if strings.HasSuffix(name, ".img") || strings.HasSuffix(name, ".raw") || strings.HasSuffix(name, ".qcow2") {
				images[sha] = true
			}
		}
		q["assets"] = clean
		if dep, exists := p["deployment"]; exists {
			a := configOf(dep)
			image := str(a["image_sha256"])
			if a["kind"] != "cloud-image" || !images[image] {
				fail("Ungültiges Cloud-Image-Rezept.")
			}
			pkgs, ok := a["packages"].([]any)
			if !ok || len(pkgs) > 50 {
				fail("Ungültige Paketliste im Rezept.")
			}
			for _, pkg := range pkgs {
				if !regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,79}$`).MatchString(str(pkg)) {
					fail("Ungültiger Paketname.")
				}
			}
			q["deployment"] = doc{"kind": "cloud-image", "image_sha256": image, "packages": pkgs, "requires_guest_internet": len(pkgs) > 0}
			if raw, exists := a["containers"]; exists {
				containers := validContainers(raw)
				hasDocker := false
				for _, pkg := range pkgs {
					hasDocker = hasDocker || pkg == "docker.io"
				}
				if len(containers) > 0 && !hasDocker {
					fail("Container-Rezepte benötigen das Paket docker.io.")
				}
				q["deployment"].(doc)["containers"] = containers
			}
		}
		out = append(out, q)
	}
	var total float64
	for _, a := range assets {
		total += num(configOf(a)["size"])
	}
	if total > limit {
		fail("Katalog überschreitet 20 GiB.")
	}
	return doc{"schema": 1, "revision": revision, "packages": out}, assets
}
func validAsset(path string, a doc) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Size() != int64(num(a["size"])) {
		return false
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == a["sha256"]
}
func freeSpace(dir string, size int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		panic(err)
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < uint64(size)+256*1024*1024 {
		fail("Nicht genügend freier Speicher für den Marketplace.")
	}
}
func (c *Catalog) download(ctx context.Context, a doc, path string) {
	if !shaRE.MatchString(str(a["sha256"])) || num(a["size"]) < 1 {
		fail("Ungültige Image-Metadaten.")
	}
	r := c.fetch(ctx, str(a["url"]))
	defer r.Body.Close()
	f := must(os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600))
	defer f.Close()
	h := sha256.New()
	size := int64(num(a["size"]))
	n := must(io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, size+1)))
	if n != size || hex.EncodeToString(h.Sum(nil)) != a["sha256"] {
		fail("Größe oder SHA-256 der Paketdatei stimmt nicht.")
	}
	if e := f.Sync(); e != nil {
		panic(e)
	}
}
func (c *Catalog) asset(ctx context.Context, a doc) (string, func()) {
	c.assetMu.Lock()
	defer c.assetMu.Unlock()
	if e := os.MkdirAll(filepath.Join(c.Root, "objects"), 0700); e != nil {
		panic(e)
	}
	path := filepath.Join(c.Root, "objects", str(a["sha256"]))
	if !shaRE.MatchString(str(a["sha256"])) {
		fail("Ungültige Prüfsumme.")
	}
	if validAsset(path, a) {
		return path, func() {}
	}
	freeSpace(c.Root, int64(num(a["size"])))
	f := must(os.CreateTemp(c.Root, ".download-"))
	f.Close()
	temp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(temp)
		}
	}()
	c.download(ctx, a, temp)
	if c.Mode() == "cache" {
		if e := os.Rename(temp, path); e != nil {
			panic(e)
		}
		ok = true
		return path, func() {}
	}
	ok = true
	return temp, func() { os.Remove(temp) }
}
func (c *Catalog) Sync(ctx context.Context) error {
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return nil
	}
	c.busy = true
	mode := c.mode
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.busy = false; c.mu.Unlock() }()
	err := guarded(func() {
		if c.URL == "" {
			fail("Zentrale Katalogquelle fehlt.")
		}
		r := c.fetch(ctx, c.URL)
		defer r.Body.Close()
		raw := must(io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1)))
		if len(raw) > 2*1024*1024 {
			fail("Katalogdatei ist größer als 2 MiB.")
		}
		var d doc
		if e := json.Unmarshal(raw, &d); e != nil {
			fail("Ungültiges Katalog-JSON.")
		}
		snapshot, sources := c.Validate(d)
		if e := os.MkdirAll(c.Root, 0700); e != nil {
			panic(e)
		}
		if mode == "cache" {
			var needed int64
			for _, v := range sources {
				a := configOf(v)
				if !validAsset(filepath.Join(c.Root, "objects", str(a["sha256"])), a) {
					needed += int64(num(a["size"]))
				}
			}
			freeSpace(c.Root, needed)
			for _, v := range sources {
				_, cleanup := c.asset(ctx, configOf(v))
				cleanup()
			}
		}
		snapshot["sources"] = sources
		snapshot["updated"] = now()
		c.atomic("snapshot.json", snapshot)
		c.atomic("status.json", doc{"error": nil, "checked": now()})
	})
	if err != nil {
		message := "Katalog nicht aktualisiert. HTTPS und Quelle prüfen. Bisheriger Stand bleibt aktiv."
		var p *Problem
		if errors.As(err, &p) {
			message = p.Message
		}
		_ = guarded(func() { c.atomic("status.json", doc{"error": message, "checked": now()}) })
	}
	return err
}
func (c *Catalog) Start(ctx context.Context) { go func() { _ = c.Sync(ctx) }() }
func (c *Catalog) Run(ctx context.Context) {
	if c.URL != "" {
		_ = c.Sync(ctx)
	}
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.URL != "" {
				_ = c.Sync(ctx)
			}
		}
	}
}
func (c *Catalog) Package(id string) (doc, doc) {
	snapshot := c.Snapshot()
	for _, p := range objects(snapshot["packages"]) {
		if p["id"] == id {
			return p, configOf(snapshot["sources"])
		}
	}
	fail("Paket ist nicht im lokalen Katalog.")
	return nil, nil
}
