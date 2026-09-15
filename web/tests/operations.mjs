import assert from 'node:assert/strict';
import {readFile,mkdir} from 'node:fs/promises';
import {createServer} from 'node:http';
import {WebSocketServer} from 'ws';
import {chromium,firefox,webkit} from 'playwright';
const build=new URL('../build/',import.meta.url),shots=new URL(process.env.LABCENTRAL_SCREENSHOTS || '../../bin/classroom-ops-20260907/screenshots/',import.meta.url);await mkdir(shots,{recursive:true});
let labs=[],deletes=[],fail=true,admin=false,usageDown=false,keys=[];
const now=()=>new Date().toISOString();
const lab=(id)=>({id:'instance.'+id,projectId:'project.class',revisionId:'revision.'+id,ownerKind:'user',ownerId:'user.student',phase:'ready',desiredLifecycle:'running',providerTargetId:'target.test',createdAt:now(),expiresAt:new Date(Date.now()+14400000).toISOString(),resources:[{id:'vm.workstation',kind:'VM',phase:'ready'}]});
const operation={id:'operation.queued',instanceId:'instance.one',kind:'create',state:'queued',attempt:0,createdAt:now(),updatedAt:now()};
const server=createServer(async(req,res)=>{
 const url=new URL(req.url,'http://localhost'),path=url.pathname;const json=(data,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(data))};
 if(path.startsWith('/api/v1/')){
 const p=path.slice(7);let body='';for await(const c of req)body+=c;
 if(p==='/server-info')return json({authMode:'oidc',defaultProviderTargetId:'target.test',uiFeatures:['browser-vnc']});
 if(p==='/session')return json({subjectId:'user.student',displayName:'Alex',roles:[admin?'platform-administrator':'participant']});
 if(p==='/projects')return json({items:[{id:'project.class',organizationId:'org.class',name:'Classroom',allowedActions:admin?['template.read','instance.batchCreate','quota.manage']:['template.read']}]});
 if(p==='/provider-targets')return json({items:[{id:'target.test',providerKind:'proxmox',status:'ready'}]});
 if(p==='/lab-templates')return json({items:['one','two'].map(id=>({id:'template.'+id,name:'Lab '+id,projectId:'project.class'}))});
 if(p.endsWith('/revisions'))return json({items:[{id:'revision.'+p.split('/')[2].split('.')[1]}]});
 if(p==='/lab-instances')return json({items:labs});
 if(p.endsWith('/actions/destroy')){const id=p.split('/')[2];deletes.push({id,key:req.headers['idempotency-key']});if(fail&&id==='instance.two')return json({detail:'Deletion temporarily unavailable'},503);labs=labs.map(i=>i.id===id?{...i,phase:'destroyed',desiredLifecycle:'destroyed'}:i);return json({operation:{...operation,id:'operation.delete.'+id,instanceId:id,kind:'destroy',state:'succeeded'}},202)}
 if(p.startsWith('/operations/'))return json({...operation,state:'succeeded'});
 if(p==='/admin/operations')return admin?json({items:url.searchParams.get('filter')==='failed'?[{...operation,id:'operation.failed',state:'failed',attempt:2,errorCode:'provider_unavailable',message:'Environment could not be reached.'}]:[operation],queued:1,running:0,failed:1}):json({detail:'Forbidden'},403);
 if(p.endsWith('/usage'))return !admin?json({detail:'Forbidden'},403):usageDown?json({detail:'Unavailable'},503):json({instanceId:p.split('/')[3],items:[{resourceId:'vm.workstation',status:'available',usage:{sampledAt:now(),cpuFraction:0.125,virtualCPUs:2,memoryUsedBytes:134217728,memoryCapacityBytes:536870912}}]});
 return json({detail:'Not found'},404);
 }
 let name=path==='/'?'index.html':path.replace(/^\//,'').replace(/\/$/,'');if(name.startsWith('access/'))name='200.html';if(['operations','lab','classroom'].includes(name))name+='.html';
 const file=new URL(name,build);if(!file.href.startsWith(build.href))return res.writeHead(404).end();
 try{res.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(await readFile(file))}catch{res.end()}
});
const wss=new WebSocketServer({server});
wss.on('connection',ws=>{
 let stage=0;
 ws.on('message',raw=>{
 const b=Buffer.from(raw);
 if(stage===0){assert(JSON.parse(b).oneTimeToken==='test-console');ws.send('{"authorized":true}');setTimeout(()=>ws.send(Buffer.from('RFB 003.008\n')),20);stage++;return}
 if(stage===1){ws.send(Buffer.from([1,1]));stage++;return}
 if(stage===2){ws.send(Buffer.alloc(4));stage++;return}
 if(stage===3){const name=Buffer.from('Test workstation');const init=Buffer.alloc(24+name.length);init.writeUInt16BE(800,0);init.writeUInt16BE(600,2);init[4]=32;init[5]=24;init[7]=1;init.writeUInt16BE(255,8);init.writeUInt16BE(255,10);init.writeUInt16BE(255,12);init[14]=16;init[15]=8;init.writeUInt32BE(name.length,20);name.copy(init,24);ws.send(init);stage++;return}
 let offset=0;while(offset<b.length){const type=b[offset];let length=0;if(type===0)length=20;else if(type===2)length=4+4*b.readUInt16BE(offset+2);else if(type===3)length=10;else if(type===4){length=8;keys.push({down:b[offset+1],key:b.readUInt32BE(offset+4)})}else if(type===5)length=6;else if(type===150)length=10;else break;offset+=length;}
 });
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin=`http://127.0.0.1:${server.address().port}`;
try{for(const[name,engine]of Object.entries({chromium,firefox,webkit})){
 const browser=await engine.launch();try{
 labs=[lab('one'),lab('two')];deletes=[];fail=true;admin=false;usageDown=false;keys=[];
 const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(10000);
 await page.waitForLoadState("networkidle");await page.goto(origin);await page.getByRole('link',{name:'Lab one',exact:true}).waitFor();assert.equal(await page.getByRole('link',{name:'Operations',exact:true}).count(),0);
 await page.getByRole('checkbox',{name:'Select all active labs'}).check();await page.getByRole('button',{name:'Delete selected'}).click();await page.getByRole('heading',{name:'Delete 2 labs?'}).waitFor();await page.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(deletes.length,0);
 await page.getByRole('button',{name:'Delete selected'}).click();await page.getByRole('button',{name:'Delete labs and files'}).click();await page.getByText('1 deletion queued. 1 failed. Select those labs to retry.').waitFor();await page.getByText('Deletion temporarily unavailable').waitFor();assert.equal(deletes.length,2);
 const retryKey=deletes.find(d=>d.id==='instance.two').key;fail=false;
 await page.getByRole('button',{name:'Delete selected'}).click();await page.getByRole('heading',{name:'Delete 1 labs?'}).waitFor();await page.getByRole('button',{name:'Delete labs and files'}).click();await page.getByRole('heading',{name:'Your next lab starts here'}).waitFor();assert.equal(deletes.at(-1).key,retryKey);assert.equal(deletes.filter(d=>d.id==='instance.one').length,1);
 await page.waitForLoadState("networkidle");await page.goto(origin+'/operations');await page.getByRole('heading',{name:'This view needs additional access'}).waitFor();
 labs=[lab('one'),lab('two')];admin=true;await page.waitForLoadState("networkidle");await page.goto(origin+'/operations');await page.getByRole('heading',{name:'VM usage',exact:true}).waitFor();await page.getByRole('cell',{name:/12\.5%/}).first().waitFor();await page.getByText('128 MiB / 512 MiB').first().waitFor();
 await page.locator('.apexcharts-canvas').nth(2).waitFor();
 await page.getByText('View vm cpu data',{exact:true}).click();await page.getByRole('cell',{name:'12.5%',exact:true}).first().waitFor();
 await page.getByText('View vm memory data',{exact:true}).click();await page.getByRole('cell',{name:'25.0%',exact:true}).first().waitFor();
 await page.getByLabel('Operation filter').selectOption('failed');await page.getByText('View error',{exact:true}).click();await page.getByText('Environment could not be reached.').waitFor();
 if(name==='chromium')await page.screenshot({path:new URL('operations-desktop.png',shots).pathname,fullPage:true});
 usageDown=true;await page.getByRole('button',{name:'Refresh',exact:true}).click();await page.getByText('Unavailable',{exact:true}).first().waitFor();assert.equal(await page.getByText('12.5%',{exact:false}).count(),0);
 await page.setViewportSize({width:390,height:844});await page.waitForTimeout(400);await page.screenshot({path:new URL(`${name}-operations-mobile.png`,shots).pathname,fullPage:true});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 await page.setViewportSize({width:1440,height:1000});await page.waitForLoadState("networkidle");await page.goto(origin);await page.evaluate(()=>sessionStorage.setItem('labcentral.access-token.v1.access.controls','test-console'));await page.waitForLoadState("networkidle");await page.goto(origin+'/access/access.controls');await page.getByText('Connected',{exact:true}).waitFor();
 await page.getByLabel('Resize',{exact:true}).selectOption('actual');await page.getByLabel('Resize',{exact:true}).selectOption('remote');await page.getByLabel('Resize',{exact:true}).selectOption('fit');
 await page.getByLabel('Special key').selectOption('ctrl-alt-delete');await page.getByRole('button',{name:'Send key'}).click();await page.waitForTimeout(100);assert(keys.some(k=>k.key===0xffff&&k.down===1),`${name}: Delete key transmitted`);assert(keys.some(k=>k.key===0xffe3&&k.down===1));assert(keys.some(k=>k.key===0xffe9&&k.down===1));
 await page.getByLabel('View only').check();assert(await page.getByRole('button',{name:'Send key'}).isDisabled());await page.getByLabel('View only').uncheck();
 await page.getByRole('button',{name:'Full screen',exact:true}).click();await page.waitForTimeout(150);
 if(await page.getByRole('button',{name:'Exit full screen'}).isVisible())await page.getByRole('button',{name:'Exit full screen'}).click();else await page.getByText('Full screen is unavailable in this browser.').waitFor();
 if(name==='chromium')await page.screenshot({path:new URL('console-desktop.png',shots).pathname});await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));assert.deepEqual(errors,[]);
 console.log(`PASS ${name}: bulk cancel/partial failure/idempotent retry, admin usage/queue/errors/unavailable, real RFB special keys, resize/fullscreen/view-only and mobile`);
 }finally{await browser.close()}
}}finally{wss.close();server.closeAllConnections();await new Promise(r=>server.close(r));}
