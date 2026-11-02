"""Session-bound noVNC gateway. Permanent PVE credentials never reach the browser."""
import base64
import hashlib
import os
import secrets
import socket
import ssl
import struct
import threading
import time
from urllib.parse import urlencode,urlsplit

MAX_FRAME=16*1024*1024

def read_exact(stream,size):
    parts=[]
    while size:
        part=stream.read(size)
        if not part:raise EOFError()
        parts.append(part);size-=len(part)
    return b''.join(parts)

def read_frame(stream):
    a,b=read_exact(stream,2)
    if a&0x70 or not b&0x80:raise ValueError('Invalid WebSocket frame')
    size=b&127
    if size==126:size=struct.unpack('!H',read_exact(stream,2))[0]
    elif size==127:size=struct.unpack('!Q',read_exact(stream,8))[0]
    if size>MAX_FRAME:raise ValueError('Frame too large')
    opcode=a&15
    if opcode>=8 and (not a&128 or size>125):raise ValueError('Invalid control frame')
    mask=read_exact(stream,4);raw=bytearray(read_exact(stream,size))
    for i in range(len(raw)):raw[i]^=mask[i%4]
    return bool(a&128),opcode,bytes(raw)

def server_frame(opcode,data=b''):
    length=len(data)
    prefix=bytes([128|opcode])
    if length<126:prefix+=bytes([length])
    elif length<65536:prefix+=bytes([126])+struct.pack('!H',length)
    else:prefix+=bytes([127])+struct.pack('!Q',length)
    return prefix+data

