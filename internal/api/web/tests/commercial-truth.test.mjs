import assert from 'node:assert/strict';
import test from 'node:test';
import {build} from 'esbuild';
import {createRequire} from 'node:module';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,dirname} from 'node:path';
import {fileURLToPath} from 'node:url';

test('bilingual commercial claims preserve settlement, geography and finite offer terms',async()=>{
 const directory=await mkdtemp(join(tmpdir(),'prism-commercial-render-'));
 try {
  const outfile=join(directory,'render.cjs');
  await build({stdin:{resolveDir:dirname(dirname(fileURLToPath(import.meta.url))),loader:'tsx',contents:`
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router-dom';
import {TooltipProvider} from '@radix-ui/react-tooltip';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {DepositPage} from './src/features/me/DepositPage';
import {PlansPage,PlanLimits} from './src/features/me/PlansPage';
import {ProxyGenerator} from './src/features/me/ProxyGenerator';
import i18n,{primeLocale} from './src/i18n';
import {ensureEnglish} from './src/i18n/english';
export async function render(){
 await primeLocale();await ensureEnglish();const out=[];
 const plan={id:'finite',name:'Finite offer',price_micro:1000000,traffic_bytes:131072,duration_days:30,period:'30d',platforms:[],max_rps:1,max_bandwidth_bps:1024,max_concurrent:2,max_keys:3};
 for(const language of ['zh-CN','en-US']){
  await i18n.changeLanguage(language);
  for(const status of ['pending','released','failed']){
   const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});
   client.setQueryData(['me','ldc','info'],{enabled:true,rate:1});
   client.setQueryData(['me','deposit','info'],{enabled:false});
   client.setQueryData(['me','deposit','intents'],{items:[{id:'order',status,amount_micro:1000000,created_at_ns:1}]});
   client.setQueryData(['me-plans'],{items:[plan]});
   const wrap=e=>renderToStaticMarkup(<QueryClientProvider client={client}><TooltipProvider><MemoryRouter>{e}</MemoryRouter></TooltipProvider></QueryClientProvider>);
   out.push({language,status,deposit:wrap(<DepositPage/>),catalog:wrap(<PlansPage/>),terms:wrap(<PlanLimits plan={plan}/>),unknown:wrap(<ProxyGenerator platforms={[{platform_id:'name',platform_name:'US-name-only'}]}/>),measured:wrap(<ProxyGenerator platforms={[{platform_id:'measured',platform_name:'JP-name',measured_countries:['US']}]}/>)});
   client.clear();
  }
 }
 return out;
}`},bundle:true,platform:'node',format:'cjs',jsx:'automatic',define:{'import.meta.env':'{}'},outfile,logLevel:'silent'});
  const results=await createRequire(import.meta.url)(outfile).render();
  for(const item of results){
   assert.doesNotMatch(item.deposit,/实时到账|即时结算|instant credit|instant settlement/i);
   assert.match(item.deposit,item.language==='zh-CN'?/结算验证/:/settlement verification/i);
   assert.match(item.deposit,item.language==='zh-CN'?/联系运营/:/contact the operator/i);
   assert.doesNotMatch(item.unknown,/country-badge|Platform \/ Country|平台 \/ 国家/);
   assert.match(item.unknown,item.language==='zh-CN'?/平台名称不代表地理位置/:/platform names do not indicate location/);
   assert.match(item.measured,item.language==='zh-CN'?/已观测出口国家：US/:/Observed exit countries: US/);
   for(const html of [item.catalog,item.terms]){
    assert.match(html,/1 RPS/);assert.match(html,/1024 B\/s/);
    for(const label of item.language==='zh-CN'?['每密钥请求上限','每密钥双向合计带宽','每密钥并发连接上限','活跃密钥上限']:['Requests per second per key','Combined bandwidth per key','Concurrent connections per key','Active key limit'])assert.ok(html.includes(label));
   }
  }
 } finally {await rm(directory,{recursive:true,force:true});}
});
