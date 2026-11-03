package orbit

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeIdP struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	claims   map[string]any // extra claims for the next token
	nonce    string
	verifier bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{key: must(rsa.GenerateKey(rand.Reader, 2048)), claims: map[string]any{}}
	enc := base64.RawURLEncoding.EncodeToString
	var challenge string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256", "n": enc(f.key.N.Bytes()), "e": enc(big.NewInt(int64(f.key.E)).Bytes())}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) { challenge = r.URL.Query().Get("code_challenge") })
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		f.verifier = enc(sum[:]) == challenge
		if !f.verifier || r.Form.Get("client_secret") != "s3cret" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		claims := map[string]any{"iss": f.srv.URL, "aud": "orbit", "exp": time.Now().Add(time.Hour).Unix(), "nonce": f.nonce, "sub": "1"}
		for k, v := range f.claims {
			claims[k] = v
		}
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
		body, _ := json.Marshal(claims)
		signing := enc(header) + "." + enc(body)
		digest := sha256.Sum256([]byte(signing))
		sig := must(rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:]))
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": signing + "." + enc(sig), "token_type": "Bearer"})
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// ssoLogin runs the browser part of the flow and returns the callback response.
func ssoLogin(t *testing.T, s *Server, f *fakeIdP) *httptest.ResponseRecorder {
	t.Helper()
	w := request(s, "GET", "/api/oidc/login", "", nil, "", "")
	if w.Code != 302 {
		t.Fatalf("login redirect: %d %s", w.Code, w.Body.String())
	}
	loc := must(url.Parse(w.Header().Get("Location")))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != s.Origin+"/api/oidc/callback" {
		t.Fatalf("authorization request: %s", loc)
	}
	f.nonce = q.Get("nonce")
	_, _ = f.srv.Client().Get(loc.String()) // provider records the PKCE challenge
	return request(s, "GET", "/api/oidc/callback?state="+url.QueryEscape(q.Get("state"))+"&code=abc", "", nil, "", "")
}

func ssoServer(t *testing.T) (*Server, *fakeIdP) {
	s, store := testServer(t)
	setup(t, s, store)
	f := newFakeIdP(t)
	s.PVE.Client.Transport = f.srv.Client().Transport
	s.saveOIDC(doc{"enabled": true, "issuer": f.srv.URL, "client_id": "orbit", "client_secret": "s3cret", "groups_claim": "groups", "admin_group": "orbit-admins", "operator_group": "orbit-ops", "username_claim": "preferred_username"})
	return s, f
}

func TestOIDCLoginMapsGroupsToRoles(t *testing.T) {
	s, f := ssoServer(t)
	f.claims = map[string]any{"preferred_username": "Anna.Admin@corp.example", "groups": []any{"staff", "orbit-ops"}}
	w := ssoLogin(t, s, f)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `url=/"`) || !f.verifier {
		t.Fatalf("callback: %d %s (pkce %v)", w.Code, w.Body.String(), f.verifier)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "orbit_session" {
			cookie = c
		}
	}
	if cookie == nil || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie missing")
	}
	auth := request(s, "GET", "/api/auth", "", cookie, "", "")
	if !strings.Contains(auth.Body.String(), `"username":"anna.admin@corp.example"`) || !strings.Contains(auth.Body.String(), `"role":"operator"`) {
		t.Fatalf("auth: %s", auth.Body.String())
	}
	// Group change at the provider updates the role on the next login.
	f.claims["groups"] = []any{"orbit-admins"}
	ssoLogin(t, s, f)
	u, _, _ := s.Store.userByName("anna.admin@corp.example")
	if u.Role != "admin" {
		t.Fatalf("role after group change: %s", u.Role)
	}
}

func TestOIDCDeniesUnmappedUsersAndBadTokens(t *testing.T) {
	s, f := ssoServer(t)
	f.claims = map[string]any{"preferred_username": "guest", "groups": []any{"staff"}}
	w := ssoLogin(t, s, f)
	if !strings.Contains(w.Body.String(), "sso-error=") || len(w.Result().Cookies()) != 0 {
		t.Fatalf("unmapped user must be denied: %s", w.Body.String())
	}
	if _, _, err := s.Store.userByName("guest"); err == nil {
		t.Fatal("denied user must not be created")
	}
	f.claims = map[string]any{"preferred_username": "x", "groups": []any{"orbit-ops"}, "aud": "someone-else"}
	if w := ssoLogin(t, s, f); !strings.Contains(w.Body.String(), "sso-error=") {
		t.Fatal("token for another audience accepted")
	}
	f.claims = map[string]any{"preferred_username": "x", "groups": []any{"orbit-ops"}, "nonce": "replayed"}
	if w := ssoLogin(t, s, f); !strings.Contains(w.Body.String(), "sso-error=") {
		t.Fatal("token with foreign nonce accepted")
	}
	if w := request(s, "GET", "/api/oidc/callback?state=unknown&code=abc", "", nil, "", ""); !strings.Contains(w.Body.String(), "sso-error=") {
		t.Fatal("unknown state accepted")
	}
}

func TestOIDCSettingsAreAdminOnlyAndHideSecret(t *testing.T) {
	s, store := testServer(t)
	admin, csrf := setup(t, s, store)
	w := request(s, "POST", "/api/users/sso", `{"enabled":true,"issuer":"https://login.example","client_id":"orbit","client_secret":"top-secret","default_role":"viewer"}`, admin, csrf, s.Origin)
	if w.Code != 200 || strings.Contains(w.Body.String(), "top-secret") || !strings.Contains(w.Body.String(), `"has_secret":true`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/users/sso", `{"enabled":true,"issuer":"http://insecure","client_id":"x"}`, admin, csrf, s.Origin); w.Code != 400 {
		t.Fatalf("http issuer must be refused: %d", w.Code)
	}
	if !strings.Contains(request(s, "GET", "/api/auth", "", nil, "", "").Body.String(), `"sso"`) {
		t.Fatal("login screen must learn that SSO is available")
	}
}
