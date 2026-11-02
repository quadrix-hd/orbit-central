package orbit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store := testStore(t)
	pve, err := NewProxmox(store, "")
	if err != nil {
		t.Fatal(err)
	}
	static := t.TempDir()
	if err = os.WriteFile(filepath.Join(static, "index.html"), []byte(`<meta name="orbit-mode" content="demo">`), 0600); err != nil {
		t.Fatal(err)
	}
	return NewServer(store, pve, "http://orbit.test:8080", static), store
}
func request(s *Server, method, path, body string, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:1234"
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-Orbit-CSRF", csrf)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func setup(t *testing.T, s *Server, store *Store) (*http.Cookie, string) {
	t.Helper()
	token, err := os.ReadFile(filepath.Join(store.Dir, "setup-token"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": string(token), "password": "password-for-test"})
	w := request(s, "POST", "/api/setup", string(body), nil, "", s.Origin)
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	var data map[string]string
	if err = json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	return cookies[0], data["csrf"]
}
func TestSetupLoginSessionAndLogout(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure cookie")
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "setup-token")); !os.IsNotExist(err) {
		t.Fatal("setup token retained")
	}
	w := request(s, "GET", "/api/auth", "", cookie, "", "")
	if !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Body.String())
	}
	w = request(s, "POST", "/api/setup", `{"password":"password-for-test","token":"x"}`, nil, "", s.Origin)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	w = request(s, "POST", "/api/logout", `{}`, cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(s, "GET", "/api/connections", "", cookie, "", "")
	if w.Code != 401 {
		t.Fatal("session remained valid")
	}
	w = request(s, "POST", "/api/login", `{"password":"password-for-test"}`, nil, "", s.Origin)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Body.String())
	}
}
func TestOriginAndCSRFGuards(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	for _, tc := range []struct{ origin, csrf string }{{"https://attacker.test", csrf}, {s.Origin, ""}, {s.Origin, "bad"}} {
		w := request(s, "POST", "/api/logout", `{}`, cookie, tc.csrf, tc.origin)
		if w.Code != 403 {
			t.Fatalf("guard: %d", w.Code)
		}
	}
	w := request(s, "POST", "/api/logout", `{}`, cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestLoginRateLimit(t *testing.T) {
	s, _ := testServer(t)
	for i := 0; i < 11; i++ {
		w := request(s, "POST", "/api/login", `{"password":"bad"}`, nil, "", s.Origin)
		want := 401
		if i == 10 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
}
func TestBodyValidationAndHead(t *testing.T) {
	s, _ := testServer(t)
	for _, body := range []string{`[]`, `null`, `{} {}`, strings.Repeat("x", 65537)} {
		w := request(s, "POST", "/api/setup", body, nil, "", s.Origin)
		if w.Code != 400 && w.Code != 413 {
			t.Fatalf("body validation: %d", w.Code)
		}
	}
	w := request(s, "HEAD", "/", "", nil, "", "")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD not supported")
	}
	w = request(s, "GET", "/", "", nil, "", "")
	if !strings.Contains(w.Body.String(), `content="live"`) {
		t.Fatal("served demo instead of live")
	}
	w = request(s, "GET", "/../master.key", "", nil, "", "")
	if w.Code != 404 {
		t.Fatal("static path traversal")
	}
}
func TestUnportedWritesAreExplicitAndDoNotQueue(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	for _, path := range []string{"/api/operations", "/api/jobs", "/api/consoles", "/api/catalog/sync"} {
		w := request(s, "POST", path, `{}`, cookie, csrf, s.Origin)
		if w.Code != 501 || !strings.Contains(w.Body.String(), "noch nicht portiert") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, table := range []string{"operations", "jobs"} {
		var count int
		if err := store.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("unported action queued")
		}
	}
}
func TestDashboardReadEndpoints(t *testing.T) {
	s, store := testServer(t)
	cookie, _ := setup(t, s, store)
	for _, path := range []string{"/api/connections", "/api/jobs", "/api/operations", "/api/blueprints", "/api/audit", "/api/catalog", "/api/capabilities"} {
		w := request(s, "GET", path, "", cookie, "", "")
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}
func TestMaintenanceBlocksWrites(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	if err := os.WriteFile(filepath.Join(store.Dir, "maintenance"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/logout", `{}`, cookie, csrf, s.Origin)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = request(s, "GET", "/api/health", "", nil, "", "")
	if w.Code != 200 {
		t.Fatal("maintenance stopped health")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestConnectionCreateEditAndTokenSecrecy(t *testing.T) {
	s, store := testServer(t)
	cookie, csrf := setup(t, s, store)
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return response(200, `{"data":{"version":"9"}}`), nil })
	body := `{"name":"Cluster","url":"https://pve:8006","token":"user@pve!orbit=private-secret","kind":"pve"}`
	w := request(s, "POST", "/api/connections", body, cookie, csrf, s.Origin)
	if w.Code != 201 || strings.Contains(w.Body.String(), "private-secret") {
		t.Fatal(w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created["id"].(string)
	c, err := store.Connection(id)
	if err != nil || c.Token == "user@pve!orbit=private-secret" {
		t.Fatal("credential not encrypted")
	}
	plain, err := store.Decrypt(c.Token)
	if err != nil || plain != "user@pve!orbit=private-secret" {
		t.Fatal("credential lost")
	}
	w = request(s, "GET", "/api/connections", "", cookie, "", "")
	if strings.Contains(w.Body.String(), "private-secret") || strings.Contains(w.Body.String(), c.Token) {
		t.Fatal("credential exposed")
	}
	w = request(s, "POST", "/api/connections/"+id, `{"name":"Renamed","token":""}`, cookie, csrf, s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	updated, err := store.Connection(id)
	if err != nil || updated.Token != c.Token || updated.Name != "Renamed" {
		t.Fatal("edit did not preserve credential")
	}
	w = request(s, "POST", "/api/connections", body, cookie, csrf, s.Origin)
	if w.Code != 409 {
		t.Fatal("duplicate accepted")
	}
}

func TestSecureSessionCookieAndExpiry(t *testing.T) {
	s, store := testServer(t)
	s.Origin = "https://orbit.example"
	cookie, _ := setup(t, s, store)
	if !cookie.Secure {
		t.Fatal("HTTPS cookie not Secure")
	}
	if _, err := store.DB.Exec("UPDATE sessions SET expires=0"); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/connections", "", cookie, "", "")
	if w.Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestGoNoticeInServedUI(t *testing.T) {
	s, _ := testServer(t)
	if err := os.WriteFile(filepath.Join(s.Static, "index.html"), []byte(`<html><head><meta name="orbit-mode" content="demo"></head><body></body></html>`), 0600); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/", "", nil, "", "")
	if !strings.Contains(w.Body.String(), "Go-Testversion") || !strings.Contains(w.Body.String(), `content="live"`) {
		t.Fatal("preview not labelled")
	}
}
