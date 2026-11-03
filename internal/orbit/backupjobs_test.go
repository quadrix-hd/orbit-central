package orbit

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func backupJobRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	d := doc{}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
		var err error
		d, err = readBody(w, r)
		if err != nil {
			t.Fatal(err)
		}
	}
	handled, err := s.backupJobRoutes(w, r, d)
	if !handled {
		t.Fatal("not handled")
	}
	if err != nil {
		writeJSON(w, err.(*Problem).Status, doc{"error": err.Error()})
	}
	return w
}

func TestBackupJobCreateUpdateDelete(t *testing.T) {
	s, f := actionServer(t)
	w := backupJobRequest(t, s, "POST", "/api/connections/test/backup-jobs", `{"schedule":"Mon..Fri 21:30","storage":"local","mode":"snapshot","selection":"all","exclude":"105, 106","enabled":true,"keep-daily":7,"keep-weekly":4,"mailto":"ops@example.com","comment":"Nachts"}`)
	if w.Code != 201 || len(f.writes) != 1 {
		t.Fatalf("create: %d %s %v", w.Code, w.Body.String(), f.writes)
	}
	q := must(url.ParseQuery(strings.SplitN(f.writes[0], " ", 3)[2]))
	if !strings.HasPrefix(f.writes[0], "POST /cluster/backup") || q.Get("schedule") != "mon..fri 21:30" || q.Get("all") != "1" || q.Get("exclude") != "105,106" || q.Get("prune-backups") != "keep-daily=7,keep-weekly=4" || q.Get("mailnotification") != "failure" {
		t.Fatalf("create fields: %s", f.writes[0])
	}
	f.result = nil
	w = backupJobRequest(t, s, "POST", "/api/connections/test/backup-jobs/backup-1", `{"schedule":"02:00","storage":"local","mode":"stop","selection":"vmids","vmids":"100 101","enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	q = must(url.ParseQuery(strings.SplitN(f.writes[1], " ", 3)[2]))
	if !strings.HasPrefix(f.writes[1], "PUT /cluster/backup/backup-1") || q.Get("vmid") != "100,101" || q.Get("all") != "0" || q.Get("enabled") != "0" || q.Get("prune-backups") != "keep-all=1" || !strings.Contains(q.Get("delete"), "exclude") || !strings.Contains(q.Get("delete"), "pool") {
		t.Fatalf("update fields: %s", f.writes[1])
	}
	if w := backupJobRequest(t, s, "DELETE", "/api/connections/test/backup-jobs/backup-1", ""); w.Code != 200 || !strings.HasPrefix(f.writes[2], "DELETE /cluster/backup/backup-1") {
		t.Fatalf("delete: %d %v", w.Code, f.writes)
	}
}

func TestBackupJobValidation(t *testing.T) {
	s, f := actionServer(t)
	for _, body := range []string{
		`{"schedule":"01:00; rm -rf","storage":"local","mode":"snapshot","selection":"all","enabled":true}`,
		`{"schedule":"01:00","storage":"local","mode":"fast","selection":"all","enabled":true}`,
		`{"schedule":"01:00","storage":"local","mode":"snapshot","selection":"vmids","vmids":"abc","enabled":true}`,
		`{"schedule":"01:00","storage":"local","mode":"snapshot","selection":"all","enabled":true,"mailto":"not-an-address"}`,
	} {
		if w := backupJobRequest(t, s, "POST", "/api/connections/test/backup-jobs", body); w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	if len(f.writes) != 0 {
		t.Fatalf("invalid input reached Proxmox: %v", f.writes)
	}
	if w := backupJobRequest(t, s, "DELETE", "/api/connections/test/backup-jobs/x$y", ""); w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
}
