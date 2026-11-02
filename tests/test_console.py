import base64,contextlib,io,sqlite3,struct,tempfile,time,types,unittest
from pathlib import Path
from unittest.mock import patch
from cryptography.fernet import Fernet
from backend.console import ConsoleGateway,read_frame,server_frame,MAX_FRAME
from test_backend import s

def masked(data,opcode=2,final=True):
    mask=b'abcd';prefix=bytes([(128 if final else 0)|opcode])
    if len(data)<126:prefix+=bytes([128|len(data)])
    else:prefix+=bytes([128|126])+struct.pack('!H',len(data))
    return prefix+mask+bytes(b^mask[i%4] for i,b in enumerate(data))

class ConsoleTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();root=Path(self.tmp.name);(root/'core').mkdir();(root/'core/rfb.js').write_text('// fixture')
        self.cipher=Fernet(Fernet.generate_key());self.calls=[]
        self.values={'Problem':s.Problem,'PUBLIC_ORIGIN':'https://orbit.test','FERNET':self.cipher,'connection':lambda _: {'url':'https://pve.test:8006','kind':'pve','token':self.cipher.encrypt(b'admin@pve!test=secret').decode()},'CONTROL':types.SimpleNamespace(vm=lambda *a:{'vmid':101}),'pve':lambda *a: {'ticket':'ephemeral-ticket','port':5900},'pathpart':s.pathpart,'audit':lambda *a:self.calls.append(a)}
        self.gateway=ConsoleGateway(self.values);self.env=patch.dict('os.environ',{'ORBIT_NOVNC_ROOT':str(root)});self.env.start()
    def tearDown(self):self.env.stop();self.tmp.cleanup()
    def ticket(self):
        with patch.dict('sys.modules',{'websocket':types.SimpleNamespace()}):return self.gateway.create('test',101,'node1',{'id':'owner'})
    def test_frames_roundtrip_binary(self):
        for size in (0,3,130):
            raw=bytes(range(10))*(size//10)+bytes(range(size%10));fin,op,data=read_frame(io.BytesIO(masked(raw)));self.assertTrue(fin);self.assertEqual(op,2);self.assertEqual(data,raw)
            wire=server_frame(2,raw);self.assertEqual(wire[0],130)
    def test_unmasked_and_oversized_frames_rejected(self):
        with self.assertRaises(ValueError):read_frame(io.BytesIO(b'\x82\x01x'))
        with self.assertRaises(ValueError):read_frame(io.BytesIO(b'\x82\xff'+struct.pack('!Q',MAX_FRAME+1)))
    def test_ticket_is_bound_to_session_and_origin(self):
        info=self.ticket();self.assertNotIn('secret',str(info));self.assertEqual(info['ticket'],'ephemeral-ticket')
        with self.assertRaises(s.Problem):self.gateway.claim(info['id'],'other','https://orbit.test')
        with self.assertRaises(s.Problem):self.gateway.claim(info['id'],'owner','https://evil.test')
        item=self.gateway.claim(info['id'],'owner','https://orbit.test');self.assertEqual(item['vmid'],101)
        with self.assertRaises(s.Problem):self.gateway.claim(info['id'],'owner','https://orbit.test')
    def test_expiry_and_pending_limits(self):
        a=self.ticket();self.ticket()
        with self.assertRaises(s.Problem):self.ticket()
        self.gateway.pending[a['id']]['expires']=time.time()-1
        with self.assertRaises(s.Problem):self.gateway.claim(a['id'],'owner','https://orbit.test')
    def test_missing_novnc_fails_explicitly(self):
        with patch.dict('os.environ',{'ORBIT_NOVNC_ROOT':str(Path(self.tmp.name)/'missing')}):
            with self.assertRaises(s.Problem) as e:self.ticket()
        self.assertEqual(e.exception.status,503)
    def test_invalid_upgrade_does_not_consume_ticket(self):
        a=self.ticket();h=types.SimpleNamespace(headers={})
        with self.assertRaises(s.Problem):self.gateway.proxy(h,a['id'],{'id':'owner'})
        self.assertIn(a['id'],self.gateway.pending)

    def test_proxy_handshake_and_binary_relay(self):
        import threading
        sent=threading.Event();captured={}
        class Timeout(Exception):pass
        class Upstream:
            def settimeout(self,value):pass
            def send(self,payload,opcode):captured['input']=(payload,opcode)
            def close(self):pass
            def recv_data(self):
                if not captured.get('greeted'):captured['greeted']=True;return 2,b'RFB 003.008\n'
                time.sleep(.01);raise Timeout()
        upstream=Upstream()
        def connect(url,**kwargs):captured['url']=url;captured['kwargs']=kwargs;return upstream
        module=types.SimpleNamespace(create_connection=connect,WebSocketTimeoutException=Timeout)
        @contextlib.contextmanager
        def db():yield types.SimpleNamespace(execute=lambda *a:types.SimpleNamespace(fetchone=lambda:(1,)))
        self.values.update(db=db,STOP=threading.Event())
        class Output(io.BytesIO):
            def write(self,data):sent.set();return super().write(data)
        class Input(io.BytesIO):
            def read(self,n=-1):sent.wait(2);return super().read(n)
        h=types.SimpleNamespace(headers={'Upgrade':'websocket','Connection':'Upgrade','Sec-WebSocket-Version':'13','Sec-WebSocket-Key':'dGhlIHNhbXBsZSBub25jZQ==','Sec-WebSocket-Protocol':'binary','Origin':'https://orbit.test'},rfile=Input(masked(b'keyboard')+masked(b'',8)),wfile=Output(),connection=types.SimpleNamespace(settimeout=lambda _:None,shutdown=lambda _:None),send_response=lambda code:captured.update(code=code),send_header=lambda key,value:captured.setdefault('headers',{}).update({key:value}),end_headers=lambda:None)
        with patch.dict('sys.modules',{'websocket':module}):
            info=self.gateway.create('test',101,'node1',{'id':'owner'});self.gateway.proxy(h,info['id'],{'id':'owner'})
        self.assertEqual(captured['code'],101);self.assertEqual(captured['headers']['Sec-WebSocket-Accept'],'s3pPLMBiTxaQ9kYGzzhZRbK+xOo=')
        self.assertEqual(captured['input'],(b'keyboard',2));self.assertIn(b'RFB 003.008',h.wfile.getvalue());self.assertEqual(self.gateway.active,0)
        self.assertIn('/qemu/101/vncwebsocket?',captured['url']);self.assertEqual(captured['kwargs']['sslopt']['cert_reqs'],2)
    def test_bad_key_does_not_consume_ticket(self):
        a=self.ticket();h=types.SimpleNamespace(headers={'Upgrade':'websocket','Connection':'upgrade','Sec-WebSocket-Version':'13','Sec-WebSocket-Key':'invalid'})
        with self.assertRaises(s.Problem):self.gateway.proxy(h,a['id'],{'id':'owner'})
        self.assertIn(a['id'],self.gateway.pending)

if __name__=='__main__':unittest.main()
