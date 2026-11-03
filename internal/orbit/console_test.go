package orbit

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConsoleRelaysBinaryAndExpiresOnLogout(t *testing.T) { testGuestConsole(t, "qemu", "legacy") }
func TestContainerConsoleUsesLXCPathAndSeparateVNCPassword(t *testing.T) {
	testGuestConsole(t, "lxc", "separate")
}
func TestContainerConsoleExtractsLegacyPrefixedPassword(t *testing.T) {
	testGuestConsole(t, "lxc", "prefix")
}
func testGuestConsole(t *testing.T, kind, mode string) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "core"), 0700)
	_ = os.WriteFile(filepath.Join(root, "core", "rfb.js"), []byte("// test"), 0600)
	t.Setenv("ORBIT_NOVNC_ROOT", root)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=user@pve!orbit=example-secret" {
			t.Error("missing server-side credential")
		}
		if strings.HasSuffix(r.URL.Path, "/vncwebsocket") {
			if !strings.Contains(r.URL.Path, "/"+kind+"/100/") {
				t.Error("wrong guest endpoint", r.URL.Path)
			}
			ticket := "short-ticket"
			if mode == "prefix" {
				ticket = "vnc-secret:PVEVNC:legacy-ticket"
			}
			if r.URL.Query().Get("vncticket") != ticket {
				t.Error("wrong ticket")
			}
			u := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, Subprotocols: []string{"binary"}}
			ws, e := u.Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer ws.Close()
			for {
				kind, payload, e := ws.ReadMessage()
				if e != nil {
					return
				}
				if e = ws.WriteMessage(kind, payload); e != nil {
					return
				}
			}
		}
		var data any
		if strings.HasSuffix(r.URL.Path, "/vncproxy") {
			_ = r.ParseForm()
			if kind == "lxc" {
				if r.Form.Get("width") != "1000" || r.Form.Get("height") != "600" {
					t.Error("wrong terminal viewport", r.Form)
				}
			} else if r.Form.Has("width") || r.Form.Has("height") {
				t.Error("QEMU display resolution changed")
			}

			if !strings.Contains(r.URL.Path, "/"+kind+"/100/") {
				t.Error("wrong creation endpoint", r.URL.Path)
			}
			data = doc{"port": 5900, "ticket": "short-ticket"}
			if mode == "separate" {
				data.(doc)["password"] = "vnc-secret"
			}
			if mode == "prefix" {
				data.(doc)["ticket"] = "vnc-secret:PVEVNC:legacy-ticket"
			}
		} else {
			data = []any{doc{"type": kind, "vmid": 100, "node": "node1", "name": "vm-one", "status": "running"}}
		}
		_ = json.NewEncoder(w).Encode(doc{"data": data})
	}))
	defer up.Close()
	s, store := testServer(t)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw}), 0600)
	p, e := NewProxmox(store, ca)
	if e != nil {
		t.Fatal(e)
	}
	s.PVE = p
	c := mockConnection(t, store)
	c.URL = up.URL
	c.Endpoints = nil
	if e = store.SaveConnection(c, true); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	s.Origin = server.URL
	cookie, csrf := setup(t, s, store)
	w := request(s, "POST", "/api/consoles", `{"connection_id":"test","node":"node1","vmid":100,"width":1000,"height":600}`, cookie, csrf, s.Origin)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var grant doc
	_ = json.Unmarshal(w.Body.Bytes(), &grant)
	if grant["guest_type"] != kind {
		t.Fatal(grant)
	}
	want := "short-ticket"
	if mode != "legacy" {
		want = "vnc-secret"
	}
	if grant["password"] != want {
		t.Fatal("wrong RFB password", grant)
	}
	if strings.Contains(w.Body.String(), "example-secret") {
		t.Fatal("permanent token leaked")
	}
	headers := http.Header{"Origin": {s.Origin}, "Cookie": {cookie.Name + "=" + cookie.Value}}
	client, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/consoles/"+str(grant["id"]), headers)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if e = client.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 2, 255}); e != nil {
		t.Fatal(e)
	}
	wireKind, payload, e := client.ReadMessage()
	if e != nil || wireKind != websocket.BinaryMessage || string(payload) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("relay %v %v", payload, e)
	}
	again := request(s, "GET", "/api/consoles/"+str(grant["id"]), "", cookie, "", "")
	if again.Code != 403 {
		t.Fatal("origin guard missing")
	}
	if _, e = store.DB.Exec("DELETE FROM sessions"); e != nil {
		t.Fatal(e)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, e = client.ReadMessage(); e == nil {
		t.Fatal("console survived logout")
	}
}

func TestStoppedContainerConsoleNeverStartsGuest(t *testing.T) {
	s, f := managementServer(t)
	f.guestType = "lxc"
	f.status = "stopped"
	cookie, csrf := setup(t, s, s.Store)
	w := request(s, "POST", "/api/consoles", `{"connection_id":"test","node":"node1","vmid":100}`, cookie, csrf, s.Origin)
	if w.Code != 400 || len(f.writes) > 0 {
		t.Fatal(w.Code, f.writes)
	}
}
