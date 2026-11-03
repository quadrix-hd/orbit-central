package orbit

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Single sign-on via OpenID Connect (authorization code flow with PKCE).
// Works with Microsoft Entra ID, Keycloak, Authentik, Google Workspace and
// other standard providers. ID tokens are verified against the provider's
// JWKS (RS256, ES256); users are created on first login and their role is
// derived from group claims on every login.

type oidcConfig struct {
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // encrypted
	ButtonLabel  string `json:"button_label"`
	UsernameFrom string `json:"username_claim"` // preferred_username | email | upn
	GroupsClaim  string `json:"groups_claim"`
	AdminGroup   string `json:"admin_group"`
	OperatorGrp  string `json:"operator_group"`
	ViewerGroup  string `json:"viewer_group"`
	DefaultRole  string `json:"default_role"` // "" = deny users without a matching group
}

type oidcProvider struct {
	AuthURL, TokenURL, JWKSURL, Issuer string
	keys                               map[string]crypto.PublicKey
	fetched                            time.Time
}

type oidcPending struct {
	verifier, nonce string
	created         time.Time
}

type oidcState struct {
	mu       sync.Mutex
	provider *oidcProvider
	issuer   string
	pending  map[string]oidcPending
}

func (s *Server) oidcConfig() oidcConfig {
	var raw string
	cfg := oidcConfig{UsernameFrom: "preferred_username", GroupsClaim: "groups", ButtonLabel: "Mit Firmenkonto anmelden"}
	if s.Store.DB.QueryRow("SELECT value FROM settings WHERE key='oidc'").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg
}

func (s *Server) publicOIDC(cfg oidcConfig) doc {
	return doc{"enabled": cfg.Enabled, "issuer": cfg.Issuer, "client_id": cfg.ClientID, "has_secret": cfg.ClientSecret != "", "button_label": cfg.ButtonLabel, "username_claim": cfg.UsernameFrom, "groups_claim": cfg.GroupsClaim, "admin_group": cfg.AdminGroup, "operator_group": cfg.OperatorGrp, "viewer_group": cfg.ViewerGroup, "default_role": cfg.DefaultRole, "redirect_uri": s.Origin + "/api/oidc/callback"}
}

func (s *Server) saveOIDC(d doc) oidcConfig {
	old := s.oidcConfig()
	cfg := oidcConfig{Enabled: d["enabled"] == true}
	cfg.Issuer = strings.TrimRight(strings.TrimSpace(str(d["issuer"])), "/")
	if u, err := url.Parse(cfg.Issuer); cfg.Issuer != "" && (err != nil || u.Scheme != "https" || u.Host == "") {
		fail("Issuer muss eine https-URL sein.")
	}
	cfg.ClientID = strings.TrimSpace(str(d["client_id"]))
	if secret := str(d["client_secret"]); secret != "" {
		cfg.ClientSecret = must(s.Store.Encrypt(secret))
	} else {
		cfg.ClientSecret = old.ClientSecret
	}
	cfg.ButtonLabel = defaultText(strings.TrimSpace(str(d["button_label"])), "Mit Firmenkonto anmelden")
	cfg.UsernameFrom = defaultText(str(d["username_claim"]), "preferred_username")
	if !contains("preferred_username,email,upn,sub", cfg.UsernameFrom) {
		fail("Ungültiges Benutzernamen-Feld.")
	}
	cfg.GroupsClaim = defaultText(strings.TrimSpace(str(d["groups_claim"])), "groups")
	cfg.AdminGroup, cfg.OperatorGrp, cfg.ViewerGroup = strings.TrimSpace(str(d["admin_group"])), strings.TrimSpace(str(d["operator_group"])), strings.TrimSpace(str(d["viewer_group"]))
	cfg.DefaultRole = str(d["default_role"])
	if cfg.DefaultRole != "" && roleRank[cfg.DefaultRole] == 0 {
		fail("Ungültige Standardrolle.")
	}
	if cfg.Enabled && (cfg.Issuer == "" || cfg.ClientID == "") {
		fail("Issuer und Client-ID werden benötigt.")
	}
	for _, v := range []string{cfg.ButtonLabel, cfg.GroupsClaim, cfg.AdminGroup, cfg.OperatorGrp, cfg.ViewerGroup, cfg.ClientID} {
		if len(v) > 300 {
			fail("Eingabe zu lang.")
		}
	}
	raw := must(json.Marshal(cfg))
	if _, err := s.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('oidc',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw)); err != nil {
		panic(err)
	}
	s.OIDC.mu.Lock()
	s.OIDC.provider = nil
	s.OIDC.mu.Unlock()
	return cfg
}

