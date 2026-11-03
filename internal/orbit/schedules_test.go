package orbit

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextRun(t *testing.T) {
	loc := time.Local
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, loc) // Tuesday
	cases := []struct {
		sc   doc
		want time.Time
	}{
		{doc{"kind": "daily", "time": "13:30"}, time.Date(2026, 11, 3, 13, 30, 0, 0, loc)},
		{doc{"kind": "daily", "time": "12:00"}, time.Date(2026, 11, 4, 12, 0, 0, 0, loc)},
		{doc{"kind": "daily", "time": "08:00", "days": []any{float64(1)}}, time.Date(2026, 11, 9, 8, 0, 0, 0, loc)},
		{doc{"kind": "daily", "time": "08:00", "days": []any{float64(2)}}, time.Date(2026, 11, 10, 8, 0, 0, 0, loc)},
		{doc{"kind": "once", "time": "18:00", "date": "2026-11-03"}, time.Date(2026, 11, 3, 18, 0, 0, 0, loc)},
		{doc{"kind": "once", "time": "06:00", "date": "2026-11-03"}, time.Time{}},
	}
	for i, tc := range cases {
		if got := nextRun(tc.sc, at); !got.Equal(tc.want) {
			t.Errorf("case %d: got %v want %v", i, got, tc.want)
		}
	}
}

func TestNormalizeTags(t *testing.T) {
	if got := strings.Join(normalizeTags(" Prod, web;prod  db "), ";"); got != "db;prod;web" {
		t.Fatal(got)
	}
	if err := guarded(func() { normalizeTags("bad<tag") }); err == nil {
		t.Fatal("invalid tag accepted")
	}
}

func scheduleDoc(action string) doc {
	return doc{"name": "Nacht", "action": action, "kind": "daily", "time": "02:00", "days": []any{}, "connection_id": "test", "target": "vmids", "vmids": "100", "enabled": true, "keep": float64(2), "storage": "local", "acknowledge": true}
}

func TestScheduleValidation(t *testing.T) {
	s, _ := actionServer(t)
	for name, mut := range map[string]func(doc){
		"time":   func(d doc) { d["time"] = "25:00" },
		"action": func(d doc) { d["action"] = "delete" },
		"vmids":  func(d doc) { d["vmids"] = "abc" },
		"past":   func(d doc) { d["kind"], d["date"] = "once", "2020-01-01" },
		"stop":   func(d doc) { d["action"], d["acknowledge"] = "stop", false },
		"keep":   func(d doc) { d["action"], d["keep"] = "snapshot", float64(0) },
	} {
		d := scheduleDoc("start")
		mut(d)
		if err := guarded(func() { s.scheduleInput(d, nil) }); err == nil {
			t.Errorf("%s: invalid schedule accepted", name)
		}
	}
	sc := s.scheduleInput(scheduleDoc("start"), nil)
	if num(sc["next_run"]) <= float64(time.Now().Unix()) || !strings.HasPrefix(str(sc["id"]), "s") {
		t.Fatalf("bad schedule %v", sc)
	}
}

func TestScheduleRunSubmitsAndSkips(t *testing.T) {
	s, f := actionServer(t)
	f.resources = append(f.resources, doc{"type": "qemu", "vmid": 101, "node": "node2", "name": "vm-two", "status": "running", "tags": "web;prod"}, doc{"type": "qemu", "vmid": 9000, "node": "node1", "name": "tpl", "status": "stopped", "template": 1, "tags": "web"})
	d := scheduleDoc("start")
	d["target"], d["tag"] = "tag", "WEB"
	sc := s.scheduleInput(d, nil)
	s.saveSchedule(sc)
	run := s.runSchedule(context.Background(), sc, time.Now(), "admin")
	res := run["results"].([]doc)
	if len(res) != 1 || res[0]["vmid"] != 101 || res[0]["status"] != "skipped" {
		t.Fatalf("running guest must be skipped, template ignored: %v", res)
	}
	d = scheduleDoc("start")
	d["vmids"] = "100, 555"
	sc = s.scheduleInput(d, nil)
	s.saveSchedule(sc)
	run = s.runSchedule(context.Background(), sc, time.Now(), "admin")
	res = run["results"].([]doc)
	if len(res) != 2 {
		t.Fatalf("results %v", res)
	}
	var queued doc
	for _, r := range res {
		if r["vmid"] == 100 {
			queued = r
		} else if r["status"] != "skipped" {
			t.Fatalf("missing guest must be skipped: %v", r)
		}
	}
	if queued["status"] != "queued" {
		t.Fatalf("start not queued: %v", queued)
	}
	op := s.readDoc("operations", str(queued["operation_id"]))
	if op["action"] != "start" || !strings.HasPrefix(str(op["requested_by"]), "Zeitplan Nacht") {
		t.Fatalf("operation %v", op)
	}
	if last := s.schedule(str(sc["id"]))["last_run"]; last == nil {
		t.Fatal("last_run not stored")
	}
}

