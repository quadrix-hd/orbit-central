package orbit

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
)

// Proxmox task center: the cluster-wide task list of each connection and the
// status and log of a single task, so administrators can follow what Proxmox
// is doing without opening the Proxmox web interface.

// PVE: UPID:node:pid:pstart:starttime:type:id:user:
// PBS: UPID:node:pid:pstart:taskid:starttime:type:id:user:
var upidPattern = regexp.MustCompile(`^UPID:[A-Za-z0-9_.-]{1,64}:[0-9A-Fa-f]{8}:[0-9A-Fa-f]{8}:(?:[0-9A-Fa-f]{8}:)?[0-9A-Fa-f]{8}:[A-Za-z0-9_-]{1,40}:[^:/\s]{0,128}:[^:/\s]{1,128}:$`)
var taskNodePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

func (s *Server) taskList(r *http.Request, c Connection) (any, error) {
	path := "/cluster/tasks"
	if c.Kind == "pbs" {
		path = "/nodes/localhost/tasks"
	}
	fields := url.Values{}
	if c.Kind == "pbs" {
		fields.Set("limit", "200")
	}
	data, err := s.PVE.Call(r.Context(), c, "GET", path, fields)
	if err != nil {
		return nil, err
	}
	rows := objects(data)
	for _, row := range rows {
		row["connection_id"] = c.ID
	}
	sort.SliceStable(rows, func(i, j int) bool { return num(rows[i]["starttime"]) > num(rows[j]["starttime"]) })
	if len(rows) > 300 {
		rows = rows[:300]
	}
	return rows, nil
}

func (s *Server) taskDetail(r *http.Request, c Connection, node, upid string) (any, error) {
	if !taskNodePattern.MatchString(node) || !upidPattern.MatchString(upid) {
		return nil, problem("Ungültige Task-ID.", 400)
	}
	base := "/nodes/" + url.PathEscape(node) + "/tasks/" + url.PathEscape(upid)
	status, err := s.PVE.Call(r.Context(), c, "GET", base+"/status", nil)
	if err != nil {
		return nil, err
	}
	start := 0
	if v, e := strconv.Atoi(r.URL.Query().Get("start")); e == nil && v > 0 && v < 10000000 {
		start = v
	}
	log, err := s.PVE.Call(r.Context(), c, "GET", base+"/log", url.Values{"start": {strconv.Itoa(start)}, "limit": {"1000"}})
	if err != nil {
		return nil, err
	}
	return doc{"status": status, "log": log, "start": start}, nil
}
