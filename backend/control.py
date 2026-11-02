"""Proxmox/PBS monitoring and durable, allowlisted VM operations."""
import json
import re
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from urllib.parse import parse_qs, urlsplit

class Context:
    def __init__(self, values): self.values = values
    def __getattr__(self, key): return self.values[key]

class ControlPlane:
    ACTIONS = {'ha','create','restore','disk_add','disk_resize','nic','metadata','iso','rollback','snapshot_delete','delete','start', 'shutdown', 'reboot', 'snapshot', 'backup', 'migrate', 'configure'}
    def __init__(self, values):
        self.s = Context(values)
        self.active = set()
        try:from backend.workspace import Workspace
        except ModuleNotFoundError:from workspace import Workspace
        self.workspace=Workspace(self)

    def kind(self, conn): return conn.get('kind', 'pve')

    def safe_read(self, conn, path, fields=None):
        try:
            return {'ok': True, 'data': self.s.pve(conn, 'GET', path, fields)}
        except self.s.Problem as exc:
            return {'ok': False, 'error': exc.message}
        except Exception:
            return {'ok': False, 'error': 'Daten konnten nicht gelesen werden.'}

    def module(self, conn, section):
        s = self.s
        if self.kind(conn) == 'pbs':
            if section != 'protection': raise s.Problem('Dieses Modul benötigt Proxmox VE.')
            return {'datastores': self.safe_read(conn, '/status/datastore-usage'), 'pbs_tasks': self.safe_read(conn, '/nodes/localhost/tasks', {'limit': 30})}
        sections = {
            'protection': {'backup_jobs': '/cluster/backup', 'replication': '/cluster/replication', 'ha': '/cluster/ha/status/current'},
            'security': {'firewall_options': '/cluster/firewall/options', 'firewall_rules': '/cluster/firewall/rules', 'vnets': '/cluster/sdn/vnets'},
            'storage': {'storage_config': '/storage', 'storage_resources': '/cluster/resources'},
        }
        if section in sections:
            with ThreadPoolExecutor(max_workers=3) as pool:
                keys = list(sections[section])
                values = list(pool.map(lambda key: self.safe_read(conn, sections[section][key]), keys))
            result = dict(zip(keys, values))
            if section == 'storage' and result['storage_resources']['ok']:
                result['storage_resources']['data'] = [r for r in result['storage_resources']['data'] if r.get('type') == 'storage']
            return result
        if section in ('network', 'lifecycle'):
            nodes = s.pve(conn, 'GET', '/nodes')
            online = [n for n in nodes if n.get('status') == 'online']
            def read_node(node):
                name = node['node']; base = '/nodes/' + s.pathpart(name)
                if section == 'network': return {'node': name, 'interfaces': self.safe_read(conn, base + '/network')}
                return {'node': name, 'updates': self.safe_read(conn, base + '/apt/update'), 'versions': self.safe_read(conn, base + '/apt/versions')}
            with ThreadPoolExecutor(max_workers=4) as pool: result = list(pool.map(read_node, online))
            return {'nodes': result}
        raise s.Problem('Unbekanntes Modul.', 404)

    def vm(self, conn, vmid, node=None):
        s = self.s
        if self.kind(conn) != 'pve': raise s.Problem('VM-Verwaltung benötigt Proxmox VE.')
        resources = s.pve(conn, 'GET', '/cluster/resources', {'type': 'vm'})
        vm = next((r for r in resources if r.get('type') in ('qemu','lxc') and int(r.get('vmid', -1)) == vmid), None)
        if not vm: raise s.Problem('VM oder Container nicht gefunden.', 404)
        if node and vm['node'] != node: raise s.Problem('Die VM befindet sich inzwischen auf einem anderen Node. Inventar aktualisieren.', 409)
        return vm

    def detail(self, conn, vmid, node):
        s = self.s; vm = self.vm(conn, vmid, node)
        base = '/nodes/' + s.pathpart(vm['node']) + '/' + vm.get('type','qemu') + '/' + str(vmid)
        config = s.pve(conn, 'GET', base + '/config')
        # Return a deliberately restricted config. Do not expose passwords, keys,
        # custom cloud-init snippets or storage credentials to the browser.
        allowed = ('name', 'cores', 'sockets', 'memory', 'ostype', 'agent', 'onboot', 'tags', 'description', 'template', 'lock','boot')
        clean = {k: config[k] for k in allowed if k in config}
        if vm.get('type')=='lxc':clean['name']=config.get('hostname',vm.get('name',''))
        disks = [{'device': k, 'size': re.search(r'(?:^|,)size=([^,]+)', str(v)).group(1) if re.search(r'(?:^|,)size=([^,]+)', str(v)) else '—'} for k, v in config.items() if re.fullmatch(r'(scsi|virtio|sata|ide)\d+', k) and 'cloudinit' not in str(v) and 'media=cdrom' not in str(v)]
        networks=[]
        for k,v in config.items():
            if re.fullmatch(r'net\d+',k):
                parts=dict(pair.split('=',1) for pair in str(v).split(',') if '=' in pair)
                networks.append({'device':k,'bridge':parts.get('bridge',''),'vlan':parts.get('tag','0'),'firewall':parts.get('firewall','0'),'mac':next((parts[m] for m in ('virtio','e1000','rtl8139','vmxnet3') if m in parts),'')})
        return {'vm': vm, 'config': clean, 'disks': disks,'networks':networks, 'snapshots': self.safe_read(conn, base + '/snapshot'),'metrics':self.safe_read(conn,base+'/rrddata',{'timeframe':'hour','cf':'AVERAGE'})}

    def persist(self, op, message=None):
        if message: op['events'].append({'at': time.time(), 'message': message})
        with self.s.db() as c: c.execute('UPDATE operations SET data=? WHERE id=?', (json.dumps(op), op['id']))

    def submit(self, data):
        s = self.s
        key = s.text_field(data, 'request_key', r'[a-zA-Z0-9-]{16,80}', 80)
        with s.db() as c:
            row = c.execute('SELECT data FROM operations WHERE request_key=?', (key,)).fetchone()
        if row: return json.loads(row[0]), 200
        cid = s.text_field(data, 'connection_id', r'[a-zA-Z0-9-]+', 64)
        action = s.text_field(data, 'action', maxlen=20)
        if action not in self.ACTIONS: raise s.Problem('Aktion wird nicht unterstützt.')
        vmid = s.integer(data, 'vmid', 100, 999999999)
        node = s.text_field(data, 'node', r'[a-zA-Z0-9_.-]+', 64)
        if action in self.workspace.CREATE_ACTIONS:return self.create_operation(data,action,key)
        expected = s.text_field(data, 'confirm_name', maxlen=100)
        conn = s.connection(cid); vm = self.vm(conn, vmid, node)
        if vm.get('type')=='lxc' and action not in ('start','shutdown','reboot','snapshot','backup','configure','migrate','ha'):raise s.Problem('Diese Aktion ist für LXC noch nicht verfügbar.')
        if vm.get('template'): raise s.Problem('Vorlagen können hier nicht als VM verwaltet werden.')
        if expected != vm.get('name'): raise s.Problem('VM-Name stimmt nicht überein. Inventar aktualisieren.', 409)
        args = {}
        if action=='ha':
            desired=s.text_field(data,'ha_state',maxlen=16)
            if desired not in ('started','stopped','disabled','ignored'):raise s.Problem('Ungültiger HA-Zielzustand.')
            status=s.pve(conn,'GET','/cluster/status')
            if not any(r.get('type')=='cluster' and r.get('quorate')==1 for r in status):raise s.Problem('HA-Änderungen benötigen Cluster-Quorum.')
            args={'state':desired}
        elif action in self.workspace.VM_ACTIONS:
            args=self.workspace.validate(conn,vm,data,action)
        elif action == 'snapshot':
            args = {'snapname': s.text_field(data, 'snapshot_name', r'[a-zA-Z][a-zA-Z0-9_-]{0,39}', 40), 'vmstate': 0}
            if args['snapname'] == 'current': raise s.Problem('Dieser Snapshot-Name ist reserviert.')
        elif action == 'backup':
            storage = s.text_field(data, 'storage', r'[a-zA-Z0-9_.-]+', 64)
            targets = s.pve(conn, 'GET', '/nodes/' + s.pathpart(node) + '/storage')
            if not any(t.get('storage') == storage and t.get('active') and 'backup' in t.get('content', '') for t in targets): raise s.Problem('Aktives Backup-Storage auf dem Quell-Node nicht gefunden.')
            args = {'vmid': vmid, 'storage': storage, 'mode': 'snapshot'}
        elif action == 'migrate':
            target = s.text_field(data, 'target', r'[a-zA-Z0-9_.-]+', 64)
            if target == node: raise s.Problem('Ziel- und Quell-Node sind identisch.')
            nodes = s.pve(conn, 'GET', '/nodes')
            if not any(n.get('node') == target and n.get('status') == 'online' for n in nodes): raise s.Problem('Ziel-Node ist nicht online.')
            if vm.get('type')=='lxc' and vm.get('status')!='stopped':raise s.Problem('LXC-Migration benötigt hier einen gestoppten Container.')
            args = {'target': target} if vm.get('type')=='lxc' else {'target': target, 'online': int(vm.get('status') == 'running')}
        elif action == 'configure':
            if vm.get('status') != 'stopped': raise s.Problem('CPU und RAM können hier nur bei gestoppter VM geändert werden.')
            args = {'cores': s.integer(data, 'cpu', 1, 128), 'sockets': 1, 'memory': s.integer(data, 'ram', 1, 1024) * 1024}
        elif action == 'start' and vm.get('status') == 'running': raise s.Problem('VM läuft bereits.')
        elif action in ('shutdown', 'reboot') and vm.get('status') != 'running': raise s.Problem('VM läuft nicht.')
        if vm.get('type')=='lxc':args.pop('sockets',None)
        op = {'guest_type':vm.get('type','qemu'),'id': uuid.uuid4().hex, 'connection_id': cid, 'vmid': vmid, 'node': node, 'name': expected, 'action': action, 'args': args, 'status': 'queued', 'stage': 'queued', 'created': time.time(), 'events': [{'at': time.time(), 'message': 'Aktion eingeplant.'}]}
        with s.db() as c:
            row = c.execute('SELECT data FROM operations WHERE request_key=?', (key,)).fetchone()
            if row: return json.loads(row[0]), 200
            for row in c.execute('SELECT data FROM operations'):
                other = json.loads(row[0])
                if other['connection_id'] == cid and other['vmid'] == vmid and other['status'] in ('queued', 'running', 'needs_attention'): raise s.Problem('Für diese VM läuft bereits eine Aktion oder eine Prüfung ist nötig.', 409)
            c.execute('INSERT INTO operations VALUES(?,?,?,?)', (op['id'], key, json.dumps(op), op['created']))
        s.audit('vm.' + action + '.queued', op['id']); s.WAKE.set()
        return op, 202

    def create_operation(self,data,action,key):
        s=self.s;cid,node,vmid,name,args=self.workspace.create(data,action)
        op={'id':uuid.uuid4().hex,'connection_id':cid,'vmid':vmid,'node':node,'name':name,'action':action,'args':args,'status':'queued','stage':'queued','created':time.time(),'events':[{'at':time.time(),'message':'Neue VM eingeplant. Bestehende VMIDs werden nicht überschrieben.'}]}
        with s.db() as c:
            existing=c.execute('SELECT data FROM operations WHERE request_key=?',(key,)).fetchone()
            if existing:return json.loads(existing[0]),200
            all_ops=[json.loads(r[0]) for r in c.execute('SELECT data FROM operations')]
            all_jobs=[json.loads(r[0]) for r in c.execute('SELECT data FROM jobs')]
            if any(j['connection_id']==cid and j['vmid']==vmid and j['status'] in ('queued','running','needs_attention') for j in all_ops+all_jobs):raise s.Problem('VMID bereits durch Orbit reserviert.',409)
            c.execute('INSERT INTO operations VALUES(?,?,?,?)',(op['id'],key,json.dumps(op),op['created']))
        s.audit('vm.'+action+'.queued',op['id']);return op,202

    def process(self, op):
        s = self.s
        if op['stage'] == 'submitting':
            op['status'] = 'needs_attention'; self.persist(op, 'Unklarer Übermittlungszustand nach Neustart. Proxmox-Aufgabe prüfen. Keine Wiederholung.'); return
        try:
            conn = s.connection(op['connection_id'])
            base = '/nodes/' + s.pathpart(op['node']) + '/' + op.get('guest_type','qemu') + '/' + str(op['vmid'])
            op['status'] = 'running'; self.persist(op)
            if op['stage'] == 'queued':
                creating=op['action'] in self.workspace.CREATE_ACTIONS
                vm={'name':op['name']} if creating else self.vm(conn, op['vmid'], op['node'])
                if creating and any(r.get('vmid')==op['vmid'] for r in s.pve(conn,'GET','/cluster/resources')):raise s.Problem('Ziel-VMID inzwischen belegt. Kein Überschreiben.')
                if not creating and vm.get('type','qemu')!=op.get('guest_type','qemu'):raise s.Problem('Gasttyp hat sich geändert. Aktion abgebrochen.')
                if vm.get('name') != op['name'] or vm.get('template'): raise s.Problem('VM-Identität hat sich geändert. Keine Aktion ausgeführt.')
                action = op['action']; fields = dict(op['args'])
                synchronous=False
                if action=='ha':
                    status=s.pve(conn,'GET','/cluster/status')
                    if not any(r.get('type')=='cluster' and r.get('quorate')==1 for r in status):raise s.Problem('Cluster hat kein Quorum. HA unverändert.')
                    sid=('ct:' if op.get('guest_type')=='lxc' else 'vm:')+str(op['vmid'])
                    resources=s.pve(conn,'GET','/cluster/ha/resources')
                    exists=any(r.get('sid')==sid for r in resources)
                    method,path=('PUT','/cluster/ha/resources/'+s.pathpart(sid)) if exists else ('POST','/cluster/ha/resources')
                    if not exists:fields['sid']=sid
                    synchronous=True
                elif action in self.workspace.VM_ACTIONS or creating:
                    if not creating and vm.get('status')!='stopped':raise s.Problem('VM ist inzwischen gestartet. Änderung abgebrochen.')
                    method,path,fields,synchronous=self.workspace.command(op,base)
                elif action == 'configure':
                    if vm.get('status') != 'stopped': raise s.Problem('VM ist inzwischen gestartet. Konfiguration unverändert.')
                    config = s.pve(conn, 'GET', base + '/config')
                    if config.get('hostname' if op.get('guest_type')=='lxc' else 'name') != op['name']:raise s.Problem('VM-Identität hat sich geändert. Keine Konfiguration ausgeführt.')
                    if config.get('digest'): fields['digest'] = config['digest']
                    method, path = 'PUT', base + '/config'
                elif action == 'backup': method, path = 'POST', '/nodes/' + s.pathpart(op['node']) + '/vzdump'
                elif action in ('snapshot', 'migrate'): method, path = 'POST', base + '/' + action
                else: method, path = 'POST', base + '/status/' + action
                op['stage'] = 'submitting'; self.persist(op, 'Aktion an Proxmox übergeben.')
                result = s.pve(conn, method, path, fields)
                if (action=='configure' or synchronous) and result is None:
                    op['stage']='done'
                else:
                    op['upid']=s.task_id(result);op['stage']='waiting'
                self.persist(op)
            if op['stage'] == 'waiting':
                s.wait_task(conn, op['node'], op['upid']); op['stage'] = 'done'
            op['status'] = 'completed'; self.persist(op, 'Proxmox hat die Aktion erfolgreich abgeschlossen.')
            s.audit('vm.' + op['action'] + '.completed', op['id'])
        except InterruptedError: return
        except Exception as exc:
            uncertain = isinstance(exc, s.Problem) and exc.uncertain and op['stage'] in ('submitting', 'waiting')
            op['status'] = 'needs_attention' if uncertain else 'failed'
            self.persist(op, exc.message if isinstance(exc, s.Problem) else 'Interner Fehler. Proxmox-Zustand manuell prüfen.')
            s.audit('vm.' + op['action'] + '.' + op['status'], op['id'])

    def worker(self):
        s = self.s
        while not s.STOP.is_set():
            picked = None
            if not (s.DATA / 'maintenance').exists():
                with s.db() as c:
                    for row in c.execute('SELECT data FROM operations ORDER BY created'):
                        op = json.loads(row[0])
                        if op['status'] in ('queued', 'running') and op['id'] not in self.active:
                            self.active.add(op['id']); picked = op; break
            if picked:
                try: self.process(picked)
                finally:
                    with s.DB_LOCK: self.active.discard(picked['id'])
            else: s.STOP.wait(1)

    def route(self, handler, path, data):
        s = self.s
        if path == '/api/operations':
            if handler.command == 'GET':
                with s.db() as c: result = [json.loads(r[0]) for r in c.execute('SELECT data FROM operations ORDER BY created DESC LIMIT 200')]
                handler.send_json(200, result); return True
            if handler.command == 'POST':
                op, status = self.submit(data); handler.send_json(status, op); return True
        match=re.fullmatch(r'/api/operations/([a-zA-Z0-9-]+)/review',path)
        if match and handler.command=='POST':
            with s.db() as c:row=c.execute('SELECT data FROM operations WHERE id=?',(match[1],)).fetchone()
            if not row:raise s.Problem('Aktion nicht gefunden.',404)
            op=json.loads(row[0])
            if op['status']!='needs_attention':raise s.Problem('Diese Aktion benötigt keine manuelle Prüfung.',409)
            if data.get('confirm_name')!=op['name']:raise s.Problem('VM-Name stimmt nicht überein.')
            note=s.text_field(data,'note',maxlen=500)
            if len(note)<10 or data.get('acknowledge') is not True:raise s.Problem('Bestätigte Prüfung und nachvollziehbare Notiz benötigt.')
            op['status']='reviewed'
            self.persist(op,'Manuell in Proxmox geprüft: '+note)
            s.audit('vm.operation.reviewed',op['id']);handler.send_json(200,op);return True
        match = re.fullmatch(r'/api/connections/([a-zA-Z0-9-]+)/modules/([a-z]+)', path)
        if match and handler.command == 'GET':
            handler.send_json(200, self.module(s.connection(match[1]), match[2])); return True
        match=re.fullmatch(r'/api/connections/([a-zA-Z0-9-]+)/catalog',path)
        if match and handler.command=='GET':
            node=parse_qs(urlsplit(handler.path).query).get('node',[''])[0]
            if not re.fullmatch(r'[a-zA-Z0-9_.-]+',node):raise s.Problem('Node fehlt.')
            handler.send_json(200,self.workspace.catalog(s.connection(match[1]),node));return True
        match = re.fullmatch(r'/api/connections/([a-zA-Z0-9-]+)/nodes', path)
        if match and handler.command == 'GET':
            conn=s.connection(match[1])
            if self.kind(conn)!='pve':raise s.Problem('Nodes benötigen Proxmox VE.')
            handler.send_json(200,s.pve(conn,'GET','/nodes'));return True
        match = re.fullmatch(r'/api/connections/([a-zA-Z0-9-]+)/vms/(\d+)', path)
        if match and handler.command == 'GET':
            vmid = s.integer({'vmid': int(match[2])}, 'vmid', 100, 999999999)
            node = parse_qs(urlsplit(handler.path).query).get('node', [None])[0]
            handler.send_json(200, self.detail(s.connection(match[1]), vmid, node)); return True
        return False
