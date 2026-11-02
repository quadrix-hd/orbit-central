// Pure rendering checks, not a replacement for real browser/layout QA.
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict'),crypto=require('node:crypto');
const elements=new Map();
function element(selector){if(!elements.has(selector))elements.set(selector,{innerHTML:'',textContent:'',value:'',content:selector==='meta[name="orbit-mode"]'?'demo':'',classList:{add(){},remove(){}},addEventListener(){},querySelector:s=>element(s),querySelectorAll:()=>[],showModal(){this.open=true},close(){this.open=false},setAttribute(){},focus(){},setSelectionRange(){},readOnly:false});return elements.get(selector)}
const context={URLSearchParams,console,document:{querySelector:element,querySelectorAll:()=>[],activeElement:null,hidden:false},localStorage:{getItem:()=>null,setItem(){}},window:{scrollTo(){}},setInterval(){},setTimeout(){return 1},clearTimeout(){},crypto,AbortSignal,FormData:class{},Date};
vm.createContext(context);for(const file of ['app.js','vm-workspace.js','infrastructure.js','live.js','bootstrap.js'])vm.runInContext(fs.readFileSync('dist/'+file,'utf8'),context);
const run=source=>vm.runInContext(source,context);
assert.equal(run('state.mode'),'demo');assert.equal(run('state.authenticated'),true);
for(const page of ['overview','clusters','vms','market','jobs','settings']){run(`nav('${page}')`);assert.ok(element('#app').innerHTML.includes('ORBIT CENTRAL'));assert.ok(!element('#app').innerHTML.includes('undefined'));}
assert.ok(run("statusTag('offline')").includes('fail'));assert.ok(run("statusTag('unknown')").includes('neutral'));
assert.equal(run('totals().nodes.length'),3);assert.equal(run('totals().vms.length'),6);
run("state.query='postgres';state.category='Alle'");assert.equal((run('market()').match(/class="appcard"/g)||[]).length,1);
run("state.query='';state.category='Container';state.marketTab='custom'");assert.equal((run('market()').match(/class="appcard"/g)||[]).length,2);
assert.equal(run("esc('<script>')"),'&lt;script&gt;');
run("state.blueprints=[{...demoApps[0],name:'<img src=x onerror=alert(1)>',description:'<script>x</script>'}];state.query='';state.category='Alle'");assert.ok(!run('market()').includes('<script>x'));
run("state.blueprints=demoApps;configure('demo-0')");assert.ok(element('#modal').innerHTML.includes('Demo-Bereitstellung starten'));assert.ok(element('#modal').open);
for(const page of ['storage','network','protection','security','lifecycle','ecosystem']){
  run(`nav('${page}')`);assert.ok(element('#app').innerHTML.includes('ORBIT CENTRAL'));assert.ok(!element('#app').innerHTML.includes('undefined'),page);
}
assert.ok(run("moduleView('protection')").includes('PBS-Datastores'));
assert.ok(run("ecosystem()").includes('Offen'));
assert.ok(run("moduleView('lifecycle')").includes('installiert keine Updates'));
assert.ok(!run("readPanel('Test',{ok:false,error:'<script>x</script>'},[],()=>[])").includes('<script>x'));
run("state.scope='not-connected'");assert.equal(run('totals().nodes.length'),0);run("state.scope='all'");
run("vmDetails('demo:100')");assert.equal(run('state.page'),'object');assert.ok(element('#app').innerHTML.includes('Virtuelle Hardware')===false);run("state.objectTab='hardware';render()");assert.ok(element('#app').innerHTML.includes('Laufwerk hinzufügen'));assert.ok(element('#app').innerHTML.includes('CPU / RAM ändern'));for(const tab of ['summary','hardware','network','snapshots','backup','console','options']){run(`state.objectTab='${tab}';render()`);assert.ok(!element('#app').innerHTML.includes('undefined'),tab)}
run("vmAction(state.resources.find(v=>v.vmid===100),'snapshot')");assert.ok(element('#modal').innerHTML.includes('confirm_name'));
for(const pattern of element('#modal').innerHTML.matchAll(/ pattern="([^"]+)"/g))assert.doesNotThrow(()=>new RegExp(pattern[1],'v'));
run("state.connections=[];state.resources=[];state.blueprints=[];state.catalog={configured:true,packages:[]};state.marketTab='central';state.mode='live'");assert.ok(run('overview()').includes('Einrichtung starten'));assert.ok(run('market()').includes('Noch keine Pakete verfügbar'));assert.ok(!run('overview()').includes('NaN'));
for(const markup of [element('#modal').innerHTML,run("openBlueprintForm([{vmid:9000,name:'base',node:'pve-01'}],[{node:'pve-01',storages:['local'],bridges:['vmbr0']}],'demo');modal.innerHTML")]){
  const ids=[...markup.matchAll(/ id="([^"]+)"/g)].map(m=>m[1]);assert.equal(new Set(ids).size,ids.length);
  for(const label of markup.matchAll(/<label for="([^"]+)"/g))assert.ok(ids.includes(label[1]),'missing label target '+label[1]);
  for(const pattern of markup.matchAll(/ pattern="([^"]+)"/g))assert.doesNotThrow(()=>new RegExp(pattern[1],'v'));
}
console.log('Frontend: 12 views, filters, totals, HTML escaping, configurator and empty live state passed.');

