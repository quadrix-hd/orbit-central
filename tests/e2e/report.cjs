'use strict';
// Builds an HTML report of every Proxmox API call the E2E run made, grouped
// by test step, with the equivalent `pvesh` command for each change.
// Usage: node tests/e2e/report.cjs <e2e work dir> [out.html]
const fs = require('fs'), path = require('path');

const esc = s => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);
const verbs = { GET: 'get', POST: 'create', PUT: 'set', DELETE: 'delete' };
const rules = [
  [/\/status\/start$/, 'Gast starten'], [/\/status\/shutdown$/, 'Gast herunterfahren'], [/\/status\/stop$/, 'Gast hart ausschalten'],
  [/\/status\/reboot$/, 'Gast neu starten'], [/\/status\/reset$/, 'Gast zurücksetzen'], [/\/status\/suspend$/, 'Gast pausieren'], [/\/status\/resume$/, 'Gast fortsetzen'],
  [/^\/nodes\/[^/]+\/status$/, 'Host-Befehl (z. B. Neustart)'], [/\/snapshot\/[^/]+\/rollback$/, 'Snapshot zurückrollen'], [/\/snapshot\/[^/]+$/, 'Snapshot löschen', 'DELETE'], [/\/snapshot$/, 'Snapshot anlegen', 'POST'],
  [/\/clone$/, 'Gast klonen'], [/\/migrate$/, 'Gast migrieren', 'POST'], [/\/template$/, 'In Vorlage umwandeln'], [/\/resize$/, 'Laufwerk vergrößern'],
  [/\/config$/, 'Gastkonfiguration ändern', 'PUT'], [/\/config$/, 'Gastkonfiguration ändern (asynchron)', 'POST'], [/^\/nodes\/[^/]+\/(qemu|lxc)$/, 'Gast erstellen / wiederherstellen', 'POST'],
  [/^\/nodes\/[^/]+\/(qemu|lxc)\/\d+$/, 'Gast löschen', 'DELETE'], [/\/vzdump$/, 'Backup erstellen'], [/^\/cluster\/backup/, 'Backup-Zeitplan'],
  [/\/storage\/[^/]+\/upload$/, 'Datei hochladen'], [/\/download-url$/, 'Datei per URL laden'], [/\/content\/.+$/, 'Volume löschen', 'DELETE'],
  [/\/apt\/update$/, 'Paketliste aktualisieren', 'POST'], [/\/vncproxy$/, 'Konsole öffnen'], [/\/vncshell$/, 'Host-Shell öffnen'], [/^\/cluster\/ha/, 'HA-Konfiguration'],
  [/\/network/, 'Netzwerkkonfiguration'], [/\/firewall/, 'Firewall'], [/^\/storage/, 'Storage-Konfiguration'], [/\/agent\/ping$/, 'Gastagent prüfen'],
];
function describe(r) {
  for (const [re, label, method] of rules) if (re.test(r.path) && (!method || method === r.method)) return label;
  return '';
}
function pvesh(r) {
  const args = Object.entries({ ...r.query, ...r.body }).map(([k, v]) => `--${k} ${/^[\w.:@\/=,+-]+$/.test(String(v)) ? v : JSON.stringify(String(v))}`);
  return ['pvesh', verbs[r.method] || r.method.toLowerCase(), r.path, ...args].join(' ');
}

