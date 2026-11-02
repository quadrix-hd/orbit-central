package orbit

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTLSRequiresTrustedCA(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"version":"9"}}`))
	}))
	defer upstream.Close()
	store := testStore(t)
	conn := mockConnection(t, store)
	conn.URL = upstream.URL
	conn.Endpoints = nil
	p, err := NewProxmox(store, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(context.Background(), conn, "GET", "/version", nil); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	p, err = NewProxmox(store, ca)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(context.Background(), conn, "GET", "/version", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = NewProxmox(store, filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("missing CA silently ignored")
	}
}

func TestAlternativeEndpointsMustMatchCluster(t *testing.T) {
	store := testStore(t)
	p, _ := NewProxmox(store, "")
	conn := mockConnection(t, store)
	different := false
	p.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		name := "cluster-a"
		if different && r.URL.Host == "node2:8006" {
			name = "cluster-b"
		}
		raw, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"type": "cluster", "name": name}, map[string]any{"type": "node", "id": "node/a", "name": "a"}}})
		return response(200, string(raw)), nil
	})
	if err := p.VerifyEndpoints(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	different = true
	if err := p.VerifyEndpoints(context.Background(), conn); err == nil {
		t.Fatal("mixed clusters accepted")
	}
}

func mockConnection(t *testing.T, s *Store) Connection {
	t.Helper()
	token, err := s.Encrypt("user@pve!orbit=example-secret")
	if err != nil {
		t.Fatal(err)
	}
	return Connection{ID: "test", Name: "Test", URL: "https://node1:8006", Token: token, Kind: "pve", Endpoints: []string{"https://node2:8006"}}
}
func TestEndpointValidation(t *testing.T) {
	for _, address := range []string{"http://pve:8006", "https://user:pass@pve", "https://pve/a", "https://pve?x=y", "https://pve#fragment", "https://pve:0", "https://pve:bad", "https://pve host", ""} {
		if _, err := ValidateEndpoints(address, nil); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	urls, err := ValidateEndpoints("https://pve:8006/", []string{"https://pve:8006", "https://node2:8006"})
	if err != nil || len(urls) != 2 {
		t.Fatalf("valid endpoints %v %v", urls, err)
	}
}
func TestReadFailoverAndPreferredEndpoint(t *testing.T) {
	s := testStore(t)
	p, _ := NewProxmox(s, "")
	c := mockConnection(t, s)
	hosts := []string{}
	p.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		if r.URL.Host == "node1:8006" {
			return nil, errors.New("offline")
		}
		if r.Header.Get("Authorization") != "PVEAPIToken=user@pve!orbit=example-secret" {
			t.Fatal("wrong authorization")
		}
		return response(200, `{"data":{"version":"9.0"}}`), nil
	})
	for i := 0; i < 2; i++ {
		if _, err := p.Call(context.Background(), c, "GET", "/version", nil); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(hosts, ",") != "node1:8006,node2:8006,node2:8006" {
		t.Fatal(hosts)
	}
}
func TestWriteNotRetriedAfterDispatch(t *testing.T) {
	s := testStore(t)
	p, _ := NewProxmox(s, "")
	c := mockConnection(t, s)
	writes, reads := 0, 0
	p.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			reads++
			return response(200, `{"data":{"version":"9"}}`), nil
		}
		writes++
		return nil, errors.New("connection lost after send")
	})
	_, err := p.Call(context.Background(), c, "POST", "/nodes/n/qemu/100/status/start", nil)
	if err == nil || writes != 1 || reads != 1 {
		t.Fatalf("reads %d writes %d error %v", reads, writes, err)
	}
}
func TestPermissionErrorDoesNotFailoverOrLeakBody(t *testing.T) {
	s := testStore(t)
	p, _ := NewProxmox(s, "")
	c := mockConnection(t, s)
	calls := 0
	p.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(403, `{"message":"secret-payload VM.Audit","errors":{"cpu":"private-content"}}`), nil
	})
	_, err := p.Call(context.Background(), c, "GET", "/nodes", nil)
	if err == nil || calls != 1 || strings.Contains(err.Error(), "secret-payload") || strings.Contains(err.Error(), "private-content") || !strings.Contains(err.Error(), "VM.Audit") {
		t.Fatalf("calls %d error %v", calls, err)
	}
}
func TestRedirectsDisabled(t *testing.T) {
	s := testStore(t)
	p, _ := NewProxmox(s, "")
	if err := p.Client.CheckRedirect(&http.Request{}, nil); err != http.ErrUseLastResponse {
		t.Fatal("credential redirects enabled")
	}
}
func TestMalformedResponseIsUncertain(t *testing.T) {
	s := testStore(t)
	p, _ := NewProxmox(s, "")
	c := mockConnection(t, s)
	c.Endpoints = nil
	p.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return response(200, `{"unexpected":"data"}`), nil })
	_, err := p.Call(context.Background(), c, "GET", "/version", nil)
	var pr *Problem
	if !errors.As(err, &pr) || !pr.Uncertain {
		t.Fatal(err)
	}
}
func TestVMDetailRedactsSecrets(t *testing.T) {
	server, store := testServer(t)
	cookie, _ := setup(t, server, store)
	c := mockConnection(t, store)
	if err := store.SaveConnection(c, true); err != nil {
		t.Fatal(err)
	}
	server.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var data any = []any{}
		switch {
		case r.URL.Path == "/api2/json/cluster/resources":
			data = []any{map[string]any{"type": "qemu", "vmid": 100, "node": "node1", "name": "vm", "status": "running"}}
		case strings.HasSuffix(r.URL.Path, "/config"):
			data = map[string]any{"name": "vm", "cipassword": "do-not-expose", "sshkeys": "private-user-data", "scsi0": "local:disk,size=16G", "net0": "virtio=AA:BB,bridge=vmbr0"}
		}
		raw, _ := json.Marshal(map[string]any{"data": data})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
	})
	w := request(server, "GET", "/api/connections/test/vms/100", "", cookie, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "do-not-expose") || strings.Contains(w.Body.String(), "private-user-data") || !strings.Contains(w.Body.String(), "16G") {
		t.Fatal(w.Body.String())
	}
}
