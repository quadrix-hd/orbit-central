package orbit

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Users, roles, two-factor authentication and API tokens.
//
// Roles:
//   admin    – everything, including connections, users, system settings
//   operator – guest lifecycle (start/stop, snapshots, backups, migrations,
//              clones, consoles, marketplace deployments), alerts read-only
//   viewer   – read-only; may only manage its own password, 2FA and tokens

var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}
var usernamePattern = `[a-z][a-z0-9._-]{1,31}`

type user struct {
	ID, Username, Role string
	Disabled           bool
	TOTP               string // encrypted secret, empty when 2FA is off
}

type ctxKey string

const userKey ctxKey = "orbit-user"

func withUser(ctx context.Context, u *session) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// requestUser returns the username behind a request context ("" for workers).
func requestUser(ctx context.Context) string {
	if u, ok := ctx.Value(userKey).(*session); ok && u != nil {
		return u.Username
	}
	return ""
}

func (s *Store) userByName(name string) (user, string, error) {
	var u user
	var hash string
	var disabled int
	err := s.DB.QueryRow("SELECT id,username,role,disabled,totp,password FROM users WHERE username=?", name).Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.TOTP, &hash)
	u.Disabled = disabled == 1
	return u, hash, err
}

func (s *Store) userByID(id string) (user, string, error) {
	var u user
	var hash string
	var disabled int
	err := s.DB.QueryRow("SELECT id,username,role,disabled,totp,password FROM users WHERE id=?", id).Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.TOTP, &hash)
	u.Disabled = disabled == 1
	return u, hash, err
}

// migrateUsers turns the former single administrator password into the user
// "admin" and assigns existing sessions to it, so upgrades keep working.
func (s *Store) migrateUsers() error {
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		var hash string
		if err := s.DB.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&hash); err == nil {
			id := randomID()
			if _, err = s.DB.Exec("INSERT INTO users(id,username,password,role,disabled,totp,created) VALUES(?,?,?,?,0,'',?)", id, "admin", hash, "admin", now()); err != nil {
				return err
			}
			if _, err = s.DB.Exec("UPDATE sessions SET user_id=? WHERE user_id=''", id); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (s *Store) activeAdmins(exceptID string) int {
	var n int
	_ = s.DB.QueryRow("SELECT count(*) FROM users WHERE role='admin' AND disabled=0 AND id!=?", exceptID).Scan(&n)
	return n
}

// AuditAs records an audit entry attributed to a user.
func (s *Store) AuditAs(username, action, details string) error {
	_, err := s.DB.Exec("INSERT INTO audit(at,action,details,user) VALUES(?,?,?,?)", now(), action, details, username)
	return err
}

// --- TOTP (RFC 6238, SHA-1, 6 digits, 30 s) ---------------------------------

func newTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func totpCode(secret string, counter int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

// verifyTOTP accepts the current code and one step of clock drift either way.
func verifyTOTP(secret, code string, at time.Time) bool {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != 6 {
		return false
	}
	step := at.Unix() / 30
	for _, d := range []int64{-1, 0, 1} {
		if hmac.Equal([]byte(totpCode(secret, step+d)), []byte(code)) {
			return true
		}
	}
	return false
}

// --- API tokens ----------------------------------------------------------------

const tokenPrefix = "orb_"

func (s *Server) tokenSession(r *http.Request) (*session, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer "+tokenPrefix) {
		return nil, nil
	}
	secret := strings.TrimPrefix(header, "Bearer ")
	var id, userID, scope string
	var expires float64
	err := s.Store.DB.QueryRow("SELECT id,user_id,scope,expires FROM api_tokens WHERE hash=?", hashToken(secret)).Scan(&id, &userID, &scope, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, problem("API-Token ungültig.", 401)
	}
	if err != nil {
		return nil, err
	}
	if expires > 0 && expires < now() {
		return nil, problem("API-Token abgelaufen.", 401)
	}
	u, _, err := s.Store.userByID(userID)
	if err != nil || u.Disabled {
		return nil, problem("API-Token ungültig.", 401)
	}
	role := u.Role
	if scope == "read" {
		role = "viewer"
	}
	_, _ = s.Store.DB.Exec("UPDATE api_tokens SET last_used=? WHERE id=?", now(), id)
	return &session{ID: "token:" + id, UserID: u.ID, Username: u.Username, Role: role, Token: true}, nil
}

// --- Authorization ---------------------------------------------------------------

var adminOnlyPrefixes = []string{"/api/connections", "/api/settings", "/api/catalog/source", "/api/catalog/config", "/api/catalog/sync", "/api/system/", "/api/alerts/config", "/api/alerts/test", "/api/users", "/api/blueprints", "/api/audit"}
var selfServicePaths = []string{"/api/logout", "/api/account/password", "/api/account/sessions", "/api/account/totp", "/api/account/tokens"}
var adminOnlyActions = map[string]bool{"delete": true, "infrastructure": true, "storage_config": true, "template": true}

func hasPrefix(path string, list []string) bool {
	for _, p := range list {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") || (strings.HasSuffix(p, "/") && strings.HasPrefix(path, p)) {
			return true
		}
	}
	return false
}