class ConsoleGateway:
    def __init__(self,values):self.values=values;self.lock=threading.Lock();self.pending={};self.active=0
    def get(self,key):return self.values[key]
    def create(self,cid,vmid,node,session):
        Problem=self.get('Problem');conn=self.get('connection')(cid)
        if conn.get('kind')=='pbs':raise Problem('Konsolen benötigen Proxmox VE.')
        root=os.environ.get('ORBIT_NOVNC_ROOT','/usr/share/novnc')
        if not os.path.isfile(os.path.join(root,'core','rfb.js')):raise Problem('noVNC ist nicht installiert. Docker-Image neu bauen oder ORBIT_NOVNC_ROOT setzen.',503)
        try:import websocket  # installed with the Docker requirements
        except ImportError:raise Problem('WebSocket-Abhängigkeit fehlt. Docker-Image neu bauen.',503)
        vm=self.get('CONTROL').vm(conn,vmid,node)
        if vm.get('type')=='lxc':raise Problem('LXC-Terminal ist noch nicht integriert.')
        with self.lock:
            now=time.time();self.pending={k:v for k,v in self.pending.items() if v['expires']>now}
            if self.active>=4 or sum(v['session']==session['id'] for v in self.pending.values())>=2:raise Problem('Zu viele offene Konsolen. Bestehende Sitzungen schließen.',429)
        path='/nodes/'+self.get('pathpart')(node)+'/qemu/'+str(vmid)+'/vncproxy'
        result=self.get('pve')(conn,'POST',path,{'websocket':1})
        if not isinstance(result,dict) or not isinstance(result.get('ticket'),str) or not isinstance(result.get('port'),int):raise Problem('Ungültige Konsolenantwort von Proxmox.',502)
        key=secrets.token_urlsafe(32)
        with self.lock:self.pending[key]={'session':session['id'],'connection':cid,'endpoint':conn.get('active_endpoint',conn['url']),'node':node,'vmid':vmid,'port':result['port'],'ticket':result['ticket'],'expires':time.time()+60}
        self.get('audit')('console.open.requested',cid+'/'+str(vmid))
        return {'id':key,'ticket':result['ticket'],'expires_in':60}
    def claim(self,key,session_id,origin):
        Problem=self.get('Problem')
        if origin!=self.get('PUBLIC_ORIGIN'):raise Problem('Konsolen-Ursprung nicht erlaubt.',403)
        with self.lock:
            item=self.pending.get(key)
            if not item or item['expires']<=time.time():raise Problem('Konsolenfreigabe abgelaufen. Konsole erneut öffnen.',410)
            if item['session']!=session_id:raise Problem('Konsolenfreigabe gehört zu einer anderen Sitzung.',403)
            if self.active>=4:raise Problem('Konsolenlimit erreicht.',429)
            del self.pending[key];self.active+=1
        return item
    def proxy(self,h,key,session):
        Problem=self.get('Problem')
        if h.headers.get('Upgrade','').lower()!='websocket' or 'upgrade' not in h.headers.get('Connection','').lower() or h.headers.get('Sec-WebSocket-Version')!='13':raise Problem('WebSocket-Upgrade erwartet.',400)
        wskey=h.headers.get('Sec-WebSocket-Key','')
        try:
            if len(base64.b64decode(wskey,validate=True))!=16:raise ValueError()
        except Exception:raise Problem('Ungültiger WebSocket-Schlüssel.')
        item=self.claim(key,session['id'],h.headers.get('Origin',''))
        upstream=None;started=False;done=threading.Event();write_lock=threading.Lock()
        try:
            try:import websocket
            except ImportError:raise Problem('WebSocket-Abhängigkeit fehlt.',503)
            conn=self.get('connection')(item['connection']);token=self.get('FERNET').decrypt(conn['token'].encode()).decode()
            conn['url']=item.get('endpoint',conn['url'])
            url=urlsplit(conn['url']);path='/api2/json/nodes/'+self.get('pathpart')(item['node'])+'/qemu/'+str(item['vmid'])+'/vncwebsocket'
            target='wss://'+url.netloc+path+'?'+urlencode({'port':item['port'],'vncticket':item['ticket']})
            options={'cert_reqs':ssl.CERT_REQUIRED}
            if os.environ.get('ORBIT_CA_FILE'):options['ca_certs']=os.environ['ORBIT_CA_FILE']
            try:upstream=websocket.create_connection(target,header=['Authorization: PVEAPIToken='+token],sslopt=options,timeout=25,origin=conn['url'],subprotocols=['binary'])
            except Exception:raise Problem('Proxmox-Konsolenverbindung abgelehnt. VM.Console, TLS und API-Token-Unterstützung dieser VE-Version prüfen.',502)
            accept=base64.b64encode(hashlib.sha1((wskey+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
            h.protocol_version='HTTP/1.1'
            h.send_response(101);h.send_header('Upgrade','websocket');h.send_header('Connection','Upgrade');h.send_header('Sec-WebSocket-Accept',accept)
            if 'binary' in h.headers.get('Sec-WebSocket-Protocol','').split(','):h.send_header('Sec-WebSocket-Protocol','binary')
            h.end_headers();h.wfile.flush();h.connection.settimeout(1800);started=True;upstream.settimeout(1)
            def send(opcode,payload):
                with write_lock:h.wfile.write(server_frame(opcode,payload));h.wfile.flush()
            def downstream():
                deadline=time.monotonic()+1800
                try:
                    while not done.is_set() and not self.get('STOP').is_set() and time.monotonic()<deadline:
                        # Authentication expiry/logout also ends an already-open console.
                        with self.get('db')() as c:valid=c.execute('SELECT 1 FROM sessions WHERE id=? AND expires>?',(session['id'],time.time())).fetchone()
                        if not valid:break
                        try:opcode,payload=upstream.recv_data()
                        except websocket.WebSocketTimeoutException:continue
                        if opcode==8:break
                        if opcode in (1,2):
                            if len(payload)>MAX_FRAME:break
                            send(2,payload.encode() if isinstance(payload,str) else payload)
                except Exception:pass
                finally:
                    done.set()
                    try:send(8,struct.pack('!H',1000))
                    except Exception:pass
                    try:h.connection.shutdown(socket.SHUT_RDWR)
                    except OSError:pass
            reader=threading.Thread(target=downstream,daemon=True);reader.start()
            fragments=bytearray();message_opcode=None
            while not done.is_set() and not self.get('STOP').is_set():
                final,opcode,payload=read_frame(h.rfile)
                if opcode==8:break
                if opcode==9:send(10,payload);continue
                if opcode==10:continue
                if opcode in (1,2):
                    if message_opcode is not None:raise ValueError('Nested fragments')
                    message_opcode=opcode
                elif opcode!=0 or message_opcode is None:raise ValueError('Invalid continuation')
                fragments.extend(payload)
                if len(fragments)>MAX_FRAME:raise ValueError('Message too large')
                if final:upstream.send(bytes(fragments),opcode=2);fragments.clear();message_opcode=None
        except Exception:
            if not started:raise
        finally:
            done.set()
            if upstream:
                try:upstream.close()
                except Exception:pass
            with self.lock:self.active=max(0,self.active-1)
            if started:
                h.close_connection=True
                try:self.get('audit')('console.closed',item['connection']+'/'+str(item['vmid']))
                except Exception:pass
        return True