func (s *Server) oidcHTTP() *http.Client {
	client := *s.PVE.Client // same trust store (system CAs + ORBIT_CA_FILE)
	client.Timeout = 15 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func (s *Server) oidcDiscover(ctx context.Context, cfg oidcConfig) *oidcProvider {
	s.OIDC.mu.Lock()
	if p := s.OIDC.provider; p != nil && s.OIDC.issuer == cfg.Issuer && time.Since(p.fetched) < time.Hour {
		s.OIDC.mu.Unlock()
		return p
	}
	s.OIDC.mu.Unlock()
	var meta struct {
		Issuer   string `json:"issuer"`
		Auth     string `json:"authorization_endpoint"`
		Token    string `json:"token_endpoint"`
		JWKS     string `json:"jwks_uri"`
		Response []string
	}
	getJSON(ctx, s.oidcHTTP(), cfg.Issuer+"/.well-known/openid-configuration", &meta)
	if strings.TrimRight(meta.Issuer, "/") != cfg.Issuer || meta.Auth == "" || meta.Token == "" || meta.JWKS == "" {
		panic(problem("OIDC-Anbieter liefert keine passende Konfiguration (Issuer prüfen).", 502))
	}
	p := &oidcProvider{AuthURL: meta.Auth, TokenURL: meta.Token, JWKSURL: meta.JWKS, Issuer: meta.Issuer, fetched: time.Now()}
	p.keys = fetchJWKS(ctx, s.oidcHTTP(), meta.JWKS)
	s.OIDC.mu.Lock()
	s.OIDC.provider, s.OIDC.issuer = p, cfg.Issuer
	s.OIDC.mu.Unlock()
	return p
}

func getJSON(ctx context.Context, client *http.Client, target string, out any) {
	req := must(http.NewRequestWithContext(ctx, "GET", target, nil))
	resp, err := client.Do(req)
	if err != nil {
		panic(problem("OIDC-Anbieter nicht erreichbar.", 502))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		panic(problem(fmt.Sprintf("OIDC-Anbieter antwortet mit HTTP %d.", resp.StatusCode), 502))
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		panic(problem("Ungültige Antwort des OIDC-Anbieters.", 502))
	}
}

func b64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		panic(problem("Ungültiges Token-Format.", 401))
	}
	return b
}

func fetchJWKS(ctx context.Context, client *http.Client, target string) map[string]crypto.PublicKey {
	var set struct {
		Keys []struct{ Kty, Kid, Use, Alg, N, E, Crv, X, Y string }
	}
	getJSON(ctx, client, target, &set)
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			e := new(big.Int).SetBytes(b64(k.E))
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(b64(k.N)), E: int(e.Int64())}
		case "EC":
			if k.Crv == "P-256" {
				keys[k.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(b64(k.X)), Y: new(big.Int).SetBytes(b64(k.Y))}
			}
		}
	}
	if len(keys) == 0 {
		panic(problem("OIDC-Anbieter veröffentlicht keine Signaturschlüssel.", 502))
	}
	return keys
}

