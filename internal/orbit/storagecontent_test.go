package orbit

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func storageRequest(t *testing.T, s *Server, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	d := doc{}
	if contentType == "application/json" {
		var err error
		if d, err = readBody(w, r); err != nil {
			t.Fatal(err)
		}
	}
	handled, err := s.storageContentRoutes(w, r, d)
	if !handled {
		t.Fatal("not handled")
	}
	if err != nil {
		writeJSON(w, err.(*Problem).Status, doc{"error": err.Error()})
	}
	return w
}

func TestStorageDownloadAndDelete(t *testing.T) {
	s, f := actionServer(t)
	w := storageRequest(t, s, "/api/connections/test/storage-download", "application/json", `{"node":"node1","storage":"local","content":"iso","filename":"debian-13.iso","url":"https://cdimage.debian.org/debian-13.iso","checksum":"`+strings.Repeat("ab", 32)+`","algorithm":"sha256"}`)
	if w.Code != 200 || len(f.writes) != 1 || !strings.Contains(f.writes[0], "/storage/local/download-url") || !strings.Contains(f.writes[0], "checksum-algorithm=sha256") {
		t.Fatalf("download: %d %s %v", w.Code, w.Body.String(), f.writes)
	}
	for _, bad := range []string{
		`{"node":"node1","storage":"local","content":"iso","filename":"x.exe","url":"https://a/x.exe"}`,
		`{"node":"node1","storage":"local","content":"images","filename":"x.qcow2","url":"https://a/x"}`,
		`{"node":"node1","storage":"local","content":"iso","filename":"x.iso","url":"file:///etc/shadow"}`,
	} {
		if w := storageRequest(t, s, "/api/connections/test/storage-download", "application/json", bad); w.Code != 400 {
			t.Fatalf("accepted %s", bad)
		}
	}
	// local content (fake): local:iso/test.iso and a backup
	if w := storageRequest(t, s, "/api/connections/test/storage-delete", "application/json", `{"node":"node1","storage":"local","volume":"local:iso/test.iso","confirm":"local:iso/other.iso"}`); w.Code != 400 {
		t.Fatal("confirmation mismatch accepted")
	}
	before := len(f.writes)
	w = storageRequest(t, s, "/api/connections/test/storage-delete", "application/json", `{"node":"node1","storage":"local","volume":"local:backup/vzdump-qemu-100.vma.zst","confirm":"local:backup/vzdump-qemu-100.vma.zst"}`)
	if w.Code != 200 || len(f.writes) != before+1 || !strings.HasPrefix(f.writes[before], "DELETE /nodes/node1/storage/local/content/") {
		t.Fatalf("delete: %d %s %v", w.Code, w.Body.String(), f.writes)
	}
}

func TestStorageDeleteRefusesInUseISOAndGuestDisks(t *testing.T) {
	s, f := actionServer(t)
	f.config["ide2"] = "local:iso/test.iso,media=cdrom"
	if w := storageRequest(t, s, "/api/connections/test/storage-delete", "application/json", `{"node":"node1","storage":"local","volume":"local:iso/test.iso","confirm":"local:iso/test.iso"}`); w.Code != 409 {
		t.Fatalf("ISO in use must not be deleted: %d %s", w.Code, w.Body.String())
	}
	if len(f.writes) != 0 {
		t.Fatal("refused delete reached Proxmox")
	}
}

func TestStorageUploadStreamsToNode(t *testing.T) {
	s, f := actionServer(t)
	q := url.Values{"node": {"node1"}, "storage": {"local"}, "content": {"iso"}, "filename": {"tools.iso"}}
	w := storageRequest(t, s, "/api/connections/test/storage-upload?"+q.Encode(), "application/octet-stream", "ISO-DATA")
	if w.Code != 200 || len(f.writes) != 1 || !strings.Contains(f.writes[0], "/storage/local/upload") || !strings.Contains(f.writes[0], "ISO-DATA") {
		t.Fatalf("upload: %d %s %v", w.Code, w.Body.String(), f.writes)
	}
	q.Set("filename", "../../evil.iso")
	if w := storageRequest(t, s, "/api/connections/test/storage-upload?"+q.Encode(), "application/octet-stream", "x"); w.Code != 400 {
		t.Fatal("path traversal in filename accepted")
	}
}
