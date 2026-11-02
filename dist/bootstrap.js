'use strict';
boot();setInterval(liveFallback,1000);setInterval(()=>{if(state.authenticated&&['network','security'].includes(state.page)&&!state.infra.busy&&!modal.open&&Date.now()-(state.infra.fetched||0)>4000)loadInfrastructure(state.page==='security'?'firewall':'network',true)},2000);
