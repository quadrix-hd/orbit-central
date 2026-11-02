"""VM hardware, creation and recovery workflows; explicit, limited API commands."""
import re
import time
import uuid
import json

class Workspace:
    VM_ACTIONS={'disk_add','disk_resize','nic','metadata','iso','rollback','snapshot_delete','delete'}
    CREATE_ACTIONS={'create','restore'}
    def __init__(self,control):self.control=control;self.s=control.s
    def catalog(self,conn,node):
        s=self.s
        if conn.get('kind')=='pbs':raise s.Problem('Benötigt Proxmox VE.')
        nodes=s.pve(conn,'GET','/nodes')
        if not any(n.get('node')==node and n.get('status')=='online' for n in nodes):raise s.Problem('Online-Node nicht gefunden.')
        base='/nodes/'+s.pathpart(node)
        stores=s.pve(conn,'GET',base+'/storage');net=s.pve(conn,'GET',base+'/network')
        media=[];errors=[]
        for store in stores:
            if not store.get('active'):continue
            for content in ('iso','backup'):
                if content not in store.get('content',''):continue
                try:
                    rows=s.pve(conn,'GET',base+'/storage/'+s.pathpart(store['storage'])+'/content',{'content':content})
                    media.extend({'storage':store['storage'],'content':content,'volid':r['volid'],'size':r.get('size'),'vmid':r.get('vmid')} for r in rows[:300] if r.get('volid'))
                except s.Problem as exc:errors.append(store['storage']+': '+exc.message)
        candidate=int(s.pve(conn,'GET','/cluster/nextid'))
        with s.db() as c:reserved={json.loads(r[0])['vmid'] for table in ('jobs','operations') for r in c.execute('SELECT data FROM '+table) if json.loads(r[0])['connection_id']==conn['id'] and json.loads(r[0])['status'] in ('queued','running','needs_attention')}
        while candidate in reserved:candidate+=1
        return {'nodes':nodes,'node':node,'nextid':candidate,'storages':[r for r in stores if r.get('active')],'bridges':[r['iface'] for r in net if r.get('type')=='bridge'],'media':media,'errors':errors}
    def volume(self,conn,node,volume,content):
        s=self.s
        if not isinstance(volume,str) or len(volume)>512 or ':' not in volume:raise s.Problem('Ungültiges Volume.')
        storage=volume.split(':',1)[0]
        rows=s.pve(conn,'GET','/nodes/'+s.pathpart(node)+'/storage/'+s.pathpart(storage)+'/content',{'content':content})
        if not any(r.get('volid')==volume for r in rows):raise s.Problem('Volume nicht verfügbar oder nicht lesbar.')
        return volume
    def image_store(self,conn,node,name):
        rows=self.s.pve(conn,'GET','/nodes/'+self.s.pathpart(node)+'/storage')
        if not any(r.get('storage')==name and r.get('active') and 'images' in r.get('content','') for r in rows):raise self.s.Problem('Aktives Image-Storage benötigt.')
    def validate(self,conn,vm,data,action):
        s=self.s;node=vm['node'];base='/nodes/'+s.pathpart(node)+'/qemu/'+str(vm['vmid'])
        config=s.pve(conn,'GET',base+'/config')
        if config.get('name')!=vm.get('name'):raise s.Problem('VM-Identität hat sich geändert.')
        if action in self.VM_ACTIONS and vm.get('status')!='stopped':raise s.Problem('Diese Änderung benötigt eine gestoppte VM.')
        if action=='disk_add':
            store=s.text_field(data,'storage',r'[a-zA-Z0-9_.-]+',64);self.image_store(conn,node,store)
            disk=next(('scsi'+str(i) for i in range(31) if 'scsi'+str(i) not in config),None)
            if not disk:raise s.Problem('Kein freier SCSI-Anschluss.')
            return {'device':disk,'storage':store,'size':s.integer(data,'disk',1,65536),'digest':config.get('digest')}
        if action=='disk_resize':
            disk=s.text_field(data,'device',r'(scsi|virtio|sata)\d+',16);size=s.integer(data,'disk',1,65536)
            current=s.disk_gib(config,disk)
            if size<=current:raise s.Problem('Neue Größe muss größer sein als das vorhandene Laufwerk.')
            return {'disk':disk,'size':str(size)+'G'}
        if action=='nic':
            device=s.text_field(data,'device',r'net[0-9]',4);bridge=s.text_field(data,'bridge',r'[a-zA-Z0-9_.-]+',64)
            networks=s.pve(conn,'GET','/nodes/'+s.pathpart(node)+'/network')
            if bridge not in [n['iface'] for n in networks if n.get('type')=='bridge']:raise s.Problem('Bridge nicht gefunden.')
            parts={};old=str(config.get(device,''))
            if old:
                for pair in old.split(','):
                    if '=' in pair:k,v=pair.split('=',1);parts[k]=v
            for key in list(parts):
                if re.fullmatch(r'(virtio|e1000|rtl8139|vmxnet3)',key):
                    mac=parts.pop(key);parts['virtio']=mac
            parts['bridge']=bridge
            tag=data.get('vlan',0)
            if isinstance(tag,bool) or not isinstance(tag,int) or not 0<=tag<=4094:raise s.Problem('VLAN muss 0 bis 4094 sein.')
            if tag:parts['tag']=str(tag)
            else:parts.pop('tag',None)
            if not isinstance(data.get('firewall'),bool):raise s.Problem('Firewall-Option fehlt.')
            parts['firewall']='1' if data['firewall'] else '0'
            prefix='' if 'virtio' in parts else 'virtio,'
            return {device:prefix+','.join(k+'='+v for k,v in parts.items()),'digest':config.get('digest')}
        if action=='metadata':
            name=s.text_field(data,'new_name',r'[a-zA-Z][a-zA-Z0-9-]{0,62}',63)
            note=data.get('description','')
            if not isinstance(note,str) or len(note)>4000:raise s.Problem('Beschreibung zu lang.')
            if not isinstance(data.get('onboot'),bool):raise s.Problem('Autostart-Option fehlt.')
            return {'name':name,'description':note,'onboot':int(data['onboot']),'digest':config.get('digest')}
        if action=='iso':
            volume=data.get('iso','')
            if 'cloudinit' in str(config.get('ide2','')):raise s.Problem('ide2 enthält Cloud-init. Dieses Laufwerk wird nicht durch eine ISO ersetzt.')
            if config.get('ide2') and 'media=cdrom' not in str(config['ide2']):raise s.Problem('ide2 ist kein CD-ROM-Laufwerk und wird nicht überschrieben.')
            if volume:self.volume(conn,node,volume,'iso')
            boot=str(config.get('boot','order=scsi0'))
            if volume and boot.startswith('order='):
                devices=[d for d in boot[6:].split(';') if re.fullmatch(r'(scsi|virtio|sata|ide|net)\d+',d) and d!='ide2'];boot='order=ide2'+(';'+';'.join(devices) if devices else '')
            return {'ide2':(volume or 'none')+',media=cdrom','boot':boot,'digest':config.get('digest')}
        if action in ('rollback','snapshot_delete'):
            name=s.text_field(data,'snapshot_name',r'[a-zA-Z][a-zA-Z0-9_-]{0,39}',40)
            rows=s.pve(conn,'GET',base+'/snapshot')
            if name=='current' or not any(r.get('name')==name for r in rows):raise s.Problem('Snapshot nicht gefunden.')
            if data.get('acknowledge') is not True:raise s.Problem('Auswirkungen ausdrücklich bestätigen.')
            return {'snapname':name}
        if action=='delete':
            if data.get('acknowledge') is not True or data.get('confirm_vmid')!=vm['vmid']:raise s.Problem('Löschen mit VMID und Datenverlustbestätigung bestätigen.')
            return {'purge':1,'destroy-unreferenced-disks':0}
        raise s.Problem('Unbekannte Hardware-Aktion.')
    def command(self,op,base):
        action=op['action'];fields={k:v for k,v in op['args'].items() if v is not None}
        if action=='disk_add':fields={fields['device']:fields['storage']+':'+str(fields['size']),'digest':fields.get('digest')};return 'POST',base+'/config',fields,False
        if action=='disk_resize':return 'PUT',base+'/resize',fields,True
        if action in ('nic','metadata','iso'):return 'PUT',base+'/config',fields,True
        if action=='rollback':return 'POST',base+'/snapshot/'+self.s.pathpart(fields['snapname'])+'/rollback',{'start':0},False
        if action=='snapshot_delete':return 'DELETE',base+'/snapshot/'+self.s.pathpart(fields['snapname']),{},False
        if action=='delete':return 'DELETE',base,fields,False
        return 'POST','/nodes/'+self.s.pathpart(op['node'])+'/qemu',fields,False
    def create(self,data,action):
        s=self.s;cid=s.text_field(data,'connection_id',r'[a-zA-Z0-9-]+',64);conn=s.connection(cid)
        if conn.get('kind')=='pbs':raise s.Problem('Erstellen benötigt Proxmox VE.')
        node=s.text_field(data,'node',r'[a-zA-Z0-9_.-]+',64);vmid=s.integer(data,'vmid',100,999999999);name=s.text_field(data,'name',r'[a-zA-Z][a-zA-Z0-9-]{0,62}',63)
        store=s.text_field(data,'storage',r'[a-zA-Z0-9_.-]+',64);self.image_store(conn,node,store)
        nodes=s.pve(conn,'GET','/nodes')
        if not any(n.get('node')==node and n.get('status')=='online' for n in nodes):raise s.Problem('Online-Node benötigt.')
        if any(r.get('vmid')==vmid or r.get('name')==name for r in s.pve(conn,'GET','/cluster/resources')):raise s.Problem('VMID oder Name bereits vorhanden.',409)
        args={'vmid':vmid,'name':name}
        if action=='restore':
            archive=s.text_field(data,'archive',maxlen=512);self.volume(conn,node,archive,'backup')
            if not (':backup/vm/' in archive or archive.endswith(('.vma','.vma.zst','.vma.gz','.vma.lzo'))):raise s.Problem('QEMU-VM-Backup benötigt, kein Container-Backup.')
            args.update(archive=archive,storage=store,force=0)
        else:
            bridge=s.text_field(data,'bridge',r'[a-zA-Z0-9_.-]+',64)
            if bridge not in [n['iface'] for n in s.pve(conn,'GET','/nodes/'+s.pathpart(node)+'/network') if n.get('type')=='bridge']:raise s.Problem('Bridge nicht gefunden.')
            iso=data.get('iso','')
            if iso:self.volume(conn,node,iso,'iso')
            args.update(cores=s.integer(data,'cpu',1,128),memory=s.integer(data,'ram',1,1024)*1024,sockets=1,scsihw='virtio-scsi-single',scsi0=store+':'+str(s.integer(data,'disk',8,65536)),net0='virtio,bridge='+bridge,ide2=(iso or 'none')+',media=cdrom',ostype='l26',boot='order=ide2;scsi0' if iso else 'order=scsi0',onboot=0)
        return cid,node,vmid,name,args
