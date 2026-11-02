package orbit

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func LockData(dir string) (func(), error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, ".orbit.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("Orbit läuft bereits gegen diesen Datenbestand; für Restore/Reset zuerst stoppen")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func Backup(dir string) (name string, err error) {
	err = guarded(func() {
		if _, e := os.Stat(filepath.Join(dir, "orbit.db")); e != nil {
			panic(e)
		}
		if _, e := os.Stat(filepath.Join(dir, "master.key")); e != nil {
			panic(e)
		}
		out := filepath.Join(dir, "backups")
		if e := os.MkdirAll(out, 0700); e != nil {
			panic(e)
		}
		stage := must(os.MkdirTemp(dir, ".backup-"))
		defer os.RemoveAll(stage)
		db := must(sql.Open("sqlite", filepath.Join(dir, "orbit.db")))
		db.SetMaxOpenConns(1)
		defer db.Close()
		if _, e := db.Exec("PRAGMA busy_timeout=20000"); e != nil {
			panic(e)
		}
		if _, e := db.Exec("VACUUM INTO ?", filepath.Join(stage, "orbit.db")); e != nil {
			panic(e)
		}
		name = filepath.Join(out, "orbit-"+time.Now().UTC().Format("20060102-150405")+"-"+randomID()[:6]+".tar.gz")
		f := must(os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600))
		success := false
		defer func() {
			f.Close()
			if !success {
				os.Remove(name)
			}
		}()
		gz := gzip.NewWriter(f)
		tarw := tar.NewWriter(gz)
		for _, file := range []string{"orbit.db", "master.key", "setup-token", "catalog/preferences.json", "catalog/snapshot.json", "catalog/status.json"} {
			source := filepath.Join(dir, file)
			if file == "orbit.db" {
				source = filepath.Join(stage, file)
			}
			src, e := os.Open(source)
			if errors.Is(e, os.ErrNotExist) && file != "orbit.db" && file != "master.key" {
				continue
			}
			if e != nil {
				panic(e)
			}
			st := must(src.Stat())
			header := &tar.Header{Name: file, Mode: 0600, Size: st.Size(), ModTime: st.ModTime()}
			if e = tarw.WriteHeader(header); e == nil {
				_, e = io.Copy(tarw, src)
			}
			src.Close()
			if e != nil {
				panic(e)
			}
		}
		if e := tarw.Close(); e != nil {
			panic(e)
		}
		if e := gz.Close(); e != nil {
			panic(e)
		}
		if e := f.Sync(); e != nil {
			panic(e)
		}
		success = true
	})
	return
}
func Restore(source io.Reader, dir string, keepMaintenance bool) (err error) {
	err = guarded(func() {
		if e := os.MkdirAll(dir, 0700); e != nil {
			panic(e)
		}
		stage := must(os.MkdirTemp(dir, ".restore-"))
		defer os.RemoveAll(stage)
		gz := must(gzip.NewReader(source))
		defer gz.Close()
		reader := tar.NewReader(gz)
		seen := map[string]bool{}
		allowed := map[string]int64{"orbit.db": 1024 * 1024 * 1024, "master.key": 256, "setup-token": 256, "catalog/preferences.json": 2 * 1024 * 1024, "catalog/snapshot.json": 2 * 1024 * 1024, "catalog/status.json": 2 * 1024 * 1024}
		for {
			header, e := reader.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				panic(e)
			}
			limit, ok := allowed[header.Name]
			if !ok || seen[header.Name] || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size < 0 || header.Size > limit {
				fail("Ungültige oder doppelte Archivmitglieder.")
			}
			seen[header.Name] = true
			path := filepath.Join(stage, header.Name)
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				panic(e)
			}
			f := must(os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600))
			_, e = io.CopyN(f, reader, header.Size)
			closeErr := f.Close()
			if e != nil {
				panic(e)
			}
			if closeErr != nil {
				panic(closeErr)
			}
		}
		if !seen["orbit.db"] || !seen["master.key"] {
			fail("Unvollständiges Archiv.")
		}
		if _, e := io.Copy(io.Discard, gz); e != nil {
			panic(e)
		} // validate gzip trailer before replacement
		raw := must(os.ReadFile(filepath.Join(stage, "master.key")))
		key := must(base64.URLEncoding.DecodeString(strings.TrimSpace(string(raw))))
		if len(key) != 32 {
			fail("Ungültiger Schlüssel.")
		}
		cipher := &Store{key: key}
		db := must(sql.Open("sqlite", filepath.Join(stage, "orbit.db")))
		db.SetMaxOpenConns(1)
		valid := false
		defer func() {
			if !valid {
				db.Close()
			}
		}()
		var check, schema string
		if e := db.QueryRow("PRAGMA quick_check").Scan(&check); e != nil || check != "ok" {
			fail("Datenbankprüfung fehlgeschlagen.")
		}
		if e := db.QueryRow("SELECT value FROM settings WHERE key='schema'").Scan(&schema); e != nil || schema != "1" {
			fail("Nicht unterstütztes Backup-Schema.")
		}
		for _, table := range []string{"jobs", "blueprints", "sessions", "audit"} {
			rows := must(db.Query("SELECT * FROM " + table + " LIMIT 1"))
			rows.Close()
		}
		rows := must(db.Query("SELECT token FROM connections"))
		for rows.Next() {
			var token string
			if e := rows.Scan(&token); e != nil {
				rows.Close()
				panic(e)
			}
			if _, e := cipher.Decrypt(token); e != nil {
				rows.Close()
				panic(e)
			}
		}
		e := rows.Err()
		rows.Close()
		if e != nil {
			panic(e)
		}
		if _, e = db.Exec("DELETE FROM sessions; PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
			panic(e)
		}
		if e = db.Close(); e != nil {
			panic(e)
		}
		valid = true
		if e = os.WriteFile(filepath.Join(dir, "maintenance"), []byte("restore"), 0600); e != nil {
			panic(e)
		}
		for _, file := range []string{"orbit.db-wal", "orbit.db-shm"} {
			if e = os.Remove(filepath.Join(dir, file)); e != nil && !errors.Is(e, os.ErrNotExist) {
				panic(e)
			}
		}
		for _, file := range []string{"master.key", "orbit.db", "setup-token", "catalog/preferences.json", "catalog/snapshot.json", "catalog/status.json"} {
			path := filepath.Join(dir, file)
			if seen[file] {
				if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
					panic(e)
				}
				if e = os.Rename(filepath.Join(stage, file), path); e != nil {
					panic(e)
				}
			} else if file != "master.key" && file != "orbit.db" {
				if e = os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
					panic(e)
				}
			}
		}
		if !keepMaintenance {
			if e = os.Remove(filepath.Join(dir, "maintenance")); e != nil {
				panic(e)
			}
		}
	})
	return
}
func ResetAccess(dir string) error {
	return guarded(func() {
		db := must(sql.Open("sqlite", filepath.Join(dir, "orbit.db")))
		defer db.Close()
		if _, err := db.Exec("DELETE FROM settings WHERE key='password'; DELETE FROM sessions;"); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "setup-token"), []byte(randomToken(32)), 0600); err != nil {
			panic(err)
		}
	})
}
func Maintenance(dir string, on bool) error {
	if on {
		return os.WriteFile(filepath.Join(dir, "maintenance"), []byte("update"), 0600)
	}
	err := os.Remove(filepath.Join(dir, "maintenance"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func CLI(args []string, dir string) error {
	if len(args) == 0 {
		return fmt.Errorf("Befehl fehlt")
	}
	switch args[0] {
	case "export-preview":
		return ExportPreview()
	case "backup":
		name, e := Backup(dir)
		if e != nil {
			return e
		}
		fmt.Println(name)
		return nil
	case "maintenance":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("orbit maintenance on|off")
		}
		return Maintenance(dir, args[1] == "on")
	case "restore", "reset-access":
		unlock, e := LockData(dir)
		if e != nil {
			return e
		}
		defer unlock()
		if args[0] == "reset-access" {
			return ResetAccess(dir)
		}
		source := io.Reader(os.Stdin)
		var f *os.File
		keep := false
		for _, arg := range args[1:] {
			if arg == "--keep-maintenance" {
				keep = true
			} else {
				if f != nil {
					return errors.New("nur ein Archiv angeben")
				}
				f, e = os.Open(arg)
				if e != nil {
					return e
				}
				defer f.Close()
				source = f
			}
		}
		return Restore(source, dir, keep)
	}
	return fmt.Errorf("unbekannter Befehl: %s", args[0])
}