// authorize enforces the role model on the server. The UI only hides what a
// role cannot do; this check is what actually prevents it.
func authorize(sess *session, method, path string, data doc) error {
	role := roleRank[sess.Role]
	if role == 0 {
		return problem("Keine Berechtigung.", 403)
	}
	if hasPrefix(path, selfServicePaths) {
		return nil
	}
	mutating := method != "GET"
	if role >= roleRank["admin"] {
		return nil
	}
	if hasPrefix(path, adminOnlyPrefixes) && (mutating || path == "/api/audit" || strings.HasPrefix(path, "/api/users")) {
		// Reading connections, settings, catalog and blueprints is fine;
		// changing them, the audit log and user management are admin-only.
		return problem("Nur für Administratoren.", 403)
	}
	if !mutating {
		return nil
	}
	if role < roleRank["operator"] {
		return problem("Nur-Lesen-Zugang: Änderungen sind nicht erlaubt.", 403)
	}
	if path == "/api/operations" && adminOnlyActions[str(data["action"])] {
		return problem("Diese Aktion ist Administratoren vorbehalten.", 403)
	}
	return nil
}

// --- Routes ------------------------------------------------------------------------

func (s *Server) userRoutes(w http.ResponseWriter, r *http.Request, d doc, current *session) (handled bool, err error) {
	path, method := r.URL.Path, r.Method
	if !strings.HasPrefix(path, "/api/users") && !strings.HasPrefix(path, "/api/account/totp") && !strings.HasPrefix(path, "/api/account/tokens") && path != "/api/me" {
		return false, nil
	}
	err = guarded(func() {
		switch {
		case path == "/api/me" && method == "GET":
			handled = true
			u, _, e := s.Store.userByID(current.UserID)
			if e != nil {
				panic(problem("Benutzer nicht gefunden.", 401))
			}
			writeJSON(w, 200, doc{"id": u.ID, "username": u.Username, "role": current.Role, "totp": u.TOTP != "", "token": current.Token})
		case path == "/api/users" && method == "GET":
			handled = true
			rows := must(s.Store.DB.Query("SELECT id,username,role,disabled,totp!='',created,(SELECT max(expires) FROM sessions WHERE user_id=users.id) FROM users ORDER BY username"))
			defer rows.Close()
			out := []doc{}
			for rows.Next() {
				var id, name, role string
				var disabled int
				var totp bool
				var created float64
				var last sql.NullFloat64
				if e := rows.Scan(&id, &name, &role, &disabled, &totp, &created, &last); e != nil {
					panic(e)
				}
				out = append(out, doc{"id": id, "username": name, "role": role, "disabled": disabled == 1, "totp": totp, "created": created, "session_expires": last.Float64, "self": id == current.UserID})
			}
			writeJSON(w, 200, out)
		case path == "/api/users" && method == "POST":
			handled = true
			name := strings.ToLower(field(d, "username", 32, usernamePattern))
			role := field(d, "role", 10, "admin|operator|viewer")
			password := str(d["password"])
			if l := len([]rune(password)); l < 12 || l > 256 {
				fail("Passwort muss 12–256 Zeichen enthalten.")
			}
			if _, _, e := s.Store.userByName(name); e == nil {
				panic(problem("Benutzername bereits vergeben.", 409))
			}
			id := randomID()
			if _, e := s.Store.DB.Exec("INSERT INTO users(id,username,password,role,disabled,totp,created) VALUES(?,?,?,?,0,'',?)", id, name, must(PasswordHash(password, "")), role, now()); e != nil {
				panic(e)
			}
			_ = s.Store.AuditAs(current.Username, "user.created", name+" ("+role+")")
			writeJSON(w, 201, doc{"id": id, "username": name, "role": role})
		case strings.HasPrefix(path, "/api/users/") && method == "POST":
			handled = true
			parts := strings.Split(strings.TrimPrefix(path, "/api/users/"), "/")
			s.mu.Lock()
			defer s.mu.Unlock()
			u, _, e := s.Store.userByID(parts[0])
			if e != nil {
				panic(problem("Benutzer nicht gefunden.", 404))
			}
			if len(parts) == 2 && parts[1] == "delete" {
				if u.ID == current.UserID {
					fail("Das eigene Konto kann nicht gelöscht werden.")
				}
				if d["confirm_name"] != u.Username {
					fail("Benutzernamen zur Bestätigung eingeben.")
				}
				if u.Role == "admin" && s.Store.activeAdmins(u.ID) == 0 {
					fail("Der letzte Administrator kann nicht gelöscht werden.")
				}
				tx := must(s.Store.DB.Begin())
				defer tx.Rollback()
				for _, q := range []string{"DELETE FROM sessions WHERE user_id=?", "DELETE FROM api_tokens WHERE user_id=?", "DELETE FROM users WHERE id=?"} {
					if _, e := tx.Exec(q, u.ID); e != nil {
						panic(e)
					}
				}
				must(0, tx.Commit())
				_ = s.Store.AuditAs(current.Username, "user.deleted", u.Username)
				writeJSON(w, 200, doc{"ok": true})
				return
			}
			if len(parts) != 1 {
				panic(problem("Nicht gefunden.", 404))
			}
			role := field(d, "role", 10, "admin|operator|viewer")
			disabled := boolean(d, "disabled")
			if u.ID == current.UserID && (disabled || role != u.Role) {
				fail("Eigene Rolle und eigenen Status bitte nicht ändern.")
			}
			if u.Role == "admin" && (role != "admin" || disabled) && s.Store.activeAdmins(u.ID) == 0 {
				fail("Mindestens ein aktiver Administrator muss bleiben.")
			}
			tx := must(s.Store.DB.Begin())
			defer tx.Rollback()
			if _, e := tx.Exec("UPDATE users SET role=?,disabled=? WHERE id=?", role, boolInt(disabled), u.ID); e != nil {
				panic(e)
			}
			if pw := str(d["password"]); pw != "" {
				if l := len([]rune(pw)); l < 12 || l > 256 {
					fail("Passwort muss 12–256 Zeichen enthalten.")
				}
				if _, e := tx.Exec("UPDATE users SET password=? WHERE id=?", must(PasswordHash(pw, "")), u.ID); e != nil {
					panic(e)
				}
			}
			if d["reset_totp"] == true {
				if _, e := tx.Exec("UPDATE users SET totp='' WHERE id=?", u.ID); e != nil {
					panic(e)
				}
			}
			if disabled || str(d["password"]) != "" || role != u.Role {
				// Changed credentials or rights end existing sessions and tokens keep
				// working only with the new role (they are re-evaluated per request).
				if _, e := tx.Exec("DELETE FROM sessions WHERE user_id=?", u.ID); e != nil {
					panic(e)
				}
			}
			must(0, tx.Commit())
			_ = s.Store.AuditAs(current.Username, "user.updated", fmt.Sprintf("%s role=%s disabled=%t", u.Username, role, disabled))
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/account/totp/setup" && method == "POST":
			handled = true
			u, _, e := s.Store.userByID(current.UserID)
			if e != nil || current.Token {
				panic(problem("Nur in einer Anmeldesitzung möglich.", 403))
			}
			secret := newTOTPSecret()
			if _, e := s.Store.DB.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "totp_pending:"+u.ID, must(s.Store.Encrypt(secret))); e != nil {
				panic(e)
			}
			issuer := "Orbit Central"
			uri := "otpauth://totp/" + url.PathEscape(issuer+":"+u.Username) + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
			writeJSON(w, 200, doc{"secret": secret, "uri": uri})
		case path == "/api/account/totp/enable" && method == "POST":
			handled = true
			var enc string
			if e := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key=?", "totp_pending:"+current.UserID).Scan(&enc); e != nil {
				fail("Zuerst einen neuen Schlüssel erzeugen.")
			}
			secret := must(s.Store.Decrypt(enc))
			if !verifyTOTP(secret, str(d["code"]), time.Now()) {
				fail("Code stimmt nicht. Uhrzeit des Geräts prüfen.")
			}
			if _, e := s.Store.DB.Exec("UPDATE users SET totp=? WHERE id=?", enc, current.UserID); e != nil {
				panic(e)
			}
			_, _ = s.Store.DB.Exec("DELETE FROM settings WHERE key=?", "totp_pending:"+current.UserID)
			_ = s.Store.AuditAs(current.Username, "account.totp.enabled", "")
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/account/totp/disable" && method == "POST":
			handled = true
			u, hash, e := s.Store.userByID(current.UserID)
			if e != nil || !CheckPassword(str(d["password"]), hash) {
				panic(problem("Passwort ist nicht korrekt.", 403))
			}
			if u.TOTP != "" && !verifyTOTP(must(s.Store.Decrypt(u.TOTP)), str(d["code"]), time.Now()) {
				fail("Aktuellen Code aus der App eingeben.")
			}
			if _, e := s.Store.DB.Exec("UPDATE users SET totp='' WHERE id=?", u.ID); e != nil {
				panic(e)
			}
			_ = s.Store.AuditAs(current.Username, "account.totp.disabled", "")
			writeJSON(w, 200, doc{"ok": true})
		case path == "/api/account/tokens" && method == "GET":
			handled = true
			rows := must(s.Store.DB.Query("SELECT id,name,scope,created,expires,last_used FROM api_tokens WHERE user_id=? ORDER BY created DESC", current.UserID))
			defer rows.Close()
			out := []doc{}
			for rows.Next() {
				var id, name, scope string
				var created, expires, last float64
				if e := rows.Scan(&id, &name, &scope, &created, &expires, &last); e != nil {
					panic(e)
				}
				out = append(out, doc{"id": id, "name": name, "scope": scope, "created": created, "expires": expires, "last_used": last})
			}
			writeJSON(w, 200, out)
		case path == "/api/account/tokens" && method == "POST":
			handled = true
			if current.Token {
				panic(problem("API-Tokens können nur in einer Anmeldesitzung erstellt werden.", 403))
			}
			name := field(d, "name", 60, "")
			scope := field(d, "scope", 5, "read|write")
			days := integer(d, "days", 0, 3650)
			var expires float64
			if days > 0 {
				expires = now() + float64(days)*86400
			}
			secret := tokenPrefix + randomToken(32)
			id := randomID()
			if _, e := s.Store.DB.Exec("INSERT INTO api_tokens(id,user_id,name,hash,scope,created,expires,last_used) VALUES(?,?,?,?,?,?,?,0)", id, current.UserID, name, hashToken(secret), scope, now(), expires); e != nil {
				panic(e)
			}
			_ = s.Store.AuditAs(current.Username, "account.token.created", name+" ("+scope+")")
			writeJSON(w, 201, doc{"id": id, "token": secret, "name": name, "scope": scope, "expires": expires})
		case strings.HasPrefix(path, "/api/account/tokens/") && method == "DELETE":
			handled = true
			id := strings.TrimPrefix(path, "/api/account/tokens/")
			res := must(s.Store.DB.Exec("DELETE FROM api_tokens WHERE id=? AND user_id=?", id, current.UserID))
			if n, _ := res.RowsAffected(); n == 0 {
				panic(problem("Token nicht gefunden.", 404))
			}
			_ = s.Store.AuditAs(current.Username, "account.token.revoked", id)
			writeJSON(w, 200, doc{"ok": true})
		}
	})
	return
}
