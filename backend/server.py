"""Orbit: local Proxmox control plane. Python 3.12+, single process."""
from __future__ import annotations
import traceback
import hashlib, hmac, http.cookies, json, logging, math, os, re, secrets, signal, sqlite3, ssl, threading, time, uuid
from contextlib import contextmanager
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from urllib import request, parse, error
from cryptography.fernet import Fernet

VERSION = '0.7.1'
DATA = Path(os.environ.get('ORBIT_DATA_DIR', './data')).resolve()
STATIC = Path(__file__).resolve().parents[1] / 'dist'
DB_LOCK = threading.RLock()
STOP = threading.Event()
WAKE = threading.Event()
FERNET = None
PUBLIC_ORIGIN = os.environ.get('ORBIT_PUBLIC_ORIGIN', 'http://localhost:8080').rstrip('/')
COOKIE_SECURE = PUBLIC_ORIGIN.startswith('https://')
SESSIONS_TTL = 12 * 3600
RATE_LOCK = threading.Lock()
ATTEMPTS = {}

try:
    from backend.catalog import Catalog
    from backend.control import ControlPlane
    from backend.console import ConsoleGateway
except ModuleNotFoundError:
    from catalog import Catalog
    from control import ControlPlane
    from console import ConsoleGateway

class Problem(Exception):
    def __init__(self, message, status=400, uncertain=False):
        self.message, self.status, self.uncertain = message, status, uncertain

@contextmanager
def db():
    with DB_LOCK:
        conn = sqlite3.connect(DATA / 'orbit.db', timeout=20)
        conn.row_factory = sqlite3.Row
        try:
            yield conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
        finally: conn.close()

