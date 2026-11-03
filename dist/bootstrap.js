'use strict';
boot();setInterval(liveFallback,1000);setInterval(()=>{if(state.authenticated&&['network','security'].includes(state.page)&&!state.infra.busy&&!modal.open&&Date.now()-(state.infra.fetched||0)>4000)loadInfrastructure(state.page==='security'?'firewall':'network',true)},2000);
setInterval(()=>{if(state.authenticated&&state.page==='jobs'&&state.jobsTab==='proxmox'&&!document.hidden&&!modal.open)loadPveTasks()},5000);
