package orbit

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type metricCache struct {
	at   time.Time
	rows []doc
}

// Coalesce simultaneous browser subscriptions: a second tab never doubles the
// direct status requests. The cache lives for one sampling interval only.
func (s *Server) currentMetrics(ctx context.Context, c Connection) []doc {
	s.metricsMu.Lock()
	if v, ok := s.metricsCache[c.ID]; ok && time.Since(v.at) < 900*time.Millisecond {
		s.metricsMu.Unlock()
		return v.rows
	}
	if waiting := s.metricsRunning[c.ID]; waiting != nil {
		s.metricsMu.Unlock()
		select {
		case <-waiting:
		case <-ctx.Done():
			return nil
		}
		s.metricsMu.Lock()
		v := s.metricsCache[c.ID]
		s.metricsMu.Unlock()
		return v.rows
	}
	if s.metricsRunning == nil {
		s.metricsRunning = map[string]chan struct{}{}
		s.metricsCache = map[string]metricCache{}
	}
	done := make(chan struct{})
	s.metricsRunning[c.ID] = done
	s.metricsMu.Unlock()
	var rows []doc
	defer func() {
		s.metricsMu.Lock()
		s.metricsCache[c.ID] = metricCache{time.Now(), rows}
		delete(s.metricsRunning, c.ID)
		close(done)
		s.metricsMu.Unlock()
	}()

	nodes, e := s.PVE.Call(ctx, c, "GET", "/nodes", nil)
	if e != nil {
		return nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 12)
	for _, v := range objects(nodes) {
		if v["status"] != "online" {
			continue
		}
		node := str(v["node"])
		for _, kind := range []string{"node", "qemu", "lxc"} {
			kind := kind
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-slots }()
				endpoint := "/nodes/" + url.PathEscape(node) + "/" + kind
				params := url.Values{}
				if kind == "node" {
					endpoint = "/nodes/" + url.PathEscape(node) + "/status"
				} else if kind == "qemu" {
					params.Set("full", "1")
				}
				readCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
				defer cancel()
				raw, err := s.PVE.Call(readCtx, c, "GET", endpoint, params)
				if err != nil {
					return
				}
				values := objects(raw)
				if kind == "node" {
					d, ok := raw.(map[string]any)
					if !ok {
						return
					}
					values = []doc{d}
				}
				local := []doc{}
				for _, d := range values {
					sample := doc{"connection_id": c.ID, "type": kind, "node": node, "sampled_at": now(), "source": "node/" + kind}
					if kind == "node" {
						sample["source"] = "node/status"
						if mem, ok := d["memory"].(map[string]any); ok {
							sample["mem"] = mem["used"]
							sample["maxmem"] = mem["total"]
						}
						if swap, ok := d["swap"].(map[string]any); ok {
							sample["swap"] = swap["used"]
							sample["maxswap"] = swap["total"]
						}
						sample["status"] = "online"
					} else {
						sample["vmid"] = d["vmid"]
						for _, k := range []string{"mem", "maxmem", "swap", "maxswap", "status", "disk", "maxdisk", "netin", "netout", "diskread", "diskwrite"} {
							if value, ok := d[k]; ok {
								sample[k] = value
							}
						}
					}
					for _, k := range []string{"cpu", "uptime"} {
						if value, ok := d[k]; ok {
							sample[k] = value
						}
					}
					local = append(local, sample)
				}
				mu.Lock()
				rows = append(rows, local...)
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	return rows
}
func metricKey(d doc) string {
	return str(d["connection_id"]) + "/" + str(d["type"]) + "/" + str(d["node"]) + "/" + strconv.Itoa(int(num(d["vmid"])))
}
