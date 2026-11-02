package orbit

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Problem struct {
	Message   string
	Status    int
	Uncertain bool
}

func (p *Problem) Error() string               { return p.Message }
func problem(message string, status int) error { return &Problem{Message: message, Status: status} }

type Proxmox struct {
	Store     *Store
	Client    *http.Client
	mu        sync.Mutex
	preferred map[string]string
}

func NewProxmox(s *Store, caFile string) (*Proxmox, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caFile != "" {
		pem, e := os.ReadFile(caFile)
		if e != nil {
			return nil, fmt.Errorf("CA-Datei kann nicht gelesen werden: %w", e)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA-Datei enthält kein gültiges Zertifikat")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return &Proxmox{Store: s, Client: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, preferred: map[string]string{}}, nil
}
func ValidateEndpoints(primary string, extra []string) ([]string, error) {
	if len(extra) > 7 {
		return nil, problem("Maximal sieben zusätzliche Cluster-Adressen angeben.", 400)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, endpoint := range append([]string{primary}, extra...) {
		endpoint = strings.TrimRight(endpoint, "/")
		u, err := url.Parse(endpoint)
		if err != nil || len(endpoint) > 300 || strings.ContainsAny(endpoint, " \t\r\n") || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Port() == "0" {
			return nil, problem("Gültige HTTPS-Adresse ohne Pfad erwartet.", 400)
		}
		if u.Port() != "" {
			port, e := strconv.Atoi(u.Port())
			if e != nil || port < 1 || port > 65535 {
				return nil, problem("Ungültiger HTTPS-Port.", 400)
			}
		}
		if !seen[endpoint] {
			out = append(out, endpoint)
			seen[endpoint] = true
		}
	}
	return out, nil
}
func (p *Proxmox) Active(c Connection) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v := p.preferred[c.ID]; v != "" {
		return v
	}
	return c.URL
}
func (p *Proxmox) selectEndpoint(c Connection, endpoint string) {
	p.mu.Lock()
	p.preferred[c.ID] = endpoint
	p.mu.Unlock()
}
func (p *Proxmox) Call(ctx context.Context, c Connection, method, path string, fields url.Values) (any, error) {
	endpoints, err := ValidateEndpoints(c.URL, c.Endpoints)
	if err != nil {
		return nil, err
	}
	active := p.Active(c)
	for i, v := range endpoints {
		if v == active {
			endpoints[0], endpoints[i] = endpoints[i], endpoints[0]
			break
		}
	}
	// Read preflight chooses a reachable endpoint. Never retry a dispatched write.
	if method != "GET" && len(endpoints) > 1 {
		if _, err = p.Call(ctx, c, "GET", "/version", nil); err != nil {
			return nil, err
		}
		return p.once(ctx, c, p.Active(c), method, path, fields)
	}
	for i, endpoint := range endpoints {
		result, e := p.once(ctx, c, endpoint, method, path, fields)
		if e == nil {
			p.selectEndpoint(c, endpoint)
			return result, nil
		}
		var pr *Problem
		if method != "GET" || !errors.As(e, &pr) || !pr.Uncertain || i == len(endpoints)-1 {
			return nil, e
		}
	}
	return nil, problem("Keine Proxmox-Adresse verfügbar.", 502)
}

var privilegePattern = regexp.MustCompile(`\b(?:VM|Sys|Datastore|SDN|Pool|Permissions|User|Group)\.[a-zA-Z]+\b`)
var fieldPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,60}$`)

func (p *Proxmox) once(ctx context.Context, c Connection, endpoint, method, path string, fields url.Values) (any, error) {
	token, err := p.Store.Decrypt(c.Token)
	if err != nil {
		return nil, problem("Gespeicherter API-Token kann nicht entschlüsselt werden.", 500)
	}
	target := endpoint + "/api2/json" + path
	var body io.Reader
	if method == "POST" || method == "PUT" {
		body = strings.NewReader(fields.Encode())
	} else if len(fields) > 0 {
		target += "?" + fields.Encode()
	}
	timeout := 25 * time.Second
	if method == "GET" {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	auth := "PVEAPIToken=" + token
	if c.Kind == "pbs" {
		auth = "PBSAPIToken=" + strings.Replace(token, "=", ":", 1)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		var certificate x509.UnknownAuthorityError
		var hostname x509.HostnameError
		message := "Proxmox nicht erreichbar. Adresse, DNS, Firewall und CA prüfen."
		if errors.As(err, &certificate) || errors.As(err, &hostname) {
			message = "TLS-Zertifikat nicht vertrauenswürdig oder Hostname passt nicht. CA-Mount und ORBIT_CA_FILE prüfen."
		}
		return nil, &Problem{message, 502, true}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
	if err != nil || len(raw) > 16*1024*1024 {
		return nil, &Problem{"Proxmox-Antwort konnte nicht vollständig gelesen werden.", 502, true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		details := ""
		var payload struct {
			Errors map[string]any `json:"errors"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			names := []string{}
			for k := range payload.Errors {
				if fieldPattern.MatchString(k) {
					names = append(names, k)
				}
			}
			sort.Strings(names)
			if len(names) > 12 {
				names = names[:12]
			}
			if len(names) > 0 {
				details = " Betroffene Felder: " + strings.Join(names, ", ") + "."
			}
		}
		privileges := privilegePattern.FindAllString(string(raw), 12)
		if len(privileges) > 0 {
			details += " Benötigte Rechte: " + strings.Join(privileges, ", ") + "."
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, problem("Proxmox verweigert den Zugriff. API-Token und Berechtigungen prüfen."+details, 502)
		}
		return nil, &Problem{fmt.Sprintf("Proxmox meldet HTTP %d.%s", resp.StatusCode, details), 502, resp.StatusCode >= 500}
	}
	var envelope map[string]json.RawMessage
	if err = json.NewDecoder(bytes.NewReader(raw)).Decode(&envelope); err != nil {
		return nil, &Problem{"Unerwartete Proxmox-Antwort.", 502, true}
	}
	data, exists := envelope["data"]
	if !exists {
		return nil, &Problem{"Unerwartete Proxmox-Antwort.", 502, true}
	}
	var result any
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, &Problem{"Unerwartete Proxmox-Antwort.", 502, true}
	}
	return result, nil
}
func objects(data any) []map[string]any {
	out := []map[string]any{}
	if rows, ok := data.([]any); ok {
		for _, row := range rows {
			if obj, ok := row.(map[string]any); ok {
				out = append(out, obj)
			}
		}
	}
	return out
}
func (p *Proxmox) VerifyEndpoints(ctx context.Context, c Connection) error {
	endpoints, err := ValidateEndpoints(c.URL, c.Endpoints)
	if err != nil {
		return err
	}
	if len(endpoints) == 1 {
		return nil
	}
	if c.Kind == "pbs" {
		return problem("Alternative Endpunkte sind nur für einen VE-Cluster möglich.", 400)
	}
	expected := ""
	for _, endpoint := range endpoints {
		data, e := p.once(ctx, c, endpoint, "GET", "/cluster/status", nil)
		if e != nil {
			return e
		}
		cluster := ""
		members := []string{}
		for _, r := range objects(data) {
			if r["type"] == "cluster" {
				cluster, _ = r["name"].(string)
			}
			if r["type"] == "node" {
				members = append(members, fmt.Sprint(r["id"])+"/"+fmt.Sprint(r["name"]))
			}
		}
		if cluster == "" || len(members) == 0 {
			return problem("Alternative Endpunkte benötigen einen bestehenden VE-Cluster.", 400)
		}
		sort.Strings(members)
		identity := cluster + "/" + strings.Join(members, ",")
		if expected != "" && expected != identity {
			return problem("Die Endpunkte gehören nicht zum selben Cluster.", 400)
		}
		expected = identity
	}
	return nil
}