// verifyIDToken checks signature, issuer, audience, expiry and nonce.
func verifyIDToken(token string, p *oidcProvider, clientID, nonce string, at time.Time) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		panic(problem("Ungültiges ID-Token.", 401))
	}
	var header struct{ Alg, Kid string }
	if json.Unmarshal(b64(parts[0]), &header) != nil {
		panic(problem("Ungültiges ID-Token.", 401))
	}
	key, ok := p.keys[header.Kid]
	if !ok && len(p.keys) == 1 && header.Kid == "" {
		for _, k := range p.keys {
			key, ok = k, true
		}
	}
	if !ok {
		panic(problem("Unbekannter Signaturschlüssel im ID-Token.", 401))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig := b64(parts[2])
	switch k := key.(type) {
	case *rsa.PublicKey:
		if header.Alg != "RS256" || rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) != nil {
			panic(problem("Signatur des ID-Tokens ungültig.", 401))
		}
	case *ecdsa.PublicKey:
		if header.Alg != "ES256" || len(sig) != 64 || !ecdsa.Verify(k, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			panic(problem("Signatur des ID-Tokens ungültig.", 401))
		}
	default:
		panic(problem("Signaturverfahren nicht unterstützt.", 401))
	}
	var claims map[string]any
	if json.Unmarshal(b64(parts[1]), &claims) != nil {
		panic(problem("Ungültiges ID-Token.", 401))
	}
	if strings.TrimRight(str(claims["iss"]), "/") != strings.TrimRight(p.Issuer, "/") {
		panic(problem("ID-Token stammt von einem anderen Aussteller.", 401))
	}
	audOK := false
	switch aud := claims["aud"].(type) {
	case string:
		audOK = aud == clientID
	case []any:
		for _, a := range aud {
			audOK = audOK || a == clientID
		}
	}
	if !audOK {
		panic(problem("ID-Token ist nicht für Orbit ausgestellt.", 401))
	}
	if exp := num(claims["exp"]); exp == 0 || float64(at.Unix()) > exp+60 {
		panic(problem("ID-Token abgelaufen.", 401))
	}
	if subtle.ConstantTimeCompare([]byte(str(claims["nonce"])), []byte(nonce)) != 1 {
		panic(problem("Anmeldung ungültig (Nonce).", 401))
	}
	return claims
}

func claimList(v any) []string {
	out := []string{}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			out = append(out, str(x))
		}
	case string:
		for _, x := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' }) {
			out = append(out, x)
		}
	}
	return out
}

