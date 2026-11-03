package orbit

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIPHonoursOnlyTrustedProxies(t *testing.T) {
	nets, invalid := parseTrustedProxies("10.0.0.0/8, 192.0.2.10 ,fd00::/8")
	if len(invalid) != 0 || len(nets) != 3 {
		t.Fatalf("parse: %v %v", nets, invalid)
	}
	s := &Server{TrustedProxies: nets}
	cases := []struct{ remote, forwarded, want string }{
		{"203.0.113.5:4000", "198.51.100.7", "203.0.113.5"},                  // untrusted peer: header ignored
		{"10.1.2.3:4000", "198.51.100.7", "198.51.100.7"},                    // trusted proxy
		{"10.1.2.3:4000", "6.6.6.6, 198.51.100.7, 10.9.9.9", "198.51.100.7"}, // spoofed prefix is skipped
		{"192.0.2.10:4000", "", "192.0.2.10"},                                // no header
		{"10.1.2.3:4000", "garbage", "10.1.2.3"},                             // malformed header
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-For", c.forwarded)
		}
		if got := s.clientIP(r); got != c.want {
			t.Errorf("%s via %q: got %s want %s", c.remote, c.forwarded, got, c.want)
		}
	}
	if (&Server{}).clientIP(httptest.NewRequest("GET", "/", nil)) != "192.0.2.1" {
		t.Error("without trusted proxies the peer address must be used")
	}
}

func TestValidateTrustedProxies(t *testing.T) {
	if err := ValidateTrustedProxies(""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustedProxies("10.0.0.1,not-an-ip"); err == nil || !strings.Contains(err.Error(), "not-an-ip") {
		t.Fatalf("expected rejection, got %v", err)
	}
}

func TestLoginRateLimitPerForwardedClient(t *testing.T) {
	s, _ := testServer(t)
	s.TrustedProxies, _ = parseTrustedProxies("192.0.2.1")
	login := func(client string) int {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong-password-x"}`))
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", client)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 10; i++ {
		login("198.51.100.1")
	}
	if code := login("198.51.100.1"); code != 429 {
		t.Fatalf("attacker should be limited, got %d", code)
	}
	if code := login("198.51.100.2"); code == 429 {
		t.Fatal("other clients behind the same proxy must not be locked out")
	}
}

func TestSecurityHeadersAndAttachment(t *testing.T) {
	s, _ := testServer(t)
	w := request(s, "GET", "/api/health", "", nil, "", "")
	if w.Header().Get("Permissions-Policy") == "" || w.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatal("missing hardening headers")
	}
	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS must not be sent for an http origin")
	}
	s.Origin = "https://orbit.example"
	if request(s, "GET", "/api/health", "", nil, "", "").Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS expected for an https origin")
	}
	if got := attachment(`evil".iso`); !strings.Contains(got, `filename="evil\".iso"`) {
		t.Fatalf("quote must be escaped: %s", got)
	}
	if got := attachment("übung.iso"); !strings.Contains(got, "filename*=utf-8''") {
		t.Fatalf("non-ascii must use RFC 2231: %s", got)
	}
}
