package orbit

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Scheduled actions: power actions, snapshots (with retention) and backups
// that Orbit runs on a daily, weekly or one-time schedule for a list of
// guests or for every guest carrying a Proxmox tag. Each run is submitted
// through the normal operation queue, so validation, audit and the
// operation log are the same as for actions started by hand.

var scheduleActions = map[string]string{"start": "Starten", "shutdown": "Herunterfahren", "reboot": "Neu starten", "stop": "Hart ausschalten", "snapshot": "Snapshot", "backup": "Backup"}
var scheduleTimeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
var tagRE = regexp.MustCompile(`^[a-z0-9_][a-z0-9_+.-]{0,63}$`)

const scheduleInterval = 30 * time.Second

// normalizeTags turns user input ("Prod, web;db") into Proxmox' tag list.
func normalizeTags(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range regexp.MustCompile(`[;,\s]+`).Split(strings.ToLower(raw), -1) {
		if t == "" || seen[t] {
			continue
		}
		if !tagRE.MatchString(t) {
			fail("Ungültiger Tag „" + t + "“: erlaubt sind a–z, 0–9, _ + . -")
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > 20 {
		fail("Höchstens 20 Tags pro Gast.")
	}
	sort.Strings(out)
	return out
}

func guestTags(r doc) []string {
	out := []string{}
	for _, t := range strings.FieldsFunc(str(r["tags"]), func(c rune) bool { return c == ';' || c == ',' || c == ' ' }) {
		out = append(out, strings.ToLower(t))
	}
	return out
}

// nextRun returns the first run time strictly after `after`, or zero when a
// one-time schedule has passed.
func nextRun(sc doc, after time.Time) time.Time {
	hm := strings.Split(str(sc["time"]), ":")
	if len(hm) != 2 {
		return time.Time{}
	}
	h, _ := strconv.Atoi(hm[0])
	m, _ := strconv.Atoi(hm[1])
	loc := time.Local
	if sc["kind"] == "once" {
		d, err := time.ParseInLocation("2006-01-02", str(sc["date"]), loc)
		if err != nil {
			return time.Time{}
		}
		t := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc)
		if !t.After(after) {
			return time.Time{}
		}
		return t
	}
	days := map[int]bool{}
	for _, v := range anyList(sc["days"]) {
		days[int(num(v))] = true
	}
	base := after.In(loc)
	for i := 0; i < 8; i++ {
		d := base.AddDate(0, 0, i)
		t := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc)
		if t.After(after) && (len(days) == 0 || days[int(t.Weekday())]) {
			return t
		}
	}
	return time.Time{}
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func (s *Server) scheduleInput(d doc, old doc) doc {
	sc := doc{}
	if old != nil {
		for k, v := range old {
			sc[k] = v
		}
	} else {
		sc["id"] = "s" + randomID()[:11]
		sc["created"] = now()
	}
	sc["name"] = field(d, "name", 80, `[^\x00-\x1f<>]{1,80}`)
	action := field(d, "action", 20, "")
	if scheduleActions[action] == "" {
		fail("Aktion wird für Zeitpläne nicht unterstützt.")
	}
	sc["action"] = action
	kind := field(d, "kind", 10, "daily|once")
	sc["kind"] = kind
	t := field(d, "time", 5, "")
	if !scheduleTimeRE.MatchString(t) {
		fail("Uhrzeit im Format HH:MM angeben.")
	}
	sc["time"] = t
	if kind == "once" {
		date := field(d, "date", 10, `\d{4}-\d{2}-\d{2}`)
		if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
			fail("Ungültiges Datum.")
		}
		sc["date"] = date
		sc["days"] = []any{}
	} else {
		days := []any{}
		seen := map[int]bool{}
		for _, v := range anyList(d["days"]) {
			n := int(num(v))
			if n < 0 || n > 6 || seen[n] {
				fail("Ungültiger Wochentag.")
			}
			seen[n] = true
			days = append(days, n)
		}
		sc["days"] = days
		delete(sc, "date")
	}
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	c := s.conn(cid)
	if c.Kind != "pve" {
		fail("Proxmox VE benötigt.")
	}
	sc["connection_id"] = cid
	target := field(d, "target", 10, "vmids|tag")
	sc["target"] = target
	if target == "tag" {
		tags := normalizeTags(str(d["tag"]))
		if len(tags) != 1 {
			fail("Genau einen Tag angeben.")
		}
		sc["tag"] = tags[0]
		sc["vmids"] = []any{}
	} else {
		ids := []any{}
		for _, part := range strings.FieldsFunc(str(d["vmids"]), func(c rune) bool { return c == ',' || c == ' ' || c == ';' }) {
			n, err := strconv.Atoi(part)
			if err != nil || n < 100 || n > 999999999 {
				fail("Ungültige VMID: " + part)
			}
			ids = append(ids, n)
		}
		if len(ids) == 0 || len(ids) > 500 {
			fail("Mindestens eine VMID angeben.")
		}
		sc["vmids"] = ids
		delete(sc, "tag")
	}
	delete(sc, "keep")
	delete(sc, "storage")
	switch action {
	case "snapshot":
		sc["keep"] = integer(d, "keep", 1, 100)
	case "backup":
		sc["storage"] = field(d, "storage", 64, `[a-zA-Z0-9_.-]+`)
	case "stop":
		if !boolean(d, "acknowledge") {
			fail("Geplantes hartes Ausschalten ausdrücklich bestätigen.")
		}
	}
	sc["enabled"] = boolean(d, "enabled")
	sc["updated"] = now()
	next := nextRun(sc, time.Now())
	if next.IsZero() {
		if kind == "once" {
			fail("Zeitpunkt liegt in der Vergangenheit.")
		}
		fail("Kein gültiger nächster Termin.")
	}
	sc["next_run"] = float64(next.Unix())
	return sc
}

