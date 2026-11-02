import hashlib,io,json,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
from backend.catalog import Catalog,CatalogError

class CatalogTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.c=Catalog(self.temp.name,'https://catalog.example/catalog.json');self.raw=b'local package payload';self.sha=hashlib.sha256(self.raw).hexdigest()
        self.manifest={'schema':1,'revision':'2026.11','packages':[{'id':'ubuntu','name':'Ubuntu','version':'1','description':'Prepared image','category':'OS','assets':[{'name':'ubuntu.img','url':'assets/ubuntu.img','size':len(self.raw),'sha256':self.sha}]}]}
    def tearDown(self):self.temp.cleanup()
    def response(self,url):return io.BytesIO(json.dumps(self.manifest).encode() if url==self.c.url else self.raw)
    def test_all_assets_cached_and_available_offline(self):
        with patch.object(self.c,'fetch',side_effect=self.response):self.assertTrue(self.c.sync())
        self.assertEqual(self.c.status()['revision'],'2026.11');self.assertEqual(self.c.artifact(self.sha)[0].read_bytes(),self.raw)
        with patch.object(self.c,'fetch',side_effect=OSError('offline secret')):self.assertFalse(self.c.sync())
        self.assertEqual(self.c.artifact(self.sha)[0].read_bytes(),self.raw);self.assertNotIn('secret',self.c.status()['error'])
    def test_failed_update_retains_previous_snapshot(self):
        with patch.object(self.c,'fetch',side_effect=self.response):self.c.sync()
        self.manifest['revision']='next';self.manifest['packages'][0]['assets'][0]['sha256']='0'*64
        with patch.object(self.c,'fetch',side_effect=self.response):self.assertFalse(self.c.sync())
        self.assertEqual(self.c.status()['revision'],'2026.11');self.assertIsNotNone(self.c.artifact(self.sha))
    def test_cached_valid_assets_are_not_redownloaded(self):
        with patch.object(self.c,'fetch',side_effect=self.response):self.c.sync()
        with patch.object(self.c,'fetch',side_effect=self.response) as fetch:self.assertTrue(self.c.sync());self.assertEqual(fetch.call_count,1)
    def test_corrupted_cache_is_repaired(self):
        with patch.object(self.c,'fetch',side_effect=self.response):self.c.sync()
        (self.c.root/'objects'/self.sha).write_bytes(b'x'*len(self.raw));self.assertIsNone(self.c.artifact(self.sha))
        with patch.object(self.c,'fetch',side_effect=self.response):self.assertTrue(self.c.sync())
        self.assertEqual(self.c.artifact(self.sha)[0].read_bytes(),self.raw)
    def test_foreign_origin_rejected(self):
        self.manifest['packages'][0]['assets'][0]['url']='https://other.example/file'
        with self.assertRaises(CatalogError):self.c.validate(self.manifest)
    def test_paths_and_oversized_catalog_rejected(self):
        self.manifest['packages'][0]['assets'][0]['name']='../../secret'
        with self.assertRaises(CatalogError):self.c.validate(self.manifest)
        with patch.object(self.c,'fetch',return_value=io.BytesIO(b'x'*(2*1024**2+1))):self.assertFalse(self.c.sync())
    def test_size_and_download_limit_enforced(self):
        self.c.limit=1
        with self.assertRaises(CatalogError):self.c.validate(self.manifest)
        self.c.limit=1000;self.manifest['packages'][0]['assets'][0]['size']=1
        with patch.object(self.c,'fetch',side_effect=self.response):self.assertFalse(self.c.sync())
        self.assertIsNone(self.c.status()['revision'])
    def test_concurrent_sync_is_not_started_twice(self):
        self.c.lock.acquire()
        try:self.assertFalse(self.c.sync())
        finally:self.c.lock.release()
    def test_duplicate_package_rejected(self):
        self.manifest['packages'].append(dict(self.manifest['packages'][0]))
        with self.assertRaises(CatalogError):self.c.validate(self.manifest)
