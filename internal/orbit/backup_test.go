package orbit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestorePreservesCredentialsAndClearsSessions(t *testing.T) {
	store := testStore(t)
	hash := must(PasswordHash("password-for-test", ""))
	if _, e := store.DB.Exec("INSERT INTO settings VALUES('password',?); INSERT INTO sessions VALUES('sid','csrf',9999999999)", hash); e != nil {
		t.Fatal(e)
	}
	token := must(store.Encrypt("user@pve!id=secret"))
	if e := store.SaveConnection(Connection{ID: "old", Name: "old", URL: "https://pve:8006", Token: token, Kind: "pve", Endpoints: []string{"https://node2:8006"}}, true); e != nil {
		t.Fatal(e)
	}
	name, e := Backup(store.Dir)
	if e != nil {
		t.Fatal(e)
	}
	target := t.TempDir()
	file := must(os.Open(name))
	defer file.Close()
	if e = Restore(file, target, false); e != nil {
		t.Fatal(e)
	}
	restored, e := OpenStore(target)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.DB.Close()
	c, e := restored.Connection("old")
	if e != nil {
		t.Fatal(e)
	}
	if got := must(restored.Decrypt(c.Token)); got != "user@pve!id=secret" || len(c.Endpoints) != 1 {
		t.Fatal("connection lost")
	}
	var n int
	if e = restored.DB.QueryRow("SELECT count(*) FROM sessions").Scan(&n); e != nil || n != 0 {
		t.Fatal("sessions restored")
	}
	var saved string
	_ = restored.DB.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&saved)
	if !CheckPassword("password-for-test", saved) {
		t.Fatal("password changed")
	}
}
func TestRestoreRejectsUnsafeArchivesBeforeChangingData(t *testing.T) {
	for _, member := range []string{"../escape", "master.key", "orbit.db"} {
		t.Run(member, func(t *testing.T) {
			store := testStore(t)
			before := must(os.ReadFile(filepath.Join(store.Dir, "master.key")))
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			for _, name := range []string{member, member} {
				_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 3})
				_, _ = tw.Write([]byte("bad"))
			}
			_ = tw.Close()
			_ = gz.Close()
			if e := Restore(bytes.NewReader(b.Bytes()), store.Dir, false); e == nil {
				t.Fatal("unsafe archive accepted")
			}
			after := must(os.ReadFile(filepath.Join(store.Dir, "master.key")))
			if !bytes.Equal(before, after) {
				t.Fatal("key changed")
			}
		})
	}
}
func TestOfflineLockAndResetAccess(t *testing.T) {
	store := testStore(t)
	unlock, e := LockData(store.Dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = LockData(store.Dir); e == nil {
		t.Fatal("second process lock accepted")
	}
	unlock()
	if _, e = store.DB.Exec("INSERT INTO settings VALUES('password','old'); INSERT INTO sessions VALUES('x','y',9999999999)"); e != nil {
		t.Fatal(e)
	}
	if e = ResetAccess(store.Dir); e != nil {
		t.Fatal(e)
	}
	if store.Configured() {
		t.Fatal("password retained")
	}
	if len(must(os.ReadFile(filepath.Join(store.Dir, "setup-token")))) < 32 {
		t.Fatal("no reset token")
	}
}
