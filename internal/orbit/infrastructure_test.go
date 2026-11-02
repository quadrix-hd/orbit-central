package orbit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type infraFixture struct {
	mu         sync.Mutex
	interfaces []any
	rules      []any
	options    doc
	writes     []string
	lose       bool
}

func infraServer(t *testing.T) (*Server, *infraFixture) {
	s, _ := actionServer(t)
	f := &infraFixture{interfaces: []any{doc{"iface": "vmbr0", "type": "bridge", "cidr": "192.0.2.10/24", "active": 1}, doc{"iface": "eno1", "type": "eth", "active": 1}}, rules: []any{doc{"pos": 0, "type": "in", "action": "ACCEPT", "proto": "tcp", "dport": "8006", "enable": 1, "digest": "rule-v1"}}, options: doc{"enable": 1, "policy_in": "DROP", "policy_out": "ACCEPT", "digest": "options-v1"}}
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		var data any
		envelope := doc{}
		if r.Method != "GET" {
			raw := []byte{}
			if r.Body != nil {
				raw, _ = io.ReadAll(r.Body)
			}
			f.writes = append(f.writes, r.Method+" "+path+"?"+r.URL.RawQuery+" "+string(raw))
			if f.lose {
				return nil, io.ErrUnexpectedEOF
			}
			if r.Method == "PUT" && strings.HasSuffix(path, "/network") {
				data = "UPID:node1:network"
			}
		} else {
			switch {
			case strings.HasSuffix(path, "/network"):
				data = f.interfaces
				envelope["changes"] = "--- current\n+++ pending\n+new bridge"
			case strings.HasSuffix(path, "/options"):
				data = f.options
			case strings.HasSuffix(path, "/rules"):
				data = f.rules
			case path == "/cluster/resources":
				data = []any{doc{"type": "node", "node": "node1", "status": "online", "maxmem": 1024}, doc{"type": "qemu", "vmid": 100, "node": "node1", "name": "vm-one", "status": "running"}, doc{"type": "lxc", "vmid": 200, "node": "node1", "name": "ct-one", "status": "running"}}
			case strings.Contains(path, "/tasks/"):
				data = doc{"status": "stopped", "exitstatus": "OK"}
			default:
				data = doc{}
			}
		}
		envelope["data"] = data
		raw, _ := json.Marshal(envelope)
		return response(200, string(raw)), nil
	})
	return s, f
}
func infraInput(s *Server, kind, intent, scope string) doc {
	d := doc{"request_key": randomID(), "action": "infrastructure", "connection_id": "test", "node": "node1", "kind": kind, "intent": intent, "scope": scope, "guest_type": "qemu", "vmid": 100, "acknowledge": true, "confirm_target": "node1", "target": "vmbr0", "fields": doc{"type": "bridge", "cidr": "10.0.20.2/24", "autostart": true}}
	if kind == "firewall" {
		d["confirm_target"] = scopeTarget(scope, "node1", 100)
		d["fields"] = doc{"type": "in", "action": "ACCEPT", "enable": true, "proto": "tcp", "dport": "443"}
		d["position"] = 0
	}
	snap := s.infraSnapshot(context.Background(), s.conn("test"), d)
	d["revision"] = snap["revision"]
	return d
}
func TestNetworkStagesChangesAndAppliesOnlyExplicitly(t *testing.T) {
	s, f := infraServer(t)
	d := infraInput(s, "network", "update", "host")
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 1 || !strings.Contains(f.writes[0], "PUT /nodes/node1/network/vmbr0") {
		t.Fatal(op, f.writes)
	}
	d = infraInput(s, "network", "apply", "host")
	d["fields"] = doc{}
	op = submit(t, s, d)
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 2 || !strings.Contains(f.writes[1], "PUT /nodes/node1/network?") {
		t.Fatal(op, f.writes)
	}
}
func TestNetworkCreateDeleteAndRevert(t *testing.T) {
	for _, intent := range []string{"create", "delete", "revert"} {
		t.Run(intent, func(t *testing.T) {
			s, f := infraServer(t)
			d := infraInput(s, "network", intent, "host")
			if intent == "create" {
				d["target"] = "vmbr1"
			}
			op := submit(t, s, d)
			s.processOperation(context.Background(), op)
			if op["status"] != "completed" || len(f.writes) != 1 {
				t.Fatal(op, f.writes)
			}
		})
	}
}
func TestNetworkProtectsPhysicalPortsAndValidatesAddresses(t *testing.T) {
	s, f := infraServer(t)
	d := infraInput(s, "network", "delete", "host")
	d["target"] = "eno1"
	if e := guarded(func() { s.submitOperation(context.Background(), d) }); e == nil {
		t.Fatal("physical port deletion accepted")
	}
	for _, fields := range []doc{{"type": "bridge", "gateway": "not-an-ip"}, {"type": "bridge", "cidr6": "10.0.0.1/24"}, {"type": "bridge", "mtu": 5}, {"type": "bridge", "pre-up": "do something"}} {
		d = infraInput(s, "network", "update", "host")
		d["fields"] = fields
		if e := guarded(func() { s.submitOperation(context.Background(), d) }); e == nil {
			t.Fatal("invalid network field accepted", fields)
		}
	}
	if len(f.writes) != 0 {
		t.Fatal("validation dispatched")
	}
}
func TestInfrastructureRevisionAndConfirmationPreventWrites(t *testing.T) {
	s, f := infraServer(t)
	d := infraInput(s, "network", "update", "host")
	d["confirm_target"] = "wrong"
	if e := guarded(func() { s.submitOperation(context.Background(), d) }); e == nil {
		t.Fatal("wrong confirmation accepted")
	}
	d["confirm_target"] = "node1"
	d["revision"] = strings.Repeat("0", 64)
	if e := guarded(func() { s.submitOperation(context.Background(), d) }); e == nil {
		t.Fatal("stale revision accepted")
	}
	d = infraInput(s, "network", "update", "host")
	op := submit(t, s, d)
	f.interfaces[0].(doc)["cidr"] = "10.0.99.1/24"
	s.processOperation(context.Background(), op)
	if op["status"] != "failed" || len(f.writes) != 0 {
		t.Fatal("race dispatched", op, f.writes)
	}
}
func TestFirewallScopesAndCRUDUseDigest(t *testing.T) {
	for _, scope := range []string{"cluster", "host", "vm"} {
		for _, intent := range []string{"create", "update", "delete", "move", "options"} {
			t.Run(scope+"/"+intent, func(t *testing.T) {
				s, f := infraServer(t)
				d := infraInput(s, "firewall", intent, scope)
				if intent == "move" {
					d["fields"] = doc{"moveto": 1}
				}
				if intent == "options" {
					d["fields"] = doc{"enable": true}
				}
				op := submit(t, s, d)
				s.processOperation(context.Background(), op)
				if op["status"] != "completed" || len(f.writes) != 1 {
					t.Fatal(op, f.writes)
				}
				if intent != "create" && !strings.Contains(f.writes[0], "digest=") {
					t.Fatal("digest omitted")
				}
			})
		}
	}
}
func TestInfrastructureUncertainSubmissionDoesNotRepeat(t *testing.T) {
	s, f := infraServer(t)
	op := submit(t, s, infraInput(s, "firewall", "create", "cluster"))
	f.lose = true
	s.processOperation(context.Background(), op)
	if op["status"] != "needs_attention" || len(f.writes) != 1 {
		t.Fatal(op)
	}
	s.processOperation(context.Background(), op)
	if len(f.writes) != 1 {
		t.Fatal("write repeated")
	}
}
func TestRevisionIgnoresLinkStateButDetectsConfiguration(t *testing.T) {
	rows := []map[string]any{{"iface": "eno1", "active": 1, "mtu": 1500}}
	first := revisionOf(rows)
	rows[0]["active"] = 0
	if revisionOf(rows) != first {
		t.Fatal("link state changes config revision")
	}
	rows[0]["mtu"] = 9000
	if revisionOf(rows) == first {
		t.Fatal("MTU change not detected")
	}
}

