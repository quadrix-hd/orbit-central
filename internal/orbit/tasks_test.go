package orbit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleUPID = "UPID:node1:0000A1B2:0012C3D4:6745F0A1:qmstart:100:root@pam:"

func TestTaskListSortedAndTagged(t *testing.T) {
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.Endpoints = nil
	var paths []string
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		rows := []any{doc{"upid": "a", "starttime": 10}, doc{"upid": "b", "starttime": 30}, doc{"upid": "c", "starttime": 20}}
		raw, _ := json.Marshal(doc{"data": rows})
		return response(200, string(raw)), nil
	})
	data, err := s.taskList(httptest.NewRequest("GET", "/", nil), c)
	if err != nil {
		t.Fatal(err)
	}
	rows := data.([]map[string]any)
	if rows[0]["upid"] != "b" || rows[2]["upid"] != "a" || rows[0]["connection_id"] != "test" {
		t.Fatalf("unexpected order or tagging: %v", rows)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], "GET /api2/json/cluster/tasks") {
		t.Fatalf("paths %v", paths)
	}
}

func TestTaskDetailValidatesAndReadsStatusAndLog(t *testing.T) {
	s, store := testServer(t)
	c := mockConnection(t, store)
	c.Endpoints = nil
	var paths []string
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		var data any = doc{"status": "stopped", "exitstatus": "OK"}
		if strings.HasSuffix(r.URL.Path, "/log") {
			data = []any{doc{"n": 1, "t": "starting"}, doc{"n": 2, "t": "TASK OK"}}
		}
		raw, _ := json.Marshal(doc{"data": data})
		return response(200, string(raw)), nil
	})
	req := httptest.NewRequest("GET", "/?start=5", nil)
	data, err := s.taskDetail(req, c, "node1", sampleUPID)
	if err != nil {
		t.Fatal(err)
	}
	result := data.(doc)
	if len(objects(result["log"])) != 2 || configOf(result["status"])["exitstatus"] != "OK" {
		t.Fatalf("result %v", result)
	}
	if len(paths) != 2 || !strings.Contains(paths[1], "/log?") || !strings.Contains(paths[1], "start=5") {
		t.Fatalf("paths %v", paths)
	}
	if _, err := s.taskDetail(req, c, "localhost", "UPID:localhost:00000A3F:0002A1B2:00000003:6745F0A1:garbage_collection:store1:root@pam:"); err != nil {
		t.Fatalf("PBS UPID rejected: %v", err)
	}
	paths = paths[:2]
	for _, bad := range [][2]string{{"node1", "UPID:../../etc"}, {"node/1", sampleUPID}, {"node1", "not-a-upid"}} {
		if _, err := s.taskDetail(req, c, bad[0], bad[1]); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if len(paths) != 2 {
		t.Fatal("invalid IDs must not reach Proxmox")
	}
}
