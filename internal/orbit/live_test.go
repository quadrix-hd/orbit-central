package orbit

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveSnapshotRedactsCredentialsAndIncludesModules(t *testing.T) {
	s, _ := infraServer(t)
	r := httptest.NewRequest("GET", "/api/events", nil)
	data := s.liveSnapshot(r, "network", 0)
	raw := string(must(json.Marshal(data)))
	if strings.Contains(raw, "example-secret") || strings.Contains(raw, "\"token\"") {
		t.Fatal("permanent credentials exposed")
	}
	if len(data["resources"].([]any)) == 0 || data["modules"] == nil || data["operations"] == nil {
		t.Fatal("incomplete snapshot")
	}
}
func TestEventStreamAuthenticatesAndClosesAfterSessionRevocation(t *testing.T) {
	s, _ := infraServer(t)
	cookie, _ := setup(t, s, s.Store)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	client := &http.Client{Timeout: 6 * time.Second}
	req := must(http.NewRequest("GET", httpServer.URL+"/api/events", nil))
	req.AddCookie(cookie)
	resp := must(client.Do(req))
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("not an unbuffered event stream")
	}
	reader := bufio.NewReader(resp.Body)
	ready, snapshot := false, false
	for !snapshot {
		line := must(reader.ReadString('\n'))
		if strings.Contains(line, "event: ready") {
			ready = true
		}
		if strings.Contains(line, "event: snapshot") {
			snapshot = true
		}
	}
	if !ready {
		t.Fatal("missing ready")
	}
	if _, e := s.Store.DB.Exec("DELETE FROM sessions"); e != nil {
		t.Fatal(e)
	}
	expired := false
	for {
		line, e := reader.ReadString('\n')
		if strings.Contains(line, "auth_expired") {
			expired = true
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	if !expired {
		t.Fatal("session revoke did not close stream")
	}
}
func TestEventStreamRejectsMissingSessionAndForeignOrigin(t *testing.T) {
	s, _ := infraServer(t)
	if request(s, "GET", "/api/events", "", nil, "", "").Code != 401 {
		t.Fatal("anonymous stream accepted")
	}
	cookie, _ := setup(t, s, s.Store)
	if request(s, "GET", "/api/events", "", cookie, "", "https://foreign.test").Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if request(s, "GET", "/api/events?section=unknown", "", cookie, "", "").Code != 400 {
		t.Fatal("unknown section accepted")
	}
}
func TestEventStreamContextCancellationReleasesClient(t *testing.T) {
	s, _ := infraServer(t)
	cookie, _ := setup(t, s, s.Store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	s.mu.Lock()
	clients := s.liveClients
	s.mu.Unlock()
	if clients != 0 {
		t.Fatal("live connection leaked")
	}
}

func TestEventStreamOverridesOrdinaryResponseWriteTimeout(t *testing.T) {
	s, _ := infraServer(t)
	cookie, _ := setup(t, s, s.Store)
	server := httptest.NewUnstartedServer(s)
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	req := must(http.NewRequest("GET", server.URL+"/api/events", nil))
	req.AddCookie(cookie)
	resp := must(client.Do(req))
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	snapshots := 0
	for snapshots < 2 {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal("stream did not survive the HTTP write timeout", e)
		}
		if strings.Contains(line, "event: snapshot") {
			snapshots++
		}
	}
}
