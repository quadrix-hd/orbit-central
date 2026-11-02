package orbit

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type consoleGrant struct {
	Session, CID, Endpoint, Node string
	VMID, Port                   int
	Ticket                       string
	Expires                      time.Time
}
type Console struct {
	mu      sync.Mutex
	pending map[string]consoleGrant
	active  int
}

func newConsole() *Console { return &Console{pending: map[string]consoleGrant{}} }
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.status = http.StatusSwitchingProtocols
	return w.ResponseWriter.(http.Hijacker).Hijack()
}
func (s *Server) createConsole(ctx context.Context, d doc, session *session) doc {
	cid := field(d, "connection_id", 64, `[a-zA-Z0-9-]+`)
	node := field(d, "node", 64, `[a-zA-Z0-9_.-]+`)
	id := integer(d, "vmid", 100, 999999999)
	c := s.conn(cid)
	vm := s.findVM(ctx, c, id, node)
	if vm["type"] != "qemu" {
		fail("LXC-Terminal wird noch nicht unterstützt.")
	}
	root := os.Getenv("ORBIT_NOVNC_ROOT")
	if root == "" {
		root = "/usr/share/novnc"
	}
	if _, err := os.Stat(filepath.Join(root, "core", "rfb.js")); err != nil {
		panic(problem("noVNC fehlt im Image.", 503))
	}
	g := s.Console
	g.mu.Lock()
	count := 0
	for key, v := range g.pending {
		if time.Now().After(v.Expires) {
			delete(g.pending, key)
		} else if v.Session == session.ID {
			count++
		}
	}
	full := g.active >= 4 || count >= 2 || len(g.pending) >= 8
	g.mu.Unlock()
	if full {
		panic(problem("Zu viele offene Konsolen.", 429))
	}
	result := configOf(s.call(ctx, c, "POST", baseVM(node, "qemu", id)+"/vncproxy", doc{"websocket": 1}))
	ticket, ok := result["ticket"].(string)
	port := int(num(result["port"]))
	if !ok || port < 1 || port > 65535 {
		panic(problem("Ungültige Proxmox-Konsolenantwort.", 502))
	}
	key := randomToken(32)
	g.mu.Lock()
	g.pending[key] = consoleGrant{session.ID, cid, s.PVE.Active(c), node, id, port, ticket, time.Now().Add(time.Minute)}
	g.mu.Unlock()
	_ = s.Store.Audit("console.open.requested", cid)
	return doc{"id": key, "ticket": ticket, "expires_in": 60}
}
func (s *Server) proxyConsole(w http.ResponseWriter, r *http.Request, key string, session *session) error {
	if r.Header.Get("Origin") != s.Origin {
		return problem("Konsolen-Ursprung nicht erlaubt.", 403)
	}
	if !websocket.IsWebSocketUpgrade(r) {
		return problem("WebSocket-Upgrade erwartet.", 400)
	}
	g := s.Console
	g.mu.Lock()
	grant, exists := g.pending[key]
	if !exists || time.Now().After(grant.Expires) {
		g.mu.Unlock()
		return problem("Konsolenfreigabe abgelaufen.", 410)
	}
	if grant.Session != session.ID {
		g.mu.Unlock()
		return problem("Konsolenfreigabe gehört zu einer anderen Sitzung.", 403)
	}
	if g.active >= 4 {
		g.mu.Unlock()
		return problem("Konsolenlimit erreicht.", 429)
	}
	delete(g.pending, key)
	g.active++
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active--; g.mu.Unlock() }()
	c, err := s.Store.Connection(grant.CID)
	if err != nil {
		return err
	}
	token, err := s.Store.Decrypt(c.Token)
	if err != nil {
		return err
	}
	roots := s.PVE.Client.Transport.(*http.Transport).TLSClientConfig.Clone()
	roots.MinVersion = tls.VersionTLS12
	dialer := websocket.Dialer{HandshakeTimeout: 25 * time.Second, TLSClientConfig: roots, Subprotocols: []string{"binary"}}
	query := url.Values{"port": {str(grant.Port)}, "vncticket": {grant.Ticket}}
	u, err := url.Parse(grant.Endpoint)
	if err != nil {
		return err
	}
	target := "wss://" + u.Host + "/api2/json" + baseVM(grant.Node, "qemu", grant.VMID) + "/vncwebsocket?" + query.Encode()
	header := http.Header{"Authorization": {"PVEAPIToken=" + token}, "Origin": {grant.Endpoint}}
	up, resp, err := dialer.DialContext(r.Context(), target, header)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return problem("Proxmox-Konsole abgelehnt. VM.Console, API-Token-Unterstützung und TLS prüfen.", 502)
	}
	defer up.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(req *http.Request) bool { return req.Header.Get("Origin") == s.Origin }, Subprotocols: []string{"binary"}, HandshakeTimeout: 10 * time.Second}
	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil
	}
	defer down.Close()
	up.SetReadLimit(16 * 1024 * 1024)
	down.SetReadLimit(16 * 1024 * 1024)
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()
	done := make(chan struct{})
	var once sync.Once
	closeBoth := func() { once.Do(func() { close(done); up.Close(); down.Close() }) }
	defer closeBoth()
	pump := func(dst, src *websocket.Conn) {
		defer closeBoth()
		for {
			kind, payload, e := src.ReadMessage()
			if e != nil {
				return
			}
			if kind != websocket.BinaryMessage && kind != websocket.TextMessage {
				continue
			}
			_ = dst.SetWriteDeadline(time.Now().Add(25 * time.Second))
			if dst.WriteMessage(websocket.BinaryMessage, payload) != nil {
				return
			}
		}
	}
	go pump(up, down)
	go pump(down, up)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			_ = s.Store.Audit("console.closed", grant.CID)
			return nil
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			var n int
			if s.Store.DB.QueryRow("SELECT 1 FROM sessions WHERE id=? AND expires>?", session.ID, now()).Scan(&n) != nil {
				return nil
			}
		}
	}
}
func (s *Server) novnc(w http.ResponseWriter, r *http.Request) error {
	root := os.Getenv("ORBIT_NOVNC_ROOT")
	if root == "" {
		root = "/usr/share/novnc"
	}
	root = must(filepath.Abs(root))
	name := strings.TrimPrefix(r.URL.Path, "/vendor/novnc/")
	path := must(filepath.Abs(filepath.Join(root, name)))
	if !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return problem("Nicht gefunden.", 404)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.HasPrefix(resolved, root+string(os.PathSeparator)) || !contains(".js,.css,.png,.svg,.woff,.woff2", filepath.Ext(resolved)) {
		return problem("Nicht gefunden.", 404)
	}
	http.ServeFile(w, r, resolved)
	return nil
}
