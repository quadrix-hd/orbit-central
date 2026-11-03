// Package orbit implements the Go control plane. Its on-disk format is shared
// with the Python service so that a migration does not discard credentials.
package orbit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB  *sql.DB
	Dir string
	key []byte
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "master.key")
	raw, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, dbErr := os.Stat(filepath.Join(dir, "orbit.db")); dbErr == nil {
			return nil, errors.New("master.key fehlt bei vorhandener Datenbank; vollständige Sicherung wiederherstellen")
		} else if !errors.Is(dbErr, os.ErrNotExist) {
			return nil, dbErr
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		raw = []byte(base64.URLEncoding.EncodeToString(key))
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(raw)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	}
	key, err := base64.URLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid master.key")
	}
	// One connection serializes SQLite writes, including transactions and setup.
	db, err := sql.Open("sqlite", filepath.Join(dir, "orbit.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Dir: dir, key: key}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	_, err = db.Exec(`PRAGMA busy_timeout=20000; PRAGMA journal_mode=WAL;
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,csrf TEXT NOT NULL,expires REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS connections(id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,token TEXT NOT NULL,created REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS blueprints(id TEXT PRIMARY KEY,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,request_key TEXT UNIQUE NOT NULL,data TEXT NOT NULL,created REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY,request_key TEXT UNIQUE NOT NULL,data TEXT NOT NULL,created REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY,at REAL NOT NULL,action TEXT NOT NULL,details TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS alerts(id TEXT PRIMARY KEY,data TEXT NOT NULL,updated REAL NOT NULL);
CREATE TABLE IF NOT EXISTS schedules(id TEXT PRIMARY KEY,data TEXT NOT NULL,updated REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,username TEXT UNIQUE NOT NULL,password TEXT NOT NULL,role TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0,totp TEXT NOT NULL DEFAULT '',created REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS api_tokens(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,name TEXT NOT NULL,hash TEXT UNIQUE NOT NULL,scope TEXT NOT NULL,created REAL NOT NULL,expires REAL NOT NULL,last_used REAL NOT NULL);
 INSERT OR IGNORE INTO settings VALUES('schema','1');`)
	if err != nil {
		return nil, err
	}
	var schema string
	if err = db.QueryRow("SELECT value FROM settings WHERE key='schema'").Scan(&schema); err != nil {
		return nil, err
	}
	if schema != "1" {
		return nil, errors.New("unsupported database schema")
	}
	columns := map[string]bool{}
	rows, err := db.Query("PRAGMA table_info(connections)")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&id, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, migration := range []struct{ name, ddl string }{{"kind", "ALTER TABLE connections ADD COLUMN kind TEXT NOT NULL DEFAULT 'pve'"}, {"endpoints", "ALTER TABLE connections ADD COLUMN endpoints TEXT NOT NULL DEFAULT '[]'"}} {
		if !columns[migration.name] {
			if _, err = db.Exec(migration.ddl); err != nil {
				return nil, err
			}
		}
	}
	for _, table := range []struct{ table, column, ddl string }{{"sessions", "user_id", "ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT ''"}, {"audit", "user", "ALTER TABLE audit ADD COLUMN user TEXT NOT NULL DEFAULT ''"}} {
		var present int
		if err = db.QueryRow("SELECT count(*) FROM pragma_table_info(?) WHERE name=?", table.table, table.column).Scan(&present); err != nil {
			return nil, err
		}
		if present == 0 {
			if _, err = db.Exec(table.ddl); err != nil {
				return nil, err
			}
		}
	}
	if err = s.migrateUsers(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT token FROM connections")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var token string
		if err = rows.Scan(&token); err != nil {
			break
		}
		if _, err = s.Decrypt(token); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, errors.New("stored credentials cannot be decrypted with master.key")
	}
	if err = os.Chmod(filepath.Join(dir, "orbit.db"), 0600); err != nil {
		return nil, err
	}
	if !s.Configured() {
		tokenPath := filepath.Join(dir, "setup-token")
		f, e := os.OpenFile(tokenPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			_, err = f.WriteString(randomToken(32))
			ce := f.Close()
			if err != nil {
				return nil, err
			}
			if ce != nil {
				return nil, ce
			}
		} else if !errors.Is(e, os.ErrExist) {
			return nil, e
		}
	}
	ok = true
	return s, nil
}

// Configured reports whether an administrator exists and no access reset is
// pending (reset-access re-opens the one-time setup for the admin account).
func (s *Store) Configured() bool {
	var n int
	if s.DB.QueryRow("SELECT 1 FROM settings WHERE key='reset_pending'").Scan(&n) == nil {
		return false
	}
	if s.DB.QueryRow("SELECT 1 FROM users WHERE role='admin' AND disabled=0 LIMIT 1").Scan(&n) == nil {
		return true
	}
	return s.DB.QueryRow("SELECT 1 FROM settings WHERE key='password'").Scan(&n) == nil
}
func (s *Store) Audit(action, details string) error {
	_, err := s.DB.Exec("INSERT INTO audit(at,action,details) VALUES(?,?,?)", float64(time.Now().UnixMilli())/1000, action, details)
	return err
}

// Fernet uses signing-key || encryption-key. Authentication precedes decryption.
func (s *Store) Encrypt(plain string) (string, error) {
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	p := append([]byte(plain), make([]byte, padding)...)
	for i := len(plain); i < len(p); i++ {
		p[i] = byte(padding)
	}
	b := make([]byte, 25+len(p))
	b[0] = 0x80
	binary.BigEndian.PutUint64(b[1:9], uint64(time.Now().Unix()))
	if _, err := rand.Read(b[9:25]); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.key[16:])
	if err != nil {
		return "", err
	}
	cipher.NewCBCEncrypter(block, b[9:25]).CryptBlocks(b[25:], p)
	mac := hmac.New(sha256.New, s.key[:16])
	mac.Write(b)
	return base64.URLEncoding.EncodeToString(append(b, mac.Sum(nil)...)), nil
}
func (s *Store) Decrypt(token string) (string, error) {
	invalid := errors.New("invalid encrypted credential")
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil || len(b) < 73 || b[0] != 0x80 || (len(b)-57)%16 != 0 {
		return "", invalid
	}
	payload := b[:len(b)-32]
	mac := hmac.New(sha256.New, s.key[:16])
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), b[len(b)-32:]) {
		return "", invalid
	}
	block, err := aes.NewCipher(s.key[16:])
	if err != nil {
		return "", invalid
	}
	p := make([]byte, len(payload)-25)
	cipher.NewCBCDecrypter(block, payload[9:25]).CryptBlocks(p, payload[25:])
	n := int(p[len(p)-1])
	if n < 1 || n > 16 || n > len(p) {
		return "", invalid
	}
	for _, v := range p[len(p)-n:] {
		if int(v) != n {
			return "", invalid
		}
	}
	return string(p[:len(p)-n]), nil
}

func PasswordHash(password, salt string) (string, error) {
	if salt == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		salt = hex.EncodeToString(b)
	}
	b, err := hex.DecodeString(salt)
	if err != nil || len(b) != 16 {
		return "", errors.New("invalid password salt")
	}
	derived, err := scrypt.Key([]byte(password), b, 16384, 8, 1, 64)
	if err != nil {
		return "", err
	}
	return salt + ":" + hex.EncodeToString(derived), nil
}
func CheckPassword(password, saved string) bool {
	salt, _, ok := strings.Cut(saved, ":")
	if !ok {
		return false
	}
	candidate, err := PasswordHash(password, salt)
	return err == nil && hmac.Equal([]byte(candidate), []byte(saved))
}

type Connection struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Token          string   `json:"-"`
	Created        float64  `json:"created"`
	Kind           string   `json:"kind"`
	Endpoints      []string `json:"endpoints"`
	ActiveEndpoint string   `json:"active_endpoint"`
}

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	c := Connection{}
	var endpoints string
	err := row.Scan(&c.ID, &c.Name, &c.URL, &c.Token, &c.Created, &c.Kind, &endpoints)
	if err == nil {
		err = json.Unmarshal([]byte(endpoints), &c.Endpoints)
	}
	if c.Endpoints == nil {
		c.Endpoints = []string{}
	}
	return c, err
}

const connectionSelect = "SELECT id,name,url,token,created,kind,endpoints FROM connections"

func (s *Store) Connection(id string) (Connection, error) {
	return scanConnection(s.DB.QueryRow(connectionSelect+" WHERE id=?", id))
}
func (s *Store) Connections() ([]Connection, error) {
	rows, err := s.DB.Query(connectionSelect + " ORDER BY created")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, e := scanConnection(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) SaveConnection(c Connection, create bool) error {
	extra, err := json.Marshal(c.Endpoints)
	if err != nil {
		return err
	}
	if create {
		_, err = s.DB.Exec("INSERT INTO connections(id,name,url,token,created,kind,endpoints) VALUES(?,?,?,?,?,?,?)", c.ID, c.Name, c.URL, c.Token, c.Created, c.Kind, string(extra))
	} else {
		_, err = s.DB.Exec("UPDATE connections SET name=?,url=?,token=?,endpoints=? WHERE id=?", c.Name, c.URL, c.Token, string(extra), c.ID)
	}
	return err
}
func (s *Store) Documents(table string) ([]map[string]any, error) {
	if table != "blueprints" && table != "jobs" && table != "operations" {
		return nil, fmt.Errorf("invalid document table")
	}
	query := "SELECT data FROM " + table
	if table != "blueprints" {
		query += " ORDER BY created DESC LIMIT 200"
	}
	rows, err := s.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var data map[string]any
		if err = json.Unmarshal([]byte(raw), &data); err != nil {
			return nil, err
		}
		delete(data, "ssh")
		delete(data, "token")
		out = append(out, data)
	}
	return out, rows.Err()
}
