import importlib.util, io, json, os, sqlite3, tarfile, tempfile, unittest
from pathlib import Path
from cryptography.fernet import Fernet
spec=importlib.util.spec_from_file_location('restore',Path(__file__).parents[1]/'scripts/restore.py')
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class RestoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.dest=self.root/'dest';self.dest.mkdir()
        (self.dest/'master.key').write_bytes(b'original key');(self.dest/'orbit.db').write_bytes(b'original database');(self.dest/'maintenance').touch()
        self.key=Fernet.generate_key();self.source=self.root/'source.db'
        c=sqlite3.connect(self.source);c.executescript("CREATE TABLE settings(key,value);INSERT INTO settings VALUES('schema','1');CREATE TABLE connections(token);CREATE TABLE jobs(data);CREATE TABLE blueprints(data);CREATE TABLE audit(id);CREATE TABLE sessions(id);INSERT INTO sessions VALUES('old-session');PRAGMA journal_mode=WAL;");c.execute('INSERT INTO connections VALUES(?)',(Fernet(self.key).encrypt(b'api-token').decode(),));c.commit();c.close()
    def tearDown(self):self.tmp.cleanup()
    def archive(self,change=None):
        files={'orbit.db':self.source.read_bytes(),'master.key':self.key}
        if change:files.update(change)
        out=io.BytesIO()
        with tarfile.open(fileobj=out,mode='w:gz') as tar:
            for name,raw in files.items():
                info=tarfile.TarInfo(name);info.size=len(raw);tar.addfile(info,io.BytesIO(raw))
        out.seek(0);return out
    def unchanged(self):
        self.assertEqual((self.dest/'master.key').read_bytes(),b'original key');self.assertEqual((self.dest/'orbit.db').read_bytes(),b'original database');self.assertTrue((self.dest/'maintenance').exists())
    def test_valid_restore_checks_key_and_clears_sessions(self):
        r.restore(self.archive(),self.dest)
        with sqlite3.connect(self.dest/'orbit.db') as c:
            self.assertEqual(c.execute('SELECT count(*) FROM sessions').fetchone()[0],0)
            self.assertEqual(Fernet((self.dest/'master.key').read_bytes()).decrypt(c.execute('SELECT token FROM connections').fetchone()[0].encode()),b'api-token')
        self.assertFalse((self.dest/'maintenance').exists());self.assertEqual((self.dest/'master.key').stat().st_mode&0o777,0o600)
    def test_invalid_database_does_not_overwrite(self):
        with self.assertRaises(Exception):r.restore(self.archive({'orbit.db':b'not sqlite'}),self.dest)
        self.unchanged()
    def test_wrong_encryption_key_does_not_overwrite(self):
        with self.assertRaises(Exception):r.restore(self.archive({'master.key':Fernet.generate_key()}),self.dest)
        self.unchanged()
    def test_path_traversal_rejected(self):
        with self.assertRaises(ValueError):r.restore(self.archive({'../escaped':b'bad'}),self.dest)
        self.unchanged();self.assertFalse((self.root/'escaped').exists())
    def test_missing_key_rejected(self):
        data=io.BytesIO()
        with tarfile.open(fileobj=data,mode='w:gz') as tar:
            raw=self.source.read_bytes();info=tarfile.TarInfo('orbit.db');info.size=len(raw);tar.addfile(info,io.BytesIO(raw))
        data.seek(0)
        with self.assertRaises(ValueError):r.restore(data,self.dest)
        self.unchanged()
    def test_duplicate_entries_rejected(self):
        data=self.archive();out=io.BytesIO()
        with tarfile.open(fileobj=data,mode='r:gz') as old,tarfile.open(fileobj=out,mode='w:gz') as new:
            for m in old.getmembers():new.addfile(m,old.extractfile(m))
            raw=self.key;info=tarfile.TarInfo('master.key');info.size=len(raw);new.addfile(info,io.BytesIO(raw))
        out.seek(0)
        with self.assertRaises(ValueError):r.restore(out,self.dest)
        self.unchanged()
    def test_update_restore_keeps_maintenance(self):
        r.restore(self.archive(),self.dest,clear_maintenance=False);self.assertTrue((self.dest/'maintenance').exists())

if __name__=='__main__':unittest.main()