// oidcRole picks the highest role whose group appears in the claims.
func oidcRole(cfg oidcConfig, claims map[string]any) string {
	groups := map[string]bool{}
	for _, g := range claimList(claims[cfg.GroupsClaim]) {
		groups[g] = true
	}
	for _, pair := range [][2]string{{cfg.AdminGroup, "admin"}, {cfg.OperatorGrp, "operator"}, {cfg.ViewerGroup, "viewer"}} {
		if pair[0] != "" && groups[pair[0]] {
			return pair[1]
		}
	}
	return cfg.DefaultRole
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) oidcRoutes(w http.ResponseWriter, r *http.Request) (bool, error) {
	if !strings.HasPrefix(r.URL.Path, "/api/oidc/") || r.Method != "GET" {
		return false, nil
	}
	cfg := s.oidcConfig()
	err := guarded(func() {
		if !cfg.Enabled {
			panic(problem("Firmenanmeldung ist nicht eingerichtet.", 404))
		}
		switch r.URL.Path {
		case "/api/oidc/login":
			p := s.oidcDiscover(r.Context(), cfg)
			stateID, verifier, nonce := randomToken(24), randomToken(48), randomToken(24)
			s.OIDC.mu.Lock()
			for k, v := range s.OIDC.pending {
				if time.Since(v.created) > 10*time.Minute {
					delete(s.OIDC.pending, k)
				}
			}
			if len(s.OIDC.pending) > 500 {
				s.OIDC.mu.Unlock()
				panic(problem("Zu viele offene Anmeldungen.", 429))
			}
			s.OIDC.pending[stateID] = oidcPending{verifier: verifier, nonce: nonce, created: time.Now()}
			s.OIDC.mu.Unlock()
			q := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "redirect_uri": {s.Origin + "/api/oidc/callback"}, "scope": {"openid profile email"}, "state": {stateID}, "nonce": {nonce}, "code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"}}
			sep := "?"
			if strings.Contains(p.AuthURL, "?") {
				sep = "&"
			}
			http.Redirect(w, r, p.AuthURL+sep+q.Encode(), http.StatusFound)
		case "/api/oidc/callback":
			if e := r.URL.Query().Get("error"); e != "" {
				s.oidcFail(w, r, "Anbieter meldet: "+e)
				return
			}
			s.OIDC.mu.Lock()
			pend, ok := s.OIDC.pending[r.URL.Query().Get("state")]
			delete(s.OIDC.pending, r.URL.Query().Get("state"))
			s.OIDC.mu.Unlock()
			if !ok || time.Since(pend.created) > 10*time.Minute {
				s.oidcFail(w, r, "Anmeldung abgelaufen. Bitte erneut versuchen.")
				return
			}
			p := s.oidcDiscover(r.Context(), cfg)
			form := url.Values{"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")}, "redirect_uri": {s.Origin + "/api/oidc/callback"}, "client_id": {cfg.ClientID}, "code_verifier": {pend.verifier}}
			if cfg.ClientSecret != "" {
				form.Set("client_secret", must(s.Store.Decrypt(cfg.ClientSecret)))
			}
			req := must(http.NewRequestWithContext(r.Context(), "POST", p.TokenURL, strings.NewReader(form.Encode())))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			resp, err := s.oidcHTTP().Do(req)
			if err != nil {
				s.oidcFail(w, r, "Token-Endpunkt nicht erreichbar.")
				return
			}
			defer resp.Body.Close()
			var tok struct {
				IDToken string `json:"id_token"`
				Error   string `json:"error"`
				Desc    string `json:"error_description"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
			if resp.StatusCode != 200 || tok.IDToken == "" {
				s.oidcFail(w, r, "Token-Austausch fehlgeschlagen: "+defaultText(tok.Desc, defaultText(tok.Error, resp.Status)))
				return
			}
			var claims map[string]any
			if e := guarded(func() { claims = verifyIDToken(tok.IDToken, p, cfg.ClientID, pend.nonce, time.Now()) }); e != nil {
				var pr *Problem
				msg := "ID-Token ungültig."
				if errors.As(e, &pr) {
					msg = pr.Message
				}
				s.oidcFail(w, r, msg)
				return
			}
			name := strings.ToLower(str(claims[cfg.UsernameFrom]))
			if cfg.UsernameFrom == "preferred_username" && name == "" {
				name = strings.ToLower(str(claims["email"]))
			}
			if name == "" || len(name) > 128 {
				s.oidcFail(w, r, "Der Anbieter liefert keinen Benutzernamen ("+cfg.UsernameFrom+").")
				return
			}
			role := oidcRole(cfg, claims)
			if role == "" {
				_ = s.Store.AuditAs(name, "auth.oidc_denied", "keine passende Gruppe")
				s.oidcFail(w, r, "Kein Zugriff: Das Konto ist keiner Orbit-Gruppe zugeordnet.")
				return
			}
			u, _, lookup := s.Store.userByName(name)
			if lookup != nil {
				u = user{ID: randomID(), Username: name, Role: role}
				// Random local password: SSO users sign in through the provider only.
				if _, e := s.Store.DB.Exec("INSERT INTO users(id,username,password,role,disabled,totp,created) VALUES(?,?,?,?,0,'',?)", u.ID, name, must(PasswordHash(randomToken(32), "")), role, now()); e != nil {
					panic(e)
				}
				_ = s.Store.AuditAs(name, "user.created_sso", role)
			} else if u.Disabled {
				s.oidcFail(w, r, "Dieses Konto ist in Orbit deaktiviert.")
				return
			} else if u.Role != role {
				if _, e := s.Store.DB.Exec("UPDATE users SET role=? WHERE id=?", role, u.ID); e != nil {
					panic(e)
				}
			}
			if _, e := s.newSession(w, u.ID); e != nil {
				panic(e)
			}
			_ = s.Store.AuditAs(name, "auth.login_sso", "client="+s.clientIP(r))
			// The session cookie is SameSite=Strict: a redirect chain started by the
			// provider would not send it. Continue from a same-origin page instead.
			sameOriginContinue(w, "/")
		default:
			panic(problem("Nicht gefunden.", 404))
		}
	})
	return true, err
}

func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, message string) {
	sameOriginContinue(w, "/#sso-error="+url.QueryEscape(message))
}

func sameOriginContinue(w http.ResponseWriter, target string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	escaped := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace(target)
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=`+escaped+`"><title>Orbit</title><p style="font-family:sans-serif">Weiter zu <a href="`+escaped+`">Orbit</a> …</p>`)
}