func (s *Server) saveSchedule(sc doc) {
	raw := must(json.Marshal(sc))
	must(s.Store.DB.Exec("INSERT INTO schedules(id,data,updated) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated=excluded.updated", sc["id"], string(raw), now()))
}

func (s *Server) schedule(id string) doc {
	var raw string
	if err := s.Store.DB.QueryRow("SELECT data FROM schedules WHERE id=?", id).Scan(&raw); err != nil {
		panic(problem("Zeitplan nicht gefunden.", 404))
	}
	var d doc
	must(0, json.Unmarshal([]byte(raw), &d))
	return d
}

func (s *Server) schedules() []doc {
	rows := must(s.Store.DB.Query("SELECT data FROM schedules ORDER BY id"))
	defer rows.Close()
	out := []doc{}
	for rows.Next() {
		var raw string
		must(0, rows.Scan(&raw))
		var d doc
		if json.Unmarshal([]byte(raw), &d) == nil {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return str(out[i]["name"]) < str(out[j]["name"]) })
	return out
}

func (s *Server) scheduleRoutes(w http.ResponseWriter, r *http.Request, d doc) (handled bool, err error) {
	if r.URL.Path != "/api/schedules" && !strings.HasPrefix(r.URL.Path, "/api/schedules/") {
		return false, nil
	}
	handled = true
	err = guarded(func() {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/schedules"), "/"), "/")
		who := requestUser(r.Context())
		switch {
		case parts[0] == "" && r.Method == "GET":
			zone, _ := time.Now().Zone()
			writeJSON(w, 200, doc{"schedules": s.schedules(), "timezone": time.Local.String(), "zone": zone})
		case parts[0] == "" && r.Method == "POST":
			sc := s.scheduleInput(d, nil)
			sc["created_by"] = who
			s.saveSchedule(sc)
			_ = s.Store.AuditAs(who, "schedule.create", str(sc["name"]))
			writeJSON(w, 201, sc)
		case len(parts) == 1 && r.Method == "POST":
			s.mu.Lock()
			old := s.schedule(parts[0])
			s.mu.Unlock()
			sc := s.scheduleInput(d, old)
			s.saveSchedule(sc)
			_ = s.Store.AuditAs(who, "schedule.update", str(sc["name"]))
			writeJSON(w, 200, sc)
		case len(parts) == 1 && r.Method == "DELETE":
			sc := s.schedule(parts[0])
			must(s.Store.DB.Exec("DELETE FROM schedules WHERE id=?", parts[0]))
			_ = s.Store.AuditAs(who, "schedule.delete", str(sc["name"]))
			writeJSON(w, 200, doc{"ok": true})
		case len(parts) == 2 && parts[1] == "run" && r.Method == "POST":
			sc := s.schedule(parts[0])
			_ = s.Store.AuditAs(who, "schedule.run", str(sc["name"]))
			res := s.runSchedule(r.Context(), sc, time.Now(), who)
			writeJSON(w, 200, res)
		default:
			panic(problem("Nicht gefunden.", 404))
		}
	})
	return
}

