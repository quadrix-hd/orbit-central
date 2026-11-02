"""Exercise shell branching with a command stub; no Docker runtime is emulated."""
import json, os, shutil, signal, subprocess, tempfile, unittest
from pathlib import Path

FAKE=r'''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
args=sys.argv[1:];root=Path(os.environ['CASE_ROOT'])
with open(root/'calls','a') as f:f.write(json.dumps({'args':args,'image':os.environ.get('ORBIT_IMAGE','')})+'\n')
if args==['compose','config','--images']:print('orbit-console:local')
elif args[:4]==['compose','ps','-q','orbit']:print('container')
elif args[0]=='inspect':print('sha256:old')
elif args[:3]==['compose','build','orbit'] and os.environ.get('FAIL_AT')=='build':sys.exit(1)
elif 'scripts/backup.py' in args:
 if os.environ.get('FAIL_AT')=='backup':sys.exit(1)
 print('/data/backups/test.tar.gz')
elif args[:3]==['compose','up','-d']:
 countfile=root/'up-count';count=int(countfile.read_text()) if countfile.exists() else 0;countfile.write_text(str(count+1))
 if os.environ.get('FAIL_AT')=='health' and count==0:sys.exit(1)
elif '-c' in args and 'read_bytes' in args[args.index('-c')+1]:sys.stdout.write('mock-archive')
'''

class UpdateFlowTests(unittest.TestCase):
    def run_case(self,fail=''):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);(root/'scripts').mkdir();(root/'bin').mkdir()
            shutil.copy(Path(__file__).parents[1]/'scripts/update.sh',root/'scripts/update.sh')
            (root/'scripts/restore.py').write_text('# test placeholder')
            fake=root/'bin/docker';fake.write_text(FAKE);fake.chmod(0o755)
            env={**os.environ,'CASE_ROOT':temp,'FAIL_AT':fail,'PATH':str(root/'bin')+os.pathsep+os.environ['PATH']};env.pop('ORBIT_IMAGE',None)
            proc=subprocess.Popen(['bash','scripts/update.sh'],cwd=root,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=True)
            try:stdout,stderr=proc.communicate(timeout=90)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid,signal.SIGKILL);proc.communicate();raise
            result=subprocess.CompletedProcess(proc.args,proc.returncode,stdout,stderr)
            calls=[json.loads(x) for x in (root/'calls').read_text().splitlines()]
            return result,calls
    def test_success_creates_backup_and_unsets_maintenance(self):
        result,calls=self.run_case();self.assertEqual(result.returncode,0,result.stderr)
        self.assertTrue(any('scripts/backup.py' in c['args'] and c['image'].startswith('orbit-console:rollback-') for c in calls))
        self.assertTrue(any(any('maintenance").unlink' in a for a in c['args']) for c in calls))
    def test_failed_build_leaves_running_service_untouched(self):
        result,calls=self.run_case('build');self.assertNotEqual(result.returncode,0)
        self.assertFalse(any(c['args'][:2]==['compose','stop'] for c in calls))
    def test_health_failure_restores_backup_and_old_image(self):
        result,calls=self.run_case('health');self.assertNotEqual(result.returncode,0)
        restore=[c for c in calls if '/tmp/orbit-restore.py' in c['args']]
        self.assertEqual(len(restore),1);self.assertTrue(restore[0]['image'].startswith('orbit-console:rollback-'));self.assertIn('--keep-maintenance',restore[0]['args'])
        self.assertEqual(len([c for c in calls if c['args'][:3]==['compose','up','-d']]),2)
    def test_backup_failure_restarts_previous_without_restoring(self):
        result,calls=self.run_case('backup');self.assertNotEqual(result.returncode,0)
        self.assertFalse(any('/tmp/orbit-restore.py' in c['args'] for c in calls));self.assertTrue(any(c['args'][:3]==['compose','up','-d'] for c in calls))

if __name__=='__main__':unittest.main()
