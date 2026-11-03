package orbit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func login(t *testing.T, s *Server, body string) (*http.Cookie, string, *httptest.ResponseRecorder) {
	t.Helper()
	w := request(s, "POST", "/api/login", body, nil, "", s.Origin)
	if w.Code != 200 {
		return nil, "", w
	}
	var data map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &data)
	for _, c := range w.Result().Cookies() {
		if c.Name == "orbit_session" {
			return c, data["csrf"], w
		}
	}
	t.Fatal("no session cookie")
	return nil, "", w
}

func createUser(t *testing.T, s *Server, cookie *http.Cookie, csrf, name, role string) string {
	t.Helper()
	w := request(s, "POST", "/api/users", `{"username":"`+name+`","role":"`+role+`","password":"password-for-`+name+`"}`, cookie, csrf, s.Origin)
	if w.Code != 201 {
		t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
	}
	var d map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	return d["id"]
}

func TestLegacyPasswordBecomesAdminUser(t *testing.T) {
	store := testStore(t)
	hash := must(PasswordHash("legacy-password-123", ""))
	if _, e := store.DB.Exec("INSERT INTO settings VALUES('password',?); INSERT INTO sessions(id,csrf,expires) VALUES('old','c',9999999999)", hash); e != nil {
		t.Fatal(e)
	}
	if e := store.migrateUsers(); e != nil {
		t.Fatal(e)
	}
	u, saved, err := store.userByName("admin")
	if err != nil || u.Role != "admin" || !CheckPassword("legacy-password-123", saved) {
		t.Fatalf("migration: %v %+v", err, u)
	}
	var owner string
	_ = store.DB.QueryRow("SELECT user_id FROM sessions WHERE id='old'").Scan(&owner)
	if owner != u.ID {
		t.Fatal("existing session must belong to admin after the upgrade")
	}
	if !store.Configured() {
		t.Fatal("migrated store must count as configured")
	}
}

func TestRolesAreEnforcedOnTheServer(t *testing.T) {
	s, store := testServer(t)
	admin, csrf := setup(t, s, store)
	createUser(t, s, admin, csrf, "olga", "operator")
	createUser(t, s, admin, csrf, "vera", "viewer")
	op, opCSRF, _ := login(t, s, `{"username":"olga","password":"password-for-olga"}`)
	view, viewCSRF, _ := login(t, s, `{"username":"vera","password":"password-for-vera"}`)

	if w := request(s, "GET", "/api/connections", "", view, "", ""); w.Code != 200 {
		t.Fatalf("viewer must read: %d", w.Code)
	}
	if w := request(s, "POST", "/api/operations", `{"action":"start"}`, view, viewCSRF, s.Origin); w.Code != 403 {
		t.Fatalf("viewer must not act: %d", w.Code)
	}
	if w := request(s, "POST", "/api/connections", `{"name":"x"}`, op, opCSRF, s.Origin); w.Code != 403 {
		t.Fatalf("operator must not manage connections: %d", w.Code)
	}
	if w := request(s, "POST", "/api/operations", `{"action":"delete"}`, op, opCSRF, s.Origin); w.Code != 403 {
		t.Fatalf("operator must not delete guests: %d", w.Code)
	}
	if w := request(s, "GET", "/api/users", "", op, "", ""); w.Code != 403 {
		t.Fatalf("operator must not list users: %d", w.Code)
	}
	if w := request(s, "GET", "/api/audit", "", view, "", ""); w.Code != 403 {
		t.Fatalf("viewer must not read the audit log: %d", w.Code)
	}
	// Operator actions get past authorization (and then fail validation).
	if w := request(s, "POST", "/api/operations", `{"action":"start"}`, op, opCSRF, s.Origin); w.Code == 403 {
		t.Fatal("operator must be allowed to start guests")
	}
	// Self-service is allowed for every role.
	if w := request(s, "POST", "/api/account/password", `{"current_password":"password-for-vera","new_password":"another-password-1"}`, view, viewCSRF, s.Origin); w.Code != 200 {
		t.Fatalf("viewer password change: %d %s", w.Code, w.Body.String())
	}
}

