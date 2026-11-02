package orbit

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetricsUseDirectNodeStatusInsteadOfCachedClusterResources(t *testing.T) {
	s, f := managementServer(t)
	rows := s.currentMetrics(context.Background(), s.conn("test"))
	found := map[string]doc{}
	for _, r := range rows {
		found[str(r["type"])] = r
		if num(r["sampled_at"]) <= 0 {
			t.Fatal("no sample timestamp")
		}
	}
	if num(found["node"]["cpu"]) != 0.66 || num(found["node"]["mem"]) != 4567 || num(found["qemu"]["cpu"]) != 0.77 || num(found["lxc"]["swap"]) != 4096 {
		t.Fatal(rows)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads["/cluster/resources"] != 0 || len(f.writes) != 0 {
		t.Fatal("metrics used inventory cache or wrote to Proxmox", f.reads, f.writes)
	}
}
func TestConcurrentMetricSubscriptionsShareOneRead(t *testing.T) {
	s, f := managementServer(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rows := s.currentMetrics(context.Background(), s.conn("test"))
			if len(rows) != 3 {
				t.Errorf("incomplete sample: %d", len(rows))
			}
		}()
	}
	close(start)
	wg.Wait()
	f.mu.Lock()
	if f.reads["/nodes/node1/qemu"] != 1 {
		t.Fatal(f.reads)
	}
	f.mu.Unlock()
	s.metricsMu.Lock()
	cache := s.metricsCache["test"]
	cache.at = time.Now().Add(-2 * time.Second)
	s.metricsCache["test"] = cache
	s.metricsMu.Unlock()
	s.currentMetrics(context.Background(), s.conn("test"))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads["/nodes/node1/qemu"] != 2 {
		t.Fatal("cache never refreshed", f.reads)
	}
}
func TestSlowModuleCannotDelayMetricFrames(t *testing.T) {
	s, _ := managementServer(t)
	transport := s.PVE.Client.Transport
	s.PVE.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/apt/") {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return transport.RoundTrip(r)
	})
	cookie, _ := setup(t, s, s.Store)
	server := httptest.NewServer(s)
	defer server.Close()
	req := must(http.NewRequest("GET", server.URL+"/api/events?section=lifecycle", nil))
	req.AddCookie(cookie)
	client := &http.Client{Timeout: 4 * time.Second}
	began := time.Now()
	resp := must(client.Do(req))
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	count := 0
	for count < 2 {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal("CPU/RAM delayed by module", e)
		}
		if strings.Contains(line, "event: metrics") {
			count++
		}
	}
	if time.Since(began) > 3*time.Second {
		t.Fatal("metrics cadence blocked", time.Since(began))
	}
}
func TestGuestDetailKeepsConnectionAndLXCMountpoints(t *testing.T) {
	s, f := managementServer(t)
	f.guestType = "lxc"
	r := httptest.NewRequest("GET", "/api/connections/test/vms/100?node=node1", nil)
	data, err := s.vmDetailData(r, s.conn("test"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if configOf(data["vm"])["connection_id"] != "test" || configOf(data["config"])["digest"] != "v1" {
		t.Fatal(data)
	}
	rows := data["disks"].([]doc)
	found := false
	for _, r := range rows {
		if r["device"] == "mp0" && r["mountpoint"] == "/existing" {
			found = true
		}
	}
	if !found {
		t.Fatal(rows)
	}
}