// scheduleTargets resolves the guests a schedule acts on right now; guests
// may have migrated or been tagged since the schedule was saved.
func (s *Server) scheduleTargets(ctx context.Context, c Connection, sc doc) ([]doc, []doc) {
	rows := objects(s.call(ctx, c, "GET", "/cluster/resources", doc{"type": "vm"}))
	byID := map[int]doc{}
	for _, r := range rows {
		byID[int(num(r["vmid"]))] = r
	}
	targets, missing := []doc{}, []doc{}
	if sc["target"] == "tag" {
		for _, r := range rows {
			for _, t := range guestTags(r) {
				if t == sc["tag"] && !truth(r["template"]) {
					targets = append(targets, r)
					break
				}
			}
		}
	} else {
		for _, v := range anyList(sc["vmids"]) {
			if r, ok := byID[int(num(v))]; ok && !truth(r["template"]) {
				targets = append(targets, r)
			} else {
				missing = append(missing, doc{"vmid": int(num(v)), "status": "skipped", "message": "Gast nicht gefunden oder Vorlage."})
			}
		}
	}
	sort.SliceStable(targets, func(i, j int) bool { return num(targets[i]["vmid"]) < num(targets[j]["vmid"]) })
	return targets, missing
}

func snapshotPrefix(sc doc) string { return "auto_" + str(sc["id"])[1:7] + "_" }

// runSchedule submits the schedule's action for every target guest.
func (s *Server) runSchedule(ctx context.Context, sc doc, at time.Time, who string) doc {
	results := []doc{}
	runID := strconv.FormatInt(at.Unix(), 10)
	err := guarded(func() {
		c := s.conn(str(sc["connection_id"]))
		actor := "Zeitplan " + str(sc["name"])
		if who != "" {
			actor += " (" + who + ")"
		}
		jobCtx := withUser(ctx, &session{Username: actor, Role: "admin"})
		targets, missing := s.scheduleTargets(ctx, c, sc)
		results = append(results, missing...)
		action := str(sc["action"])
		for _, g := range targets {
			id := int(num(g["vmid"]))
			res := doc{"vmid": id, "name": g["name"], "node": g["node"]}
			status := str(g["status"])
			skip := ""
			switch action {
			case "start":
				if status == "running" {
					skip = "läuft bereits"
				}
			case "shutdown", "reboot", "stop":
				if status != "running" {
					skip = "läuft nicht"
				}
			}
			if skip != "" {
				res["status"], res["message"] = "skipped", "Übersprungen: "+skip+"."
				results = append(results, res)
				continue
			}
			req := doc{"request_key": "sched-" + str(sc["id"]) + "-" + runID + "-" + strconv.Itoa(id), "action": action, "connection_id": c.ID, "node": g["node"], "vmid": id, "confirm_name": g["name"], "acknowledge": true}
			switch action {
			case "snapshot":
				req["snapshot_name"] = snapshotPrefix(sc) + at.In(time.Local).Format("20060102T1504")
			case "backup":
				req["storage"] = sc["storage"]
			}
			if e := guarded(func() {
				op, _ := s.submitOperation(jobCtx, req)
				res["operation_id"] = op["id"]
			}); e != nil {
				res["status"], res["message"] = "failed", errorText(e)
			} else {
				res["status"] = "queued"
			}
			results = append(results, res)
		}
	})
	run := doc{"at": float64(at.Unix()), "results": results}
	if err != nil {
		run["error"] = errorText(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.scheduleOrNil(str(sc["id"])); cur != nil {
		cur["last_run"] = run
		if sc["action"] == "snapshot" {
			cur["prune"] = true
		}
		s.saveSchedule(cur)
	}
	return run
}

func (s *Server) scheduleOrNil(id string) (d doc) {
	_ = guarded(func() { d = s.schedule(id) })
	return d
}

func errorText(e error) string {
	if p, ok := e.(*Problem); ok {
		return p.Message
	}
	return e.Error()
}

// pruneSnapshots removes the oldest scheduled snapshots beyond `keep`, one
// per guest per pass, once the guest has no other pending operation.
func (s *Server) pruneSnapshots(ctx context.Context, sc doc) bool {
	c := s.conn(str(sc["connection_id"]))
	keep := int(num(sc["keep"]))
	prefix := snapshotPrefix(sc)
	done := true
	targets, _ := s.scheduleTargets(ctx, c, sc)
	busy := map[int]bool{}
	for _, op := range s.allDocs("operations") {
		if pending(op) && op["connection_id"] == c.ID {
			busy[int(num(op["vmid"]))] = true
		}
	}
	for _, g := range targets {
		id := int(num(g["vmid"]))
		if busy[id] {
			done = false
			continue
		}
		snaps := []string{}
		for _, r := range objects(s.call(ctx, c, "GET", baseVM(str(g["node"]), str(g["type"]), id)+"/snapshot", nil)) {
			if n := str(r["name"]); strings.HasPrefix(n, prefix) {
				snaps = append(snaps, n)
			}
		}
		if len(snaps) <= keep {
			continue
		}
		sort.Strings(snaps)
		done = false
		jobCtx := withUser(ctx, &session{Username: "Zeitplan " + str(sc["name"]) + " (Aufbewahrung)", Role: "admin"})
		_ = guarded(func() {
			s.submitOperation(jobCtx, doc{"request_key": "prune-" + str(sc["id"]) + "-" + strconv.Itoa(id) + "-" + strings.TrimPrefix(snaps[0], prefix), "action": "snapshot_delete", "connection_id": c.ID, "node": g["node"], "vmid": id, "confirm_name": g["name"], "snapshot_name": snaps[0], "acknowledge": true})
		})
	}
	return done
}

func (s *Server) evaluateSchedules(ctx context.Context, at time.Time) {
	for _, sc := range s.schedules() {
		if ctx.Err() != nil {
			return
		}
		if truth(sc["prune"]) {
			if e := guarded(func() {
				if s.pruneSnapshots(ctx, sc) {
					s.mu.Lock()
					if cur := s.scheduleOrNil(str(sc["id"])); cur != nil {
						delete(cur, "prune")
						s.saveSchedule(cur)
					}
					s.mu.Unlock()
				}
			}); e != nil {
				log.Print("schedule_prune_failed")
			}
		}
		if sc["enabled"] != true || num(sc["next_run"]) == 0 || num(sc["next_run"]) > float64(at.Unix()) {
			continue
		}
		due := time.Unix(int64(num(sc["next_run"])), 0)
		// Advance the schedule before running so a crash cannot repeat it.
		s.mu.Lock()
		cur := s.scheduleOrNil(str(sc["id"]))
		if cur == nil || num(cur["next_run"]) != num(sc["next_run"]) {
			s.mu.Unlock()
			continue
		}
		next := nextRun(cur, at)
		if next.IsZero() {
			cur["next_run"] = 0
			cur["enabled"] = false
		} else {
			cur["next_run"] = float64(next.Unix())
		}
		s.saveSchedule(cur)
		s.mu.Unlock()
		// A run missed by more than an hour (Orbit was down) is skipped.
		if at.Sub(due) > time.Hour {
			s.mu.Lock()
			cur["last_run"] = doc{"at": float64(at.Unix()), "results": []doc{}, "error": "Termin " + due.In(time.Local).Format("02.01. 15:04") + " verpasst (Orbit war nicht erreichbar)."}
			s.saveSchedule(cur)
			s.mu.Unlock()
			continue
		}
		s.runSchedule(ctx, cur, due, "")
	}
}

func (s *Server) RunSchedules(ctx context.Context) {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if err := guarded(func() { s.evaluateSchedules(ctx, time.Now()) }); err != nil && ctx.Err() == nil {
				log.Print("schedule_evaluation_failed")
			}
			timer.Reset(scheduleInterval)
		}
	}()
}

