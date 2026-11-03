'use strict';
// Stateful Proxmox VE API simulator for end-to-end tests.
// Three nodes listen on 127.0.0.1/2/3 (same port) so "local" node detection,
// node endpoint discovery and node-pinned uploads behave like a real cluster.
// Usage: node mock-pve.cjs <certdir> <port>
const https = require('https'), fs = require('fs'), path = require('path');
const [certDir, port = '18006'] = process.argv.slice(2);
const opts = { key: fs.readFileSync(path.join(certDir, 'server.key')), cert: fs.readFileSync(path.join(certDir, 'server.crt')) };
const GiB = 1073741824;
const nodes = { pve1: '127.0.0.1', pve2: '127.0.0.2', pve3: '127.0.0.3' };
const now = () => Math.floor(Date.now() / 1000);
let upidSeq = 0x1000;
const state = {
  guests: {
    100: { type: 'qemu', node: 'pve1', name: 'web-01', status: 'running', maxcpu: 2, maxmem: 4 * GiB, mem: 2 * GiB, cpu: 0.12, maxdisk: 32 * GiB, template: 0, tags: 'prod', config: { name: 'web-01', cores: 2, sockets: 1, memory: 4096, scsi0: 'local-lvm:vm-100-disk-0,size=32G', net0: 'virtio=BC:24:11:00:00:01,bridge=vmbr0', ide2: 'none,media=cdrom', boot: 'order=scsi0', ostype: 'l26', digest: 'd100' } },
    101: { type: 'qemu', node: 'pve1', name: 'db-01', status: 'running', maxcpu: 4, maxmem: 16 * GiB, mem: 12 * GiB, cpu: 0.45, maxdisk: 100 * GiB, template: 0, tags: 'prod;anti-affinity-db', config: { name: 'db-01', cores: 4, sockets: 1, memory: 16384, scsi0: 'local-lvm:vm-101-disk-0,size=100G', net0: 'virtio=BC:24:11:00:00:02,bridge=vmbr0', ide2: 'none,media=cdrom', boot: 'order=scsi0', ostype: 'l26', digest: 'd101' } },
    102: { type: 'qemu', node: 'pve2', name: 'db-02', status: 'stopped', maxcpu: 4, maxmem: 16 * GiB, mem: 0, cpu: 0, maxdisk: 100 * GiB, template: 0, tags: 'prod;anti-affinity-db', config: { name: 'db-02', cores: 4, sockets: 1, memory: 16384, scsi0: 'local-lvm:vm-102-disk-0,size=100G', net0: 'virtio=BC:24:11:00:00:03,bridge=vmbr0', ide2: 'none,media=cdrom', boot: 'order=scsi0', ostype: 'l26', digest: 'd102' } },
    200: { type: 'lxc', node: 'pve3', name: 'proxy-ct', status: 'running', maxcpu: 1, maxmem: 1 * GiB, mem: 300 * 1048576, cpu: 0.03, maxdisk: 8 * GiB, template: 0, tags: '', config: { hostname: 'proxy-ct', cores: 1, memory: 1024, swap: 512, rootfs: 'local-lvm:vm-200-disk-0,size=8G', net0: 'name=eth0,bridge=vmbr0,ip=dhcp', ostype: 'debian', digest: 'd200' } },
    9000: { type: 'qemu', node: 'pve1', name: 'ubuntu-template', status: 'stopped', maxcpu: 2, maxmem: 2 * GiB, mem: 0, cpu: 0, maxdisk: 10 * GiB, template: 1, tags: '', config: { name: 'ubuntu-template', cores: 2, memory: 2048, scsi0: 'local-lvm:base-9000-disk-0,size=10G', ide2: 'local-lvm:cloudinit,media=cdrom', net0: 'virtio=BC:24:11:00:00:09,bridge=vmbr0', template: 1, digest: 'd9000' } },
  },
  content: {
    local: [{ volid: 'local:iso/debian-13.iso', content: 'iso', size: 700 * 1048576, format: 'iso' }, { volid: 'local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst', content: 'vztmpl', size: 120 * 1048576, format: 'tzst' }],
    'pbs-store': [{ volid: 'pbs-store:backup/vm/100/2026-11-01T01:00:00Z', content: 'backup', size: 5 * GiB, format: 'pbs-vm', vmid: 100, ctime: now() - 86400 }],
  },
  tasks: [],
  backupJobs: [{ id: 'backup-nightly', schedule: '01:00', storage: 'pbs-store', mode: 'snapshot', all: 1, enabled: 1, 'prune-backups': 'keep-daily=7,keep-weekly=4', type: 'vzdump' }],
  snapshots: { 100: [{ name: 'before-update', description: '', snaptime: now() - 3600 }] },
  ha: [],
  unknown: new Set(),
};
const log = (...a) => { if (process.env.MOCK_VERBOSE) console.error(...a) };
function task(node, type, id, fail) {
  const hex = n => n.toString(16).toUpperCase().padStart(8, '0');
  const upid = `UPID:${node}:${hex(upidSeq++)}:${hex(1)}:${hex(now())}:${type}:${id || ''}:orbit@pve!e2e:`;
  state.tasks.unshift({ upid, node, type, id: String(id || ''), user: 'orbit@pve!e2e', starttime: now(), endtime: now() + 1, status: fail ? 'ERROR: simulated failure' : 'OK' });
  return upid;
}
function resources(type) {
  const out = [];
  if (!type || type === 'node') for (const [node] of Object.entries(nodes)) {
    const guests = Object.values(state.guests).filter(g => g.node === node && g.status === 'running');
    out.push({ id: 'node/' + node, type: 'node', node, status: 'online', cpu: Math.min(0.95, 0.05 + guests.reduce((s, g) => s + g.cpu * g.maxcpu, 0) / 32), maxcpu: 32, mem: 8 * GiB + guests.reduce((s, g) => s + g.mem, 0), maxmem: 128 * GiB, disk: 20 * GiB, maxdisk: 100 * GiB, uptime: 86400 * 12 });
  }
  if (!type || type === 'vm') for (const [vmid, g] of Object.entries(state.guests)) out.push({ id: `${g.type}/${vmid}`, type: g.type, vmid: +vmid, node: g.node, name: g.name, status: g.status, cpu: g.status === 'running' ? g.cpu : 0, maxcpu: g.maxcpu, mem: g.status === 'running' ? g.mem : 0, maxmem: g.maxmem, disk: 0, maxdisk: g.maxdisk, template: g.template, tags: g.tags, uptime: g.status === 'running' ? 3600 : 0 });
  if (!type || type === 'storage') for (const node of Object.keys(nodes)) {
    out.push({ id: `storage/${node}/local`, type: 'storage', node, storage: 'local', status: 'available', disk: 30 * GiB, maxdisk: 100 * GiB, shared: 0, content: 'iso,vztmpl,backup,import', plugintype: 'dir' });
    out.push({ id: `storage/${node}/local-lvm`, type: 'storage', node, storage: 'local-lvm', status: 'available', disk: 400 * GiB, maxdisk: 900 * GiB, shared: 0, content: 'images,rootdir', plugintype: 'lvmthin' });
    out.push({ id: `storage/${node}/pbs-store`, type: 'storage', node, storage: 'pbs-store', status: 'available', disk: 2000 * GiB, maxdisk: 8000 * GiB, shared: 1, content: 'backup', plugintype: 'pbs' });
  }
  return out;
}
const storageList = () => [
  { storage: 'local', type: 'dir', content: 'iso,vztmpl,backup,import', active: 1, enabled: 1, shared: 0, total: 100 * GiB, used: 30 * GiB, avail: 70 * GiB, path: '/var/lib/vz', digest: 's1' },
  { storage: 'local-lvm', type: 'lvmthin', content: 'images,rootdir', active: 1, enabled: 1, shared: 0, total: 900 * GiB, used: 400 * GiB, avail: 500 * GiB, thinpool: 'data', vgname: 'pve', digest: 's2' },
  { storage: 'pbs-store', type: 'pbs', content: 'backup', active: 1, enabled: 1, shared: 1, total: 8000 * GiB, used: 2000 * GiB, avail: 6000 * GiB, server: 'pbs.example', datastore: 'store1', digest: 's3' },
];
function rrd(timeframe) {
  const step = { hour: 60, day: 1800, week: 10800, month: 43200, year: 604800 }[timeframe] || 60, out = [];
  for (let i = 70; i >= 0; i--) { const t = now() - i * step, w = Math.sin(i / 7) * 0.5 + 0.5; out.push({ time: t, cpu: 0.1 + 0.3 * w, maxcpu: 32, mem: (20 + 30 * w) * GiB, maxmem: 128 * GiB, netin: 2e6 * w, netout: 1e6 * w, diskread: 5e5 * w, diskwrite: 8e5 * w, loadavg: 2 * w }) }
  return out;
}
function parseBody(req, raw) {
  const type = req.headers['content-type'] || '';
  if (type.startsWith('application/x-www-form-urlencoded')) return Object.fromEntries(new URLSearchParams(raw.toString()));
  if (type.startsWith('application/json')) { try { return JSON.parse(raw.toString() || '{}') } catch { return {} } }
  if (type.startsWith('multipart/form-data')) {
    // Same strict field order as pveproxy's file_upload_multipart.
    const boundary = type.split('boundary=')[1], text = raw.toString('latin1'), parts = text.split('--' + boundary).slice(1, -1);
    const names = parts.map(p => (p.match(/name="([^"]+)"/) || [])[1]);
    const expected = ['content', 'checksum-algorithm', 'checksum', 'filename'];
    if (names.join() !== expected.join()) return { __multipart_error: 'wrong field order ' + names.join() };
    const filename = (parts[3].match(/filename="([^"]+)"/) || [])[1];
    return { content: parts[0].split('\r\n\r\n')[1].trim(), filename };
  }
  return Object.fromEntries(new URLSearchParams(raw.toString()));
}
function route(local, method, p, q, body) {
  let m;
  if (p === '/version') return { version: '9.0.10', release: '9.0', repoid: 'mock' };
  if (p === '/cluster/status') return [{ type: 'cluster', name: 'mock-dc', nodes: 3, quorate: 1, version: 3 }, ...Object.entries(nodes).map(([name, ip], i) => ({ type: 'node', name, ip, id: 'node/' + name, nodeid: i + 1, online: 1, local: name === local ? 1 : 0 }))];
  if (p === '/cluster/resources') return resources(q.get('type'));
  if (p === '/cluster/nextid') { let id = 100; while (state.guests[id]) id++; return String(id) }
  if (p === '/cluster/tasks') return state.tasks.slice(0, 50);
  if (p === '/cluster/ha/resources') return method === 'GET' ? state.ha : (state.ha.push({ sid: body.sid, state: body.state }), null);
  if ((m = p.match(/^\/cluster\/ha\/resources\/(.+)$/))) { const r = state.ha.find(h => h.sid === decodeURIComponent(m[1])); if (r) r.state = body.state; return null }
  if (p === '/cluster/ha/status/current') return [{ id: 'quorum', type: 'quorum', status: 'OK', quorate: 1 }];
  if (p === '/cluster/backup') {
    if (method === 'GET') return state.backupJobs;
    if (method === 'POST') { const job = { id: body.id || 'backup-' + (state.backupJobs.length + 1), type: 'vzdump', ...body }; state.backupJobs.push(job); return null }
  }
  if ((m = p.match(/^\/cluster\/backup\/([^/]+)$/))) {
    const i = state.backupJobs.findIndex(j => j.id === decodeURIComponent(m[1]));
    if (i < 0) throw { status: 404, message: 'job not found' };
    if (method === 'GET') return state.backupJobs[i];
    if (method === 'PUT') { const del = String(body.delete || '').split(',').filter(Boolean); Object.assign(state.backupJobs[i], body); delete state.backupJobs[i].delete; for (const k of del) delete state.backupJobs[i][k]; return null }
    if (method === 'DELETE') { state.backupJobs.splice(i, 1); return null }
  }
  if (p === '/cluster/replication') return [];
  if (p === '/cluster/firewall/options') return { enable: 0, policy_in: 'ACCEPT', policy_out: 'ACCEPT', digest: 'fw1' };
  if (p === '/cluster/firewall/rules') return [];
  if (p.startsWith('/cluster/sdn')) return [];
  if (p === '/storage') return storageList();
  if ((m = p.match(/^\/storage\/([^/]+)$/))) { const s = storageList().find(x => x.storage === m[1]); if (!s) throw { status: 404, message: 'storage not found' }; return s }
  if (p === '/nodes') return Object.keys(nodes).map(node => ({ node, status: 'online', cpu: 0.2, maxcpu: 32, mem: 40 * GiB, maxmem: 128 * GiB, uptime: 86400, level: '' }));
  if (!(m = p.match(/^\/nodes\/([^/]+)(\/.*)?$/))) return undefined;
  const node = m[1], rest = m[2] || '';
  if (!nodes[node]) throw { status: 400, message: `no such node '${node}'` };
  if (rest === '/status') return { cpu: 0.2, memory: { total: 128 * GiB, used: 40 * GiB, free: 88 * GiB }, uptime: 86400, pveversion: 'pve-manager/9.0.10', kversion: 'Linux 6.14', cpuinfo: { cpus: 32, model: 'Mock CPU' }, swap: { total: 8 * GiB, used: 0 }, rootfs: { total: 100 * GiB, used: 20 * GiB } };
  if (rest === '/rrddata') return rrd(q.get('timeframe'));
  if (rest === '/storage') return storageList();
  if (rest === '/network') return [{ iface: 'vmbr0', type: 'bridge', active: 1, autostart: 1, bridge_ports: 'eno1', cidr: '10.0.0.11/24', method: 'static', families: ['inet'] }, { iface: 'vmbr1', type: 'bridge', active: 1, autostart: 1, bridge_ports: 'eno2', method: 'manual', families: ['inet'] }, { iface: 'eno1', type: 'eth', active: 1 }];
  if (rest === '/apt/update') return method === 'GET' ? [{ Package: 'pve-manager', OldVersion: '9.0.9', Version: '9.0.10', Priority: 'important' }] : task(node, 'aptupdate', '');
  if (rest === '/apt/versions') return [{ Package: 'pve-manager', Version: '9.0.10' }, { Package: 'proxmox-ve', Version: '9.0.0' }];
  if (rest === '/vzdump') return task(node, 'vzdump', body.vmid || '');
  if (rest === '/tasks') return state.tasks.filter(t => t.node === node);
  if ((m = rest.match(/^\/tasks\/([^/]+)\/(status|log)$/))) {
    const t = state.tasks.find(x => x.upid === decodeURIComponent(m[1]));
    if (!t) throw { status: 404, message: 'no such task' };
    return m[2] === 'status' ? { upid: t.upid, node: t.node, type: t.type, status: 'stopped', exitstatus: t.status } : [{ n: 1, t: `starting task ${t.type}` }, { n: 2, t: t.status === 'OK' ? 'TASK OK' : 'TASK ' + t.status }];
  }
  if ((m = rest.match(/^\/storage\/([^/]+)(\/.*)?$/))) {
    const store = m[1], sub = m[2] || '';
    if (sub === '/status') return storageList().find(s => s.storage === store);
    if (sub === '/content') {
      if (method === 'GET') return (state.content[store] || []).filter(v => !q.get('content') || v.content === q.get('content'));
      if (method === 'POST') { (state.content[store] ||= []).push({ volid: `${store}:${body.filename}`, content: 'images', size: 1 }); return `${store}:${body.filename}` }
    }
    if ((m = sub.match(/^\/content\/(.+)$/))) {
      const volid = decodeURIComponent(m[1]), list = state.content[store] || [], i = list.findIndex(v => v.volid === volid);
      if (method === 'DELETE') { if (i < 0) throw { status: 404, message: `volume '${volid}' does not exist` }; list.splice(i, 1); return task(node, 'imgdel', '') }
      if (i < 0) throw { status: 404, message: 'volume not found' }; return list[i];
    }
    if (sub === '/upload') {
      if (body.__multipart_error) throw { status: 400, message: body.__multipart_error };
      (state.content[store] ||= []).push({ volid: `${store}:${body.content}/${body.filename}`, content: body.content, size: 1024 * 1024 });
      return task(node, 'imgcopy', '');
    }
    if (sub === '/download-url') { (state.content[store] ||= []).push({ volid: `${store}:${body.content}/${body.filename}`, content: body.content, size: 600 * 1048576 }); return task(node, 'download', '') }
  }
  if (rest === '/qemu' || rest === '/lxc') {
    if (method === 'GET') return resources('vm').filter(v => v.node === node && v.type === rest.slice(1));
    const id = +body.vmid; if (state.guests[id]) throw { status: 500, message: `unable to create VM ${id} - config file already exists` };
    const name = body.name || body.hostname || 'vm' + id;
    state.guests[id] = { type: rest.slice(1), node, name, status: 'stopped', maxcpu: +body.cores || 1, maxmem: (+body.memory || 512) * 1048576, mem: 0, cpu: 0, maxdisk: 32 * GiB, template: 0, tags: '', config: { ...body, digest: 'n' + id } };
    return task(node, rest === '/qemu' ? 'qmcreate' : 'vzcreate', id);
  }
  if ((m = rest.match(/^\/(qemu|lxc)\/(\d+)(\/.*)?$/))) {
    const kind = m[1], id = +m[2], sub = m[3] || '', g = state.guests[id];
    if (!g || g.node !== node) throw { status: 500, message: `Configuration file 'nodes/${node}/${kind}-server/${id}.conf' does not exist` };
    const prefix = kind === 'qemu' ? 'qm' : 'vz';
    if (sub === '' && method === 'DELETE') { delete state.guests[id]; return task(node, prefix + 'destroy', id) }
    if (sub === '/config') {
      if (method === 'GET') return { ...g.config, name: g.name };
      const del = String(body.delete || '').split(',').filter(Boolean); Object.assign(g.config, body); delete g.config.delete; delete g.config.digest; for (const k of del) delete g.config[k];
      g.config.digest = 'c' + Date.now(); if (body.name) g.name = body.name; if (body.cores) g.maxcpu = +body.cores; if (body.memory) g.maxmem = +body.memory * 1048576;
      return method === 'POST' ? task(node, prefix + 'config', id) : null;
    }
    if (sub === '/status/current') return { status: g.status, qmpstatus: g.paused ? 'paused' : g.status, cpu: g.status === 'running' ? g.cpu : 0, cpus: g.maxcpu, mem: g.status === 'running' ? g.mem : 0, maxmem: g.maxmem, uptime: g.status === 'running' ? 3600 : 0, agent: 1, name: g.name };
    if ((m = sub.match(/^\/status\/(start|stop|shutdown|reboot|reset|suspend|resume)$/))) {
      const a = m[1];
      if (a === 'start') { if (g.status === 'running') throw { status: 500, message: 'VM already running' }; g.status = 'running'; g.mem = g.maxmem / 2; g.cpu = 0.1 }
      if (['stop', 'shutdown'].includes(a)) { g.status = 'stopped'; g.paused = false }
      if (a === 'suspend') g.paused = true; if (a === 'resume') g.paused = false;
      return task(node, prefix + a, id);
    }
    if (sub === '/snapshot') {
      if (method === 'GET') return [...(state.snapshots[id] || []), { name: 'current', description: 'You are here!' }];
      (state.snapshots[id] ||= []).push({ name: body.snapname, description: body.description || '', snaptime: now() }); return task(node, prefix + 'snapshot', id);
    }
    if ((m = sub.match(/^\/snapshot\/([^/]+)(\/rollback)?$/))) {
      const list = state.snapshots[id] || [], i = list.findIndex(s => s.name === m[1]);
      if (i < 0) throw { status: 500, message: 'snapshot does not exist' };
      if (m[2]) return task(node, prefix + 'rollback', id);
      list.splice(i, 1); return task(node, prefix + 'delsnapshot', id);
    }
    if (sub === '/clone') {
      const nid = +body.newid; if (state.guests[nid]) throw { status: 500, message: 'VM exists' };
      if (!+body.full && !g.template) throw { status: 500, message: 'linked clone requires template' };
      state.guests[nid] = { ...JSON.parse(JSON.stringify(g)), node: body.target || node, name: body.name || body.hostname, status: 'stopped', template: 0, mem: 0, cpu: 0 };
      state.guests[nid].config.name = state.guests[nid].name; return task(node, prefix + 'clone', id);
    }
    if (sub === '/template') { if (g.status !== 'stopped') throw { status: 500, message: 'VM is running' }; g.template = 1; g.config.template = 1; return kind === 'qemu' ? task(node, 'qmtemplate', id) : null }
    if (sub === '/migrate') { if (!nodes[body.target]) throw { status: 400, message: 'invalid target' }; g.node = body.target; return task(node, kind === 'qemu' ? 'qmigrate' : 'vzmigrate', id) }
    if (sub === '/resize') return kind === 'qemu' ? null : task(node, 'resize', id);
    if (sub === '/rrddata') return rrd(q.get('timeframe'));
    if (sub === '/firewall/options') return { enable: 0, digest: 'gfw' };
    if (sub === '/firewall/rules') return [];
    if (sub === '/pending') return [];
    if (sub === '/vncproxy' || sub === '/termproxy') return { port: '5900', ticket: 'PVEVNC:mock', user: 'orbit@pve!e2e', cert: '' };
  }
  return undefined;
}
function handler(local) {
  return (req, res) => {
    const chunks = [];
    req.on('data', c => chunks.push(c));
    req.on('end', () => {
      const url = new URL(req.url, 'https://x'), p = url.pathname.replace(/^\/api2\/json/, ''), body = req.method === 'GET' ? {} : parseBody(req, Buffer.concat(chunks));
      const send = (status, payload) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(payload)) };
      if (!String(req.headers.authorization || '').startsWith('PVEAPIToken=')) return send(401, { data: null, message: 'authentication failure' });
      try {
        const data = route(local, req.method, p, url.searchParams, body);
        if (data === undefined) {
          const key = req.method + ' ' + p.replace(/\d{3,}/g, '{id}');
          if (!state.unknown.has(key)) { state.unknown.add(key); console.error('UNKNOWN', key) }
          return send(req.method === 'GET' ? 200 : 501, { data: req.method === 'GET' ? [] : null, message: 'not implemented in mock' });
        }
        log(req.method, p, '->', typeof data === 'string' ? data : '');
        send(200, { data });
      } catch (e) { send(e.status || 500, { data: null, message: e.message || String(e) }) }
    });
  };
}
for (const [name, ip] of Object.entries(nodes)) https.createServer(opts, handler(name)).listen(+port, ip, () => console.error(`mock ${name} https://${ip}:${port}`));
