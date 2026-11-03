package orbit

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pveproxyAccepts mirrors PVE::APIServer::AnyEvent::file_upload_multipart:
//
//	s/^${delim_re}.*?Content-Disposition: (.*?); name="$name"(.*?${delim_re})/$2/s
//
// is applied for "content", "checksum-algorithm" and "checksum" in this order.
// The lazy ".*?" may skip over (and thereby delete) other parts, so a wrong
// order silently drops "checksum" and Proxmox rejects checksum-algorithm
// without checksum. The file part named "filename" must follow.
func pveproxyAccepts(body, boundary string) bool {
	delim := "--" + regexp.QuoteMeta(boundary) + "\r?\n"
	buf := body
	params := map[string]bool{}
	for _, name := range []string{"content", "checksum-algorithm", "checksum"} {
		re := regexp.MustCompile(`(?s)^` + delim + `.*?Content-Disposition: (.*?); name="` + regexp.QuoteMeta(name) + `"(.*?` + delim + `)`)
		if m := re.FindStringSubmatchIndex(buf); m != nil {
			rest := buf[m[4]:m[5]] + buf[m[1]:] // $2 keeps the next delimiter
			value := regexp.MustCompile(`(?s)^.*?\r?\n\r?\n`).ReplaceAllString(rest, "")
			if v := regexp.MustCompile(`(?s)^(.*?)(` + delim + `)`).FindStringSubmatchIndex(value); v != nil {
				buf = value[v[4]:]
				params[name] = true
			}
		}
	}
	if params["checksum-algorithm"] != params["checksum"] || !params["content"] {
		return false
	}
	return regexp.MustCompile(`(?s)^` + delim + `Content-Disposition: (.*?); name="filename"; filename="([^"]+)"`).MatchString(buf)
}

func TestUploadFieldOrderMatchesPveproxy(t *testing.T) {
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.Endpoints = nil
	file := filepath.Join(t.TempDir(), "seed.iso")
	if err := os.WriteFile(file, []byte("iso-data"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ { // map iteration used to make this order random
		var accepted bool
		s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			boundary := strings.TrimPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=")
			accepted = pveproxyAccepts(string(raw), boundary)
			if !accepted {
				return response(500, `{"data":null}`), nil
			}
			return response(200, `{"data":"UPID:node1:00000001:00000001:00000001:imgcopy::root@pam:"}`), nil
		})
		if err := guarded(func() { s.upload(context.Background(), c, "node1", "local", file, "orbit-seed-x.iso", "iso") }); err != nil || !accepted {
			t.Fatalf("run %d: upload rejected by pveproxy parser: %v", i, err)
		}
	}
}

func TestUploadErrorIncludesProxmoxReason(t *testing.T) {
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.Endpoints = nil
	file := filepath.Join(t.TempDir(), "seed.iso")
	_ = os.WriteFile(file, []byte("x"), 0600)
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return response(403, `{"data":null,"message":"Permission check failed (/storage/local, Datastore.AllocateTemplate)"}`), nil
	})
	err := guarded(func() { s.upload(context.Background(), c, "node1", "local", file, "orbit-seed-x.iso", "iso") })
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "Datastore.AllocateTemplate)") {
		t.Fatalf("expected status and Proxmox reason, got %v", err)
	}
}
