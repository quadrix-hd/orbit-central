package orbit

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Storage content management: upload ISO images, container templates and
// import images from the browser, let Proxmox download them from a URL,
// and delete ISOs, templates, import images and backups. Guest disks are
// deliberately not deletable here.

var storageFileRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,180}$`)
var deletableContent = map[string]bool{"iso": true, "vztmpl": true, "import": true, "backup": true}

const maxUploadBytes = 64 << 30 // 64 GiB

func checkUploadName(content, name string) {
	if !storageFileRE.MatchString(name) {
		fail("Ungültiger Dateiname.")
	}
	lower := strings.ToLower(name)
	ok := map[string][]string{"iso": {".iso", ".img"}, "vztmpl": {".tar.gz", ".tar.xz", ".tar.zst", ".tgz"}, "import": {".qcow2", ".raw", ".vmdk", ".img", ".ova"}}[content]
	if ok == nil {
		fail("Inhaltstyp nicht erlaubt.")
	}
	for _, ext := range ok {
		if strings.HasSuffix(lower, ext) {
			return
		}
	}
	fail("Dateiendung passt nicht zum Inhaltstyp (" + strings.Join(ok, ", ") + ").")
}

func (s *Server) storageContentRoutes(w http.ResponseWriter, r *http.Request, d doc) (handled bool, err error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/connections/"), "/")
	if len(parts) != 2 || r.Method != "POST" || !contains("storage-upload,storage-download,storage-delete", parts[1]) {
		return false, nil
	}
	err = guarded(func() {
		handled = true
		c := s.conn(parts[0])
		if c.Kind != "pve" {
			fail("Proxmox VE benötigt.")
		}
		who := requestUser(r.Context())
		switch parts[1] {
		case "storage-upload":
			q := r.URL.Query()
			node := field(doc{"v": q.Get("node")}, "v", 64, `[a-zA-Z0-9_.-]+`)
			store := field(doc{"v": q.Get("storage")}, "v", 64, `[a-zA-Z0-9_.-]+`)
			content := q.Get("content")
			name := q.Get("filename")
			checkUploadName(content, name)
			if r.ContentLength <= 0 || r.ContentLength > maxUploadBytes {
				fail("Dateigröße fehlt oder ist zu groß (max. 64 GiB).")
			}
			s.online(r.Context(), c, node)
			s.storeTarget(r.Context(), c, node, store, content)
			// Large uploads take longer than the server's default read timeout.
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(6 * time.Hour))
			_ = rc.SetWriteDeadline(time.Now().Add(6 * time.Hour))
			work := filepath.Join(s.Store.Dir, "registry-work")
			must(0, os.MkdirAll(work, 0700))
			freeSpace(work, r.ContentLength)
			tmp := must(os.CreateTemp(work, "upload-"))
			defer os.Remove(tmp.Name())
			written, copyErr := io.Copy(tmp, io.LimitReader(r.Body, maxUploadBytes+1))
			closeErr := tmp.Close()
			if copyErr != nil || closeErr != nil || written != r.ContentLength {
				fail("Upload abgebrochen. Bitte erneut versuchen.")
			}
			result := s.upload(r.Context(), s.nodeConnection(r.Context(), c, node), node, store, tmp.Name(), name, content)
			_ = s.Store.AuditAs(who, "storage.upload", node+"/"+store+" "+name)
			writeJSON(w, 200, doc{"upid": taskID(result), "volid": store + ":" + content + "/" + name})
		case "storage-download":
			node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
			store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
			content := field(d, "content", 10, "iso|vztmpl|import")
			name := field(d, "filename", 181, "")
			checkUploadName(content, name)
			source := strings.TrimSpace(str(d["url"]))
			u, e := url.Parse(source)
			if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(source) > 2000 {
				fail("Download-URL muss mit http:// oder https:// beginnen.")
			}
			s.online(r.Context(), c, node)
			s.storeTarget(r.Context(), c, node, store, content)
			args := doc{"content": content, "url": source, "filename": name, "verify-certificates": 1}
			if sum := strings.ToLower(strings.TrimSpace(str(d["checksum"]))); sum != "" {
				algo := field(d, "algorithm", 10, "sha256|sha512|sha1|md5")
				if !regexp.MustCompile(`^[0-9a-f]{32,128}$`).MatchString(sum) {
					fail("Prüfsumme muss hexadezimal sein.")
				}
				args["checksum"], args["checksum-algorithm"] = sum, algo
			}
			result := s.call(r.Context(), c, "POST", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(store)+"/download-url", args)
			_ = s.Store.AuditAs(who, "storage.download", node+"/"+store+" "+name+" ← "+u.Host)
			writeJSON(w, 200, doc{"upid": taskID(result)})
		case "storage-delete":
			node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
			store := field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
			volid := field(d, "volume", 300, "")
			if !strings.HasPrefix(volid, store+":") || d["confirm"] != volid {
				fail("Volume zur Bestätigung exakt eingeben.")
			}
			var found doc
			for _, v := range objects(s.call(r.Context(), c, "GET", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(store)+"/content", nil)) {
				if v["volid"] == volid {
					found = v
				}
			}
			if found == nil {
				panic(problem("Volume nicht gefunden.", 404))
			}
			if !deletableContent[str(found["content"])] {
				fail("Gast-Datenträger werden hier nicht gelöscht. Dafür den Gast bearbeiten oder löschen.")
			}
			if str(found["content"]) == "iso" {
				for _, g := range objects(s.call(r.Context(), c, "GET", "/cluster/resources", doc{"type": "vm"})) {
					cfg, e := s.PVE.Call(r.Context(), c, "GET", baseVM(str(g["node"]), str(g["type"]), int(num(g["vmid"])))+"/config", nil)
					if e != nil {
						continue
					}
					for _, v := range configOf(cfg) {
						if strings.Contains(str(v), volid) {
							panic(problem("ISO ist noch in "+str(g["name"])+" ("+str(g["vmid"])+") eingelegt. Zuerst auswerfen.", 409))
						}
					}
				}
			}
			result := s.call(r.Context(), c, "DELETE", "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(store)+"/content/"+url.PathEscape(volid), nil)
			_ = s.Store.AuditAs(who, "storage.delete", node+"/"+volid)
			upid, _ := result.(string) // some storage types delete synchronously
			writeJSON(w, 200, doc{"upid": upid})
		}
	})
	return
}
