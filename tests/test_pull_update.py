import os,subprocess,tempfile,unittest
from pathlib import Path
SCRIPT=Path(__file__).parents[1]/'scripts/pull-update.sh'
class PullUpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name);self.upstream=self.root/'upstream';self.upstream.mkdir();self.local=self.root/'local'
        self.env={**os.environ,'GIT_AUTHOR_NAME':'Fixture','GIT_AUTHOR_EMAIL':'fixture@example.invalid','GIT_COMMITTER_NAME':'Fixture','GIT_COMMITTER_EMAIL':'fixture@example.invalid'}
        self.git(self.upstream,'init','-b','main');(self.upstream/'scripts').mkdir()
        (self.upstream/'compose.yaml').write_text('services:\n  orbit:\n    environment:\n      OLD: old\n    volumes:\n      # - ./certs/proxmox-ca.pem:/certs/proxmox-ca.pem:ro\n')
        (self.upstream/'scripts/update.sh').write_text('#!/bin/bash\ntouch updated.flag\n')
        (self.upstream/'.gitignore').write_text('.env\nbackups/\nupdated.flag\n');(self.upstream/'README.md').write_text('original\n')
        self.git(self.upstream,'add','.');self.git(self.upstream,'commit','-m','initial');self.git(self.root,'clone',str(self.upstream),str(self.local))
        (self.local/'.env').write_text('ORBIT_CA_FILE=/certs/proxmox-ca.pem\nSECRET=preserved\n');p=self.local/'compose.yaml';p.write_text(p.read_text().replace('      # -','      -'))
    def tearDown(self):self.temp.cleanup()
    def git(self,cwd,*args):return subprocess.run(['git',*args],cwd=cwd,env=self.env,check=True,capture_output=True,text=True)
    def run_update(self):return subprocess.run(['bash',str(SCRIPT),str(self.local)],env=self.env,capture_output=True,text=True,timeout=20)
    def test_update_preserves_env_and_local_ca_mount(self):
        p=self.upstream/'compose.yaml';p.write_text(p.read_text().replace('      OLD: old','      NEW: new\n      OLD: old'));self.git(self.upstream,'add','.');self.git(self.upstream,'commit','-m','update')
        result=self.run_update();self.assertEqual(result.returncode,0,result.stderr);self.assertIn('NEW: new',(self.local/'compose.yaml').read_text());self.assertIn('      - ./certs/',(self.local/'compose.yaml').read_text());self.assertIn('SECRET=preserved',(self.local/'.env').read_text());self.assertTrue((self.local/'updated.flag').exists());self.assertEqual(len(list((self.local/'backups').glob('config-*/.env'))),1)
    def test_compose_conflict_aborts_without_overwrite(self):
        before=(self.local/'compose.yaml').read_text();p=self.upstream/'compose.yaml';p.write_text(p.read_text().replace('# - ./certs/proxmox-ca.pem:/certs/proxmox-ca.pem:ro','# remove mount here'));self.git(self.upstream,'add','.');self.git(self.upstream,'commit','-m','conflict')
        result=self.run_update();self.assertNotEqual(result.returncode,0);self.assertEqual((self.local/'compose.yaml').read_text(),before);self.assertFalse((self.local/'updated.flag').exists())
    def test_other_local_changes_are_not_overwritten(self):
        (self.local/'README.md').write_text('my edits\n');result=self.run_update();self.assertNotEqual(result.returncode,0);self.assertEqual((self.local/'README.md').read_text(),'my edits\n');self.assertFalse((self.local/'updated.flag').exists())
