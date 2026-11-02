import io,json,unittest
from unittest.mock import patch
from urllib.error import URLError
import test_transport
s=test_transport.s
class FailoverTests(unittest.TestCase):
    def setUp(self):
        test_transport.TransportTests.setUp(self);s.PREFERRED_ENDPOINT.clear()
        self.conn.update(id='cluster-test',endpoints=json.dumps(['https://pve2.example:8006']))
    def test_read_switches_and_remembers(self):
        with patch.object(s,'pve_once',side_effect=[s.Problem('offline',502,uncertain=True),[],[]]) as call:
            self.assertEqual(s.pve(self.conn,'GET','/nodes'),[])
            self.assertEqual(s.pve(self.conn,'GET','/nodes'),[])
            self.assertEqual([c.args[0]['url'] for c in call.call_args_list],[self.conn['url'],'https://pve2.example:8006','https://pve2.example:8006'])
    def test_write_only_dispatched_once_after_probe(self):
        def transport(conn,method,path,fields=None):
            if method=='GET' and conn['url']==self.conn['url']:raise s.Problem('offline',502,uncertain=True)
            if method=='GET':return {'version':'9'}
            raise s.Problem('response lost',502,uncertain=True)
        with patch.object(s,'pve_once',side_effect=transport) as call:
            with self.assertRaises(s.Problem):s.pve(self.conn,'POST','/clone')
            writes=[c for c in call.call_args_list if c.args[1]=='POST']
            self.assertEqual(len(writes),1);self.assertEqual(writes[0].args[0]['url'],'https://pve2.example:8006')
    def test_permission_denied_does_not_switch(self):
        with patch.object(s,'pve_once',side_effect=s.Problem('denied',502)) as call:
            with self.assertRaises(s.Problem):s.pve(self.conn,'GET','/nodes')
            self.assertEqual(call.call_count,1)
    def test_wrong_cluster_rejected_even_with_same_name(self):
        def status(conn,*args):return [{'type':'cluster','name':'prod'},{'type':'node','id':'node/'+('a' if conn['url']==self.conn['url'] else 'b'),'name':'host'}]
        with patch.object(s,'pve_once',side_effect=status):
            with self.assertRaises(s.Problem):s.verify_endpoints(self.conn)
    def test_matching_cluster_accepted(self):
        rows=[{'type':'cluster','name':'prod'},{'type':'node','id':'node/a','name':'a'}]
        with patch.object(s,'pve_once',return_value=rows):s.verify_endpoints(self.conn)
    def test_validation(self):
        for invalid in ['http://host:8006','https://user:secret@host:8006','https://host:8006/path','https://host:8006?token=secret']:
            with self.assertRaises(s.Problem):s.endpoint_urls(self.conn['url'],[invalid])
        self.assertEqual(s.endpoint_urls(self.conn['url'],self.conn['url']),[self.conn['url']])
