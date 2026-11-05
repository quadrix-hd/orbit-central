'use strict';
// End-to-end test: real Orbit server (live mode) against the stateful Proxmox
// simulator, driven through the browser like an administrator would.
// Usage: npm run e2e   (requires Go, openssl and Playwright/Chromium)
const { execFileSync, spawn } = require('child_process');
const fs = require('fs'), os = require('os'), path = require('path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..', '..');
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'orbit-e2e-'));
const PVE_PORT = 18006, ORBIT_PORT = 18090, ORIGIN = `http://127.0.0.1:${ORBIT_PORT}`;
const procs = [], failures = [], passes = [];
const sleep = ms => new Promise(r => setTimeout(r, ms));

function certs() {
  const ssl = (...a) => execFileSync('openssl', a, { cwd: work, stdio: 'pipe' });
  ssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'ca.key', '-out', 'ca.crt', '-days', '2', '-subj', '/CN=Orbit E2E CA');
  ssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'server.key', '-out', 'server.csr', '-subj', '/CN=pve1');
  fs.writeFileSync(path.join(work, 'san.ext'), 'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:127.0.0.2,IP:127.0.0.3\n');
  ssl('x509', '-req', '-in', 'server.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-out', 'server.crt', '-days', '2', '-extfile', 'san.ext');
}
function start(cmd, args, env, name) {
  const p = spawn(cmd, args, { env: { ...process.env, ...env }, stdio: ['ignore', 'pipe', 'pipe'] });
  const logFile = fs.createWriteStream(path.join(work, name + '.log'));
  p.stdout.pipe(logFile); p.stderr.pipe(logFile);
  procs.push(p); return p;
}
async function waitFor(url, tries = 60) {
  for (let i = 0; i < tries; i++) { try { const r = await fetch(url); if (r.ok) return } catch {} await sleep(250) }
  throw new Error('timeout waiting for ' + url);
}
let currentPage = null;
const stepLog = [];
async function step(name, fn) {
  const started = Date.now(); try { return await runStep(name, fn) } finally { stepLog.push({ name, start: started, end: Date.now(), ok: !failures.some(f => f.startsWith(name + ':')) }); fs.writeFileSync(path.join(work, 'steps.json'), JSON.stringify(stepLog)) }
}
async function runStep(name, fn) {
  if (process.env.E2E_ONLY && failures.length) { console.log('  -', name, '(übersprungen)'); return }
  try { await fn(); passes.push(name); console.log('  ✓', name) }
  catch (e) {
    failures.push(name + ': ' + (e.message || e).split('\n')[0]); console.log('  ✗', name, '-', (e.message || e).split('\n')[0]);
    if (currentPage) { const f = path.join(work, 'fail-' + failures.length); await currentPage.screenshot({ path: f + '.png' }).catch(() => {}); fs.writeFileSync(f + '.txt', await currentPage.evaluate(() => document.body.innerText).catch(() => '')) }
  }
}
const expect = (cond, msg) => { if (!cond) throw new Error(msg) };
function totp(secret, at = Date.now()) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const c of secret.toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g).map(b => parseInt(b, 2)));
  const msg = Buffer.alloc(8); msg.writeBigUInt64BE(BigInt(Math.floor(at / 30000)));
  const h = require('crypto').createHmac('sha1', key).update(msg).digest(), o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0');
}