module.exports = function report(work, out = path.join(work, 'api-report.html')) {
  const calls = fs.existsSync(path.join(work, 'api.jsonl')) ? fs.readFileSync(path.join(work, 'api.jsonl'), 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l)) : [];
  const steps = JSON.parse(fs.readFileSync(path.join(work, 'steps.json'), 'utf8'));
  const groups = steps.map(s => ({ ...s, calls: [] })), background = { name: 'Zwischen den Schritten (Live-Aktualisierung, Alarme, Zeitpläne, Wartungs-Worker)', ok: true, calls: [] };
  for (const c of calls) (groups.find(g => c.t >= g.start && c.t <= g.end + 150) || background).calls.push(c);
  const all = [...groups, background];
  const writes = calls.filter(c => c.method !== 'GET'), errors = calls.filter(c => c.status >= 400);
  const section = (g, i) => {
    const w = g.calls.filter(c => c.method !== 'GET'), reads = g.calls.filter(c => c.method === 'GET');
    const uniq = [...new Set(reads.map(c => c.path.replace(/\/\d{3,}(?=\/|$)/g, '/{vmid}').replace(/UPID:[^/]+/g, '{upid}')))].sort();
    return `<section class="step ${g.ok ? '' : 'failed'}" id="s${i}"><h2><span class="n">${i < groups.length ? i + 1 : '·'}</span>${esc(g.name)}<span class="badge ${g.ok ? 'ok' : 'fail'}">${g.ok ? 'bestanden' : 'fehlgeschlagen'}</span><span class="count">${w.length} ändernd · ${reads.length} lesend</span></h2>
${w.length ? `<ol class="writes">${w.map(c => `<li class="${c.status >= 400 ? 'err' : ''}"><div class="line"><b class="m ${c.method}">${c.method}</b><code>${esc(c.path)}</code>${describe(c) ? `<span class="what">${esc(describe(c))}</span>` : ''}<span class="st">HTTP ${c.status}</span></div>${Object.keys({ ...c.query, ...c.body }).length ? `<table>${Object.entries({ ...c.query, ...c.body }).map(([k, v]) => `<tr><th>${esc(k)}</th><td>${esc(typeof v === 'object' ? JSON.stringify(v) : v)}</td></tr>`).join('')}</table>` : ''}<pre>${esc(pvesh(c))}</pre>${c.error ? `<p class="errmsg">Antwort: ${esc(c.error)}</p>` : ''}</li>`).join('')}</ol>` : '<p class="none">Keine ändernden Aufrufe.</p>'}
${uniq.length ? `<details><summary>Lesende Aufrufe (${uniq.length} verschiedene Pfade)</summary><ul class="reads">${uniq.map(p => `<li><code>GET ${esc(p)}</code></li>`).join('')}</ul></details>` : ''}</section>`;
  };
  const html = `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Orbit API-Prüfbericht</title><style>
:root{--bg:#f6f7f9;--panel:#fff;--text:#111827;--muted:#6b7280;--line:#e5e7eb;--accent:#ea580c;--ok:#15803d;--okbg:#dcfce7;--fail:#b91c1c;--failbg:#fee2e2;--code:#f3f4f6}
@media (prefers-color-scheme:dark){:root:not([data-theme="light"]){--bg:#0f1115;--panel:#171a21;--text:#e5e7eb;--muted:#9ca3af;--line:#2a2f3a;--ok:#4ade80;--okbg:#14321f;--fail:#f87171;--failbg:#3a1616;--code:#0b0d12}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 Inter,system-ui,-apple-system,sans-serif}
main{max-width:1100px;margin:0 auto;padding:24px 16px 60px}h1{font-size:22px;margin:0 0 4px}.lead{color:var(--muted);margin:0 0 18px}
.kpis{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin-bottom:18px}.kpi{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:12px 14px}.kpi b{display:block;font-size:22px}.kpi span{color:var(--muted);font-size:12px}
.howto{background:var(--panel);border:1px solid var(--line);border-left:3px solid var(--accent);border-radius:10px;padding:12px 16px;margin-bottom:18px}.howto p{margin:4px 0}
nav.toc{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:10px 16px;margin-bottom:18px;columns:2 280px;font-size:13px}nav.toc a{color:inherit;text-decoration:none;display:block;padding:2px 0}nav.toc a:hover{color:var(--accent)}
.step{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px 16px;margin-bottom:12px}.step.failed{border-color:var(--fail)}
.step h2{display:flex;flex-wrap:wrap;align-items:center;gap:8px;font-size:15px;margin:0 0 10px}.n{display:inline-grid;place-items:center;min-width:24px;height:24px;border-radius:6px;background:var(--code);font-size:12px}
.badge{font-size:11px;font-weight:600;padding:2px 8px;border-radius:6px}.badge.ok{background:var(--okbg);color:var(--ok)}.badge.fail{background:var(--failbg);color:var(--fail)}.count{margin-left:auto;color:var(--muted);font-size:12px;font-weight:400}
ol.writes{margin:0;padding-left:20px;display:grid;gap:10px}ol.writes li.err .st{color:var(--fail)}.line{display:flex;flex-wrap:wrap;gap:8px;align-items:center}
.m{font-size:11px;padding:1px 6px;border-radius:4px;background:var(--code)}.m.POST{color:#2563eb}.m.PUT{color:#a16207}.m.DELETE{color:var(--fail)}
code,pre{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px}.line code{word-break:break-all}.what{color:var(--accent);font-weight:600;font-size:12.5px}.st{color:var(--muted);font-size:12px;margin-left:auto}
pre{background:var(--code);border:1px solid var(--line);border-radius:6px;padding:7px 9px;margin:6px 0 0;white-space:pre-wrap;word-break:break-all}
table{border-collapse:collapse;margin-top:6px;font-size:12.5px}th,td{text-align:left;padding:2px 10px 2px 0;vertical-align:top}th{color:var(--muted);font-weight:500}td{word-break:break-all}
.none{color:var(--muted);margin:0}.errmsg{color:var(--fail);margin:4px 0 0;font-size:12.5px}details{margin-top:8px}summary{cursor:pointer;color:var(--muted);font-size:12.5px}ul.reads{margin:6px 0 0;padding-left:18px;columns:2 320px}
</style></head><body><main><h1>Orbit · API-Prüfbericht</h1><p class="lead">Jeder Aufruf, den Orbit beim automatischen Durchklicken aller Funktionen an die (simulierte) Proxmox-API geschickt hat · ${esc(new Date().toLocaleString('de-DE'))}</p>
<div class="kpis"><div class="kpi"><b>${steps.filter(s => s.ok).length} / ${steps.length}</b><span>Schritte bestanden</span></div><div class="kpi"><b>${writes.length}</b><span>ändernde Aufrufe</span></div><div class="kpi"><b>${calls.length - writes.length}</b><span>lesende Aufrufe</span></div><div class="kpi"><b>${errors.length}</b><span>Fehlerantworten</span></div></div>
<div class="howto"><p><b>So prüfst du:</b> Bei jedem ändernden Aufruf steht, was er tun soll, welche Parameter Orbit sendet und das gleichwertige <code>pvesh</code>-Kommando. Vergleiche mit der <a href="https://pve.proxmox.com/pve-docs/api-viewer/">Proxmox-API-Doku</a> oder führe den <code>pvesh get</code>-Teil auf einem Host aus.</p><p>Fehlerantworten sind teils gewollt (z. B. absichtlich abgelehnte Aktionen). Gegenseite ist ein Simulator – Namen wie <code>mock-dc</code>, <code>pve1–3</code> stammen von dort.</p></div>
<nav class="toc">${all.map((g, i) => `<a href="#s${i}">${i < groups.length ? i + 1 + '. ' : ''}${esc(g.name)}</a>`).join('')}</nav>
${all.map(section).join('')}</main></body></html>`;
  fs.writeFileSync(out, html);
  return out;
};

if (require.main === module) {
  const [work, out] = process.argv.slice(2);
  if (!work) { console.error('Usage: node tests/e2e/report.cjs <work dir> [out.html]'); process.exit(2) }
  console.log(module.exports(work, out));
}