func TestLastAdminIsProtectedAndDisabledUsersLoseAccess(t *testing.T) {
	s, store := testServer(t)
	admin, csrf := setup(t, s, store)
	me, _, _ := store.userByName("admin")
	if w := request(s, "POST", "/api/users/"+me.ID, `{"role":"viewer","disabled":false}`, admin, csrf, s.Origin); w.Code != 400 {
		t.Fatalf("own role change must be refused: %d", w.Code)
	}
	id := createUser(t, s, admin, csrf, "otto", "operator")
	otto, ottoCSRF, _ := login(t, s, `{"username":"otto","password":"password-for-otto"}`)
	if w := request(s, "POST", "/api/users/"+id, `{"role":"operator","disabled":true}`, admin, csrf, s.Origin); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/connections", "", otto, ottoCSRF, ""); w.Code != 401 {
		t.Fatalf("disabled user must lose the session: %d", w.Code)
	}
	if _, _, w := login(t, s, `{"username":"otto","password":"password-for-otto"}`); w.Code != 401 {
		t.Fatal("disabled user must not log in")
	}
	if w := request(s, "POST", "/api/users/"+id+"/delete", `{"confirm_name":"otto"}`, admin, csrf, s.Origin); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
}

func TestTOTPLogin(t *testing.T) {
	s, store := testServer(t)
	admin, csrf := setup(t, s, store)
	w := request(s, "POST", "/api/account/totp/setup", `{}`, admin, csrf, s.Origin)
	var setupData map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &setupData)
	secret := setupData["secret"]
	if secret == "" || !strings.HasPrefix(setupData["uri"], "otpauth://totp/") {
		t.Fatalf("setup: %s", w.Body.String())
	}
	if w := request(s, "POST", "/api/account/totp/enable", `{"code":"000000"}`, admin, csrf, s.Origin); w.Code != 400 {
		t.Fatal("wrong code must not enable 2FA")
	}
	code := totpCode(secret, time.Now().Unix()/30)
	if w := request(s, "POST", "/api/account/totp/enable", `{"code":"`+code+`"}`, admin, csrf, s.Origin); w.Code != 200 {
		t.Fatalf("enable: %s", w.Body.String())
	}
	_, _, w = login(t, s, `{"username":"admin","password":"password-for-test"}`)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "totp_required") {
		t.Fatalf("password alone must not be enough: %d %s", w.Code, w.Body.String())
	}
	if _, _, w = login(t, s, `{"username":"admin","password":"password-for-test","code":"123456"}`); w.Code != 401 && totpCode(secret, time.Now().Unix()/30) != "123456" {
		t.Fatal("wrong code accepted")
	}
	if c, _, w := login(t, s, `{"username":"admin","password":"password-for-test","code":"`+totpCode(secret, time.Now().Unix()/30)+`"}`); c == nil {
		t.Fatalf("valid code rejected: %s", w.Body.String())
	}
}

func TestTOTPReferenceVector(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, T=59 -> 94287082 (last 6 digits: 287082).
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	if got := totpCode(secret, 59/30); got != "287082" {
		t.Fatalf("got %s", got)
	}
}

func TestAPITokens(t *testing.T) {
	s, store := testServer(t)
	admin, csrf := setup(t, s, store)
	create := func(scope string) string {
		w := request(s, "POST", "/api/account/tokens", `{"name":"ci","scope":"`+scope+`","days":30}`, admin, csrf, s.Origin)
		var d map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		if w.Code != 201 || !strings.HasPrefix(str(d["token"]), "orb_") {
			t.Fatalf("token: %d %s", w.Code, w.Body.String())
		}
		return str(d["token"])
	}
	call := func(token, method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.9:1"
		r.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	read, write := create("read"), create("write")
	if c := call(read, "GET", "/api/connections", ""); c != 200 {
		t.Fatalf("read token GET: %d", c)
	}
	if c := call(read, "POST", "/api/connections", `{"name":"x"}`); c != 403 {
		t.Fatalf("read token must not write: %d", c)
	}
	if c := call(write, "POST", "/api/connections", `{"name":"x"}`); c == 403 || c == 401 {
		t.Fatalf("write token (admin) must pass auth without CSRF: %d", c)
	}
	if c := call("orb_invalid", "GET", "/api/connections", ""); c != 401 {
		t.Fatalf("invalid token: %d", c)
	}
	if c := call(read, "POST", "/api/account/tokens", `{"name":"y","scope":"write","days":1}`); c != 403 {
		t.Fatalf("tokens must not mint tokens: %d", c)
	}
}

func TestResetAccessKeepsOtherUsers(t *testing.T) {
	s, store := testServer(t)
	admin, csrf := setup(t, s, store)
	createUser(t, s, admin, csrf, "olga", "operator")
	if e := ResetAccess(store.Dir); e != nil {
		t.Fatal(e)
	}
	if store.Configured() {
		t.Fatal("reset must re-open setup")
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "setup-token")); err != nil {
		t.Fatal("setup token missing")
	}
	if _, _, err := store.userByName("olga"); err != nil {
		t.Fatal("other users must be kept")
	}
	setup(t, s, store) // sets a new admin password
	if !store.Configured() {
		t.Fatal("setup must complete the reset")
	}
}