func TestScheduleDueAdvancesOnce(t *testing.T) {
	s, f := actionServer(t)
	sc := s.scheduleInput(scheduleDoc("start"), nil)
	due := time.Now().Add(-time.Minute)
	sc["next_run"] = float64(due.Unix())
	s.saveSchedule(sc)
	s.evaluateSchedules(context.Background(), time.Now())
	s.evaluateSchedules(context.Background(), time.Now())
	ops := s.allDocs("operations")
	if len(ops) != 1 {
		t.Fatalf("expected one queued operation, got %d", len(ops))
	}
	if cur := s.schedule(str(sc["id"])); num(cur["next_run"]) <= float64(time.Now().Unix()) {
		t.Fatalf("next_run not advanced: %v", cur["next_run"])
	}
	_ = f
	// A run missed by more than an hour is skipped and recorded.
	sc2 := s.scheduleInput(scheduleDoc("start"), nil)
	sc2["next_run"] = float64(time.Now().Add(-3 * time.Hour).Unix())
	s.saveSchedule(sc2)
	s.evaluateSchedules(context.Background(), time.Now())
	if len(s.allDocs("operations")) != 1 || !strings.Contains(str(configOf(s.schedule(str(sc2["id"]))["last_run"])["error"]), "verpasst") {
		t.Fatal("missed run must be skipped")
	}
}

func TestSnapshotScheduleAndPrune(t *testing.T) {
	s, f := actionServer(t)
	f.resources[0].(doc)["status"] = "running"
	sc := s.scheduleInput(scheduleDoc("snapshot"), nil)
	s.saveSchedule(sc)
	run := s.runSchedule(context.Background(), sc, time.Date(2026, 11, 3, 2, 0, 0, 0, time.Local), "")
	op := s.readDoc("operations", str(run["results"].([]doc)[0]["operation_id"]))
	name := str(configOf(op["args"])["snapname"])
	if name != snapshotPrefix(sc)+"20261103T0200" {
		t.Fatalf("snapshot name %q", name)
	}
	s.processOperation(context.Background(), op)
	p := snapshotPrefix(sc)
	f.snaps = []any{doc{"name": p + "20261101T0200"}, doc{"name": p + "20261102T0200"}, doc{"name": p + "20261103T0200"}, doc{"name": "manual"}}
	f.writes = nil
	if s.pruneSnapshots(context.Background(), s.schedule(str(sc["id"]))) {
		t.Fatal("prune must report pending work")
	}
	var del doc
	for _, o := range s.allDocs("operations") {
		if o["action"] == "snapshot_delete" {
			del = o
		}
	}
	if del == nil || configOf(del["args"])["snapname"] != p+"20261101T0200" {
		t.Fatalf("oldest scheduled snapshot must be deleted first: %v", del)
	}
	s.processOperation(context.Background(), del)
	if del["status"] != "completed" || len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "DELETE /nodes/node1/qemu/100/snapshot/"+p+"20261101T0200") {
		t.Fatalf("delete %v %v %v", del["status"], f.writes, del["events"])
	}
	f.snaps = f.snaps[1:]
	if !s.pruneSnapshots(context.Background(), s.schedule(str(sc["id"]))) {
		t.Fatal("prune must finish when only keep snapshots remain")
	}
}

func TestTagsAction(t *testing.T) {
	s, f := actionServer(t)
	d := operationInput("tags")
	d["tags"] = "Prod web"
	op := submit(t, s, d)
	s.processOperation(context.Background(), op)
	if op["status"] != "completed" || len(f.writes) != 1 || !strings.Contains(f.writes[0], "tags=prod%3Bweb") {
		t.Fatalf("%v %v", op["status"], f.writes)
	}
	f.writes = nil
	d = operationInput("tags")
	d["tags"] = ""
	op = submit(t, s, d)
	s.processOperation(context.Background(), op)
	if !strings.Contains(f.writes[0], "delete=tags") {
		t.Fatalf("clearing tags must delete them: %v", f.writes)
	}
}

func TestGuestHistoryMergesOrbitAndProxmox(t *testing.T) {
	s, _ := actionServer(t)
	op := submit(t, s, operationInput("start"))
	_ = op
	srv := s
	req := httptest.NewRequest("GET", "/api/connections/test/guest-history?vmid=100", nil)
	out, err := srv.guestHistory(req, srv.conn("test"))
	if err != nil {
		t.Fatal(err)
	}
	rows := out.([]doc)
	if len(rows) != 1 || rows[0]["source"] != "orbit" || rows[0]["action"] != "start" {
		t.Fatalf("rows %v", rows)
	}
}
