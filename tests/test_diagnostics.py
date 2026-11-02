import http.client,json,tempfile,threading,unittest
from pathlib import Path
import test_backend
s=test_backend.s

class DiagnosticTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();s.DATA=Path(self.temp.name);s.initialize()
        self.http=s.ThreadingHTTPServer(('127.0.0.1',0),s.Handler);threading.Thread(target=self.http.serve_forever,daemon=True).start()
    def tearDown(self):self.http.shutdown();self.http.server_close();self.temp.cleanup()
    def call(self,path,method='GET',body=None,headers=None):
        c=http.client.HTTPConnection('127.0.0.1',self.http.server_port,timeout=5);c.request(method,path,body,headers or {});r=c.getresponse();result=r.status,dict(r.getheaders()),r.read();c.close();return result
    def test_error_id_matches_header_and_log(self):
        with self.assertLogs(level='INFO') as captured:code,headers,raw=self.call('/api/connections?secret=never-log-this')
        data=json.loads(raw);self.assertEqual(code,401);self.assertEqual(data['request_id'],headers['X-Request-ID'])
        logs=' '.join(captured.output);self.assertIn('status=401',logs);self.assertIn(data['request_id'],logs);self.assertNotIn('never-log-this',logs)
    def test_head_returns_headers_without_body(self):
        code,headers,raw=self.call('/','HEAD');self.assertEqual(code,200);self.assertEqual(raw,b'');self.assertGreater(int(headers['Content-Length']),0)
    def test_request_ids_are_unique(self):
        a=self.call('/api/health')[1]['X-Request-ID'];b=self.call('/api/health')[1]['X-Request-ID'];self.assertNotEqual(a,b)
    def test_catalog_requires_authentication(self):self.assertEqual(self.call('/api/catalog')[0],401)