(async () => {
  certs();
  const bin = path.join(work, 'orbit');
  execFileSync('go', ['build', '-o', bin, './cmd/orbit'], { cwd: root, stdio: 'inherit' });
  start('node', [path.join(__dirname, 'mock-pve.cjs'), work, String(PVE_PORT)], { MOCK_RECORD: path.join(work, 'api.jsonl') }, 'mock');
  const data = path.join(work, 'data');
  start(bin, [], { ORBIT_DATA_DIR: data, PORT: String(ORBIT_PORT), ORBIT_STATIC_DIR: path.join(root, 'dist'), ORBIT_PUBLIC_ORIGIN: ORIGIN, ORBIT_CA_FILE: path.join(work, 'ca.crt'), ORBIT_CATALOG_URL: '', ORBIT_NOVNC_ROOT: path.join(work, 'novnc') }, 'orbit');
  await waitFor(ORIGIN + '/api/health');

  const browser = await chromium.launch();
  const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 } })).newPage();
  const jsErrors = [];
  currentPage = page;
  page.on('pageerror', e => jsErrors.push(e.message));
  page.on('console', m => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) jsErrors.push(m.text()) });
  const api = (p, method = 'GET', body) => page.evaluate(async ([p, method, body]) => {
    const r = await fetch('/api/' + p, { method, headers: { 'Content-Type': 'application/json', 'X-Orbit-CSRF': state.csrf || '' }, body: body ? JSON.stringify(body) : undefined });
    return { status: r.status, data: await r.json().catch(() => null) };
  }, [p, method, body]);
  const waitOp = async (match, timeout = 20000) => {
    const until = Date.now() + timeout;
    while (Date.now() < until) {
      const ops = (await api('operations')).data || [];
      const op = ops.find(match);
      if (op && ['completed', 'failed', 'needs_attention'].includes(op.status)) { await page.evaluate(() => { if (modal.open) closeModal(true) }); return op }
      await sleep(400);
    }
    throw new Error('operation did not finish');
  };
  const shot = n => page.screenshot({ path: path.join(work, n + '.png') });
  console.log('Orbit E2E · work dir', work);

  await step('Ersteinrichtung mit Einmalcode', async () => {
    await page.goto(ORIGIN);
    await page.waitForSelector('input[name=token]', { timeout: 10000 });
    await page.fill('input[name=token]', fs.readFileSync(path.join(data, 'setup-token'), 'utf8').trim());
    await page.fill('input[name=password]', 'e2e-test-password-123');
    const second = await page.$('input[name=repeat]');
    if (second) await second.fill('e2e-test-password-123');
    await page.click('form button.primary, form button[type=submit]');
    await page.waitForSelector('.prism-topbar', { timeout: 10000 });
  });
  await step('Proxmox-Verbindung anlegen (API, eigene CA)', async () => {
    const r = await api('connections', 'POST', { name: 'Mock-DC', url: `https://127.0.0.1:${PVE_PORT}`, token: 'orbit@pve!e2e=11111111-2222-3333-4444-555555555555', kind: 'pve', endpoints: '' });
    expect(r.status === 200 || r.status === 201, 'status ' + r.status + ' ' + JSON.stringify(r.data));
    await page.reload(); await page.waitForSelector('.prism-topbar');
  });
  await step('Dashboard zeigt Cluster, Hosts und Health', async () => {
    await page.waitForFunction(() => state.resources.filter(r => r.type === 'node').length === 3, null, { timeout: 15000 });
    const text = await page.textContent('#page');
    expect(/Infrastruktur-Health/.test(text) && /3\/3/.test(text.replace(/\s/g, '')), 'dashboard content');
    await shot('dashboard');
  });
  await step('VM-Liste: alle Gäste, Filter, Sortierung', async () => {
    await page.evaluate(() => nav('vms'));
    await page.waitForSelector('[data-vm]');
    expect((await page.$$('[data-vm]')).length >= 4, 'guest rows');
    await page.click('[data-vm-filter="stopped"]');
    const stopped = await page.$$eval('[data-vm]', e => e.map(x => x.textContent));
    expect(stopped.includes('db-02') && !stopped.includes('web-01'), 'stopped filter ' + stopped);
    await page.click('[data-vm-filter="all"]');
  });
  const actOnVM = async (key, action, fill = async () => {}) => {
    await page.evaluate(k => vmDetails(state.connections[0].id + ':' + k.split(':')[1]), key);
    await page.waitForFunction(() => state.object && !state.object.loading, null, { timeout: 10000 });
    await page.waitForSelector('[data-core-action]');
    await page.evaluate(a => vmAction(state.object.vm, a, state.object.config), action);
    await page.waitForSelector('#vm-action-form');
    await fill();
    await page.fill('#f-confirm_name', await page.evaluate(() => state.object.vm.name));
    await page.click('#vm-action-form button.primary');
  };
  await step('VM starten (db-02)', async () => {
    await actOnVM('mock-dc:102', 'start');
    const op = await waitOp(o => o.vmid === 102 && o.action === 'start');
    expect(op.status === 'completed', 'status ' + op.status + ' ' + JSON.stringify(op.events?.slice(-1)));
  });
  await step('Snapshot erstellen (web-01)', async () => {
    await actOnVM('mock-dc:100', 'snapshot', () => page.fill('#f-snapshot_name', 'e2e-snap'));
    const op = await waitOp(o => o.vmid === 100 && o.action === 'snapshot');
    expect(op.status === 'completed', 'status ' + op.status);
  });
  await step('Klonen von Vorlage (verknüpft) auf pve2', async () => {
    await actOnVM('mock-dc:9000', 'clone', async () => { await page.fill('#f-newid', '150'); await page.fill('#f-new_name', 'clone-e2e'); await page.selectOption('select[name=target]', 'pve2'); await page.uncheck('input[name=full]') });
    const op = await waitOp(o => o.action === 'clone' && o.vmid === 9000);
    expect(op.status === 'completed', 'status ' + op.status + ' ' + JSON.stringify(op.events?.slice(-1)));
  });
  await step('Migration (web-01 → pve3)', async () => {
    await actOnVM('mock-dc:100', 'migrate', () => page.selectOption('#f-target', 'pve3'));
    const op = await waitOp(o => o.vmid === 100 && o.action === 'migrate');
    expect(op.status === 'completed', 'status ' + op.status);
  });
  await step('Backup erstellen (web-01)', async () => {
    await page.evaluate(() => refresh(false)); await sleep(800);
    await actOnVM('mock-dc:100', 'backup', () => page.selectOption('#f-storage', 'pbs-store'));
    const op = await waitOp(o => o.vmid === 100 && o.action === 'backup');
    expect(op.status === 'completed', 'status ' + op.status + ' ' + JSON.stringify(op.events?.slice(-1)));
  });
  await step('Neue VM auf pve3 erstellen', async () => {
    await page.evaluate(() => nav('vms'));
    await page.click('[data-action="create_vm"]');
    await page.waitForSelector('#create-target');
    const nodesOffered = await page.$$eval('#create-target select[name=node] option', o => o.map(x => x.value));
    expect(nodesOffered.join() === 'pve1,pve2,pve3', 'nodes ' + nodesOffered);
    await page.selectOption('#create-target select[name=node]', 'pve3');
    await page.click('#create-target button.primary');
    await page.waitForSelector('#create-vm-form', { timeout: 10000 });
    await page.fill('#create-vm-form [name=name]', 'new-e2e');
    await page.click('#create-vm-form button.primary');
    const op = await waitOp(o => o.action === 'create' && o.name === 'new-e2e');
    expect(op.status === 'completed' && op.node === 'pve3', 'status ' + op.status + ' node ' + op.node + ' ' + JSON.stringify(op.events?.slice(-1)));
  });
  await step('Hart ausschalten (db-01) mit Bestätigung', async () => {
    await actOnVM('mock-dc:101', 'stop', () => page.check('input[name=acknowledge]'));
    const op = await waitOp(o => o.vmid === 101 && o.action === 'stop');
    expect(op.status === 'completed', 'status ' + op.status);
  });
  await step('Proxmox-Tasks und Task-Log', async () => {
    await page.evaluate(() => { state.jobsTab = 'proxmox'; nav('jobs'); loadPveTasks(true) });
    await page.waitForSelector('[data-task-log]', { timeout: 10000 });
    await page.click('[data-task-log]');
    await page.waitForSelector('.task-log', { timeout: 10000 });
    expect(/TASK OK/.test(await page.textContent('.task-log')), 'log content');
    await page.keyboard.press('Escape');
  });
  await step('Alarme: Prüfung, Regeln speichern', async () => {
    const ev = await api('alerts/evaluate', 'POST', {});
    expect(ev.status === 200, 'evaluate ' + ev.status);
    const cfg = await api('alerts/config', 'POST', { rules: { node_cpu: { enabled: true, threshold: 85, minutes: 3 } } });
    expect(cfg.status === 200 && cfg.data.rules.node_cpu.threshold === 85, 'rules ' + JSON.stringify(cfg.data));
    await page.evaluate(() => nav('alerts')); await page.waitForSelector('[data-alert-tab]');
  });
  await step('DRS-Seite rendert Empfehlungen ohne Fehler', async () => {
    await page.evaluate(() => nav('drs')); await page.waitForSelector('.drs-cluster');
  });
  await step('Suche navigiert zur VM', async () => {
    await page.fill('#global-search', 'db-0'); await sleep(150); await page.keyboard.press('Enter');
    await page.waitForFunction(() => state.page === 'object', null, { timeout: 8000 });
  });
  await step('Leistungsverlauf (RRD) im VM-Tab und Host-Verlauf', async () => {
    await page.evaluate(() => { state.objectTab = 'metrics'; render() });
    await page.waitForSelector('#page svg.chart-svg path.chart-line', { timeout: 10000 });
    await page.evaluate(() => nav('clusters')); await page.waitForSelector('[data-host-metrics]', { timeout: 8000 });
    await page.click('[data-host-metrics]');
    await page.waitForSelector('dialog svg.chart-svg path.chart-line', { timeout: 10000 });
    await page.click('dialog [data-cancel]');
  });
  const openVM = async vmid => {
    await page.evaluate(id => vmDetails(state.connections[0].id + ':' + id), vmid);
    await page.waitForFunction(() => state.object && !state.object.loading, null, { timeout: 10000 });
  };
  await step('Tags bearbeiten und nach Tag filtern', async () => {
    await openVM(100); await page.evaluate(() => { state.objectTab = 'summary'; render() });
    await page.click('[data-edit-tags]'); await page.fill('#f-tags', 'prod, Web, e2e');
    await page.click('#tag-form button.primary');
    const op = await waitOp(o => o.vmid === 100 && o.action === 'tags');
    expect(op.status === 'completed', 'tags ' + op.status + ' ' + JSON.stringify(op.events?.slice(-1)));
    await page.evaluate(() => refresh(false));
    await page.waitForFunction(() => state.resources.find(r => r.vmid === 100)?.tags === 'e2e;prod;web', null, { timeout: 10000 });
    await page.evaluate(() => nav('vms')); await page.selectOption('#vm-tag-filter', 'e2e');
    const names = await page.$$eval('[data-vm]', e => e.map(x => x.textContent));
    expect(names.length === 1 && names[0] === 'web-01', 'tag filter ' + names);
    await page.evaluate(() => { state.vmTag = ''; render() });
  });
  await step('Änderungsprotokoll zeigt Orbit-Aktionen und Proxmox-Tasks', async () => {
    await openVM(100); await page.evaluate(() => { state.objectTab = 'history'; render() });
    await page.waitForSelector('.history-panel tbody tr', { timeout: 10000 });
    const kinds = await page.$$eval('.history-panel tbody tr td:nth-child(2)', e => [...new Set(e.map(x => x.textContent.trim()))]);
    expect(kinds.includes('Orbit') && kinds.includes('Proxmox'), 'sources ' + kinds);
    const users = await page.$$eval('.history-panel tbody tr td:nth-child(4)', e => e.map(x => x.textContent.trim()));
    expect(users.includes('admin'), 'user column ' + users.slice(0, 5));
  });
  await step('Geplante Aktion anlegen und sofort ausführen (Snapshot per Tag)', async () => {
    await page.evaluate(() => nav('schedules'));
    await page.waitForSelector('[data-schedule-edit=""]', { timeout: 10000 });
    await page.click('[data-schedule-edit=""]');
    await page.fill('#f-name', 'E2E Snapshot'); await page.selectOption('#f-action', 'snapshot'); await page.fill('#f-keep', '2');
    await page.check('input[name=target][value=tag]'); await page.fill('#f-tag', 'e2e'); await page.fill('#f-time', '03:15');
    await page.click('.weekday-picker label:has(input[value="1"])');
    await page.click('#schedule-form button.primary');
    await page.waitForFunction(() => !modal.open && scheduleState().list.length === 1, null, { timeout: 8000 });
    const sc = (await api('schedules')).data.schedules[0];
    expect(sc.tag === 'e2e' && sc.keep === 2 && sc.days.join() === '1' && sc.next_run > Date.now() / 1000, 'schedule ' + JSON.stringify(sc));
    await page.click('.row-actions summary'); await page.click('[data-schedule-run]'); await page.click('[data-confirm-run]');
    await page.waitForSelector('dialog [data-open-op]', { timeout: 10000 });
    const op = await waitOp(o => o.vmid === 100 && o.action === 'snapshot' && String(o.requested_by || '').startsWith('Zeitplan E2E Snapshot'));
    expect(op.status === 'completed' && /^auto_/.test(op.args.snapname), 'snapshot ' + op.status + ' ' + op.args?.snapname);
  });
  const guestsOn = async node => ((await api('connections/' + (await page.evaluate(() => state.connections[0].id)) + '/inventory')).data || []).filter(r => ['qemu', 'lxc'].includes(r.type) && r.node === node && r.status === 'running').map(r => r.name).sort();
  const waitMaint = async (pred, timeout = 60000) => {
    const until = Date.now() + timeout;
    while (Date.now() < until) { const items = (await api('maintenance')).data.items; if (pred(items)) return items; await sleep(700) }
    throw new Error('maintenance state not reached: ' + JSON.stringify((await api('maintenance')).data.items).slice(0, 400));
  };
  await step('Wartungsmodus: Host evakuieren und Gäste zurückholen', async () => {
    const before = await guestsOn('pve3');
    expect(before.length >= 2, 'pve3 guests ' + before);
    await page.evaluate(() => nav('maintenance'));
    await page.waitForSelector('tr:has-text("pve3") .row-actions summary', { timeout: 10000 });
    await page.click('tr:has-text("pve3") .row-actions summary'); await sleep(2500);
    expect(await page.$eval('tr:has-text("pve3") details.action-menu', d => d.open), 'action menu closed by live update');
    await page.click('[data-maint-enter$="|pve3"]');
    await page.waitForSelector('#maint-form', { timeout: 10000 });
    await page.check('#maint-form [name=lxc_restart]'); await page.waitForSelector('#maint-form', { timeout: 10000 }); await sleep(500);
    await page.fill('#f-note', 'E2E'); await page.click('#maint-form button.primary');
    await waitMaint(items => items.some(d => d.node === 'pve3' && d.state === 'active'));
    expect(!(await guestsOn('pve3')).length, 'pve3 still runs ' + (await guestsOn('pve3')));
    await page.evaluate(() => loadMaintenance(true)); await sleep(400);
    await page.click('tr:has-text("pve3") .row-actions summary'); await page.click('[data-maint-exit$="|pve3"]');
    await page.click('#maint-exit button.primary');
    await waitMaint(items => !items.some(d => d.node === 'pve3'));
    const after = await guestsOn('pve3');
    expect(JSON.stringify(after) === JSON.stringify(before), `guests back ${after} vs ${before}`);
  });
  await step('Rolling-Neustart eines Hosts mit Evakuierung', async () => {
    await page.evaluate(() => loadMaintenance(true)); await sleep(300);
    await page.click('[data-rolling-start]');
    for (const n of ['pve1', 'pve3']) await page.uncheck(`#rolling-form [name=node][value=${n}]`);
    await page.check('#rolling-form [name=acknowledge]'); await page.click('#rolling-form button.primary');
    const items = await waitMaint(items => items.some(d => d.kind === 'rolling' && ['done', 'paused'].includes(d.state)), 90000);
    const r = items.find(d => d.kind === 'rolling');
    expect(r.state === 'done', 'rolling ' + r.state + ' ' + r.error);
    expect(r.events.some(e => /wieder online/.test(e.message)), 'no reboot seen');
    await page.evaluate(() => loadMaintenance(true)); await sleep(300);
    await page.click('[data-rolling^="dismiss|"]'); await waitMaint(items => !items.length, 10000);
  });
  await step('Restore-Test: neuestes Backup wiederherstellen, starten, aufräumen', async () => {
    await page.evaluate(() => nav('protection'));
    await page.waitForSelector('[data-restore-test]', { timeout: 10000 });
    await page.click('[data-restore-test]');
    await page.selectOption('#f-source', '100'); await page.selectOption('#f-backup_storage', 'pbs-store'); await page.selectOption('#f-storage', 'local-lvm');
    await page.click('#restore-test-form button.primary');
    const until = Date.now() + 60000; let rt;
    while (Date.now() < until) { rt = (await api('restore-tests')).data.items[0]; if (rt && ['passed', 'failed'].includes(rt.state)) break; await sleep(700) }
    expect(rt?.state === 'passed', 'restore test ' + rt?.state + ' ' + rt?.result);
    const inv = (await api('connections/' + (await page.evaluate(() => state.connections[0].id)) + '/inventory')).data;
    expect(!inv.some(r => String(r.name).startsWith('restoretest-')), 'temporary VM left behind');
  });
  await step('Berichte: CSV-Inventar und Kapazitätsbericht', async () => {
    await page.evaluate(() => nav('reports')); await page.waitForSelector('[data-report="guests"]');
    const csv = await page.evaluate(() => reportDefs.guests[2]());
    expect(csv.includes('web-01') && csv.includes('e2e prod web') && csv.split('\n').length >= 5, 'csv ' + csv.slice(0, 200));
    const html = await page.evaluate(() => capacityReportHTML());
    expect(html.includes('Kapazitätsbericht') && html.includes('pve1') && html.includes('local-lvm'), 'report html');
  });
  await step('Storage-, Netzwerk-, Datensicherungs- und Lifecycle-Seiten laden', async () => {
    for (const p of ['storage', 'network', 'protection', 'security', 'lifecycle', 'clusters', 'settings']) {
      await page.evaluate(x => nav(x), p); await sleep(900);
      const errs = (await page.$$eval('#page .hint.error', e => e.map(x => x.textContent.trim()))).filter(Boolean);
      if (errs.length) throw new Error(p + ': ' + errs[0]);
    }
  });
  await step('Backup-Zeitplan anlegen (Live)', async () => {
    await page.evaluate(() => nav('protection'));
    await page.waitForSelector('[data-backup-job-edit$="|"]', { timeout: 10000 });
    await page.click('[data-backup-job-edit$="|"]');
    await page.selectOption('#f-preset', 'sun 03:00'); await page.check('input[name=selection][value=vmids]'); await page.fill('#f-vmids', '100, 101');
    await page.fill('input[name="keep-weekly"]', '8');
    await page.click('#backup-job-form button.primary');
    await page.waitForFunction(() => !modal.open, null, { timeout: 8000 });
    const jobs = (await api('connections/' + await page.evaluate(() => state.connections[0].id) + '/backup-jobs')).data;
    const job = jobs.find(j => j.schedule === 'sun 03:00');
    expect(job && job.vmid === '100,101' && /keep-weekly=8/.test(job['prune-backups']), 'job ' + JSON.stringify(jobs));
  });
  await step('ISO hochladen, per URL laden und löschen (Live)', async () => {
    await page.evaluate(() => nav('storage'));
    await page.waitForSelector('[data-storage-browse$=":local"]', { timeout: 10000 });
    await page.click('[data-storage-browse$=":local"]');
    await page.waitForSelector('#storage-volumes table, #storage-volumes p', { timeout: 10000 });
    await page.click('[data-st-mode="upload"]');
    await page.selectOption('#f-st_content', 'iso');
    await page.setInputFiles('#st-file', { name: 'tools-e2e.iso', mimeType: 'application/octet-stream', buffer: Buffer.alloc(256 * 1024, 7) });
    await page.click('#st-upload button.primary');
    await page.waitForFunction(() => /tools-e2e\.iso/.test(document.querySelector('#storage-volumes')?.textContent || ''), null, { timeout: 15000 });
    await page.click('[data-st-mode="download"]');
    await page.fill('#f-st_url', 'https://cdimage.example/debian-13-netinst.iso');
    await page.click('#st-download button.primary');
    await page.waitForFunction(() => /debian-13-netinst\.iso/.test(document.querySelector('#storage-volumes')?.textContent || ''), null, { timeout: 15000 });
    page.once('dialog', d => d.accept('local:iso/tools-e2e.iso'));
    await page.click('[data-st-delete="local:iso/tools-e2e.iso"]');
    await page.waitForFunction(() => !/tools-e2e\.iso/.test(document.querySelector('#storage-volumes')?.textContent || ''), null, { timeout: 15000 });
    await page.evaluate(() => closeModal(true));
  });
  await step('Benutzer anlegen (Operator) über die Oberfläche', async () => {
    await page.evaluate(() => { state.settingsTab = 'users'; nav('settings') });
    await page.waitForSelector('[data-user-edit=""]', { timeout: 8000 });
    await page.click('[data-user-edit=""]');
    await page.fill('#f-username', 'olga'); await page.selectOption('#f-role', 'operator'); await page.fill('#f-password', 'operator-password-1');
    await page.click('#user-form button.primary');
    await page.waitForFunction(() => (accountState().users || []).some(u => u.username === 'olga'), null, { timeout: 8000 });
  });
  await step('API-Token erstellen und per Bearer nutzen', async () => {
    await page.evaluate(() => { state.settingsTab = 'account'; nav('settings') });
    await page.waitForSelector('[data-token-new]', { timeout: 8000 });
    await page.click('[data-token-new]');
    await page.fill('#f-name', 'e2e'); await page.selectOption('#f-scope', 'read');
    await page.click('#token-form button.primary');
    await page.waitForSelector('.token-once code', { timeout: 8000 });
    const token = (await page.textContent('.token-once code')).trim();
    const ok = await fetch(ORIGIN + '/api/connections', { headers: { Authorization: 'Bearer ' + token } });
    const denied = await fetch(ORIGIN + '/api/operations', { method: 'POST', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, body: '{"action":"start"}' });
    expect(ok.status === 200 && denied.status === 403, `read token: GET ${ok.status}, POST ${denied.status}`);
  });
  let totpSecret = '';
  await step('2FA einrichten', async () => {
    await page.click('[data-totp-setup]');
    await page.waitForSelector('.totp-secret');
    totpSecret = (await page.textContent('.totp-secret')).replace(/\s/g, '');
    await page.fill('#totp-enable-form [name=code]', totp(totpSecret));
    await page.click('#totp-enable-form button.primary');
    await page.waitForFunction(() => accountState().me?.totp === true, null, { timeout: 8000 });
  });
  await step('Abmelden', async () => {
    await page.evaluate(() => actions.logout()); await page.waitForSelector('input[name=password]', { timeout: 8000 });
  });
  await step('Anmeldung mit Passwort und 2FA-Code', async () => {
    await page.fill('#f-username', 'admin'); await page.fill('#f-password', 'e2e-test-password-123');
    await page.click('#auth-form button.primary');
    await page.waitForSelector('#auth-code:not([hidden])', { timeout: 8000 });
    await page.fill('#f-code', totp(totpSecret));
    await page.click('#auth-form button.primary');
    await page.waitForSelector('.prism-topbar', { timeout: 10000 });
    await page.evaluate(() => actions.logout()); await page.waitForSelector('input[name=password]', { timeout: 8000 });
  });
  await step('Operator: Admin-Bereiche gesperrt, Gäste bedienbar', async () => {
    await page.waitForLoadState('networkidle'); await sleep(600);
    await page.fill('#f-username', 'olga'); await page.fill('#f-password', 'operator-password-1');
    await page.click('#auth-form button.primary');
    await page.waitForSelector('.prism-topbar', { timeout: 10000 });
    const role = await page.evaluate(() => state.user?.role);
    expect(role === 'operator', 'role ' + role);
    const users = await api('users'), conn = await api('connections', 'POST', { name: 'x' }), del = await api('operations', 'POST', { action: 'delete' }), sched = await api('schedules', 'POST', { name: 'x' }), schedList = await api('schedules');
    expect(users.status === 403 && conn.status === 403 && del.status === 403 && sched.status === 403 && schedList.status === 200, `users ${users.status}, connections ${conn.status}, delete ${del.status}, schedules ${sched.status}/${schedList.status}`);
    await page.evaluate(() => nav('settings'));
    expect(!(await page.$('[data-settings-nav="users"]')), 'users nav visible for operator');
    await actOnVM('mock-dc:102', 'shutdown');
    const op = await waitOp(o => o.vmid === 102 && o.action === 'shutdown');
    expect(op.status === 'completed' && op.requested_by === 'olga', 'status ' + op.status + ' by ' + op.requested_by);
  });
  await step('Oberfläche: UI 2 aktiv, Umschalter auf klassisch und zurück, Design-System-Seite', async () => {
    expect(await page.evaluate(() => document.documentElement.dataset.ui) === '2', 'UI 2 not active');
    await page.evaluate(() => nav('vms')); await page.waitForSelector('#page button.primary', { timeout: 5000 });
    const h = await page.$eval('#page button.primary', e => getComputedStyle(e).backgroundColor);
    expect(h === 'rgb(194, 65, 12)', 'primary button colour ' + h);
    await page.click('.user-menu summary'); await page.click('[data-ui-toggle]');
    expect(await page.evaluate(() => document.documentElement.dataset.ui) === '1', 'classic UI not applied');
    await page.click('.user-menu summary'); await page.click('[data-ui-toggle]');
    expect(await page.evaluate(() => document.documentElement.dataset.ui) === '2', 'UI 2 not restored');
    await page.evaluate(() => nav('design')); await page.waitForSelector('.swatches', { timeout: 5000 });
  });
  await step('Keine JavaScript-Fehler', async () => expect(!jsErrors.length, jsErrors.slice(0, 3).join(' | ')));

  await browser.close();
  for (const p of procs) p.kill();
  const unknown = fs.readFileSync(path.join(work, 'mock.log'), 'utf8').split('\n').filter(l => l.startsWith('UNKNOWN'));
  console.log(`\n${passes.length} bestanden, ${failures.length} fehlgeschlagen`);
  if (unknown.length) console.log('Vom Mock nicht unterstützte Aufrufe:\n  ' + unknown.join('\n  '));
  try { console.log('API-Prüfbericht: ' + require('./report.cjs')(work, process.env.E2E_REPORT || undefined)) } catch (e) { console.log('Bericht nicht erstellt: ' + e.message) }
  if (failures.length) { console.log('Fehler:\n  ' + failures.join('\n  ') + '\nLogs: ' + work); process.exit(1) }
})().catch(e => { console.error(e); for (const p of procs) p.kill(); process.exit(1) });
