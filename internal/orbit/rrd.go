package orbit

import (
	"net/http"
	"net/url"
	"strconv"
)

// History charts: Proxmox RRD data for a host or guest, normalised to the
// fields the UI plots. Timeframes follow Proxmox (hour … year).
func (s *Server) rrd(r *http.Request, c Connection) (any, error) {
	var out any
	err := guarded(func() {
		q := r.URL.Query()
		kind := field(doc{"v": q.Get("kind")}, "v", 5, "node|qemu|lxc")
		node := field(doc{"v": q.Get("node")}, "v", 64, `[a-zA-Z0-9_.-]+`)
		timeframe := field(doc{"v": defaultText(q.Get("timeframe"), "hour")}, "v", 5, "hour|day|week|month|year")
		path := "/nodes/" + url.PathEscape(node) + "/rrddata"
		if kind != "node" {
			id, err := strconv.Atoi(q.Get("vmid"))
			if err != nil || id < 100 || id > 999999999 {
				fail("Ungültige VMID.")
			}
			path = baseVM(node, kind, id) + "/rrddata"
		}
		rows := objects(s.call(r.Context(), c, "GET", path, doc{"timeframe": timeframe, "cf": "AVERAGE"}))
		points := []doc{}
		for _, row := range rows {
			if num(row["time"]) == 0 {
				continue
			}
			p := doc{"t": num(row["time"])}
			for _, k := range []string{"cpu", "mem", "maxmem", "netin", "netout", "diskread", "diskwrite", "loadavg", "maxcpu"} {
				if v, ok := row[k]; ok && v != nil {
					p[k] = num(v)
				}
			}
			if _, ok := row["memused"]; ok && p["mem"] == nil { // PVE 9 node schema
				p["mem"], p["maxmem"] = num(row["memused"]), num(row["memtotal"])
			}
			points = append(points, p)
		}
		out = doc{"timeframe": timeframe, "points": points}
	})
	return out, err
}
