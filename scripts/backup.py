"""Consistent SQLite backup including key; execute inside container."""
import os, sqlite3, tarfile, tempfile, time, secrets
from pathlib import Path
root=Path(os.environ.get('ORBIT_DATA_DIR','/data')).resolve()
out=root/'backups';out.mkdir(mode=0o700,exist_ok=True)
name='orbit-'+time.strftime('%Y%m%d-%H%M%S')+'-'+secrets.token_hex(3)+'.tar.gz'
with tempfile.TemporaryDirectory() as tmp:
    temp=Path(tmp)
    source=sqlite3.connect(root/'orbit.db')
    target=sqlite3.connect(temp/'orbit.db')
    source.backup(target);target.close();source.close()
    with tarfile.open(out/name,'w:gz') as tar:
        tar.add(temp/'orbit.db',arcname='orbit.db')
        for file in ('master.key','setup-token'):
            if (root/file).is_file():tar.add(root/file,arcname=file)
    os.chmod(out/name,0o600)
print('/data/backups/'+name)
