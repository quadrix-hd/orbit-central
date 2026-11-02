"""Validate a trusted backup completely before touching stopped Orbit data."""
import os, shutil, sqlite3, sys, tarfile, tempfile
from pathlib import Path
from contextlib import closing
from cryptography.fernet import Fernet

ALLOWED={'orbit.db','master.key','setup-token'}

def restore(source,root,clear_maintenance=True):
    root=Path(root).resolve()
    root.mkdir(mode=0o700,parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.restore-',dir=root) as tmp:
        stage=Path(tmp)
        with tarfile.open(fileobj=source,mode='r:gz') as tar:
            members=tar.getmembers();names=[m.name for m in members]
            if len(set(names))!=len(names) or not {'orbit.db','master.key'}<=set(names):raise ValueError('Unvollständiges oder doppeltes Archiv.')
            if any(m.name not in ALLOWED or not m.isfile() or m.size>(1024**3 if m.name=='orbit.db' else 256) for m in members):raise ValueError('Ungültige Archivmitglieder.')
            for m in members:
                with tar.extractfile(m) as src,open(stage/m.name,'wb') as dst:shutil.copyfileobj(src,dst)
                os.chmod(stage/m.name,0o600)
        cipher=Fernet((stage/'master.key').read_bytes())
        uri=(stage/'orbit.db').as_uri()+'?mode=ro&immutable=1'
        with closing(sqlite3.connect(uri,uri=True)) as c:
            if c.execute('PRAGMA quick_check').fetchone()[0]!='ok':raise ValueError('Datenbankprüfung fehlgeschlagen.')
            if c.execute("SELECT value FROM settings WHERE key='schema'").fetchone()[0]!='1':raise ValueError('Nicht unterstütztes Backup-Schema.')
            for token, in c.execute('SELECT token FROM connections'):cipher.decrypt(token.encode())
            for table in ('jobs','blueprints','sessions','audit'):c.execute('SELECT * FROM '+table+' LIMIT 1').fetchall()
        # Old browser sessions must not become valid again after a restore.
        with closing(sqlite3.connect(stage/'orbit.db')) as c:
            c.execute('DELETE FROM sessions');c.commit();c.execute('PRAGMA wal_checkpoint(TRUNCATE)')
        for name in ('orbit.db-wal','orbit.db-shm'):(root/name).unlink(missing_ok=True)
        # All archive bytes, SQLite and encryption keys have passed validation.
        # Service must remain stopped until all replacements are complete.
        for name in ('master.key','orbit.db','setup-token'):
            if (stage/name).exists():os.replace(stage/name,root/name)
            elif name=='setup-token':(root/name).unlink(missing_ok=True)
        if clear_maintenance:(root/'maintenance').unlink(missing_ok=True)

def main():
    root=os.environ.get('ORBIT_DATA_DIR','/data')
    try:
        if len(sys.argv)>1:
            with open(sys.argv[1],'rb') as f:restore(f,root,clear_maintenance='--keep-maintenance' not in sys.argv)
        else:restore(sys.stdin.buffer,root)
    except Exception:
        print('Backup ungültig oder Restore fehlgeschlagen. Dienst gestoppt lassen; vorherigen Datenstand behalten und prüfen.',file=sys.stderr)
        sys.exit(1)
    print('Backup geprüft und wiederhergestellt. Alte Sitzungen wurden verworfen.')

if __name__=='__main__':main()
