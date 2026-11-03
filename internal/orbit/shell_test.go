package orbit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shellServer(t *testing.T, needTFA bool) (*Server, *[]string) {
	s, _ := actionServer(t)
	novnc := t.TempDir()
	_ = os.MkdirAll(filepath.Join(novnc, "core"), 0700)
	_ = os.WriteFile(filepath.Join(novnc, "core", "rfb.js"), []byte("x"), 0600)
	t.Setenv("ORBIT_NOVNC_ROOT", novnc)
	calls := []string{}
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		form, _ := url.ParseQuery(string(raw))
		cookie, _ := r.Cookie("PVEAuthCookie")
		auth := r.Header.Get("Authorization")
		calls = append(calls, r.Method+" "+path+" auth="+auth+" cookie="+map[bool]string{true: "yes", false: "no"}[cookie != nil])
		var data any
		switch {
		case path == "/access/ticket":
			if auth != "" {
				t.Errorf("login must not send the API token")
			}
			if form.Get("username") != "root@pam" || (form.Get("password") != "secret" && form.Get("password") != "totp:123456") {
				return response(401, `{"data":null}`), nil
			}
			if needTFA && form.Get("tfa-challenge") == "" {
				data = doc{"ticket": "PVE:root@pam:CHALLENGE", "NeedTFA": 1, "CSRFPreventionToken": "c0"}
			} else {
				data = doc{"ticket": "PVE:root@pam:FULL", "CSRFPreventionToken": "csrf1", "username": "root@pam"}
			}
		case strings.HasSuffix(path, "/vncshell"):
			if cookie == nil || cookie.Value != "PVE:root@pam:FULL" || r.Header.Get("CSRFPreventionToken") != "csrf1" || auth != "" {
				return response(500, `{"data":null}`), nil
			}
			data = doc{"ticket": "PVEVNC:abc", "port": "5901", "user": "root@pam"}
		case path == "/cluster/resources":
			data = []any{}
		case path == "/nodes":
			data = []any{doc{"node": "node1", "status": "online"}}
		default:
			data = []any{}
		}
		out, _ := json.Marshal(doc{"data": data})
		return response(200, string(out)), nil
	})
	return s, &calls
}

func TestHostShellUsesUserTicket(t *testing.T) {
	s, calls := shellServer(t, false)
	sess := &session{ID: "s1", Role: "admin", Username: "admin"}
	d := doc{"connection_id": "test", "node": "node1", "shell": true, "pve_username": "root@pam", "pve_password": "secret"}
	var res doc
	if err := guarded(func() { res = s.createConsole(context.Background(), d, sess) }); err != nil {
		t.Fatalf("%v %v", err, *calls)
	}
	g := s.Console.pending[str(res["id"])]
	if g.AuthCookie != "PVE:root@pam:FULL" || g.Kind != "node" || g.Port != 5901 {
		t.Fatalf("grant %+v", g)
	}
	if err := guarded(func() {
		s.createConsole(context.Background(), doc{"connection_id": "test", "node": "node1", "shell": true}, sess)
	}); err == nil || !strings.Contains(err.Error(), "Anmeldung") {
		t.Fatalf("missing credentials must ask for login, got %v", err)
	}
	if err := guarded(func() {
		s.createConsole(context.Background(), doc{"connection_id": "test", "node": "node1", "shell": true, "pve_username": "root@pam", "pve_password": "wrong"}, sess)
	}); err == nil || err.(*Problem).Status == 401 {
		t.Fatalf("wrong password must fail without logging out of Orbit: %v", err)
	}
	if err := guarded(func() { s.createConsole(context.Background(), d, &session{ID: "s2", Role: "operator"}) }); err == nil {
		t.Fatal("operators must not open a host shell")
	}
}

func TestHostShellTOTP(t *testing.T) {
	s, _ := shellServer(t, true)
	sess := &session{ID: "s1", Role: "admin", Username: "admin"}
	d := doc{"connection_id": "test", "node": "node1", "shell": true, "pve_username": "root@pam", "pve_password": "secret"}
	if err := guarded(func() { s.createConsole(context.Background(), d, sess) }); err == nil || !strings.Contains(err.Error(), "2FA") {
		t.Fatalf("expected 2FA request, got %v", err)
	}
	d["pve_otp"] = "123456"
	if err := guarded(func() { s.createConsole(context.Background(), d, sess) }); err != nil {
		t.Fatal(err)
	}
}

func TestProxmoxReason(t *testing.T) {
	if r := proxmoxReason("500 value 'root@pam!orbit' does not look like a valid user name"); r != "value 'root@pam!orbit' does not look like a valid user name" {
		t.Fatal(r)
	}
	if proxmoxReason("500 Internal Server Error") != "" || proxmoxReason("403 Forbidden") != "" {
		t.Fatal("generic reasons must be dropped")
	}
}