// guestHistory merges Orbit's operation log for a guest with the Proxmox
// task history of every node, so moves between nodes stay visible.
func (s *Server) guestHistory(r *http.Request, c Connection) (any, error) {
	var out []doc
	err := guarded(func() {
		id, e := strconv.Atoi(r.URL.Query().Get("vmid"))
		if e != nil || id < 100 || id > 999999999 {
			fail("Ungültige VM-ID.")
		}
		out = []doc{}
		for _, table := range []string{"operations", "jobs"} {
			for _, op := range s.allDocs(table) {
				args := configOf(op["args"])
				if op["connection_id"] != c.ID || (int(num(op["vmid"])) != id && int(num(args["newid"])) != id && int(num(args["vmid"])) != id) {
					continue
				}
				detail := doc{}
				for k, v := range args {
					if k != "digest" && k != "password" && k != "ssh-keys" && k != "cipassword" {
						detail[k] = v
					}
				}
				msg := ""
				if ev := anyList(op["events"]); len(ev) > 0 {
					msg = str(configOf(ev[len(ev)-1])["message"])
				}
				out = append(out, doc{"source": "orbit", "at": op["created"], "action": op["action"], "status": op["status"], "user": op["requested_by"], "node": op["node"], "args": detail, "message": msg, "id": op["id"]})
			}
		}
		if c.Kind == "pve" {
			for _, n := range objects(s.call(r.Context(), c, "GET", "/nodes", nil)) {
				if n["status"] != "online" {
					continue
				}
				node := str(n["node"])
				rows, e := s.PVE.Call(r.Context(), c, "GET", "/nodes/"+url.PathEscape(node)+"/tasks", url.Values{"vmid": {strconv.Itoa(id)}, "limit": {"100"}, "source": {"all"}})
				if e != nil {
					continue
				}
				for _, t := range objects(rows) {
					out = append(out, doc{"source": "proxmox", "at": t["starttime"], "end": t["endtime"], "action": t["type"], "status": defaultText(str(t["status"]), "running"), "user": t["user"], "node": node, "upid": t["upid"]})
				}
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return num(out[i]["at"]) > num(out[j]["at"]) })
		if len(out) > 300 {
			out = out[:300]
		}
	})
	return out, err
}
