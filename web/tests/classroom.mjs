import assert from 'node:assert/strict';
import { readFile, mkdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import { chromium, firefox, webkit } from 'playwright';
const build=new URL('../build/',import.meta.url), shots=new URL(process.env.LABCENTRAL_SCREENSHOTS || '../../bin/classroom-testbed/screenshots/',import.meta.url);
await mkdir(shots,{recursive:true});
const template={id:'template.debian',projectId:'project.class',name:'Debian starter',description:'Explore files and processes in your own Debian environment.',resourceVersion:1};
const revision={id:'revision.debian',templateId:template.id,revisionNumber:1};
const lab={id:'instance.student',projectId:'project.class',revisionId:revision.id,providerTargetId:'target.test',ownerKind:'user',ownerId:'user.student',phase:'ready',expiresAt:new Date(Date.now()+14400000).toISOString(),resources:[{id:'workstation',kind:'VM',phase:'ready'}]};
let subjectId="user.student",noProjects=false,sessionDenied=false,slowNavigation=false,reads=[],labs=[],mutations=[],outage=false,role='participant',consoleLaunchKind='relative';
const server=createServer(async(req,res)=>{
 const url=new URL(req.url,'http://localhost'),path=url.pathname;
 const json=(data,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(data));};
 if(path.startsWith('/api/v1/')) {
  if(slowNavigation && req.method==='GET')await new Promise(r=>setTimeout(r,200));
  const p=path.slice(7);if(req.method==='GET')reads.push(p+url.search);let body='';for await(const chunk of req)body+=chunk;
  if(['POST','PUT'].includes(req.method))mutations.push({path:p,body:body?JSON.parse(body):null,headers:req.headers});
  if(p.startsWith('/quotas/')||p.startsWith('/lifetime-policies/')){
   if(url.searchParams.get('organizationId')!=='org.class')return json({detail:'Quota scope is invalid'},400);
   if(req.method==='GET')return json({detail:'Not configured'},404);
   return json({...JSON.parse(body),resourceVersion:1});
  }
  if(p.endsWith('/topology'))return json({instanceId:lab.id,generation:1,resources:lab.resources.map(r=>({...r,interfaces:[],actions:[{method:'vnc',title:'Console',presentation:'graphical',targetKinds:['VM'],endpointSchemas:[],maximumOfflineSeconds:30}]}))});
  if(p==='/server-info')return json({authMode:'oidc',defaultProviderTargetId:'target.test',uiFeatures:['browser-vnc']});
  if(p==='/session'&&sessionDenied)return json({detail:'Sign in required'},401);
  if(p==='/session')return json({subjectId,displayName:'Alex Morgan',roles:[role]});
 if(p.startsWith('/access-sessions/') && p.endsWith('/presentation'))return json({method:'vnc',presentation:'graphical',streamPath:'/access-proxy/v1/access/'+p.split('/')[2]+'/websocket'});
  if(p==='/access-sessions'&&req.method==='POST')return json({session:{id:'access.test'},oneTimeToken:'test-token',launchUrl:consoleLaunchKind==='relative'?'/access/access.test':consoleLaunchKind==='absolute'?origin+'/access/access.test':'https://foreign.example/access/access.test'},201);
  if(p==='/projects')return json({items:noProjects?[]:[{id:'project.class',name:'Linux fundamentals',organizationId:'org.class',allowedActions:role==='participant'?['template.read']:['template.read','template.create','instance.batchCreate','quota.manage','project.manage']}]});
  if(p==='/provider-targets')return json({items:[{id:'target.test',providerKind:'proxmox',status:'ready'}]});
  if(p==='/lab-templates')return json({items:[template]});
  if(p===`/lab-templates/${template.id}/revisions`)return json({items:[revision]});
  if(p.endsWith('/content'))return json({source:'apiVersion: labcentral.io/v1alpha1',parameters:[{name:'hostname',type:'string',description:'Workstation name',required:true}]});
  if(p==='/lab-instances'&&req.method==='GET')return outage?json({detail:'Temporarily unavailable'},503):json({items:labs});
  if(p==='/lab-instances'&&req.method==='POST'){labs=[lab];return json({operation:{id:'operation.create',instanceId:lab.id,state:'succeeded'}},202);}
  if(p.endsWith('/actions/destroy')){labs=[{...lab,phase:'destroyed'}];return json({operation:{id:'operation.end',state:'succeeded'}},202);}
  if(p==='/projects/project.class/members'||p==='/users')return json({items:[{id:'user.student',displayName:'Alex Morgan',username:'alex',resourceVersion:1}]});
  if(p.startsWith('/lab-instance-batches'))return json({id:'batch.test',items:[{ownerId:'user.student',operation:{state:'succeeded'}}]},req.method==='POST'?202:200);
  return json({detail:'Not configured'},404);
 }
 let name=path==='/'?'index.html':path.replace(/^\//,'').replace(/\/$/,'');
 if(name.startsWith('access/'))name='200.html';
 if(['launch','lab','templates','classroom','people','settings','operations'].includes(name))name+='.html';
 const file=new URL(name,build);if(!file.href.startsWith(build.href))return res.writeHead(404).end();
 try{const data=await readFile(file);res.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(data);}catch{res.writeHead(404).end();}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin=`http://127.0.0.1:${server.address().port}`;
try{for(const[name,engine]of Object.entries({chromium,firefox,webkit})){
 if(process.env.LABCENTRAL_BROWSER && process.env.LABCENTRAL_BROWSER!==name)continue;
 const browser=await engine.launch();try{
  subjectId="user.student";noProjects=false;sessionDenied=false;reads=[];labs=[];mutations=[];outage=false;role='participant';
  const page=await browser.newPage({viewport:{width:1440,height:1000}});page.setDefaultTimeout(10000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.waitForLoadState("networkidle");await page.goto(origin);await page.getByRole('heading',{name:'Your next lab starts here'}).waitFor();
  assert.equal(await page.getByRole('link',{name:'People',exact:true}).count(),0);
  await page.evaluate(()=>{
   window.navigationShell=document.querySelector('.drawer');window.navigationFlashes=[];
   window.navigationObserver=new MutationObserver(()=>{
    if(!window.navigationShell.isConnected)window.navigationFlashes.push('shell replaced');
    if(document.querySelector('[aria-label="Loading workspace"]'))window.navigationFlashes.push('workspace skeleton');
    if(document.body.innerText.includes('Opening your workspace'))window.navigationFlashes.push('startup screen');
   });window.navigationObserver.observe(document.body,{childList:true,subtree:true});
  });slowNavigation=true;
  await page.getByRole('link',{name:'Browse labs'}).click();
  await page.getByLabel('Workstation name',{exact:false}).fill('student-workstation');
  assert.equal(reads.filter(p=>p.startsWith('/lab-templates?')).length,1,`${name}: catalogue cached across route navigation`);
  assert.equal(reads.filter(p=>p.startsWith('/lab-templates/template.debian/revisions?')).length,1,`${name}: revisions reused by both metadata and launch reads`);
  await page.getByRole('button',{name:'Launch lab',exact:true}).click();await page.getByRole('heading',{name:'My labs',exact:true}).waitFor();
  await page.getByRole('link',{name:'Debian starter',exact:true}).waitFor();
  slowNavigation=false;
  assert.deepEqual(await page.evaluate(()=>window.navigationFlashes),[],`${name}: navigation preserves shell under delayed requests`);
  assert(await page.evaluate(()=>window.navigationShell===document.querySelector('.drawer')));
  const create=mutations.find(m=>m.path==='/lab-instances');assert.equal(create.body.revisionId,revision.id);assert.equal(create.body.ownerId,'user.student');assert(create.headers['idempotency-key']);assert.equal(create.body.inputs[0].string,'student-workstation');
  await page.getByRole('link',{name:'Open Debian starter',exact:true}).click();await page.getByRole('button',{name:'Open console'}).waitFor();await page.getByRole('heading',{name:'Lab instructions'}).waitFor();
  await page.goBack();await page.getByRole('heading',{name:'My labs',exact:true}).waitFor();
  await page.goForward();await page.getByRole('button',{name:'Open console'}).waitFor();
  assert.deepEqual(await page.evaluate(()=>window.navigationFlashes),[],`${name}: back/forward preserves shell`);
  await page.getByRole('navigation',{name:'Breadcrumb',exact:true}).getByRole('link',{name:'My labs',exact:true}).click();
  await page.locator('a[href="/launch/"]').last().click();
  await page.locator('a[href="/"]').last().click();await page.getByRole('heading',{name:'My labs',exact:true}).waitFor();
  await page.getByRole('link',{name:'Open Debian starter',exact:true}).click();await page.getByRole('button',{name:'Open console'}).waitFor();
  assert.deepEqual(await page.evaluate(()=>window.navigationFlashes),[],`${name}: rapid navigation preserves shell`);
  await page.evaluate(()=>window.navigationObserver.disconnect());
  if(name==='chromium')await page.screenshot({path:new URL('desktop-lab.png',shots).pathname,fullPage:true});
  await page.getByRole('button',{name:'End lab',exact:true}).click();await page.getByRole('dialog').waitFor();await page.getByRole('button',{name:'Keep lab'}).click();assert(!mutations.some(m=>m.path.endsWith('/destroy')));
  await page.addInitScript(()=>{window.WebSocket=class extends EventTarget{constructor(url){super();window.consoleSocketURL=url;this.readyState=0;}close(){this.readyState=3;}send(){}};});
  for(consoleLaunchKind of ['relative','absolute']){
   await page.waitForLoadState("networkidle");await page.goto(origin+'/lab?id='+lab.id);await page.getByRole('button',{name:'Open console'}).click();await page.waitForURL(origin+'/access/access.test');
   await page.getByRole('heading',{name:'Lab console',exact:true}).waitFor();
   await page.waitForFunction(()=>window.consoleSocketURL?.endsWith('/access-proxy/v1/access/access.test/websocket'));
   assert.equal(await page.evaluate(()=>sessionStorage.getItem('labcentral.access-token.v1.access.test')),null);
  }
  consoleLaunchKind='foreign';await page.waitForLoadState("networkidle");await page.goto(origin+'/lab?id='+lab.id);await page.getByRole('button',{name:'Open console'}).click();await page.getByText('The console launch was incomplete. Please try again.').waitFor();assert.equal(new URL(page.url()).origin,origin);
  await page.waitForLoadState("networkidle");await page.goto(origin);outage=true;await page.getByRole('button',{name:'Refresh labs'}).click();await page.getByText('Updates are paused. Showing the last known state.').waitFor();outage=false;await page.getByRole('button',{name:'Retry',exact:true}).click();
  await page.waitForLoadState("networkidle");await page.goto(origin+'/people');await page.getByRole('heading',{name:'This view needs additional access'}).waitFor();role='platform-administrator';
  for(const route of ['classroom','templates','people','settings']){
   await page.waitForLoadState("networkidle");await page.goto(origin+'/'+route);await page.getByRole('heading',{name:route[0].toUpperCase()+route.slice(1),exact:true}).waitFor();await page.waitForTimeout(200);if(name==='chromium')await page.screenshot({path:new URL(`desktop-${route}.png`,shots).pathname,fullPage:true});
  }
  await page.getByRole('button',{name:'Save limits'}).click();
  await page.getByText('Project limits saved. Existing lab expiry times are unchanged.').waitFor();
  assert.equal(mutations.find(m=>m.path==='/quotas/project/project.class/instances').body.limit,25);
  assert.equal(mutations.find(m=>m.path==='/lifetime-policies/project/project.class').body.defaultTtlSeconds,14400);
  await page.waitForLoadState("networkidle");await page.goto(origin+'/classroom');await page.getByRole('heading',{name:'Students & labs'}).waitFor();await page.getByLabel('Select Alex Morgan').check();await page.getByRole('button',{name:'Launch labs',exact:true}).click();await page.getByRole('dialog').waitFor();await page.getByLabel('Workstation name',{exact:false}).fill('classroom');await page.getByLabel('Lab lifetime (minutes)',{exact:false}).fill('90');await page.getByRole('button',{name:'Launch for 1 selected'}).click();await page.getByText('Classroom launch accepted.').waitFor();await page.waitForFunction(()=>!document.querySelector('button.btn-primary:disabled'));assert.equal(mutations.find(m=>m.path==='/lab-instance-batches').body.owners[0].ownerId,'user.student');assert.equal(mutations.find(m=>m.path==='/lab-instance-batches').body.ttlSeconds,5400);await page.getByRole('dialog',{name:'Launch classroom labs'}).waitFor({state:'hidden'});
  labs=[{...lab,id:'instance.bulk-a'},{...lab,id:'instance.bulk-b'}];
  await page.reload();await page.getByRole('heading',{name:'Students & labs'}).waitFor();
  await page.getByRole('checkbox',{name:'Select all active labs'}).check();
  await page.getByRole('button',{name:'End selected labs'}).click();
  await page.getByRole('heading',{name:'Delete 2 labs?'}).waitFor();
  await page.getByRole('button',{name:'Delete labs and files'}).click();
  await page.getByText('2 deletions queued.').waitFor();
  assert(mutations.some(m=>m.path==='/lab-instances/instance.bulk-a/actions/destroy'));
  assert(mutations.some(m=>m.path==='/lab-instances/instance.bulk-b/actions/destroy'));
  await page.setViewportSize({width:390,height:844});
  for(const route of ['','launch','classroom','people','settings']){
   await page.waitForLoadState("networkidle");await page.goto(origin+'/'+route);await page.getByRole('heading',{level:1}).waitFor();await page.waitForTimeout(200);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${name} ${route} mobile overflow`);if(name==='chromium')await page.screenshot({path:new URL(`mobile-${route||'labs'}.png`,shots).pathname,fullPage:true});
  }
  subjectId='user.other';noProjects=true;await page.evaluate(()=>document.querySelector('a[href="/"]').click());await page.getByRole('heading',{name:'No projects assigned yet',exact:true}).waitFor();
  sessionDenied=true;await page.evaluate(()=>document.querySelector('a[href="/launch/"]').click());await page.getByRole('heading',{name:'Sign in to LabCentral',exact:true}).waitFor();assert.equal(await page.locator('.drawer').count(),0);sessionDenied=false;
  assert.deepEqual(errors,[],`${name} page errors`);console.log(`PASS: ${name} launch, details, cancel, recovery, roles, batch, admin routes and mobile layout`);
 }finally{await browser.close();}
}}finally{server.closeAllConnections();await new Promise(r=>server.close(r));}