(async()=>{
  run("state.mode='demo';state.authenticated=true;loadDemo();vmDetails('demo:105')");
  for(const action of ['disk_add','disk_resize','nic','metadata','iso','delete']){
    await run(`hardwareAction('${action}')`);assert.ok(element('#modal').innerHTML.includes('confirm_name'),action);
    for(const pattern of element('#modal').innerHTML.matchAll(/ pattern="([^"]+)"/g))assert.doesNotThrow(()=>new RegExp(pattern[1],'v'));
    run('closeModal(true)');
  }
  await run("createVM('create')");assert.ok(element('#modal').innerHTML.includes('Ziele und Medien laden'));run('closeModal(true)');
  run("state.objectTab='console';render()");await run('openConsole()');assert.ok(element('#console-state').textContent.includes('Live-Installation'));
  run("state.mode='live';state.authenticated=true");
  run("state.mode='demo';applyDemoWorkspace({action:'create',connection_id:'demo',node:'pve-01',vmid:333,name:'demo-created',cpu:2,ram:4,disk:32});loadDemo()");assert.ok(run("state.resources.some(v=>v.vmid===333)"));run("applyDemoWorkspace({action:'delete',connection_id:'demo',vmid:333});loadDemo()");assert.equal(run("state.resources.some(v=>v.vmid===333)"),false);run("state.mode='live'");
  // A pending request cannot be closed by Escape/backdrop/cancel; forced auth cleanup can.
  run("modal.showModal();setModalBusy(true);closeModal()");assert.ok(element('#modal').open);
  run('closeModal(true)');assert.equal(element('#modal').open,false);
  // A 401 must close the modal so that the sign-in form is reachable.
  context.fetch=async()=>({ok:false,status:401,json:async()=>({error:'Bitte anmelden.'})});
  run("state.authenticated=true;state.csrf='old';modal.showModal();setModalBusy(true)");
  await assert.rejects(run("api('jobs')"),/Bitte anmelden/);
  assert.equal(element('#modal').open,false);assert.equal(run('state.csrf'),'');assert.ok(element('#app').innerHTML.includes('Willkommen zurück.'));
  // Failed submissions can be retried with the same idempotency key.
  const submitted=[];context.fetch=async(path,opts)=>{submitted.push(JSON.parse(opts.body));return {ok:false,status:400,json:async()=>({error:'Testfehler'})}};
  context.FormData=class{constructor(form){this.data=form.values}[Symbol.iterator](){return Object.entries(this.data)[Symbol.iterator]()}};
  run("state.authenticated=true;state.mode='live';state.blueprints=demoApps;configure('demo-0')");
  const form=element('#deploy-form');form.values={name:'retry-vm',cpu:'2',ram:'4',disk:'32',ssh:''};form.elements={start:{checked:true}};
  const event={preventDefault(){},target:form};await form.onsubmit(event);assert.equal(run('modalBusy'),false);await form.onsubmit(event);
  assert.equal(submitted.length,2);assert.equal(submitted[0].request_key,submitted[1].request_key);
  // Refresh requests issued during an earlier poll must not be silently discarded.
  let hits=0;context.fetch=async path=>{hits++;return {ok:true,status:200,json:async()=>[]}};
  await Promise.all([run('refresh(false)'),run('refresh(false)')]);assert.equal(hits,14);
  console.log('Frontend: modal submission lock, 401 cleanup, stable retry keys and queued refreshes passed.');
})().catch(e=>{console.error(e);process.exitCode=1});

// Connection failover form and the dedicated LXC workspace.
assert.ok(run("field('Nodes','endpoints','https://node2:8006\\nhttps://node3:8006',{textarea:true,optional:true})").includes('<textarea'));
assert.ok(run("containerView({vm:{type:'lxc',name:'ct-web',vmid:201,node:'pve-01',status:'stopped'},config:{cores:2,memory:2048},snapshots:{ok:true,data:[]}})").includes('data-core-action="ha"'));
assert.ok(!run("containerView({vm:{type:'lxc',name:'ct-web',vmid:201,node:'pve-01',status:'stopped'},config:{},snapshots:{ok:true,data:[]}})").includes('data-hardware-action'));
console.log('Frontend: failover textarea and separate LXC/HA workspace passed.');

run("state.mode='live';state.catalog={configured:true,revision:'test',updated:1,packages:[{id:'pkg',name:'<script>unsafe</script>',description:'Local package',version:'1',category:'OS',assets:[]}]}");
assert.ok(run('market()').includes('&lt;script&gt;unsafe&lt;/script&gt;'));
assert.ok(!run('market()').includes('<script>unsafe</script>'));
run("state.mode='demo';loadDemo()");assert.ok(run('vms()').includes('data-vm="demo:100"'));
assert.ok(run('shell()').includes('prism-topbar'));assert.ok(!run('shell()').includes('resource-tree'));assert.ok(!run('shell()').includes('class="rail"'));
console.log('Frontend: central marketplace escaping, VM links and single-navigation shell passed.');

// Settings navigation and HTML escaping share the real rendering functions.
for(const tab of ['appearance','connections','defaults','marketplace','account','system']){run(`state.settingsTab='${tab}';state.systemStatus={version:'0.10.0',public_origin:'<script>bad</script>'};state.sessions=[]`);const html=run('settings()');assert.ok(html.includes('settings-content'));assert.ok(!html.includes('<script>bad</script>'));assert.ok(!html.includes('undefined'));}
run("state.preferences={...preferenceDefaults,default_cpu:8,theme:'ocean'}");assert.equal(run('preferences().default_cpu'),8);assert.ok(run("state.settingsTab='defaults';settings()").includes('value="8"'));
console.log('Frontend: six settings sections, preference values and system-status escaping passed.');