func TestFirewallOptionsRespectScopeAndInheritedDefaults(t *testing.T) {
	for _, tc := range []struct {
		scope  string
		fields doc
	}{{"cluster", doc{"log_level_in": "info"}}, {"host", doc{"policy_in": "DROP"}}, {"cluster", doc{"macfilter": false}}} {
		if e := guarded(func() { firewallOptions(tc.fields, tc.scope) }); e == nil {
			t.Fatal("unsupported scope accepted", tc.scope)
		}
	}
	var result doc
	if e := guarded(func() { result = firewallOptions(doc{"policy_in": ""}, "vm") }); e != nil || result["delete"] != "policy_in" {
		t.Fatal(result, e)
	}
}
func TestFirewallRuleIPVersionAndControlCharacters(t *testing.T) {
	for _, version := range []int{4, 6} {
		if e := guarded(func() { firewallRuleFields(doc{"type": "in", "action": "ACCEPT", "ipversion": version}) }); e != nil {
			t.Fatal(e)
		}
	}
	for _, bad := range []doc{{"type": "in", "action": "ACCEPT", "ipversion": 5}, {"type": "in", "action": "ACCEPT", "comment": "bad\nline"}} {
		if e := guarded(func() { firewallRuleFields(bad) }); e == nil {
			t.Fatal("invalid rule accepted")
		}
	}
}
