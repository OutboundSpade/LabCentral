import {connect} from './sdk.js';
const host=await connect();
const context=await host.request('context.read');
document.querySelector('#project').textContent=context.projectId;
document.querySelector('#create').onclick=async()=>{
 try {
  const template=await host.request('templates.create',{input:{name:`fixture-${crypto.randomUUID().slice(0,8)}`,idempotencyKey:crypto.randomUUID()}});
  const result=await host.request('templates.list',{input:{}});
  document.querySelector('#created').textContent=result.items.some(t=>t.id===template.id)?'Created':'Missing';
 }catch(e){document.querySelector('#created').textContent=e.message;}
};
document.querySelector('#probe').onclick=async()=>{
 try {await host.request('templates.list',{input:{projectId:'project.foreign'}});document.querySelector('#denied').textContent='Unexpected success';}
 catch {document.querySelector('#denied').textContent='Denied';}
};