def initialize():
    global FERNET
    DATA.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(DATA, 0o700)
    keyfile = DATA / 'master.key'
    if not keyfile.exists():
        fd = os.open(keyfile, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, 'wb') as f: f.write(Fernet.generate_key())
    FERNET = Fernet(keyfile.read_bytes())
    with db() as c:
        c.executescript('''
        PRAGMA journal_mode=WAL;
        CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, csrf TEXT NOT NULL, expires REAL NOT NULL);
        CREATE TABLE IF NOT EXISTS connections(id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, token TEXT NOT NULL, created REAL NOT NULL);
        CREATE TABLE IF NOT EXISTS blueprints(id TEXT PRIMARY KEY, data TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, request_key TEXT UNIQUE NOT NULL, data TEXT NOT NULL, created REAL NOT NULL);
        CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY, request_key TEXT UNIQUE NOT NULL, data TEXT NOT NULL, created REAL NOT NULL);
        CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, at REAL NOT NULL, action TEXT NOT NULL, details TEXT NOT NULL);
        INSERT OR IGNORE INTO settings VALUES('schema','1');
        ''')
        if c.execute("SELECT value FROM settings WHERE key='schema'").fetchone()[0] != '1':
            raise RuntimeError('Unsupported database schema; restore matching application version')
        if 'kind' not in {r[1] for r in c.execute('PRAGMA table_info(connections)')}:c.execute("ALTER TABLE connections ADD COLUMN kind TEXT NOT NULL DEFAULT 'pve'")
        configured = bool(c.execute("SELECT value FROM settings WHERE key='password'").fetchone())
        if 'endpoints' not in {r[1] for r in c.execute('PRAGMA table_info(connections)')}:c.execute("ALTER TABLE connections ADD COLUMN endpoints TEXT NOT NULL DEFAULT '[]'")
        for row in c.execute('SELECT token FROM connections'):FERNET.decrypt(row[0].encode())
    os.chmod(DATA / 'orbit.db', 0o600)
    setup = DATA / 'setup-token'
    if not configured and not setup.exists():
        fd = os.open(setup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as f: f.write(secrets.token_urlsafe(32))

def audit(action, details=''):
    with db() as c: c.execute('INSERT INTO audit(at,action,details) VALUES(?,?,?)',(time.time(),action,details))

def password_hash(password, salt=None):
    salt = salt or secrets.token_hex(16)
    return salt + ':' + hashlib.scrypt(password.encode(),salt=bytes.fromhex(salt),n=16384,r=8,p=1).hex()

def validate_password(p):
    if not isinstance(p,str) or not 12 <= len(p) <= 256: raise Problem('Passwort muss 12–256 Zeichen enthalten.')

def text_field(data, key, pattern=None, maxlen=200):
    value = data.get(key)
    if not isinstance(value,str) or not value.strip() or len(value)>maxlen: raise Problem('Ungültiges Feld: '+key)
    value = value.strip()
    if pattern and not re.fullmatch(pattern,value): raise Problem('Ungültiges Feld: '+key)
    return value

def integer(data,key,lo,hi,default=None):
    n = data.get(key,default)
    if isinstance(n,bool) or not isinstance(n,int) or not lo<=n<=hi: raise Problem('Ungültiger Wert: '+key)
    return n

def limited_login(addr):
    with RATE_LOCK:
        now=time.time()
        for k in list(ATTEMPTS):
            ATTEMPTS[k]=[t for t in ATTEMPTS[k] if now-t<300]
            if not ATTEMPTS[k]: del ATTEMPTS[k]
        recent=ATTEMPTS.setdefault(addr,[])
        if len(recent)>=10: raise Problem('Zu viele Anmeldeversuche. Bitte fünf Minuten warten.',429)
        recent.append(now)

def connection(cid):
    with db() as c: row=c.execute('SELECT * FROM connections WHERE id=?',(cid,)).fetchone()
    if not row: raise Problem('Verbindung nicht gefunden.',404)
    return dict(row)

ENDPOINT_LOCK = threading.RLock()
PREFERRED_ENDPOINT = {}

def endpoint_urls(primary, extra):
    if isinstance(extra,str):extra=[v.strip() for v in extra.splitlines() if v.strip()]
    if not isinstance(extra,list) or len(extra)>7:raise Problem('Maximal sieben zusätzliche Cluster-Adressen angeben.')
    result=[]
    for value in [primary]+extra:
        if not isinstance(value,str):raise Problem('Ungültige Cluster-Adresse.')
        value=value.rstrip('/')
        try:
            u=parse.urlsplit(value)
            if len(value)>300 or u.scheme!='https' or not u.hostname or u.username or u.password or u.query or u.fragment or u.path or any(ch.isspace() for ch in value) or u.port==0:raise ValueError()
        except ValueError:raise Problem('Gültige HTTPS-Adresse ohne Pfad erwartet.')
        if value not in result:result.append(value)
    return result

def configured_endpoints(conn):
    extra=conn.get('endpoints','[]')
    if isinstance(extra,str):
        try:extra=json.loads(extra)
        except ValueError:raise Problem('Gespeicherte Cluster-Adressen sind beschädigt.',500)
    return endpoint_urls(conn['url'],extra)

def verify_endpoints(conn):
    urls=configured_endpoints(conn)
    if len(urls)==1:return
    if conn.get('kind')=='pbs':raise Problem('Alternative Endpunkte sind nur für einen VE-Cluster möglich.')
    expected=None
    for url in urls:
        rows=pve_once(dict(conn,url=url),'GET','/cluster/status')
        cluster_name=next((r.get('name') for r in rows if r.get('type')=='cluster'),None)
        members=tuple(sorted((str(r.get('id','')),str(r.get('name',''))) for r in rows if r.get('type')=='node'))
        cluster=(cluster_name,members) if cluster_name and members else None
        if not cluster:raise Problem('Alternative Endpunkte benötigen einen bestehenden VE-Cluster.')
        if expected is None:expected=cluster
        elif cluster!=expected:raise Problem('Die Endpunkte gehören nicht zum selben Cluster.')

def pve(conn, method, path, fields=None):
    urls=configured_endpoints(conn)
    key=conn.get('id',conn['url'])
    with ENDPOINT_LOCK:preferred=PREFERRED_ENDPOINT.get(key)
    if preferred in urls:urls.remove(preferred);urls.insert(0,preferred)
    # Select a reachable API before writing. A dispatched write is never retried.
    if method!='GET' and len(urls)>1:
        pve(conn,'GET','/version')
        with ENDPOINT_LOCK:target=PREFERRED_ENDPOINT.get(key,urls[0])
        conn['active_endpoint']=target
        return pve_once(dict(conn,url=target),method,path,fields)
    for index,url in enumerate(urls):
        try:
            result=pve_once(dict(conn,url=url),method,path,fields)
            with ENDPOINT_LOCK:PREFERRED_ENDPOINT[key]=url
            return result
        except Problem as exc:
            if method!='GET' or not exc.uncertain or index==len(urls)-1:raise

def pve_once(conn, method, path, fields=None):
    token=FERNET.decrypt(conn['token'].encode()).decode()
    body = parse.urlencode(fields or {}).encode() if method in ('POST','PUT') else None
    try:ctx=ssl.create_default_context(cafile=os.environ.get('ORBIT_CA_FILE') or None)
    except (OSError,ssl.SSLError):raise Problem('CA-Datei fehlt oder ist ungültig. ORBIT_CA_FILE und Zertifikats-Mount prüfen.',502)
    target=conn['url']+'/api2/json'+path
    if method in ('GET','DELETE') and fields:target+='?'+parse.urlencode(fields)
    req=request.Request(target,data=body,method=method,
        headers={'Authorization':('PBSAPIToken='+token.replace('=',':',1) if conn.get('kind')=='pbs' else 'PVEAPIToken='+token),'Content-Type':'application/x-www-form-urlencoded','Accept':'application/json'})
    try:
        # Proxmox credentials must never follow redirects to another host.
        class NoRedirect(request.HTTPRedirectHandler):
            def redirect_request(self,*args,**kwargs): return None
        with request.build_opener(request.HTTPSHandler(context=ctx),NoRedirect()).open(req,timeout=8 if method=='GET' else 25) as r:
            return json.load(r)['data']
    except error.HTTPError as e:
        details=''
        try:
            payload=json.loads(e.read(65536));fields=payload.get('errors',{})
            names=[k for k in fields if isinstance(k,str) and re.fullmatch(r'[a-zA-Z0-9_.-]{1,60}',k)] if isinstance(fields,dict) else []
            if names:details=' Betroffene Felder: '+', '.join(sorted(names)[:12])+'.'
            # Extract only privilege identifiers, never arbitrary upstream bodies.
            required=sorted(set(re.findall(r'\b(?:VM|Sys|Datastore|SDN|Pool|Permissions|User|Group)\.[a-zA-Z]+\b',str(payload))))[:12]
            if required:details+=' Benötigte Rechte: '+', '.join(required)+'.'
        except (ValueError,TypeError,OSError):pass
        finally:e.close()
        logging.warning('upstream=proxmox method=%s path=%s status=%s%s',method,path,e.code,details)
        if e.code in (401,403): raise Problem('Proxmox verweigert den Zugriff. API-Token und Berechtigungen prüfen.'+details,502)
        raise Problem('Proxmox meldet HTTP '+str(e.code)+'. Aufgabe und Proxmox-Protokoll prüfen.'+details,502,uncertain=e.code>=500)
    except (error.URLError, TimeoutError, OSError) as exc:
        reason=getattr(exc,'reason',exc)
        if isinstance(reason,ssl.SSLCertVerificationError):message='TLS-Zertifikat nicht vertrauenswürdig oder Hostname passt nicht. CA-Mount, ORBIT_CA_FILE und verwendeten Hostnamen prüfen.'
        elif isinstance(reason,TimeoutError):message='Zeitüberschreitung beim Proxmox-Zugriff. Erreichbarkeit und Firewall prüfen.'
        elif isinstance(reason,ConnectionRefusedError):message='Proxmox lehnt die Verbindung ab. HTTPS-Adresse, Port und Dienst prüfen.'
        else:message='Proxmox nicht erreichbar oder TLS-Verbindung fehlgeschlagen. Adresse, DNS, Firewall und CA prüfen.'
        raise Problem(message,502,uncertain=True)
    except (ValueError,KeyError,TypeError): raise Problem('Unerwartete Proxmox-Antwort.',502,uncertain=True)

def pathpart(s): return parse.quote(str(s),safe='')

def save_job(job):
    with db() as c: c.execute('UPDATE jobs SET data=? WHERE id=?',(json.dumps(job),job['id']))

def log_job(job, msg):
    job.setdefault('events',[]).append({'at':time.time(),'message':msg})
    save_job(job)

def wait_task(conn, node, upid):
    deadline=time.monotonic()+1800
    while not STOP.is_set():
        result=pve(conn,'GET','/nodes/'+pathpart(node)+'/tasks/'+pathpart(upid)+'/status')
        if result.get('status')=='stopped':
            if result.get('exitstatus')!='OK': raise Problem('Proxmox-Aufgabe fehlgeschlagen: '+str(result.get('exitstatus','unbekannt')))
            return
        if time.monotonic()>deadline: raise Problem('Aufgabe nach 30 Minuten noch aktiv. Zustand in Proxmox prüfen; Orbit löscht die VM nicht.',uncertain=True)
        STOP.wait(1)
    raise InterruptedError()

def disk_gib(config, device):
    spec=str(config.get(device,''))
    match=re.search(r'(?:^|,)size=(\d+(?:\.\d+)?)([KMGT])(?:,|$)',spec)
    if not match:raise Problem('Größe des Systemlaufwerks konnte nicht ermittelt werden.')
    return float(match[1])*{'K':1/1024**2,'M':1/1024,'G':1,'T':1024}[match[2]]

def check_template(config, device, target_size):
    if not isinstance(config,dict) or not config.get('template'):raise Problem('Die Quelle ist keine Proxmox-Vorlage mehr.')
    if len([k for k in config if re.fullmatch(r'net\d+',k)])>1:raise Problem('Vorlage benötigt maximal eine Netzwerkkarte.')
    if not any('cloudinit' in str(v) for k,v in config.items() if re.fullmatch(r'(ide|scsi|sata)\d+',k)):raise Problem('Vorlage benötigt ein Cloud-init-Laufwerk.')
    minimum=math.ceil(disk_gib(config,device))
    if target_size<minimum:raise Problem('Speicher darf nicht kleiner als das aktuelle Vorlagenlaufwerk sein ('+str(minimum)+' GiB).')
    return minimum

def task_id(value):
    if not isinstance(value,str) or not value.startswith('UPID:'):raise Problem('Proxmox hat keine gültige Aufgaben-ID geliefert. VMID manuell prüfen.',502,uncertain=True)
    return value

def process_job(job):
    conn=connection(job['connection_id']); node=job['node']; base='/nodes/'+pathpart(node)+'/qemu/'+str(job['vmid'])
    # A crash inside a non-atomic external submission cannot be retried safely.
    if job['stage'] in ('clone_submitting','start_submitting'):
        job['status']='needs_attention'; log_job(job,'Unklarer Zustand nach Neustart. VM und Proxmox-Aufgabe manuell prüfen; keine automatische Wiederholung.'); return
    try:
        job['status']='running';save_job(job)
        if job['stage']=='queued':
            fresh=pve(conn,'GET','/nodes/'+pathpart(job['source_node'])+'/qemu/'+str(job['template_vmid'])+'/config')
            check_template(fresh,job['disk_device'],job['disk'])
            job['stage']='clone_submitting'; log_job(job,'Vorlage wird vollständig geklont.')
            job['upid']=pve(conn,'POST','/nodes/'+pathpart(job['source_node'])+'/qemu/'+str(job['template_vmid'])+'/clone',{
                'newid':job['vmid'],'name':job['name'],'full':1,'target':node,'storage':job['storage']})
            job['upid']=task_id(job['upid'])
            job['stage']='clone_wait';save_job(job)
        if job['stage']=='clone_wait':
            wait_task(conn,job['source_node'],job['upid']);job['stage']='configure';log_job(job,'Klon erstellt. Cloud-init und Ressourcen werden gesetzt.')
        if job['stage']=='configure':
            config=pve(conn,'GET',base+'/config')
            if not any('cloudinit' in str(v) for k,v in config.items() if re.fullmatch(r'(ide|scsi|sata)\d+',k)):
                raise Problem('Vorlage hat kein Cloud-init-Laufwerk. VM bleibt gestoppt; Vorlage korrigieren.')
            if config.get('name')!=job['name']:raise Problem('VM-Identität stimmt nicht mit der Aufgabe überein. Keine Konfiguration geändert.',uncertain=True)
            current_size=disk_gib(config,job['disk_device'])
            if job['disk']<current_size:raise Problem('Das Systemlaufwerk ist inzwischen größer als die gewünschte Zielgröße. Keine Verkleinerung ausgeführt.')
            fields={'cores':job['cpu'],'sockets':1,'memory':job['ram']*1024,'ciuser':job['username'],'ipconfig0':'ip=dhcp',
                    'net0':'virtio,bridge='+job['bridge'],'onboot':0,'tags':';'.join(sorted(set(filter(None,str(config.get('tags','')).split(';')))|{'orbit'}))}
            if job.get('ssh'):fields['sshkeys']=parse.quote(job['ssh'],safe='')
            pve(conn,'PUT',base+'/config',fields)
            disk=job['disk_device']
            if job['disk']>current_size:pve(conn,'PUT',base+'/resize',{'disk':disk,'size':str(job['disk'])+'G'})
            job['stage']='start' if job['start'] else 'done';log_job(job,'Konfiguration abgeschlossen.')
        if job['stage']=='start':
            job['stage']='start_submitting';save_job(job)
            job['upid']=task_id(pve(conn,'POST',base+'/status/start'))
            job['stage']='start_wait';log_job(job,'VM wird gestartet.')
        if job['stage']=='start_wait':
            wait_task(conn,node,job['upid']);job['stage']='done'
        job['status']='completed';log_job(job,'Bereitstellung erfolgreich. Software stammt aus der verknüpften Vorlage.')
        audit('deployment.completed',job['id'])
    except InterruptedError: return
    except Exception as e:
        job['status']='needs_attention' if isinstance(e,Problem) and e.uncertain and job['stage'] in ('clone_submitting','clone_wait','start_submitting','start_wait') else 'failed'
        # Do not expose upstream data or credentials through exception text.
        msg=e.message if isinstance(e,Problem) else 'Interner Fehler. Serverprotokoll prüfen.'
        log_job(job,msg+' Bereits erstellte VMs bleiben zur Prüfung erhalten.')
        audit('deployment.failed',job['id'])

def worker():
    while not STOP.is_set():
        with db() as c: rows=c.execute('SELECT data FROM jobs ORDER BY created').fetchall()
        for row in rows:
            job=json.loads(row[0])
            if job['status'] in ('queued','running') and not STOP.is_set() and not (DATA/'maintenance').exists():
                try:process_job(job)
                except Exception:
                    job['status']='needs_attention';log_job(job,'Worker konnte den Vorgang nicht fortsetzen. Verbindung und Serverstatus prüfen.')
        WAKE.wait(2); WAKE.clear()

def blueprint_validate(d):
    result={k:text_field(d,k,maxlen=n) for k,n in [('name',80),('description',500),('connection_id',64),('node',64),('storage',64),('bridge',64),('username',32)]}
    result['category']=text_field(d,'category',maxlen=40)
    if result['category'] not in ('Betriebssystem','Container','Datenbanken','Anwendungen'):raise Problem('Ungültige Kategorie.')
    for k in ('node','storage','bridge','username'):
        if not re.fullmatch(r'[a-zA-Z0-9_.:-]+',result[k]):raise Problem('Ungültiges Feld: '+k)
    result.update({k:integer(d,k,lo,hi) for k,lo,hi in [('template_vmid',100,999999999),('cpu',1,128),('ram',1,1024),('disk',8,65536)]})
    result['disk_device']=text_field(d,'disk_device',r'(scsi|virtio|sata)\d+',16)
    conn=connection(result['connection_id'])
    if conn.get('kind')=='pbs':raise Problem('Blueprint benötigt eine Proxmox-VE-Verbindung.')
    config=pve(conn,'GET','/nodes/'+pathpart(result['node'])+'/qemu/'+str(result['template_vmid'])+'/config')
    result['min_disk']=check_template(config,result['disk_device'],result['disk'])
    return result

CONTROL = ControlPlane(globals())
CONSOLE = ConsoleGateway(globals())
CATALOG = Catalog(DATA,os.environ.get('ORBIT_CATALOG_URL',''))

class Handler(BaseHTTPRequestHandler):
    server_version='Orbit'
    def setup(self):
        super().setup();self.connection.settimeout(30)
    def handle_one_request(self):
        self.request_id=uuid.uuid4().hex[:12];self.started=time.monotonic()
        return super().handle_one_request()
    def log_message(self,fmt,*args):pass
    def log_request(self,code='-',size='-'):
        path=re.sub(r'[^a-zA-Z0-9_./:-]','?',getattr(self,'path','').split('?')[0])[:300]
        duration=int((time.monotonic()-getattr(self,'started',time.monotonic()))*1000)
        logging.info('request_id=%s method=%s path=%s status=%s duration_ms=%s',getattr(self,'request_id','unknown'),'HEAD' if getattr(self,'head_only',False) else getattr(self,'command','unknown'),path,code,duration)
    def send_json(self,status,data,cookie=None):
        if status>=400 and isinstance(data,dict):data={**data,'request_id':getattr(self,'request_id','unknown')}
        raw=json.dumps(data,ensure_ascii=False).encode()
        self.send_response(status);self.security_headers();self.send_header('Content-Type','application/json; charset=utf-8');self.send_header('Content-Length',str(len(raw)));self.send_header('Cache-Control','no-store')
        if cookie:self.send_header('Set-Cookie',cookie)
        self.end_headers();self.wfile.write(b'' if getattr(self,'head_only',False) else raw)
    def security_headers(self):
        self.send_header('X-Request-ID',getattr(self,'request_id','unknown'))
        self.send_header('X-Content-Type-Options','nosniff');self.send_header('Referrer-Policy','no-referrer');self.send_header('X-Frame-Options','DENY')
        self.send_header('Content-Security-Policy',"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
    def read_body(self):
        try: n=int(self.headers.get('Content-Length','0'))
        except ValueError:raise Problem('Ungültige Anfrage.')
        if n<0 or n>65536:raise Problem('Anfrage zu groß.',413)
        if self.headers.get('Content-Type','').split(';')[0]!='application/json':raise Problem('JSON erwartet.',415)
        try:data=json.loads(self.rfile.read(n))
        except (ValueError,UnicodeDecodeError):raise Problem('Ungültiges JSON.')
        if not isinstance(data,dict):raise Problem('JSON-Objekt erwartet.')
        return data
    def session(self):
        jar=http.cookies.SimpleCookie()
        try:jar.load(self.headers.get('Cookie',''))
        except http.cookies.CookieError: return None
        sid=jar.get('orbit_session')
        if not sid:return None
        hashed=hashlib.sha256(sid.value.encode()).hexdigest()
        with db() as c:
            c.execute('DELETE FROM sessions WHERE expires<?',(time.time(),))
            row=c.execute('SELECT * FROM sessions WHERE id=?',(hashed,)).fetchone()
        return dict(row) if row else None
    def new_session(self):
        token=secrets.token_urlsafe(32);csrf=secrets.token_urlsafe(32)
        with db() as c:c.execute('INSERT INTO sessions VALUES(?,?,?)',(hashlib.sha256(token.encode()).hexdigest(),csrf,time.time()+SESSIONS_TTL))
        cookie='orbit_session='+token+'; HttpOnly; SameSite=Strict; Path=/; Max-Age='+str(SESSIONS_TTL)+('; Secure' if COOKIE_SECURE else '')
        return csrf,cookie
    def do_HEAD(self):
        self.head_only=True;self.command='GET'
        try:self.dispatch()
        finally:self.head_only=False;self.command='HEAD'
    def do_GET(self): self.dispatch()
    def do_POST(self): self.dispatch()
    def do_DELETE(self): self.dispatch()
    def dispatch(self):
        try:
            path=parse.urlsplit(self.path).path
            if not path.startswith('/api/'):
                if self.command!='GET':raise Problem('Nicht erlaubt.',405)
                if path.startswith('/vendor/novnc/'):
                    root=Path(os.environ.get('ORBIT_NOVNC_ROOT','/usr/share/novnc')).resolve()
                    file=(root/parse.unquote(path[len('/vendor/novnc/'):])).resolve()
                    if not file.is_relative_to(root) or not file.is_file() or file.suffix not in ('.js','.css','.png','.svg','.woff','.woff2'):raise Problem('Nicht gefunden.',404)
                    raw=file.read_bytes();self.send_response(200);self.security_headers();self.send_header('Content-Type',{'.js':'text/javascript','.css':'text/css','.png':'image/png','.svg':'image/svg+xml','.woff':'font/woff','.woff2':'font/woff2'}[file.suffix]);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(b'' if getattr(self,'head_only',False) else raw);return
                file=STATIC / (path.lstrip('/') or 'index.html')
                if file.name not in ('index.html','app.js','app.css','vm-workspace.js','bootstrap.js','orbit-sans-regular.woff','orbit-sans-bold.woff') or file.parent!=STATIC or not file.is_file():raise Problem('Nicht gefunden.',404)
                raw=file.read_bytes()
                if file.name=='index.html':raw=raw.replace(b'name="orbit-mode" content="demo"',b'name="orbit-mode" content="live"')
                self.send_response(200);self.security_headers();self.send_header('Content-Type',{'html':'text/html; charset=utf-8','js':'text/javascript; charset=utf-8','css':'text/css; charset=utf-8','woff':'font/woff'}[file.suffix[1:]]);self.send_header('Content-Length',str(len(raw)));self.send_header('Cache-Control','no-cache');self.end_headers();self.wfile.write(b'' if getattr(self,'head_only',False) else raw);return
            if self.command in ('POST','DELETE'):
                origin=self.headers.get('Origin')
                if origin and origin!=PUBLIC_ORIGIN:raise Problem('Fremder Ursprung nicht erlaubt.',403)
            data=self.read_body() if self.command=='POST' else {}
            if path=='/api/health' and self.command=='GET':
                with db() as c:c.execute("SELECT value FROM settings WHERE key='schema'").fetchone()
                return self.send_json(200,{'ok':True,'version':VERSION,'mode':'live'})
            if path=='/api/auth' and self.command=='GET':
                session=self.session()
                with db() as c: configured=bool(c.execute("SELECT 1 FROM settings WHERE key='password'").fetchone())
                return self.send_json(200,{'configured':configured,'authenticated':bool(session),'csrf':session['csrf'] if session else None,'version':VERSION})
            if path=='/api/setup' and self.command=='POST':
                limited_login(self.client_address[0]);validate_password(data.get('password'))
                with db() as c:
                    if c.execute("SELECT 1 FROM settings WHERE key='password'").fetchone():raise Problem('Bereits eingerichtet.',409)
                    tokenfile=DATA/'setup-token'
                    if not tokenfile.exists() or not hmac.compare_digest(str(data.get('token','')).encode(),tokenfile.read_text().strip().encode()):raise Problem('Einrichtungscode ungültig.',403)
                    c.execute('INSERT INTO settings VALUES(?,?)',('password',password_hash(data['password'])))
                tokenfile.unlink(missing_ok=True);csrf,cookie=self.new_session();audit('setup.completed');return self.send_json(200,{'csrf':csrf},cookie)
            if path=='/api/login' and self.command=='POST':
                limited_login(self.client_address[0]);p=data.get('password','')
                if not isinstance(p,str) or len(p)>256:raise Problem('Anmeldung fehlgeschlagen.',401)
                with db() as c: row=c.execute("SELECT value FROM settings WHERE key='password'").fetchone()
                saved=row[0] if row else password_hash('unconfigured')
                candidate=password_hash(p,saved.split(':')[0])
                if not row or not hmac.compare_digest(saved,candidate):raise Problem('Anmeldung fehlgeschlagen.',401)
                csrf,cookie=self.new_session();audit('auth.login');return self.send_json(200,{'csrf':csrf},cookie)
            if self.command in ('POST','DELETE') and (DATA/'maintenance').exists():raise Problem('Wartung läuft. Bitte später erneut versuchen.',503)
            session=self.session()
            if not session:raise Problem('Bitte anmelden.',401)
            if self.command in ('POST','DELETE') and not hmac.compare_digest(self.headers.get('X-Orbit-CSRF','').encode(),session['csrf'].encode()):raise Problem('Sitzung ungültig. Bitte neu anmelden.',403)
            match=re.fullmatch(r'/api/consoles/([a-zA-Z0-9_-]+)',path)
            if match and self.command=='GET':CONSOLE.proxy(self,match[1],session);return
            if path=='/api/consoles' and self.command=='POST':
                cid=text_field(data,'connection_id',r'[a-zA-Z0-9-]+',64);node=text_field(data,'node',r'[a-zA-Z0-9_.-]+',64);vmid=integer(data,'vmid',100,999999999)
                return self.send_json(201,CONSOLE.create(cid,vmid,node,session))
            if path=='/api/catalog' and self.command=='GET':return self.send_json(200,CATALOG.status())
            if path=='/api/catalog/sync' and self.command=='POST':
                if not CATALOG.url:raise Problem('Zentrale Katalogquelle ist noch nicht konfiguriert.')
                CATALOG.start();return self.send_json(202,{'ok':True})
            if path.startswith('/api/catalog/assets/') and self.command=='GET':
                artifact=CATALOG.artifact(path.rsplit('/',1)[-1])
                if not artifact:raise Problem('Lokale Paketdatei nicht verfügbar.',404)
                file,name=artifact
                self.send_response(200);self.security_headers();self.send_header('Content-Type','application/octet-stream');self.send_header('Content-Disposition','attachment; filename="'+name+'"');self.send_header('Content-Length',str(file.stat().st_size));self.end_headers()
                if getattr(self,'head_only',False):return
                with file.open('rb') as source:
                    while True:
                        chunk=source.read(1024*1024)
                        if not chunk:break
                        self.wfile.write(chunk)
                return
            if CONTROL.route(self,path,data):return
            if path=='/api/logout' and self.command=='POST':
                with db() as c:c.execute('DELETE FROM sessions WHERE id=?',(session['id'],))
                return self.send_json(200,{'ok':True},'orbit_session=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0'+('; Secure' if COOKIE_SECURE else ''))
            if path=='/api/connections':
                if self.command=='GET':
                    with db() as c:rows=[dict(r) for r in c.execute('SELECT id,name,url,created,kind,endpoints FROM connections')]
                    for row in rows:
                        row['endpoints']=json.loads(row['endpoints'])
                        with ENDPOINT_LOCK:row['active_endpoint']=PREFERRED_ENDPOINT.get(row['id'],row['url'])
                    return self.send_json(200,rows)
                if self.command=='POST':
                    name=text_field(data,'name',maxlen=80);url=text_field(data,'url',maxlen=300).rstrip('/')
                    try:
                        u=parse.urlsplit(url);port=u.port
                        if u.scheme!='https' or not u.hostname or u.username or u.password or u.query or u.fragment or u.path or any(ch.isspace() for ch in url) or port==0:raise ValueError()
                    except ValueError:raise Problem('Gültige HTTPS-Adresse ohne Pfad erwartet, z. B. https://pve.example:8006.')
                    kind=data.get('kind','pve')
                    if kind not in ('pve','pbs'):raise Problem('Verbindungstyp muss pve oder pbs sein.')
                    token=text_field(data,'token',r'[^\s=]+![^\s=]+=[^\s]+',500)
                    cid=uuid.uuid4().hex;encrypted=FERNET.encrypt(token.encode()).decode();conn={'id':cid,'name':name,'url':url,'token':encrypted,'kind':kind}
                    conn['endpoints']=json.dumps(endpoint_urls(url,data.get('endpoints',[]))[1:]);verify_endpoints(conn)
                    version=pve(conn,'GET','/version')
                    with db() as c:
                        if c.execute('SELECT 1 FROM connections WHERE url=?',(url,)).fetchone():raise Problem('Adresse bereits verbunden.',409)
                        c.execute('INSERT INTO connections(id,name,url,token,created,kind,endpoints) VALUES(?,?,?,?,?,?,?)',(cid,name,url,encrypted,time.time(),kind,conn['endpoints']))
                    audit('connection.created',cid);return self.send_json(201,{'id':cid,'name':name,'url':url,'version':version.get('version'),'kind':kind})
            if path.startswith('/api/connections/'):
                parts=path.split('/');conn=connection(parts[3])
                if len(parts)==4 and self.command=='POST':
                    name=text_field(data,'name',maxlen=80)
                    token=data.get('token','')
                    if not isinstance(token,str):raise Problem('Ungültiges Token-Format.')
                    updated=dict(conn,name=name,url=endpoint_urls(data.get('url',conn['url']),[])[0])
                    if token:
                        if not isinstance(token,str) or len(token)>500 or not re.fullmatch(r'[^\s=]+![^\s=]+=[^\s]+',token):raise Problem('Ungültiges Token-Format.')
                        updated['token']=FERNET.encrypt(token.encode()).decode()
                    updated['endpoints']=json.dumps(endpoint_urls(updated['url'],data.get('endpoints',json.loads(conn.get('endpoints','[]'))))[1:]);verify_endpoints(updated)
                    pve(updated,'GET','/version')
                    with db() as c:
                        if c.execute('SELECT 1 FROM connections WHERE url=? AND id!=?',(updated['url'],conn['id'])).fetchone():raise Problem('Adresse bereits verbunden.',409)
                        c.execute('UPDATE connections SET name=?,url=?,token=?,endpoints=? WHERE id=?',(name,updated['url'],updated['token'],updated['endpoints'],conn['id']))
                    audit('connection.updated',conn['id']);return self.send_json(200,{'ok':True})
                if len(parts)==5 and self.command=='GET':
                    if conn.get('kind')=='pbs' and parts[4] in ('templates','targets'):raise Problem('Diese Funktion benötigt Proxmox VE.')
                    if parts[4]=='inventory':
                        if conn.get('kind')=='pbs':raise Problem('PBS hat kein PVE-Inventar.')
                        return self.send_json(200,pve(conn,'GET','/cluster/resources'))
                    if parts[4]=='templates':return self.send_json(200,[r for r in pve(conn,'GET','/cluster/resources',{'type':'vm'}) if r.get('template') and r.get('type')=='qemu'])
                    if parts[4]=='targets':
                        result=[]
                        for n in pve(conn,'GET','/nodes'):
                            if n.get('status')!='online':continue
                            node=n['node'];storage=pve(conn,'GET','/nodes/'+pathpart(node)+'/storage');net=pve(conn,'GET','/nodes/'+pathpart(node)+'/network')
                            result.append({'node':node,'storages':[s['storage'] for s in storage if s.get('active') and 'images' in s.get('content','')],'bridges':[n['iface'] for n in net if n.get('type')=='bridge']})
                        return self.send_json(200,result)
            if path=='/api/blueprints':
                if self.command=='GET':
                    with db() as c:rows=[json.loads(r[0]) for r in c.execute('SELECT data FROM blueprints')]
                    return self.send_json(200,rows)
                if self.command=='POST':
                    bp=blueprint_validate(data);bp['id']=uuid.uuid4().hex
                    with db() as c:c.execute('INSERT INTO blueprints VALUES(?,?)',(bp['id'],json.dumps(bp)))
                    audit('blueprint.created',bp['id']);return self.send_json(201,bp)
            if path.startswith('/api/blueprints/') and self.command=='DELETE':
                bid=path.split('/')[-1]
                with db() as c:c.execute('DELETE FROM blueprints WHERE id=?',(bid,))
                audit('blueprint.deleted',bid);return self.send_json(200,{'ok':True})
            if path=='/api/jobs':
                if self.command=='POST' and (DATA/'maintenance').exists():raise Problem('Update läuft. Bitte nach Abschluss erneut versuchen.',503)
                if self.command=='GET':
                    with db() as c:rows=[json.loads(r[0]) for r in c.execute('SELECT data FROM jobs ORDER BY created DESC LIMIT 200')]
                    for j in rows:j.pop('ssh',None)
                    return self.send_json(200,rows)
                if self.command=='POST':
                    key=text_field(data,'request_key',r'[a-zA-Z0-9-]{16,80}',80)
                    with db() as c:
                        existing=c.execute('SELECT data FROM jobs WHERE request_key=?',(key,)).fetchone()
                        if existing:
                            old=json.loads(existing[0]);old.pop('ssh',None);return self.send_json(200,old)
                        bid=text_field(data,'blueprint_id',r'[a-zA-Z0-9-]+',64)
                        row=c.execute('SELECT data FROM blueprints WHERE id=?',(bid,)).fetchone()
                    if not row:raise Problem('Blueprint nicht gefunden.',404)
                    bp=json.loads(row[0]);name=text_field(data,'name',r'[a-zA-Z][a-zA-Z0-9-]{0,62}',63)
                    cpu=integer(data,'cpu',1,128);ram=integer(data,'ram',1,1024);disk=integer(data,'disk',bp['min_disk'],65536)
                    if not isinstance(data.get('start'),bool):raise Problem('Startoption fehlt.')
                    ssh=data.get('ssh','')
                    if not isinstance(ssh,str):raise Problem('Ungültiger öffentlicher SSH-Schlüssel.')
                    ssh=ssh.strip()
                    if len(ssh)>16384 or (ssh and not all(re.fullmatch(r'(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp\d+) [A-Za-z0-9+/=]+(?: [^\r\n]+)?',x) for x in ssh.splitlines())):raise Problem('Ungültiger öffentlicher SSH-Schlüssel.')
                    conn=connection(bp['connection_id'])
                    fresh=pve(conn,'GET','/nodes/'+pathpart(bp['node'])+'/qemu/'+str(bp['template_vmid'])+'/config')
                    check_template(fresh,bp['disk_device'],disk)
                    resources=pve(conn,'GET','/cluster/resources')
                    node_path='/nodes/'+pathpart(bp['node'])
                    storages=pve(conn,'GET',node_path+'/storage')
                    bridges=pve(conn,'GET',node_path+'/network')
                    if not any(s.get('storage')==bp['storage'] and s.get('active') and 'images' in s.get('content','') for s in storages):raise Problem('Ziel-Storage nicht verfügbar oder nicht für VM-Images geeignet.')
                    if not any(n.get('iface')==bp['bridge'] and n.get('type')=='bridge' for n in bridges):raise Problem('Netzwerk-Bridge nicht gefunden.')
                    if any(r.get('name')==name for r in resources):raise Problem('VM-Name wird im Cluster bereits verwendet.',409)
                    # Allocate while locked against other Orbit submissions. Proxmox itself arbitrates external races.
                    with db() as c:
                        existing=c.execute('SELECT data FROM jobs WHERE request_key=?',(key,)).fetchone()
                        if existing:
                            old=json.loads(existing[0]);old.pop('ssh',None);return self.send_json(200,old)
                        existing_jobs=[json.loads(r[0]) for r in c.execute('SELECT data FROM jobs')]
                        if any(j['connection_id']==bp['connection_id'] and j['name']==name and j['status'] in ('queued','running') for j in existing_jobs):raise Problem('Name wird bereits bereitgestellt.',409)
                        vmid=int(pve(conn,'GET','/cluster/nextid'));used={j['vmid'] for j in existing_jobs if j['connection_id']==bp['connection_id']}
                        used.update(json.loads(r[0])['vmid'] for r in c.execute('SELECT data FROM operations') if json.loads(r[0])['connection_id']==bp['connection_id'])
                        used.update(int(r['vmid']) for r in resources if r.get('vmid'))
                        while vmid in used:vmid+=1
                        job={'id':uuid.uuid4().hex,'name':name,'connection_id':bp['connection_id'],'blueprint':bp['name'],'template_vmid':bp['template_vmid'],'source_node':bp['node'],'node':bp['node'],'storage':bp['storage'],'bridge':bp['bridge'],'username':bp['username'],'disk_device':bp['disk_device'],'cpu':cpu,'ram':ram,'disk':disk,'ssh':ssh,'start':data['start'],'vmid':vmid,'status':'queued','stage':'queued','created':time.time(),'events':[{'at':time.time(),'message':'Bereitstellung eingeplant.'}]}
                        c.execute('INSERT INTO jobs VALUES(?,?,?,?)',(job['id'],key,json.dumps(job),job['created']))
                    audit('deployment.queued',job['id']);WAKE.set();result=dict(job);result.pop('ssh');return self.send_json(202,result)
            if path=='/api/audit' and self.command=='GET':
                with db() as c:rows=[dict(r) for r in c.execute('SELECT * FROM audit ORDER BY id DESC LIMIT 100')]
                return self.send_json(200,rows)
            raise Problem('Nicht gefunden.',404)
        except Problem as e:
            logging.warning('request_id=%s error=%s',getattr(self,'request_id','unknown'),e.message)
            self.send_json(e.status,{'error':e.message})
        except (BrokenPipeError,ConnectionResetError):pass
        except Exception as exc:
            frames=','.join(Path(f.filename).name+':'+str(f.lineno)+':'+f.name for f in traceback.extract_tb(exc.__traceback__))
            logging.error('request_id=%s internal_error=%s frames=%s',getattr(self,'request_id','unknown'),type(exc).__name__,frames);self.send_json(500,{'error':'Interner Fehler. Fehler-ID im Serverprotokoll prüfen.'})

def main():
    logging.basicConfig(level=logging.INFO,format='%(asctime)s %(message)s');initialize()
    server=ThreadingHTTPServer((os.environ.get('ORBIT_BIND','0.0.0.0'),int(os.environ.get('PORT','8080'))),Handler)
    server.daemon_threads=True
    if CATALOG.url:CATALOG.start()
    t=threading.Thread(target=worker,daemon=True);t.start()
    op_threads=[threading.Thread(target=CONTROL.worker,daemon=True) for _ in range(3)]
    for thread in op_threads:thread.start()
    def stop(*_):STOP.set();WAKE.set();threading.Thread(target=server.shutdown,daemon=True).start()
    signal.signal(signal.SIGTERM,stop);signal.signal(signal.SIGINT,stop)
    logging.info('Orbit %s ready. First-run code: read /data/setup-token inside the container.',VERSION)
    server.serve_forever();STOP.set();WAKE.set();t.join(timeout=28)
    for thread in op_threads:thread.join(timeout=28)
    server.server_close()

if __name__=='__main__':main()
