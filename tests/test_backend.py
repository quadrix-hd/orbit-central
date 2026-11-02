import http.client, importlib.util, json, os, sqlite3, tempfile, threading, unittest, uuid
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('orbit_server',Path(__file__).parents[1]/'backend/server.py')
s=importlib.util.module_from_spec(spec);spec.loader.exec_module(s)

class OrbitIntegration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp=tempfile.TemporaryDirectory();s.DATA=Path(cls.temp.name);s.initialize()
        cls.http=s.ThreadingHTTPServer(('127.0.0.1',0),s.Handler)
        cls.thread=threading.Thread(target=cls.http.serve_forever,daemon=True);cls.thread.start()
        cls.cookie='';cls.csrf='';cls.calls=[]
        cls.resources=[{'type':'node','node':'pve-01','status':'online','maxcpu':32,'maxmem':128*1073741824}, {'type':'qemu','node':'pve-01','vmid':9000,'name':'ubuntu-template','template':1}]
        cls.config={'template':1,'ide2':'local:cloudinit','scsi0':'local:base-9000-disk-0,size=16G'}
        def fake(conn,method,path,fields=None):
            cls.calls.append((method,path,fields))
            if path=='/version':return {'version':'9.0'}
            if path=='/cluster/resources':return cls.resources
            if path=='/cluster/nextid':return '100'
            if path=='/nodes':return [{'node':'pve-01','status':'online'}]
            if path.endswith('/storage'):return [{'storage':'local-lvm','active':1,'content':'images,rootdir'}]
            if path.endswith('/network'):return [{'iface':'vmbr0','type':'bridge'}]
            if method=='GET' and path.endswith('/config'):
                vmid=int(path.split('/')[-2])
                if vmid!=9000:
                    with s.db() as c:jobs=[json.loads(r[0]) for r in c.execute('SELECT data FROM jobs')]
                    found=next((j for j in jobs if j['vmid']==vmid),None)
                    return {**cls.config,'name':found['name'] if found else 'unknown'}
                return cls.config
            if path.endswith('/status'):return {'status':'stopped','exitstatus':'OK'}
            if method=='POST' and path.endswith(('/clone','/start')):return 'UPID:pve-01:000:000:000:qmclone:100:orbit@pve:'
            return None
        cls.fake=fake;cls.mock=patch.object(s,'pve',side_effect=fake);cls.mock.start()
    @classmethod
    def tearDownClass(cls):
        cls.mock.stop();cls.http.shutdown();cls.http.server_close();cls.temp.cleanup()
    def req(self,path,method='GET',data=None,auth=True,csrf=True,origin=None):
        h={'Content-Type':'application/json'}
        if auth and self.cookie:h['Cookie']=self.cookie
        if csrf and self.csrf:h['X-Orbit-CSRF']=self.csrf
        if origin:h['Origin']=origin
        c=http.client.HTTPConnection('127.0.0.1',self.http.server_port,timeout=10);c.request(method,path,body=json.dumps(data).encode() if data is not None else None,headers=h);r=c.getresponse();raw=r.read();headers=dict(r.getheaders());c.close()
        try:body=json.loads(raw)
        except:body=raw.decode()
        return r.status,body,headers
    def test_01_setup_and_session(self):
        self.assertEqual(self.req('/api/connections',auth=False)[0],401)
        token=(s.DATA/'setup-token').read_text()
        self.assertEqual(self.req('/api/setup','POST',{'token':token,'password':'short'})[0],400)
        self.assertEqual(self.req('/api/setup','POST',{'token':'wrong','password':'long-password-for-tests'})[0],403)
        code,body,h=self.req('/api/setup','POST',{'token':token,'password':'long-password-for-tests'})
        self.assertEqual(code,200);type(self).cookie=h['Set-Cookie'].split(';')[0];type(self).csrf=body['csrf']
        self.assertIn('HttpOnly',h['Set-Cookie']);self.assertIn('SameSite=Strict',h['Set-Cookie']);self.assertFalse((s.DATA/'setup-token').exists())
        self.assertEqual(self.req('/api/setup','POST',{'token':token,'password':'another-long-password'})[0],409)
        self.assertTrue(self.req('/api/auth')[1]['authenticated'])
    def test_02_csrf_and_origin(self):
        self.assertEqual(self.req('/api/connections','POST',{},csrf=False)[0],403)
        self.assertEqual(self.req('/api/connections','POST',{},origin='https://attacker.example')[0],403)
    def test_03_connection_validation_and_secret_redaction(self):
        data={'name':'Lab','url':'http://pve.example:8006','token':'orbit@pve!console=secret'}
        self.assertEqual(self.req('/api/connections','POST',data)[0],400)
        data['url']='https://pve.example:8006';code,body,_=self.req('/api/connections','POST',data);self.assertEqual(code,201);type(self).cid=body['id']
        self.assertEqual(self.req('/api/connections','POST',data)[0],409)
        self.assertNotIn('secret',json.dumps(self.req('/api/connections')[1]))
        with s.db() as c:encrypted=c.execute('SELECT token FROM connections').fetchone()[0]
        self.assertNotIn('secret',encrypted);self.assertEqual(s.FERNET.decrypt(encrypted.encode()).decode(),data['token'])
    def bp(self):
        return {'name':'Ubuntu','description':'Cloud-init base','category':'Betriebssystem','connection_id':self.cid,'node':'pve-01','template_vmid':9000,'storage':'local-lvm','bridge':'vmbr0','username':'orbit','disk_device':'scsi0','cpu':2,'ram':4,'disk':32}
    def test_04_blueprint_preconditions(self):
        data=self.bp();data['disk']=8;self.assertEqual(self.req('/api/blueprints','POST',data)[0],400)
        with patch.dict(self.config,{'template':0}):self.assertEqual(self.req('/api/blueprints','POST',self.bp())[0],400)
        config=dict(self.config);config.pop('ide2')
        with patch.object(type(self),'config',config):self.assertEqual(self.req('/api/blueprints','POST',self.bp())[0],400)
        code,bp,_=self.req('/api/blueprints','POST',self.bp());self.assertEqual(code,201);type(self).bid=bp['id'];self.assertEqual(bp['min_disk'],16)
    def job_payload(self,name='test-vm'):
        return {'blueprint_id':self.bid,'request_key':str(uuid.uuid4()),'name':name,'cpu':2,'ram':4,'disk':32,'ssh':'','start':True}
    def test_05_deployment_and_idempotency(self):
        d=self.job_payload();bad=dict(d,cpu=0);self.assertEqual(self.req('/api/jobs','POST',bad)[0],400)
        bad=dict(d,ssh='-----BEGIN PRIVATE KEY-----');self.assertEqual(self.req('/api/jobs','POST',bad)[0],400)
        code,j,_=self.req('/api/jobs','POST',d);self.assertEqual(code,202);self.assertNotIn('ssh',j)
        self.assertEqual(self.req('/api/jobs','POST',d)[1]['id'],j['id'])
        # Process persisted payload, including its public key.
        with s.db() as c:job=json.loads(c.execute('SELECT data FROM jobs WHERE id=?',(j['id'],)).fetchone()[0])
        type(self).calls.clear();s.process_job(job)
        self.assertEqual(job['status'],'completed');self.assertEqual(job['stage'],'done')
        clone=[c for c in self.calls if c[1].endswith('/clone')];self.assertEqual(len(clone),1);self.assertEqual(clone[0][2]['full'],1)
        config=[c for c in self.calls if c[0]=='PUT' and c[1].endswith('/config')][0][2]
        self.assertEqual(config['memory'],4096);self.assertEqual(config['ipconfig0'],'ip=dhcp')
        self.assertTrue(any(c[1].endswith('/resize') and c[2]['size']=='32G' for c in self.calls))
    def test_06_concurrent_allocations(self):
        a=self.req('/api/jobs','POST',self.job_payload('unique-a'))[1]
        b=self.req('/api/jobs','POST',self.job_payload('unique-b'))[1]
        self.assertNotEqual(a['vmid'],b['vmid'])
        self.assertEqual(self.req('/api/jobs','POST',self.job_payload('unique-a'))[0],409)
    def test_07_failed_task_preserves_vm(self):
        j=self.req('/api/jobs','POST',self.job_payload('failed-vm'))[1]
        original=type(self).fake
        def fail(conn,method,path,fields=None):
            if path.endswith('/status'):return {'status':'stopped','exitstatus':'disk full'}
            return original(conn,method,path,fields)
        self.calls.clear()
        with patch.object(s,'pve',side_effect=fail):s.process_job(j)
        self.assertEqual(j['status'],'failed');self.assertIn('disk full',j['events'][-1]['message']);self.assertFalse(any(c[0]=='DELETE' for c in self.calls))
    def test_08_resume_does_not_clone_twice(self):
        j=self.req('/api/jobs','POST',self.job_payload('resumed-vm'))[1];j.update(stage='clone_wait',upid='UPID:test',status='running')
        self.calls.clear();s.process_job(j);self.assertEqual(j['status'],'completed');self.assertFalse(any(c[1].endswith('/clone') for c in self.calls))
    def test_09_uncertain_submission_requires_review(self):
        j=self.req('/api/jobs','POST',self.job_payload('uncertain-vm'))[1];j.update(stage='clone_submitting',status='running');self.calls.clear();s.process_job(j)
        self.assertEqual(j['status'],'needs_attention');self.assertFalse(self.calls)
    def test_10_unauthorized_upstream_no_credentials(self):
        with patch.object(s,'pve',side_effect=s.Problem('Proxmox verweigert den Zugriff.',502)):
            code,data,_=self.req('/api/connections/'+self.cid+'/inventory');self.assertEqual(code,502);self.assertNotIn('secret',str(data))
    def test_10a_malformed_input_is_client_error(self):
        for changes in ({'ssh':None},{'ssh':[]},{'blueprint_id':{}},{'cpu':True},{'disk':'32'},{'start':'yes'}):
            with self.subTest(changes=changes):self.assertEqual(self.req('/api/jobs','POST',{**self.job_payload('malformed'),**changes})[0],400)
        for url in ('https://pve.example:wrong','https://[broken','https://pve.example:0','https://user:password@pve.example'):
            with self.subTest(url=url):self.assertEqual(self.req('/api/connections','POST',{'name':'Invalid','url':url,'token':'a@pve!x=secret'})[0],400)
        self.assertEqual(self.req('/api/connections/'+self.cid,'POST',{'name':'Lab','token':[]})[0],400)
    def test_10b_changed_template_rejected_before_clone(self):
        for change in ({'ide2':'local:other-disk'},{'scsi0':'local:base-9000,size=64G'},{'template':0}):
            with self.subTest(change=change),patch.dict(self.config,change):
                self.calls.clear();code,_,_=self.req('/api/jobs','POST',self.job_payload('changed-template'))
                self.assertEqual(code,400);self.assertFalse(any(c[1].endswith('/clone') for c in self.calls))
    def test_10c_queued_template_revalidated(self):
        j=self.req('/api/jobs','POST',self.job_payload('queue-changed'))[1];self.calls.clear()
        with patch.dict(self.config,{'scsi0':'local:disk,size=128G'}):s.process_job(j)
        self.assertEqual(j['status'],'failed');self.assertFalse(any(c[1].endswith('/clone') for c in self.calls))
    def test_10d_equal_disk_size_skips_resize(self):
        d=self.job_payload('same-size');d['disk']=16;j=self.req('/api/jobs','POST',d)[1];self.calls.clear();s.process_job(j)
        self.assertEqual(j['status'],'completed');self.assertFalse(any(c[1].endswith('/resize') for c in self.calls))
    def test_10e_uncertain_network_submission_requires_attention(self):
        j=self.req('/api/jobs','POST',self.job_payload('network-timeout'))[1];original=type(self).fake
        def interrupted(conn,method,path,fields=None):
            if path.endswith('/clone'):raise s.Problem('Verbindung unterbrochen.',502,uncertain=True)
            return original(conn,method,path,fields)
        with patch.object(s,'pve',side_effect=interrupted):s.process_job(j)
        self.assertEqual(j['status'],'needs_attention');self.assertEqual(j['stage'],'clone_submitting')
    def test_10f_missing_task_id_requires_attention(self):
        j=self.req('/api/jobs','POST',self.job_payload('missing-upid'))[1];original=type(self).fake
        def invalid(conn,method,path,fields=None):
            if path.endswith('/clone'):return None
            return original(conn,method,path,fields)
        with patch.object(s,'pve',side_effect=invalid):s.process_job(j)
        self.assertEqual(j['status'],'needs_attention')
    def test_10g_wrong_vm_identity_prevents_configuration(self):
        j=self.req('/api/jobs','POST',self.job_payload('vm-identity'))[1];original=type(self).fake
        def changed(conn,method,path,fields=None):
            value=original(conn,method,path,fields)
            if method=='GET' and path.endswith('/config') and '/9000/' not in path:return {**value,'name':'another-vm'}
            return value
        self.calls.clear()
        with patch.object(s,'pve',side_effect=changed):s.process_job(j)
        self.assertNotEqual(j['status'],'completed');self.assertFalse(any(c[0]=='PUT' for c in self.calls))
    def test_10h_parallel_requests_have_distinct_vmids(self):
        from concurrent.futures import ThreadPoolExecutor
        with ThreadPoolExecutor(max_workers=6) as pool:results=list(pool.map(lambda i:self.req('/api/jobs','POST',self.job_payload('parallel-'+str(i))),range(6)))
        self.assertTrue(all(r[0]==202 for r in results));self.assertEqual(len({r[1]['vmid'] for r in results}),6)
    def test_10i_parallel_duplicate_request_creates_one_job(self):
        from concurrent.futures import ThreadPoolExecutor
        data=self.job_payload('parallel-duplicate')
        with ThreadPoolExecutor(max_workers=4) as pool:results=list(pool.map(lambda _:self.req('/api/jobs','POST',data),range(4)))
        self.assertTrue(all(r[0] in (200,202) for r in results),results);self.assertEqual(len({r[1]['id'] for r in results}),1)
    def test_11_inventory_and_targets(self):
        self.assertEqual(self.req('/api/connections/'+self.cid+'/templates')[1][0]['vmid'],9000)
        targets=self.req('/api/connections/'+self.cid+'/targets')[1];self.assertEqual(targets[0]['bridges'],['vmbr0'])
    def test_11b_rotation_and_maintenance(self):
        self.assertEqual(self.req('/api/connections/'+self.cid,'POST',{'name':'Renamed Lab','token':'orbit@pve!console=new-secret'})[0],200)
        listing=self.req('/api/connections')[1]
        self.assertEqual(listing[0]['name'],'Renamed Lab');self.assertNotIn('new-secret',json.dumps(listing))
        (s.DATA/'maintenance').touch()
        try:self.assertEqual(self.req('/api/jobs','POST',self.job_payload('blocked-update'))[0],503)
        finally:(s.DATA/'maintenance').unlink()
    def test_11c_backup_is_complete(self):
        import subprocess,tarfile,io
        result=subprocess.run(['python',str(Path(__file__).parents[1]/'scripts/backup.py')],env={**os.environ,'ORBIT_DATA_DIR':str(s.DATA)},capture_output=True,text=True,check=True)
        filename=Path(result.stdout.strip()).name
        with tarfile.open(s.DATA/'backups'/filename) as tar:
            self.assertIn('orbit.db',tar.getnames());self.assertIn('master.key',tar.getnames())
            key=tar.extractfile('master.key').read();self.assertEqual(key,(s.DATA/'master.key').read_bytes())
            backed=Path(self.temp.name)/'copied.db';backed.write_bytes(tar.extractfile('orbit.db').read())
            with sqlite3.connect(backed) as c:self.assertGreater(c.execute('SELECT count(*) FROM jobs').fetchone()[0],0)
    def test_12_static_and_logout(self):
        code,html,h=self.req('/');self.assertEqual(code,200);self.assertIn('app.js',html);self.assertIn("script-src 'self'",h['Content-Security-Policy'])
        self.assertEqual(self.req('/../backend/server.py')[0],404)
        self.assertEqual(self.req('/api/logout','POST',{})[0],200)
        self.assertEqual(self.req('/api/jobs')[0],401)
    def test_13_login_failure_and_success(self):
        self.assertEqual(self.req('/api/login','POST',{'password':'wrong'})[0],401)
        self.assertEqual(self.req('/api/login','POST',{'password':'long-password-for-tests'})[0],200)

if __name__=='__main__':unittest.main(verbosity=2)
