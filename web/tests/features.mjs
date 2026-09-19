import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createServer} from 'node:http';
import {chromium,firefox,webkit} from 'playwright';
import {featureExtensions,serveFeature} from './feature-fixture.mjs';
const build=new URL('../build/',import.meta.url);
const template={id:'tpl.one',name:'Shared workstation',description:'Work together.',projectId:'project.one',resourceVersion:1};
const revision={id:'rev.one',templateId:template.id,revisionNumber:1};
let groups,mutations,labs,preparations,installed=true,admin=true,stage='awaiting-approval';
const server=createServer(async(req,res)=>{
 if(await serveFeature(req,res))return;
 const url=new URL(req.url,'http://local'),path=url.pathname;
 const json=(body,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(body));};
 if(path.startsWith('/api/v1/')){const p=path.slice(7);let raw='';for await(const chunk of req)raw+=chunk;const body=raw?JSON.parse(raw):{};if(req.method!=='GET')mutations.push({path:p,body,headers:req.headers});
 if(p==='/server-info')return json({authMode:'oidc',defaultProviderTargetId:'target.one'});
 if(p==='/session')return json({subjectId:'user.alice',displayName:'Alice',roles:[admin?'platform-administrator':'participant']});
 if(p==='/projects')return json({items:[{id:'project.one',name:'Workshop',organizationId:'org.one',allowedActions:admin?['template.read','template.create','team.manage']:['template.read']}]});
 if(p==='/extensions/browser')return json(installed?await featureExtensions(origin,['template-library','teams']):{items:[]});
 if(p==='/provider-targets')return json({items:[{id:'target.one',providerKind:'proxmox',status:'ready'}]});
 if(p==='/lab-templates')return json({items:[template]});
 if(p===`/lab-templates/${template.id}/revisions`)return json({items:[revision]});
 if(p.endsWith('/content'))return json({source:'apiVersion: labcentral.io/v1alpha1',parameters:[{name:'count',type:'integer',description:'Machine count',required:true},{name:'choice',type:'enum',enum:[0,1],description:'Variant'}]});
 if(p==='/lab-instances')return json({items:labs});
 if(p.startsWith('/lab-instances/')&&req.method==='GET')return json(labs.find(l=>l.id===p.split('/')[2]));
 if(p.endsWith('/actions/destroy')){labs=labs.map(l=>l.id===p.split('/')[2]?{...l,phase:'destroyed'}:l);return json({operation:{id:'op.destroy',state:'succeeded'}});}
 if(p==='/projects/project.one/groups'){
  if(req.method==='POST'){groups.push(...body.names.map((name,i)=>({id:`team.created${i}`,name,projectId:'project.one',joined:false,enrollmentOpen:body.enrollmentOpen,memberCount:0,resourceVersion:1})));return json({items:groups});}
  return json({items:groups});
 }
 if(p.startsWith('/projects/project.one/groups/')){const group=groups.find(g=>g.id===p.split('/')[4]);assert(group);if(p.endsWith('/enrollment')){assert.equal(req.headers['if-match'],`"${group.resourceVersion}"`);group.enrollmentOpen=body.open;group.resourceVersion++;}if(p.endsWith('/join')){group.joined=true;group.memberCount++;}if(p.endsWith('/leave')){group.joined=false;group.memberCount--;}return json(group);}
 if(p==='/deployment-preparations'){if(req.method==='POST'){assert.equal(body.preparation.inputs[0].Integer,2);assert.equal(body.preparation.inputs[0].Name,'count');assert.deepEqual(body.preparation.inputs[1].Enum,{String:null,Integer:0});const work={id:'prep.one',state:'running',request:{kind:'preparation',preparation:body.preparation},result:{stage,importIds:['image.one']}};preparations.push(work);return json(work);}return json({items:preparations});}
 if(p.startsWith('/extension-operations/')){const w=preparations.find(w=>w.id===p.split('/')[2]);if(p.endsWith('/cancel'))w.state='canceled';return json({...w,result:{stage,importIds:['image.one']}});}
 if(['/source-bindings','/template-libraries','/extension-operations'].includes(p))return json({items:[]});
 return json({detail:'Unavailable'},404);
 }
 let name=path==='/'?'index.html':path.replace(/^\//,'').replace(/\/$/,'');if(['teams','templates','classroom','lab','launch'].includes(name))name+='.html';const file=new URL(name,build);if(!file.href.startsWith(build.href))return res.writeHead(404).end();try{res.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(await readFile(file));}catch{res.end();}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin=`http://127.0.0.1:${server.address().port}`;
try{for(const [name,engine] of Object.entries({chromium,firefox,webkit})){
 if(process.env.LABCENTRAL_BROWSER&&process.env.LABCENTRAL_BROWSER!==name)continue;
 const browser=await engine.launch();try{
 groups=[{id:'team.one',name:'Blue team',projectId:'project.one',joined:false,enrollmentOpen:true,memberCount:1,resourceVersion:1},{id:'team.two',name:'Closed team',projectId:'project.one',joined:false,enrollmentOpen:false,memberCount:0,resourceVersion:1}];labs=[{id:'lab.shared',projectId:'project.one',ownerKind:'team',ownerId:'team.one',providerTargetId:'target.one',revisionId:revision.id,phase:'ready',resources:[]}];mutations=[];preparations=[];installed=true;admin=true;stage='awaiting-approval';
 const page=await browser.newPage({viewport:{width:1280,height:900}});page.setDefaultTimeout(12000);const errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error('PAGE ERROR',e.message);});
 await page.goto(origin+'/teams/');const teams=page.frameLocator('iframe[title="Teams"]');await teams.getByRole('heading',{name:'Project groups'}).waitFor();
 assert.equal(await page.getByRole('link',{name:'Classroom',exact:true}).count(),0,'Teams works without Classroom installed');
 const blue=teams.locator('article').filter({has:teams.getByRole('heading',{name:'Blue team',exact:true})});
 const closed=teams.locator('article').filter({has:teams.getByRole('heading',{name:'Closed team',exact:true})});assert(await closed.getByRole('button',{name:'Join group',exact:true}).isDisabled());
 await blue.getByRole('button',{name:'Join group',exact:true}).click();await teams.getByRole('button',{name:'lab.shared',exact:true}).waitFor();assert.equal(mutations.find(m=>m.path.endsWith('/join')).body.userId,undefined);
 await blue.getByRole('button',{name:'Close enrollment',exact:true}).click();await blue.getByText('2 members · Enrollment closed · Your group',{exact:true}).waitFor();
 await teams.getByText('Deploy a group lab',{exact:true}).click();await teams.getByLabel('Template',{exact:true}).selectOption(template.id);await teams.getByLabel('Machine count',{exact:true}).fill('2');await teams.getByLabel('Variant',{exact:true}).selectOption('0');await teams.getByRole('button',{name:'Prepare and deploy',exact:true}).click();await teams.getByText('Waiting for an administrator to approve the imported images.',{exact:true}).waitFor();assert.deepEqual(mutations.find(m=>m.path==='/deployment-preparations').body.preparation.owner,{kind:'team',id:'team.one'});
 await page.waitForLoadState('networkidle');await page.reload();await teams.getByText('team team.one · Waiting for image approval',{exact:true}).waitFor();
 await teams.getByRole('button',{name:'End lab',exact:true}).click();await teams.getByRole('button',{name:'Cancel',exact:true}).click();assert(!mutations.some(m=>m.path.endsWith('/actions/destroy')));
 await blue.getByRole('button',{name:'Leave group',exact:true}).click();await teams.getByRole('button',{name:'Confirm',exact:true}).click();await teams.getByText('Join a group to deploy and access its labs.',{exact:true}).waitFor();assert.equal(labs[0].phase,'ready','Leaving retained team lab');
 await teams.getByText('Create project groups',{exact:true}).click();await teams.getByLabel('Group names — one per line (up to 100)',{exact:true}).fill('Red team\nGreen team');await teams.getByRole('button',{name:'Create groups',exact:true}).click();await teams.getByRole('heading',{name:'Red team',exact:true}).waitFor();assert.deepEqual(mutations.find(m=>m.path==='/projects/project.one/groups'&&m.body.names).body.names,['Red team','Green team']);
 admin=false;await page.waitForLoadState('networkidle');await page.goto(origin+'/templates/');const library=page.frameLocator('iframe[title="Template Library"]');await library.getByRole('heading',{name:'Published templates'}).waitFor();assert.equal(await library.getByText('Author a template',{exact:true}).count(),0,'Participant can browse without author rights');
 await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));const frame=page.frames().find(f=>f.url().includes('__features/template-library'));assert(await frame.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Feature has no mobile overflow');
 installed=false;await page.waitForLoadState('networkidle');await page.goto(origin+'/teams/');await page.getByText('This feature is not installed or is unavailable.',{exact:true}).waitFor();await page.goto(origin);await page.getByRole('heading',{name:'My labs',exact:true}).waitFor();assert.equal(await page.getByRole('link',{name:'Teams',exact:true}).count(),0);
 assert.deepEqual(errors,[]);console.log(`PASS: ${name} independent Teams, enrollment, team preparation, approval recovery, retained labs, participant library, mobile and core-only navigation`);
 }finally{await browser.close();}
}}finally{server.closeAllConnections();await new Promise(r=>server.close(r));}
