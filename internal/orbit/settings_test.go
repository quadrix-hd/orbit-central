package orbit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPreferencesPersistAndRejectInvalidValues(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	d := defaultPreferences()
	d["theme"] = "ocean"
	d["default_cpu"] = 8
	d["refresh_seconds"] = 60
	raw, _ := json.Marshal(d)
	w := request(s, "POST", "/api/settings", string(raw), cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(s, "GET", "/api/settings", "", cookie, "", "")
	var got doc
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["theme"] != "ocean" || num(got["default_cpu"]) != 8 {
		t.Fatal(got)
	}
	other := NewServer(store, s.PVE, s.Origin, s.Static)
	w = request(other, "GET", "/api/settings", "", cookie, "", "")
	if !strings.Contains(w.Body.String(), "ocean") {
		t.Fatal("settings not durable")
	}
	for _, bad := range []doc{{"theme": "invalid"}, {"refresh_seconds": 1}, {"default_cpu": 0}, {"default_disk": 4}} {
		candidate := defaultPreferences()
		for k, v := range bad {
			candidate[k] = v
		}
		raw, _ = json.Marshal(candidate)
		w = request(s, "POST", "/api/settings", string(raw), cookie, csrf, s.Origin)
		if w.Code != 400 {
			t.Fatal("invalid value accepted", w.Code)
		}
	}
	w = request(s, "POST", "/api/settings", string(raw), cookie, "", s.Origin)
	if w.Code != 403 {
		t.Fatal("CSRF missing")
	}
}
func TestPasswordChangeRevokesOtherSessionsAndPreservesCurrent(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	login := request(s, "POST", "/api/login", `{"password":"password-for-test"}`, nil, "", s.Origin)
	other := login.Result().Cookies()[0]
	w := request(s, "POST", "/api/account/password", `{"current_password":"wrong","new_password":"new-password-for-test"}`, cookie, csrf, s.Origin)
	if w.Code != 403 {
		t.Fatal("wrong password accepted")
	}
	w = request(s, "POST", "/api/account/password", `{"current_password":"password-for-test","new_password":"new-password-for-test"}`, cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if request(s, "GET", "/api/connections", "", other, "", "").Code != 401 {
		t.Fatal("other session survived")
	}
	if request(s, "GET", "/api/connections", "", cookie, "", "").Code != 200 {
		t.Fatal("current session lost")
	}
	if request(s, "POST", "/api/login", `{"password":"password-for-test"}`, nil, "", s.Origin).Code != 401 {
		t.Fatal("old password accepted")
	}
	if request(s, "POST", "/api/login", `{"password":"new-password-for-test"}`, nil, "", s.Origin).Code != 200 {
		t.Fatal("new password rejected")
	}
}
func TestPasswordChangeAcceptsWhitespaceInCurrentPassword(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	hash := must(PasswordHash("  password-for-test  ", ""))
	_, _ = store.DB.Exec("UPDATE users SET password=? WHERE username='admin'", hash)
	w := request(s, "POST", "/api/account/password", `{"current_password":"  password-for-test  ","new_password":"new-password-for-test"}`, cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestSessionListDoesNotExposeTokensAndRevocationRequiresConfirmation(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	_ = request(s, "POST", "/api/login", `{"password":"password-for-test"}`, nil, "", s.Origin)
	w := request(s, "GET", "/api/account/sessions", "", cookie, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), cookie.Value) || strings.Contains(w.Body.String(), csrf) {
		t.Fatal("session secrets exposed")
	}
	if request(s, "POST", "/api/account/sessions/revoke", `{}`, cookie, csrf, s.Origin).Code != 400 {
		t.Fatal("missing confirmation accepted")
	}
	if request(s, "POST", "/api/account/sessions/revoke", `{"acknowledge":true}`, cookie, csrf, s.Origin).Code != 200 {
		t.Fatal("revoke failed")
	}
	var n int
	_ = store.DB.QueryRow("SELECT count(*) FROM sessions").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestCatalogSourceIsValidatedAndAppliedAfterRestart(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	for _, source := range []string{"http://example.test/catalog.json", "https://user:pw@example.test/catalog.json"} {
		raw, _ := json.Marshal(doc{"url": source})
		if request(s, "POST", "/api/catalog/source", string(raw), cookie, csrf, s.Origin).Code != 400 {
			t.Fatal("invalid source accepted")
		}
	}
	w := request(s, "POST", "/api/catalog/source", `{"url":"https://registry.test/catalog.json"}`, cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.Catalog.URL == "https://registry.test/catalog.json" {
		t.Fatal("source changed during running download")
	}
	next := NewServer(store, s.PVE, s.Origin, s.Static)
	if next.Catalog.URL != "https://registry.test/catalog.json" {
		t.Fatal("source not applied on restart")
	}
}
func TestConnectionDeletionDoesNotTouchProxmoxAndBlocksPendingWork(t *testing.T) {
	s, f := actionServer(t)
	cookie, csrf := setup(t, s, s.Store)
	w := request(s, "POST", "/api/connections/test/delete", `{"confirm_name":"Wrong"}`, cookie, csrf, s.Origin)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	op := newTask("test", "node1", "vm-one", "start", 100, doc{})
	_ = guarded(func() { s.insertTask(context.Background(), "operations", randomID(), op) })
	w = request(s, "POST", "/api/connections/test/delete", `{"confirm_name":"Test"}`, cookie, csrf, s.Origin)
	if w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	_, _ = s.Store.DB.Exec("DELETE FROM operations")
	w = request(s, "POST", "/api/connections/test/delete", `{"confirm_name":"Test"}`, cookie, csrf, s.Origin)
	if w.Code != 200 || len(f.writes) != 0 {
		t.Fatal(w.Body.String(), f.writes)
	}
}
func TestAuthenticatedBackupDownloadIsValidArchive(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	w := request(s, "POST", "/api/system/backup", `{}`, cookie, csrf, s.Origin)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/gzip" {
		t.Fatal(w.Code, w.Body.String())
	}
	gz, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	members := map[string]bool{}
	for {
		h, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		members[h.Name] = true
	}
	if !members["master.key"] || !members["orbit.db"] {
		t.Fatal("incomplete backup")
	}
	if request(s, "POST", "/api/system/backup", `{}`, nil, "", s.Origin).Code != 401 {
		t.Fatal("backup accessible without login")
	}
}
