import http.client
import json
import tempfile
import threading
import unittest
import uuid
from pathlib import Path
from unittest.mock import patch
import test_backend as legacy
s=legacy.s

class ControlTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp=tempfile.TemporaryDirectory();s.DATA=Path(cls.temp.name);s.ATTEMPTS.clear();s.initialize()
        cls.calls=[]
        cls.vms=[{'type':'qemu','vmid':101,'node':'pve-01','name':'web-01','status':'running'},{'type':'qemu','vmid':102,'node':'pve-01','name':'db-01','status':'stopped'},{'type':'qemu','vmid':9000,'node':'pve-01','name':'template','template':1,'status':'stopped'}]
        def fake(conn,method,path,fields=None):
            cls.calls.append((conn.get('kind','pve'),method,path,fields))
            if path=='/version':return {'version':'test'}
            if path=='/cluster/resources':return cls.vms
            if path=='/nodes':return [{'node':'pve-01','status':'online'},{'node':'pve-02','status':'online'},{'node':'pve-off','status':'offline'}]
            if path.endswith('/config') and method=='GET':return {'name':'db-01' if '/102/' in path else 'web-01','cores':2,'memory':4096,'digest':'digest-test','cipassword':'secret-password','sshkeys':'private-value','scsi0':'local:disk,size=32G'}
            if path.endswith('/storage'):return [{'storage':'pbs-backup','active':1,'content':'backup'},{'storage':'images-only','active':1,'content':'images'}]
            if path.endswith('/network'):return [{'iface':'vmbr0','type':'bridge'}]
            if path.endswith('/snapshot') and method=='GET':return [{'name':'current'}]
            if '/tasks/' in path and path.endswith('/status'):return {'status':'stopped','exitstatus':'OK'}
            if path=='/status/datastore-usage':return [{'store':'main','used':100,'total':500}]
            if path=='/cluster/firewall/options':return {'enable':1}
            if path.endswith('/apt/update'):return [{'Package':'pve-manager','Version':'test-update'}]
            if path.endswith('/apt/versions'):return [{'Package':'pve-manager','Version':'test-current'}]
            if method=='PUT':return None
            if method=='POST':return 'UPID:pve-01:test'
            return []
        cls.fake=fake;cls.mock=patch.object(s,'pve',side_effect=fake);cls.mock.start()
        cls.http=s.ThreadingHTTPServer(('127.0.0.1',0),s.Handler);threading.Thread(target=cls.http.serve_forever,daemon=True).start()
        cls.cookie='';cls.csrf=''
        _,auth,h=cls.req('/api/setup','POST',{'token':(s.DATA/'setup-token').read_text(),'password':'control-test-password'})
        cls.csrf=auth['csrf'];cls.cookie=h['Set-Cookie'].split(';')[0]
        cls.cid=cls.req('/api/connections','POST',{'name':'VE','url':'https://pve.test:8006','token':'orbit@pve!app=secret','kind':'pve'})[1]['id']
        cls.pbs=cls.req('/api/connections','POST',{'name':'PBS','url':'https://pbs.test:8007','token':'orbit@pbs!app=secret','kind':'pbs'})[1]['id']
    @classmethod
    def tearDownClass(cls):cls.mock.stop();cls.http.shutdown();cls.http.server_close();cls.temp.cleanup()
    @classmethod
    def req(cls,path,method='GET',data=None,auth=True,csrf=True):
        h={'Content-Type':'application/json'}
        if auth:h['Cookie']=cls.cookie
        if csrf:h['X-Orbit-CSRF']=cls.csrf
        c=http.client.HTTPConnection('127.0.0.1',cls.http.server_port,timeout=15);c.request(method,path,json.dumps(data) if data is not None else None,h);r=c.getresponse();raw=r.read();heads=dict(r.getheaders());c.close();return r.status,json.loads(raw),heads
    def payload(self,action='snapshot',vmid=101):return {'request_key':str(uuid.uuid4()),'connection_id':self.cid,'vmid':vmid,'node':'pve-01','confirm_name':'db-01' if vmid==102 else 'web-01','action':action,'snapshot_name':'before-update','storage':'pbs-backup','target':'pve-02','cpu':4,'ram':8}
    def process(self,op):
        s.CONTROL.process(op)
        # Finish or release a failed fixture so it cannot block another independent test.
        with s.db() as c:c.execute('DELETE FROM operations WHERE id=?',(op['id'],))
        return op
    def test_connection_kind_and_pbs_inventory_guard(self):
        listing=self.req('/api/connections')[1];self.assertEqual({c['kind'] for c in listing},{'pve','pbs'})
        self.assertEqual(self.req('/api/connections/'+self.pbs+'/inventory')[0],400)
    def test_unauthorized_operations_and_csrf(self):
        self.assertEqual(self.req('/api/operations',auth=False)[0],401)
        self.assertEqual(self.req('/api/operations','POST',self.payload(),csrf=False)[0],403)
    def test_vm_detail_redacts_sensitive_config(self):
        code,data,_=self.req('/api/connections/'+self.cid+'/vms/101?node=pve-01');self.assertEqual(code,200)
        self.assertNotIn('secret-password',json.dumps(data));self.assertNotIn('private-value',json.dumps(data));self.assertEqual(data['disks'][0]['size'],'32G')
    def test_vm_moved_or_missing_is_rejected(self):
        self.assertEqual(self.req('/api/connections/'+self.cid+'/vms/101?node=pve-02')[0],409)
        self.assertEqual(self.req('/api/connections/'+self.cid+'/vms/999')[0],404)
    def test_snapshot_end_to_end_and_idempotency(self):
        d=self.payload();code,op,_=self.req('/api/operations','POST',d);self.assertEqual(code,202)
        self.assertEqual(self.req('/api/operations','POST',d)[1]['id'],op['id']);self.calls.clear();self.process(op)
        self.assertEqual(op['status'],'completed');self.assertTrue(any(c[1]=='POST' and c[2].endswith('/snapshot') and c[3]['snapname']=='before-update' for c in self.calls))
    def test_power_operations_end_to_end(self):
        for action,vmid in [('start',102),('shutdown',101),('reboot',101)]:
            with self.subTest(action=action):
                code,op,_=self.req('/api/operations','POST',self.payload(action,vmid));self.assertEqual(code,202);self.calls.clear();self.process(op)
                self.assertEqual(op['status'],'completed');self.assertTrue(any(c[2].endswith('/status/'+action) for c in self.calls))
    def test_backup_only_uses_backup_capable_storage(self):
        d=self.payload('backup');d['storage']='images-only';self.assertEqual(self.req('/api/operations','POST',d)[0],400)
        code,op,_=self.req('/api/operations','POST',self.payload('backup'));self.assertEqual(code,202);self.calls.clear();self.process(op)
        self.assertEqual(op['status'],'completed');self.assertTrue(any(c[2]=='/nodes/pve-01/vzdump' and c[3]['mode']=='snapshot' for c in self.calls))
    def test_migration_preconditions_and_request(self):
        for target in ['pve-01','pve-off']:
            d=self.payload('migrate');d['target']=target;self.assertEqual(self.req('/api/operations','POST',d)[0],400)
        code,op,_=self.req('/api/operations','POST',self.payload('migrate'));self.assertEqual(code,202);self.calls.clear();self.process(op)
        self.assertEqual(op['status'],'completed');self.assertTrue(any(c[2].endswith('/migrate') and c[3]=={'target':'pve-02','online':1} for c in self.calls))
    def test_cpu_ram_change_uses_config_digest(self):
        self.assertEqual(self.req('/api/operations','POST',self.payload('configure',101))[0],400)
        code,op,_=self.req('/api/operations','POST',self.payload('configure',102));self.assertEqual(code,202);self.calls.clear();self.process(op)
        self.assertEqual(op['status'],'completed');fields=next(c[3] for c in self.calls if c[1]=='PUT');self.assertEqual(fields['memory'],8192);self.assertEqual(fields['digest'],'digest-test')
    def test_identity_template_and_invalid_action_rejected(self):
        for changes in ({'confirm_name':'wrong'},{'action':'delete'},{'action':'shell'},{'cpu':True,'action':'configure','vmid':102,'confirm_name':'db-01'},{'action':'snapshot','snapshot_name':'current'},{'vmid':9000,'confirm_name':'template'}):
            with self.subTest(changes=changes):self.assertIn(self.req('/api/operations','POST',{**self.payload(),**changes})[0],(400,409))
    def test_pbs_vm_operations_rejected(self):
        d=self.payload();d['connection_id']=self.pbs;self.assertEqual(self.req('/api/operations','POST',d)[0],400)
    def test_uncertain_submission_never_repeats(self):
        code,op,_=self.req('/api/operations','POST',self.payload());self.assertEqual(code,202)
        op.update(stage='submitting',status='running');self.calls.clear();self.process(op)
        self.assertEqual(op['status'],'needs_attention');self.assertFalse(self.calls)
    def test_resume_waits_without_resubmission(self):
        code,op,_=self.req('/api/operations','POST',self.payload());self.assertEqual(code,202)
        op.update(stage='waiting',status='running',upid='UPID:test');self.calls.clear();self.process(op)
        self.assertEqual(op['status'],'completed');self.assertFalse(any(c[1] in ('PUT','POST') for c in self.calls))
    def test_manual_review_requires_identity_note_and_acknowledgement(self):
        code,op,_=self.req('/api/operations','POST',self.payload());self.assertEqual(code,202)
        op.update(stage='submitting',status='running');s.CONTROL.process(op)
        path='/api/operations/'+op['id']+'/review'
        self.assertEqual(self.req(path,'POST',{'confirm_name':op['name'],'note':'ok','acknowledge':True})[0],400)
        self.assertEqual(self.req(path,'POST',{'confirm_name':'wrong','note':'Manuell vollständig geprüft','acknowledge':True})[0],400)
        self.calls.clear();code,data,_=self.req(path,'POST',{'confirm_name':op['name'],'note':'Manuell in Proxmox geprüft; keine laufende Aufgabe.','acknowledge':True})
        self.assertEqual(code,200);self.assertEqual(data['status'],'reviewed');self.assertFalse(self.calls)
        with s.db() as c:c.execute('DELETE FROM operations WHERE id=?',(op['id'],))
    def test_modules_and_pbs_use_real_read_paths(self):
        for section in ('storage','network','security','protection','lifecycle'):
            with self.subTest(section=section):self.assertEqual(self.req('/api/connections/'+self.cid+'/modules/'+section)[0],200)
        code,data,_=self.req('/api/connections/'+self.pbs+'/modules/protection');self.assertEqual(code,200);self.assertTrue(data['datastores']['ok']);self.assertEqual(data['datastores']['data'][0]['store'],'main')
        self.assertEqual(self.req('/api/connections/'+self.pbs+'/modules/lifecycle')[0],400)
    def test_module_acl_error_is_visible_without_losing_other_results(self):
        original=type(self).fake
        def deny(conn,method,path,fields=None):
            if path=='/cluster/firewall/rules':raise s.Problem('Zugriff verweigert.',502)
            return original(conn,method,path,fields)
        with patch.object(s,'pve',side_effect=deny):code,data,_=self.req('/api/connections/'+self.cid+'/modules/security')
        self.assertEqual(code,200);self.assertFalse(data['firewall_rules']['ok']);self.assertTrue(data['firewall_options']['ok'])


class ContainerTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();s.DATA=Path(self.temp.name);s.initialize()
        self.vm={'type':'lxc','vmid':201,'node':'pve-01','name':'ct-web','status':'stopped'}
        self.calls=[]
        def fake(conn,method,path,fields=None):
            self.calls.append((method,path,fields))
            if path=='/cluster/resources':return [dict(self.vm)]
            if path=='/nodes':return [{'node':'pve-02','status':'online'}]
            if path.endswith('/config') and method=='GET':return {'hostname':'ct-web','cores':2,'memory':2048,'digest':'test','password':'secret'}
            if '/tasks/' in path:return {'status':'stopped','exitstatus':'OK'}
            if method=='PUT':return None
            if method=='POST':return 'UPID:pve-01:test'
            return []
        self.mock=patch.object(s,'pve',side_effect=fake);self.mock.start()
        self.conn={'id':'ctconn','url':'https://pve.test:8006','kind':'pve'}
        self.mockconn=patch.object(s,'connection',return_value=self.conn);self.mockconn.start()
    def tearDown(self):self.mock.stop();self.mockconn.stop();self.temp.cleanup()
    def payload(self,action):return {'request_key':str(uuid.uuid4()),'connection_id':'ctconn','vmid':201,'node':'pve-01','confirm_name':'ct-web','action':action,'cpu':4,'ram':4,'target':'pve-02'}
    def test_start_uses_lxc_endpoint(self):
        op,code=s.CONTROL.submit(self.payload('start'));self.assertEqual(code,202);s.CONTROL.process(op)
        self.assertEqual(op['status'],'completed');self.assertTrue(any(p=='/nodes/pve-01/lxc/201/status/start' for m,p,f in self.calls))
        self.assertFalse(any('/qemu/' in p for m,p,f in self.calls))
    def test_configure_uses_hostname_and_no_sockets(self):
        op,_=s.CONTROL.submit(self.payload('configure'));s.CONTROL.process(op);self.assertEqual(op['status'],'completed')
        fields=next(f for m,p,f in self.calls if m=='PUT');self.assertEqual(fields['cores'],4);self.assertNotIn('sockets',fields)
    def test_running_container_migration_rejected(self):
        self.vm['status']='running'
        with self.assertRaises(s.Problem):s.CONTROL.submit(self.payload('migrate'))
    def test_unsupported_disk_action_rejected(self):
        with self.assertRaises(s.Problem):s.CONTROL.submit(self.payload('disk_add'))
    def test_container_config_redacts_password(self):
        detail=s.CONTROL.detail(self.conn,201,'pve-01');self.assertEqual(detail['config']['name'],'ct-web');self.assertNotIn('secret',json.dumps(detail))
    def test_ha_add_checks_quorum_and_tracks_state(self):
        original=s.pve.side_effect
        def fake(conn,method,path,fields=None):
            if path=='/cluster/status':return [{'type':'cluster','quorate':1}]
            if path=='/cluster/ha/resources' and method=='POST':self.calls.append((method,path,fields));return None
            return original(conn,method,path,fields)
        s.pve.side_effect=fake
        op,_=s.CONTROL.submit({**self.payload('ha'),'ha_state':'started'});s.CONTROL.process(op)
        self.assertEqual(op['status'],'completed')
        fields=next(f for m,p,f in self.calls if p=='/cluster/ha/resources' and m=='POST')
        self.assertEqual(fields,{'state':'started','sid':'ct:201'})
    def test_ha_without_quorum_is_rejected(self):
        with self.assertRaises(s.Problem):s.CONTROL.submit({**self.payload('ha'),'ha_state':'started'})
    def test_ha_invalid_state_rejected(self):
        with self.assertRaises(s.Problem):s.CONTROL.submit({**self.payload('ha'),'ha_state':'force'})
    def test_ha_quorum_lost_after_queue_does_not_write(self):
        original=s.pve.side_effect
        s.pve.side_effect=lambda conn,method,path,fields=None: [{'type':'cluster','quorate':1}] if path=='/cluster/status' else original(conn,method,path,fields)
        op,_=s.CONTROL.submit({**self.payload('ha'),'ha_state':'started'})
        s.pve.side_effect=original;s.CONTROL.process(op)
        self.assertEqual(op['status'],'failed');self.assertFalse(any(m in ('POST','PUT') for m,p,f in self.calls))

if __name__=='__main__':unittest.main()
