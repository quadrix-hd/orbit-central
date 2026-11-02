"""Verified local mirror of an operator-owned marketplace; never execute downloads."""
import hashlib,json,os,re,shutil,ssl,tempfile,threading,time
from pathlib import Path
from urllib import request,parse

class CatalogError(Exception):pass

class Catalog:
    def __init__(self,root,url='',limit=20*1024**3):
        self.root=Path(root)/'catalog';self.url=url;self.limit=limit;self.lock=threading.Lock();self.busy=False
    @staticmethod
    def origin(url):
        p=parse.urlsplit(url)
        if p.scheme!='https' or not p.hostname or p.username or p.password or p.fragment or p.query:raise CatalogError('Katalog benötigt eine HTTPS-Adresse ohne Zugangsdaten, Query oder Fragment.')
        return p.scheme,p.hostname,p.port or 443
    def fetch(self,url):
        if self.origin(url)!=self.origin(self.url):raise CatalogError('Paketquelle liegt außerhalb der konfigurierten Katalog-Domain.')
        class NoRedirect(request.HTTPRedirectHandler):
            def redirect_request(self,*args,**kwargs):return None
        return request.build_opener(request.HTTPSHandler(context=ssl.create_default_context()),NoRedirect()).open(request.Request(url,headers={'Accept':'application/json, application/octet-stream','User-Agent':'Orbit-Catalog/1'}),timeout=30)
    def status(self):
        data={'configured':bool(self.url),'busy':self.busy,'revision':None,'packages':[],'updated':None,'error':None}
        for name in ('snapshot.json','status.json'):
            try:data.update(json.loads((self.root/name).read_text()))
            except FileNotFoundError:pass
            except (ValueError,OSError):data['error']='Lokaler Katalogstatus ist nicht lesbar.'
        data['busy']=self.busy;data['configured']=bool(self.url)
        return data
    def atomic(self,name,value):
        self.root.mkdir(parents=True,exist_ok=True,mode=0o700)
        with tempfile.NamedTemporaryFile('w',dir=self.root,delete=False) as f:
            json.dump(value,f,ensure_ascii=False);f.flush();os.fsync(f.fileno());temp=f.name
        try:os.replace(temp,self.root/name)
        finally:Path(temp).unlink(missing_ok=True)
    def validate(self,d):
        if not isinstance(d,dict) or d.get('schema')!=1 or not isinstance(d.get('revision'),str) or not 0<len(d['revision'])<=100:raise CatalogError('Nicht unterstütztes Katalogformat.')
        packages=d.get('packages')
        if not isinstance(packages,list) or len(packages)>500:raise CatalogError('Ungültige Paketliste (maximal 500).')
        ids=set();assets={};result=[]
        for p in packages:
            if not isinstance(p,dict):raise CatalogError('Ungültiges Paket.')
            q={}
            for key,maximum in [('id',80),('name',100),('version',80),('description',2000),('category',60)]:
                value=p.get(key)
                if not isinstance(value,str) or not value or len(value)>maximum:raise CatalogError('Ungültiges Paketfeld: '+key)
                q[key]=value
            if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_.-]*',q['id']) or q['id'] in ids:raise CatalogError('Ungültige oder doppelte Paket-ID.')
            ids.add(q['id']);q['assets']=[]
            files=p.get('assets')
            if not isinstance(files,list) or not 1<=len(files)<=20:raise CatalogError('Jedes Paket benötigt 1 bis 20 Dateien.')
            for a in files:
                if not isinstance(a,dict):raise CatalogError('Ungültige Paketdatei.')
                sha=a.get('sha256');size=a.get('size');name=a.get('name');url=a.get('url')
                if not isinstance(sha,str) or not re.fullmatch('[a-f0-9]{64}',sha) or type(size)!=int or not 0<size<=self.limit:raise CatalogError('Ungültige Dateigröße oder SHA-256-Prüfsumme.')
                if not isinstance(name,str) or not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,159}',name):raise CatalogError('Ungültiger Dateiname.')
                if not isinstance(url,str) or len(url)>2000:raise CatalogError('Ungültige Downloadadresse.')
                url=parse.urljoin(self.url,url)
                if self.origin(url)!=self.origin(self.url):raise CatalogError('Paketdateien müssen von derselben HTTPS-Domain kommen.')
                item={'sha256':sha,'size':size,'name':name,'url':url}
                if sha in assets and assets[sha]['size']!=size:raise CatalogError('Widersprüchliche Dateigrößen.')
                assets[sha]=item;q['assets'].append({k:item[k] for k in ('sha256','size','name')})
            result.append(q)
        if sum(a['size'] for a in assets.values())>self.limit:raise CatalogError('Katalog überschreitet das lokale Downloadlimit.')
        return {'schema':1,'revision':d['revision'],'packages':result},assets
    @staticmethod
    def valid_file(path,item):
        if not path.is_file() or path.stat().st_size!=item['size']:return False
        h=hashlib.sha256()
        with path.open('rb') as f:
            for chunk in iter(lambda:f.read(1024*1024),b''):h.update(chunk)
        return h.hexdigest()==item['sha256']
    def sync(self):
        if not self.lock.acquire(False):return False
        self.busy=True
        try:
            if not self.url:raise CatalogError('Zentrale Katalogquelle ist noch nicht konfiguriert.')
            self.origin(self.url)
            with self.fetch(self.url) as response:raw=response.read(2*1024**2+1)
            if len(raw)>2*1024**2:raise CatalogError('Katalogdatei ist größer als 2 MiB.')
            snapshot,assets=self.validate(json.loads(raw))
            objects=self.root/'objects';objects.mkdir(parents=True,exist_ok=True,mode=0o700)
            missing=[a for a in assets.values() if not self.valid_file(objects/a['sha256'],a)]
            needed=sum(a['size'] for a in missing)
            if shutil.disk_usage(self.root).free<needed+256*1024**2:raise CatalogError('Nicht genügend freier Speicher für den lokalen Marketplace.')
            with tempfile.TemporaryDirectory(prefix='.sync-',dir=self.root) as stage:
                for a in missing:
                    h=hashlib.sha256();count=0;dest=Path(stage)/a['sha256']
                    with self.fetch(a['url']) as response,dest.open('wb') as out:
                        while True:
                            chunk=response.read(1024*1024)
                            if not chunk:break
                            count+=len(chunk)
                            if count>a['size']:raise CatalogError('Paketdatei ist größer als im Katalog angegeben.')
                            h.update(chunk);out.write(chunk)
                        out.flush();os.fsync(out.fileno())
                    if count!=a['size'] or h.hexdigest()!=a['sha256']:raise CatalogError('Größe oder SHA-256 einer Paketdatei stimmt nicht. Bisheriger Katalog bleibt aktiv.')
                for a in missing:os.replace(Path(stage)/a['sha256'],objects/a['sha256'])
            snapshot['updated']=time.time();self.atomic('snapshot.json',snapshot);self.atomic('status.json',{'error':None,'checked':time.time()})
            return True
        except Exception as exc:
            # No upstream body, URL query, credential or raw exception is persisted.
            message=str(exc) if isinstance(exc,CatalogError) else 'Katalog konnte nicht aktualisiert werden. Quelle, HTTPS und Dateiformat prüfen. Der bisherige lokale Stand bleibt aktiv.'
            self.atomic('status.json',{'error':message,'checked':time.time()});return False
        finally:self.busy=False;self.lock.release()
    def start(self):
        if self.busy:return
        threading.Thread(target=self.sync,daemon=True,name='catalog-sync').start()
    def artifact(self,sha):
        if not re.fullmatch('[a-f0-9]{64}',sha):return None
        for p in self.status()['packages']:
            for a in p['assets']:
                if a['sha256']==sha:
                    path=self.root/'objects'/sha
                    if self.valid_file(path,a):return path,a['name']
        return None
