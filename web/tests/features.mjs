import assert from 'node:assert/strict';
import {readFile,mkdir} from 'node:fs/promises';
import {createServer} from 'node:http';
import {chromium,firefox,webkit} from 'playwright';
import {featureExtensions,serveFeature} from './feature-fixture.mjs';
const build=new URL('../build/',import.meta.url);
const template={id:'tpl.one',name:'Shared workstation',description:'Work together.',projectId:'project.one',resourceVersion:1};
const revision={id:'rev.one',templateId:template.id,revisionNumber:1};
let imageFixtures=false,approved=false,failEnrollment=false;
let groups,mutations,labs,preparations,installed=true,admin=true,stage='awaiting-approval';
const server=createServer(async(req,res)=>{
 if(await serveFeature(req,res))return;
 const url=new URL(req.url,'http://local'),path=url.pathname;
 const json=(body,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(body));};
 if(path.startsWith('/api/v1/')){const p=path.slice(7);let raw='';for await(const chunk of req)raw+=chunk;const body=raw?JSON.parse(raw):{};if(req.method!=='GET')mutations.push({path:p,body,headers:req.headers});
 if(p==='/server-info')return json({authMode:'oidc',defaultProviderTargetId:'target.one'});
 if(p==='/session')return json({subjectId:'user.alice',displayName:'Alice',roles:[admin?'platform-administrator':'participant']});
 if(p==='/projects')return json({items:[{id:'project.one',name:'Workshop',organizationId:'org.one',allowedActions:admin?['template.read','template.create','template.revision.publish','team.manage']:['template.read']}]});
 if(p==='/extensions/browser')return json(installed?await featureExtensions(origin,admin?['template-library','teams']:['teams']):{items:[]});
 if(p==='/provider-targets')return json({items:[{id:'target.one',providerKind:'proxmox',status:'ready'}]});
 if(p==='/lab-templates')return json({items:[template]});
 if(p===`/lab-templates/${template.id}/revisions`)return json({items:[revision]});
 if(p.endsWith('/content'))return json({recipes:imageFixtures?[{id:'desktop',builder:'ansible',architecture:'amd64',available:true}]:[],source:'apiVersion: labcentral.io/v1alpha1',parameters:[{name:'count',type:'integer',description:'Machine count',required:true},{name:'choice',type:'enum',enum:[0,1],description:'Variant'}]});
 if(p==='/lab-instances')return json({items:labs});
 if(p.startsWith('/lab-instances/')&&req.method==='GET')return json(labs.find(l=>l.id===p.split('/')[2]));
 if(p.endsWith('/actions/destroy')){labs=labs.map(l=>l.id===p.split('/')[2]?{...l,phase:'destroyed'}:l);return json({operation:{id:'op.destroy',state:'succeeded'}});}
 if(p==='/projects/project.one/groups'){
  if(req.method==='POST'){const created=body.names.map((name,i)=>({id:`team.created${i}`,name,projectId:'project.one',joined:false,enrollmentOpen:body.enrollmentOpen,memberCount:0,resourceVersion:1}));groups.push(...created);return json({items:created});}
  return json({items:groups});
 }
 if(p.endsWith('/members'))return json({items:[{id:'user.alice',displayName:'Alice'},{id:'user.bob',displayName:'Bob'}]});
 if(p.startsWith('/projects/project.one/groups/')){const group=groups.find(g=>g.id===p.split('/')[4]);assert(group);if(req.method==='DELETE'){assert.equal(req.headers['if-match'],`"${group.resourceVersion}"`);groups=groups.filter(g=>g.id!==group.id);return json({});}if(p.endsWith('/enrollment')){if(failEnrollment&&group.id==='team.two'){failEnrollment=false;return json({detail:'Temporarily unavailable'},503);}assert.equal(req.headers['if-match'],`"${group.resourceVersion}"`);group.enrollmentOpen=body.open;group.resourceVersion++;}if(p.endsWith('/join')){group.joined=true;group.memberCount++;}if(p.endsWith('/leave')){group.joined=false;group.memberCount--;}return json(group);}
 if(p==='/deployment-preparations'){if(req.method==='POST'){assert.equal(body.preparation.inputs[0].Integer,2);assert.equal(body.preparation.inputs[0].Name,'count');assert.deepEqual(body.preparation.inputs[1].Enum,{String:null,Integer:0});const work={id:`prep.${preparations.length+1}`,state:'running',request:{kind:'preparation',preparation:body.preparation},result:{stage,importIds:['image.one']}};preparations.push(work);return json(work);}return json({items:preparations});}
 if(p.startsWith('/extension-operations/')){const w=preparations.find(w=>w.id===p.split('/')[2]);if(p.endsWith('/cancel'))w.state='canceled';return json({...w,result:{stage,importIds:['image.one']}});}
 if(p==='/extension-operations'&&imageFixtures)return json({items:[{id:'image.active',state:'succeeded',request:{kind:'image-import',import:{targetId:'target.one'}}},{id:'image.retired',state:'succeeded',request:{kind:'image-import',import:{targetId:'target.one'}}},{id:'retirement.one',state:'succeeded',request:{kind:'image-import',import:{retireBindingId:'image.retired'}}}]});
 if(p.startsWith('/image-bindings/')){if(p.endsWith('/policy')){approved=body.approved;return json({});}return json({id:p.split('/')[2],targetId:'target.one',retired:p.includes('retired'),approved:p.includes('active')&&approved,binding:{artifactDigest:'sha256:verified-test-source',architecture:'amd64'}});}
 if(p==='/builds'){assert.equal(body.recipeId,'desktop');return json({id:'build.one'});}
 if(['/source-bindings','/template-libraries','/extension-operations'].includes(p))return json({items:[]});
 return json({detail:'Unavailable'},404);
 }
 let name=path==='/'?'index.html':path.replace(/^\//,'').replace(/\/$/,'');if(['teams','templates','classroom','lab','launch'].includes(name))name+='.html';const file=new URL(name,build);if(!file.href.startsWith(build.href))return res.writeHead(404).end();try{res.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(await readFile(file));}catch{res.end();}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin=`http://127.0.0.1:${server.address().port}`;
try{for(const [name,engine] of Object.entries({chromium,firefox,webkit})){
 if(process.env.LABCENTRAL_BROWSER&&process.env.LABCENTRAL_BROWSER!==name)continue;
 const browser=await engine.launch();try{
 imageFixtures=false;approved=false;groups=[{id:'team.one',name:'Blue team',projectId:'project.one',joined:false,enrollmentOpen:true,memberCount:1,resourceVersion:1},{id:'team.two',name:'Closed team',projectId:'project.one',joined:false,enrollmentOpen:false,memberCount:0,resourceVersion:1}];labs=[{id:'lab.shared',name:'Shared workstation',projectId:'project.one',ownerKind:'team',ownerId:'team.one',providerTargetId:'target.one',revisionId:revision.id,phase:'ready',resources:[]}];mutations=[];preparations=[];installed=true;admin=true;stage='awaiting-approval';
 const page=await browser.newPage({viewport:{width:1280,height:900}});page.setDefaultTimeout(12000);const errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error('PAGE ERROR',e.message);});
 await mkdir(new URL('../../bin/plugin-usability/screenshots/',import.meta.url),{recursive:true});
 await page.goto(origin+'/teams/');const teams=page.frameLocator('iframe[title="Teams"]');await teams.getByRole('tab',{name:'Your team',exact:true}).click();await teams.getByRole('button',{name:'Find a group',exact:true}).click();
 assert.equal(await page.getByRole('link',{name:'Classroom',exact:true}).count(),0,'Teams works without Classroom installed');
 const blue=teams.locator('article').filter({has:teams.getByRole('heading',{name:'Blue team',exact:true})});
 const closed=teams.locator('article').filter({has:teams.getByRole('heading',{name:'Closed team',exact:true})});assert(await closed.getByRole('button',{name:'Join group',exact:true}).isDisabled());
 await blue.getByRole('button',{name:'Join group',exact:true}).click();await teams.getByRole('tabpanel',{name:'Your team',exact:true}).getByRole('heading',{name:'Shared workstation',exact:true}).waitFor();await teams.getByRole('tabpanel',{name:'Your team',exact:true}).getByText('Bob',{exact:true}).waitFor();assert.equal(mutations.find(m=>m.path.endsWith('/join')).body.userId,undefined);
 const teamPanel=teams.getByRole('tabpanel',{name:'Your team',exact:true});
 const labsBox=await teamPanel.getByRole('heading',{name:'Labs',exact:true}).boundingBox(),membersBox=await teamPanel.getByRole('heading',{name:'Members',exact:true}).boundingBox();assert(membersBox.x>labsBox.x+200&&Math.abs(membersBox.y-labsBox.y)<30,'Desktop members sit beside labs');
 labs[0].name='Investigating suspicious authentication activity across a shared Linux workstation';
 await teamPanel.getByRole('heading',{name:labs[0].name,exact:true}).waitFor();
 const titleBox=await teamPanel.getByRole('heading',{name:labs[0].name,exact:true}).boundingBox(),buttonBox=await teamPanel.getByRole('button',{name:'Open lab',exact:true}).boundingBox();assert(titleBox.x+titleBox.width+8<=buttonBox.x||titleBox.y+titleBox.height+8<=buttonBox.y,'Long lab names wrap without crowding actions');
  await page.screenshot({path:new URL(`../../bin/plugin-usability/screenshots/${name}-teams.png`,import.meta.url).pathname,fullPage:true});await teams.getByRole('tab',{name:'Manage',exact:true}).click();await teams.getByRole('button',{name:'Close enrollment',exact:true}).click();await teams.getByRole('tabpanel',{name:'Manage',exact:true}).getByText('2 members · Enrollment closed',{exact:true}).waitFor();
 await teams.getByRole('button',{name:'Deploy group lab',exact:true}).click();await teams.getByRole('dialog').getByLabel('Template',{exact:true}).selectOption(template.id);await teams.getByRole('dialog').getByLabel('Machine count',{exact:true}).fill('2');await teams.getByRole('dialog').getByLabel('Variant',{exact:true}).selectOption('0');await teams.getByRole('button',{name:'Prepare and deploy',exact:true}).click();await teams.getByText('Blue team · Waiting for image approval',{exact:true}).waitFor();assert.deepEqual(mutations.find(m=>m.path==='/deployment-preparations').body.preparation.owner,{kind:'team',id:'team.one'});
 await page.reload();await teams.getByText('Blue team · Waiting for image approval',{exact:true}).waitFor();
 const cancel=teams.getByRole('button',{name:'Cancel preparation',exact:true});await cancel.focus();await page.waitForTimeout(5500);assert(await cancel.evaluate(n=>n===document.activeElement),'Polling preserves keyboard focus');
 await teams.getByRole('button',{name:'End lab',exact:true}).click();await teams.getByRole('dialog',{name:'End this lab?'}).waitFor();await teams.getByRole('button',{name:'Cancel',exact:true}).click();assert(!mutations.some(m=>m.path.endsWith('/actions/destroy')));
 await teams.getByRole('tab',{name:'Your team',exact:true}).click();await teams.getByRole('button',{name:'Leave group',exact:true}).click();await teams.getByRole('dialog').getByRole('button',{name:'Leave group',exact:true}).click();await teams.getByRole('button',{name:'Find a group',exact:true}).waitFor();assert.equal(labs[0].phase,'ready','Leaving retained team lab');
 await teams.getByRole('tab',{name:'Manage',exact:true}).click();await teams.getByRole('button',{name:'Create groups',exact:true}).click();await teams.getByLabel('Group names — one per line (up to 100)',{exact:true}).fill('Red team\nGreen team');await teams.getByRole('dialog').getByRole('button',{name:'Create groups',exact:true}).click();await teams.getByRole('combobox',{name:'Group',exact:true}).locator('option').filter({hasText:'Red team'}).waitFor({state:'attached'});

 await teams.getByRole('combobox',{name:'Group',exact:true}).selectOption('team.created0');await teams.getByRole('button',{name:'Delete group',exact:true}).click();await teams.getByRole('dialog').getByText('Red team',{exact:true}).waitFor();await teams.getByRole('dialog').getByRole('button',{name:'Delete group',exact:true}).click();await teams.getByText('Group deleted.',{exact:true}).waitFor();assert(!groups.some(g=>g.id==='team.created0'));
 await teams.getByRole('combobox',{name:'Group',exact:true}).selectOption('team.one');await teams.getByRole('button',{name:'Deploy group lab',exact:true}).click();await teams.getByRole('dialog').getByText('Group: Blue team',{exact:true}).waitFor();await teams.getByRole('dialog').getByLabel('Template',{exact:true}).selectOption(template.id);await teams.getByRole('dialog').getByLabel('Machine count',{exact:true}).fill('2');await teams.getByRole('dialog').getByLabel('Variant',{exact:true}).selectOption('0');await teams.getByRole('button',{name:'Prepare and deploy',exact:true}).click();await teams.getByText('Lab preparation started.',{exact:true}).waitFor();assert(!groups.find(g=>g.id==='team.one').joined,'Admin deployed without joining');

 const manage=teams.getByRole('tabpanel',{name:'Manage',exact:true});
 await manage.getByRole('checkbox',{name:'Blue team',exact:true}).check();
 await manage.getByRole('checkbox',{name:'Closed team',exact:true}).check();
 failEnrollment=true;const enrollmentStart=mutations.length;
 await teams.getByRole('button',{name:'Open selected enrollment',exact:true}).click();
 await teams.getByRole('dialog').getByRole('button',{name:'Open enrollment',exact:true}).click();
 await teams.getByText('1 accepted · 1 failed',{exact:true}).waitFor();
 await teams.getByRole('button',{name:'Retry failed actions',exact:true}).click();
 await teams.getByText('1 accepted · 0 failed',{exact:true}).waitFor();
 const enrollmentCalls=mutations.slice(enrollmentStart).filter(m=>m.path.endsWith('/enrollment'));
 assert.equal(enrollmentCalls.filter(m=>m.path.includes('team.one')).length,1,'Bulk retry never repeats successful commands');
 const retried=enrollmentCalls.filter(m=>m.path.includes('team.two'));assert.equal(retried.length,2);assert(retried[0].headers['idempotency-key']);assert.equal(retried[0].headers['idempotency-key'],retried[1].headers['idempotency-key']);
 await manage.getByRole('checkbox',{name:'Blue team',exact:true}).check();
 await manage.getByRole('checkbox',{name:'Closed team',exact:true}).check();
 const prepStart=preparations.length;
 await teams.getByRole('button',{name:'Deploy selected groups',exact:true}).click();
 const bulkDialog=teams.getByRole('dialog',{name:'Deploy group labs',exact:true});
 await bulkDialog.getByLabel('Template',{exact:true}).selectOption(template.id);await bulkDialog.getByLabel('Machine count',{exact:true}).fill('2');await bulkDialog.getByLabel('Variant',{exact:true}).selectOption('0');
 await page.waitForTimeout(350);await page.screenshot({path:new URL(`../../bin/plugin-usability/screenshots/${name}-bulk-launch.png`,import.meta.url).pathname,fullPage:true});
 await bulkDialog.getByRole('button',{name:'Deploy selected groups',exact:true}).click();
 await teams.getByText('2 accepted · 0 failed',{exact:true}).waitFor();
 assert.deepEqual(preparations.slice(prepStart).map(p=>p.request.preparation.owner),[{kind:'team',id:'team.one'},{kind:'team',id:'team.two'}]);
 assert(!mutations.some(m=>m.path==='/lab-instance-batches'),'Ordinary group launches use individually authorized preparation');
 await bulkDialog.waitFor({state:'hidden'});assert(await teams.getByRole('button',{name:'Deploy selected groups',exact:true}).isDisabled(),'Cleared bulk selection disables actions');
 await teams.getByRole('tab',{name:'Labs',exact:true}).click();await teams.getByRole('checkbox',{name:'Select all matching labs',exact:true}).check();
 await teams.getByRole('button',{name:'End selected labs',exact:true}).click();await teams.getByRole('dialog').getByRole('button',{name:'End labs',exact:true}).click();
 await teams.getByRole('tabpanel',{name:'Labs',exact:true}).getByText('1 accepted · 0 failed',{exact:true}).waitFor();assert.equal(labs[0].phase,'destroyed');
 await teams.getByRole('tab',{name:'Manage',exact:true}).click();await manage.getByRole('checkbox',{name:'Closed team',exact:true}).check();await manage.getByRole('checkbox',{name:'Green team',exact:true}).check();
 await teams.getByRole('button',{name:'Delete selected groups',exact:true}).click();await teams.getByRole('dialog').getByRole('button',{name:'Delete groups',exact:true}).click();await teams.getByText('2 accepted · 0 failed',{exact:true}).waitFor();assert.deepEqual(groups.map(g=>g.id),['team.one']);
 await page.goto(origin+'/templates/');const library=page.frameLocator('iframe[title="Template Library"]');await library.getByRole('heading',{name:'Published templates'}).waitFor();
 assert.equal(await library.getByRole('combobox',{name:'Template',exact:true}).count(),1,'Launch uses one visible template selection');
 await library.getByRole('tab',{name:'Author',exact:true}).click();const source=library.getByLabel('Lab YAML',{exact:true});await source.fill('# unsaved draft');
 await library.getByRole('tab',{name:'Browse',exact:true}).click();await library.getByRole('button',{name:'Refresh templates',exact:true}).click();await library.getByRole('tab',{name:'Author',exact:true}).click();assert.equal(await source.inputValue(),'# unsaved draft','Refresh retains edits');
 page.once('dialog',d=>d.dismiss());await page.getByRole('link',{name:'Teams',exact:true}).click();assert(page.url().includes('/templates'),'Navigation guard retains draft');
 await library.getByRole('button',{name:'New template',exact:true}).click();await library.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(await source.inputValue(),'# unsaved draft');
 await library.getByRole('button',{name:'New template',exact:true}).click();await library.getByRole('dialog').getByRole('button',{name:'Discard changes',exact:true}).click();assert.equal(await source.inputValue(),'');
 await library.getByRole('tab',{name:'Libraries',exact:true}).click();await library.getByText('No GitHub source is configured',{exact:false}).waitFor();assert(await library.getByRole('button',{name:'Add library',exact:true}).isDisabled());
 await library.getByRole('tab',{name:'Images',exact:true}).click();await library.getByRole('button',{name:'Build image',exact:true}).click();await library.getByRole('alert').filter({hasText:'Choose a template with build recipes'}).waitFor();
 imageFixtures=true;await page.reload();await library.getByRole('tab',{name:'Images',exact:true}).click();await library.getByRole('heading',{name:'Imported image · Retired',exact:true}).waitFor();assert.equal(await library.getByRole('button',{name:'Approve image',exact:true}).count(),1,'Only the active unapproved binding offers approval');assert.equal(await library.getByRole('button',{name:'Retire image',exact:true}).count(),1,'Retired bindings and retirement operations offer no destructive action');
 await library.getByRole('button',{name:'Approve image',exact:true}).click();await library.getByRole('dialog').getByRole('button',{name:'Cancel',exact:true}).click();assert(!approved);
 await library.getByRole('button',{name:'Approve image',exact:true}).click();await library.getByRole('dialog').getByRole('button',{name:'Approve image',exact:true}).click();await library.getByRole('heading',{name:'Imported image · Approved',exact:true}).waitFor();
 await library.getByRole('button',{name:'Build image',exact:true}).click();await library.getByText('Image build queued.',{exact:true}).waitFor();assert.equal(mutations.filter(m=>m.path==='/builds').length,1);
 await library.getByRole('tab',{name:'Browse',exact:true}).click();
 for(const mode of ['light','dark']){await page.emulateMedia({colorScheme:mode});const frame=page.frames().find(f=>f.url().includes('__features/template-library'));assert.equal(await frame.evaluate(()=>getComputedStyle(document.documentElement).colorScheme),'dark');}
 await page.screenshot({path:new URL(`../../bin/plugin-usability/screenshots/${name}-library.png`,import.meta.url).pathname,fullPage:true});
 admin=false;await page.goto(origin+'/templates/');await page.getByText('This feature is not installed or is unavailable.',{exact:true}).waitFor();assert.equal(await page.locator('iframe[title="Template Library"]').count(),0);assert.equal(await page.getByRole('link',{name:'Template Library',exact:true}).count(),0);
 await page.goto(origin+'/teams/');await teams.getByRole('button',{name:'Find a group',exact:true}).waitFor();assert.equal(await teams.getByRole('tab',{name:'Manage',exact:true}).count(),0);
 await page.setViewportSize({width:390,height:844});await page.waitForTimeout(400);const frame=page.frames().find(f=>f.url().includes('__features/teams'));assert(await frame.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Feature has no mobile overflow');await page.screenshot({path:new URL(`../../bin/plugin-usability/screenshots/${name}-mobile.png`,import.meta.url).pathname});
 installed=false;await page.waitForLoadState('networkidle');await page.goto(origin+'/teams/');await page.getByText('This feature is not installed or is unavailable.',{exact:true}).waitFor();await page.goto(origin);await page.getByRole('heading',{name:'My labs',exact:true}).waitFor();assert.equal(await page.getByRole('link',{name:'Teams',exact:true}).count(),0);
 assert.deepEqual(errors,[]);console.log(`PASS: ${name} independent Teams, enrollment, team preparation, approval recovery, retained labs, participant Teams-only access, mobile and core-only navigation`);
 }finally{await browser.close();}
}}finally{server.closeAllConnections();await new Promise(r=>server.close(r));}
