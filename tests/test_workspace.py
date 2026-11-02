import json,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import patch
import test_backend as base
s=base.s

class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();s.DATA=Path(self.tmp.name);s.initialize();self.calls=[]
        self.config={'name':'web','cores':2,'memory':4096,'digest':'test-digest','scsi0':'local:disk,size=32G','ide2':'none,media=cdrom','boot':'order=scsi0','net0':'virtio=BC:24:11:10:20:01,bridge=vmbr0,rate=100'}
        self.resources=[{'type':'qemu','vmid':101,'name':'web','node':'node1','status':'stopped'}]
        with s.db() as c:c.execute('INSERT INTO connections(id,name,url,token,created,kind) VALUES(?,?,?,?,?,?)',('test','test','https://pve.test',s.FERNET.encrypt(b'user@pve!id=secret').decode(),0,'pve'))
        def fake(conn,method,path,fields=None):
            self.calls.append((method,path,fields))
            if path=='/cluster/resources':return self.resources
            if path=='/cluster/nextid':return '200'
            if path=='/nodes':return [{'node':'node1','status':'online'}]
            if path.endswith('/storage'):return [{'storage':'local','active':1,'content':'images,iso,backup'}]
            if path.endswith('/network'):return [{'iface':'vmbr0','type':'bridge'},{'iface':'vmbr1','type':'bridge'}]
            if path.endswith('/content'):return [{'volid':'local:iso/install.iso'}] if fields.get('content')=='iso' else [{'volid':'local:backup/vzdump-qemu-101.vma.zst'}]
            if path.endswith('/config') and method=='GET':return self.config
            if path.endswith('/snapshot') and method=='GET':return [{'name':'before'},{'name':'current'}]
            if path.endswith('/status'):return {'status':'stopped','exitstatus':'OK'}
            if method in ('POST','DELETE'):return 'UPID:node1:test'
            return None
        self.mock=patch.object(s,'pve',side_effect=fake);self.mock.start()
    def tearDown(self):self.mock.stop();self.tmp.cleanup()
    def data(self,action):return {'request_key':str(uuid.uuid4()),'connection_id':'test','node':'node1','vmid':101,'confirm_name':'web','action':action,'disk':64,'storage':'local','device':'scsi0','bridge':'vmbr1','vlan':12,'firewall':True,'new_name':'web-renamed','description':'note','onboot':True,'snapshot_name':'before','acknowledge':True,'confirm_vmid':101,'iso':'local:iso/install.iso'}
    def execute(self,data):
        op,status=s.CONTROL.submit(data);self.assertEqual(status,202);self.calls.clear();s.CONTROL.process(op);self.assertEqual(op['status'],'completed',op)
        with s.db() as c:c.execute('DELETE FROM operations WHERE id=?',(op['id'],))
        return op
    def test_add_disk_waits_for_async_config_task(self):
        self.execute(self.data('disk_add'));self.assertTrue(any(m=='POST' and p.endswith('/config') and f.get('scsi1')=='local:64' for m,p,f in self.calls));self.assertTrue(any('/tasks/' in p for _,p,_ in self.calls))
    def test_resize_refuses_shrink(self):
        data=self.data('disk_resize');data['disk']=16
        with self.assertRaises(s.Problem):s.CONTROL.submit(data)
        self.execute(self.data('disk_resize'));self.assertTrue(any(p.endswith('/resize') and f['size']=='64G' for _,p,f in self.calls))
    def test_nic_preserves_mac_and_other_properties(self):
        data=self.data('nic');data['device']='net0';self.execute(data)
        config=next(f for m,p,f in self.calls if m=='PUT' and p.endswith('/config'))
        self.assertIn('virtio=BC:24:11:10:20:01',config['net0']);self.assertIn('rate=100',config['net0']);self.assertIn('tag=12',config['net0']);self.assertIn('bridge=vmbr1',config['net0'])
    def test_iso_does_not_overwrite_cloudinit(self):
        self.config['ide2']='local:vm-101-cloudinit';
        with self.assertRaises(s.Problem):s.CONTROL.submit(self.data('iso'))
    def test_metadata_and_iso(self):
        self.execute(self.data('metadata'));self.assertTrue(any(f.get('name')=='web-renamed' for m,p,f in self.calls if m=='PUT'))
        self.execute(self.data('iso'));self.assertTrue(any(f.get('boot')=='order=ide2;scsi0' for m,p,f in self.calls if m=='PUT'))
    def test_snapshot_rollback_and_delete(self):
        for action,suffix in [('rollback','/snapshot/before/rollback'),('snapshot_delete','/snapshot/before')]:
            with self.subTest(action=action):self.execute(self.data(action));self.assertTrue(any(p.endswith(suffix) for _,p,_ in self.calls))
    def test_deletion_requires_additional_confirmation(self):
        data=self.data('delete');data['acknowledge']=False
        with self.assertRaises(s.Problem):s.CONTROL.submit(data)
        data['acknowledge']=True;data['confirm_vmid']=999
        with self.assertRaises(s.Problem):s.CONTROL.submit(data)
        self.execute(self.data('delete'));self.assertTrue(any(m=='DELETE' and p.endswith('/qemu/101') for m,p,_ in self.calls))
    def creation(self,action='create'):
        d=self.data(action);d.update(vmid=201,name='new-vm',cpu=2,ram=4,archive='local:backup/vzdump-qemu-101.vma.zst');return d
    def test_create_from_iso_and_restore_use_new_vmid(self):
        for action in ('create','restore'):
            with self.subTest(action=action):
                op=self.execute(self.creation(action));fields=next(f for m,p,f in self.calls if m=='POST' and p.endswith('/qemu'));self.assertEqual(fields['vmid'],201)
                if action=='create':self.assertEqual(fields['ide2'],'local:iso/install.iso,media=cdrom')
                else:self.assertEqual(fields['force'],0)
    def test_create_refuses_used_vmid_and_duplicate_reservation(self):
        data=self.creation();data['vmid']=101
        with self.assertRaises(s.Problem):s.CONTROL.submit(data)
        data['vmid']=201;s.CONTROL.submit(data)
        with self.assertRaises(s.Problem):s.CONTROL.submit({**data,'request_key':str(uuid.uuid4())})
    def test_catalog_skips_reserved_orbit_ids(self):
        data=self.creation();data['vmid']=200;s.CONTROL.submit(data)
        result=s.CONTROL.workspace.catalog(s.connection('test'),'node1');self.assertEqual(result['nextid'],201);self.assertEqual(len(result['media']),2)
    def test_hardware_changes_refuse_running_vm(self):
        self.resources[0]['status']='running'
        for action in ('nic','disk_add','metadata','delete'):
            with self.subTest(action=action),self.assertRaises(s.Problem):s.CONTROL.submit(self.data(action))

if __name__=='__main__':unittest.main()
