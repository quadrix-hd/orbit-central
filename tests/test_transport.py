import importlib.util, io, json, tempfile, unittest
from pathlib import Path
from unittest.mock import patch
from urllib import error, parse
from cryptography.fernet import Fernet
spec=importlib.util.spec_from_file_location('transport_server',Path(__file__).parents[1]/'backend/server.py')
s=importlib.util.module_from_spec(spec);spec.loader.exec_module(s)

class TransportTests(unittest.TestCase):
    def setUp(self):
        s.FERNET=Fernet(Fernet.generate_key());self.conn={'url':'https://pve.example:8006','token':s.FERNET.encrypt(b'user@pve!test=private-secret').decode()}
    def response(self,data):return io.BytesIO(json.dumps(data).encode())
    def test_get_query_and_authorization(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.return_value=self.response({'data':[]})
            self.assertEqual(s.pve(self.conn,'GET','/cluster/resources',{'type':'vm'}),[])
            req=build.return_value.open.call_args.args[0]
            self.assertEqual(req.full_url,'https://pve.example:8006/api2/json/cluster/resources?type=vm')
            self.assertEqual(req.get_header('Authorization'),'PVEAPIToken=user@pve!test=private-secret')
            redirect=build.call_args.args[1];self.assertIsNone(redirect.redirect_request(None,None,None,None,None,None))
    def test_pbs_uses_pbs_authentication_header(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.return_value=self.response({'data':[]})
            s.pve({**self.conn,'kind':'pbs'},'GET','/status/datastore-usage')
            req=build.return_value.open.call_args.args[0]
            self.assertEqual(req.get_header('Authorization'),'PBSAPIToken=user@pve!test:private-secret')
    def test_delete_sends_query_parameters(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.return_value=self.response({'data':'UPID:test'})
            s.pve(self.conn,'DELETE','/nodes/n/qemu/101',{'purge':1})
            req=build.return_value.open.call_args.args[0]
            self.assertEqual(req.full_url,'https://pve.example:8006/api2/json/nodes/n/qemu/101?purge=1');self.assertIsNone(req.data)
    def test_post_form_preserves_values(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.return_value=self.response({'data':'UPID:test'})
            self.assertEqual(s.pve(self.conn,'POST','/path',{'name':'vm-test','cores':2}),'UPID:test')
            req=build.return_value.open.call_args.args[0];self.assertEqual(req.method,'POST');self.assertEqual(parse.parse_qs(req.data.decode()),{'name':['vm-test'],'cores':['2']})
    def test_network_failure_is_uncertain_without_secret(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.side_effect=error.URLError('sensitive internal error private-secret')
            with self.assertRaises(s.Problem) as raised:s.pve(self.conn,'POST','/clone')
        self.assertTrue(raised.exception.uncertain);self.assertNotIn('private-secret',raised.exception.message)
    def test_unauthorized_is_definitive(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.side_effect=error.HTTPError('https://pve.example',403,'denied',{},None)
            with self.assertRaises(s.Problem) as raised:s.pve(self.conn,'POST','/clone')
        self.assertFalse(raised.exception.uncertain);self.assertEqual(raised.exception.status,502)
    def test_malformed_upstream_response(self):
        with patch.object(s.request,'build_opener') as build:
            build.return_value.open.return_value=self.response([])
            with self.assertRaises(s.Problem) as raised:s.pve(self.conn,'GET','/version')
        self.assertTrue(raised.exception.uncertain)
    def test_missing_ca_has_actionable_error(self):
        with patch.object(s.ssl,'create_default_context',side_effect=FileNotFoundError()):
            with self.assertRaises(s.Problem) as raised:s.pve(self.conn,'GET','/version')
        self.assertEqual(raised.exception.status,502);self.assertIn('ORBIT_CA_FILE',raised.exception.message)

if __name__=='__main__':unittest.main()
