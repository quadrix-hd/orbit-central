"""Offline access recovery. Stop the service before running this script."""
import os,secrets,sqlite3
from pathlib import Path
root=Path(os.environ.get('ORBIT_DATA_DIR','/data'))
with sqlite3.connect(root/'orbit.db') as c:
    c.execute("DELETE FROM settings WHERE key='password'")
    c.execute('DELETE FROM sessions')
target=root/'setup-token'
fd=os.open(target,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as f:f.write(secrets.token_urlsafe(32))
print('Zugriff zurückgesetzt. Dienst starten und /data/setup-token im Container lesen.')
