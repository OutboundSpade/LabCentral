import assert from 'node:assert/strict';
import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try {
 const app=process.argv[2];
 const context=await browser.newContext({ignoreHTTPSErrors:true,reducedMotion:'reduce',viewport:{width:1365,height:950}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();
 let documents=0,creations=0;
 page.on('request',request=>{if(request.isNavigationRequest()&&request.frame()===page.mainFrame())documents++;if(request.url().endsWith('/api/admin/meeting')&&request.method()==='POST')creations++});
 await page.goto(app+'/admin/');
 await page.getByLabel('Number of teams').waitFor();
 assert.equal(await page.getByLabel('Number of teams').inputValue(),'5');
 assert.equal(await page.getByLabel('Students per team').inputValue(),'6');
 assert.equal(await page.getByLabel('Duration (hours)').inputValue(),'4');
 await page.getByLabel('Number of teams').fill('101');
 await page.getByRole('button',{name:'Create meeting',exact:true}).click();
 assert.equal(creations,0,'out-of-range input submitted');
 // Exercise values above the old API limits through the real hosted handler.
 await page.getByLabel('Number of teams').fill('12');
 await page.getByLabel('Students per team').fill('20');
 await page.getByLabel('Duration (hours)').fill('2');
 await page.getByText('240 student places.',{exact:false}).waitFor();
 const output=process.env.LABCENTRAL_BROWSER_EVIDENCE;
 if(output){fs.mkdirSync(output,{recursive:true});await page.screenshot({animations:'disabled',path:output+'/capacity-desktop.png',fullPage:true})}
 await page.setViewportSize({width:390,height:844});
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 if(output)await page.screenshot({animations:'disabled',path:output+'/capacity-mobile.png',fullPage:true});
 await page.getByRole('button',{name:'Create meeting',exact:true}).click();
 await page.getByRole('button',{name:'Prepare labs',exact:true}).waitFor();
 assert.equal(await page.getByRole('cell',{name:'0 / 20',exact:true}).count(),12);
 let data=await page.evaluate(async()=>await(await fetch('/api/admin')).json());
 assert.equal(data.teams.length,12);
 assert(data.teams.every(team=>team.capacity===20));
 assert.equal(Date.parse(data.meeting.expires)-Date.parse(data.meeting.created),2*3600000);
 assert.deepEqual(data.teams.map(t=>t.name),Array.from({length:12},(_,i)=>`Team ${i+1}`));
 await page.getByLabel('Extend by').selectOption('6');
 await page.getByRole('button',{name:'Add time',exact:true}).click();
 await page.getByText('Eight-hour limit reached',{exact:true}).waitFor();
 data=await page.evaluate(async()=>await(await fetch('/api/admin')).json());
 assert.equal(Date.parse(data.meeting.expires)-Date.parse(data.meeting.created),8*3600000);
 const end=page.getByRole('button',{name:'End meeting',exact:true});
 assert((await end.getAttribute('class')).includes('btn-error'));
 assert.equal(await end.locator('input[type="checkbox"]').count(),0);
 if(output)await page.screenshot({animations:'disabled',path:output+'/meeting-mobile.png',fullPage:true});
 await end.click();await page.getByRole('dialog').waitFor();
 if(output)await page.screenshot({animations:'disabled',path:output+'/end-confirmation-mobile.png'});
 await page.getByRole('button',{name:'Cancel',exact:true}).click();
 assert.equal((await page.evaluate(async()=>await(await fetch('/api/admin')).json())).meeting.status,'waiting');
 await page.setViewportSize({width:1365,height:950});
 if(output)await page.screenshot({animations:'disabled',path:output+'/meeting-desktop.png',fullPage:true});
 // Internal navigation must preserve this document, authentication and warmed admin content.
 await page.evaluate(()=>{window.__navigationMarker='same-document';window.__workspaceFlash=false;new MutationObserver(()=>{if(document.body.innerText.includes('Loading your workspace')||document.body.innerText.includes('Loading revisions'))window.__workspaceFlash=true}).observe(document.body,{childList:true,subtree:true})});
 await page.getByRole('navigation').getByRole('link',{name:'Templates',exact:true}).click();
 await page.getByRole('heading',{name:'Lab templates',exact:true}).waitFor();
 await page.getByText('Ready to use',{exact:true}).waitFor();
 const nav=page.getByRole('navigation',{name:'Main navigation'});
 for(const [label,url,heading] of [['Manage meeting','/admin/','Manage meeting'],['Teams','/teams/','Choose your team'],['Your lab','/lab/','Your lab'],['Manage meeting','/admin/','Manage meeting']]){
  await nav.getByRole('link',{name:label,exact:true}).click();await page.waitForURL(app+url);
  await page.getByRole('heading',{name:heading,exact:true}).first().waitFor();
  assert.equal(await page.evaluate(()=>window.__navigationMarker),'same-document');
 }
 await page.goBack();await page.waitForURL(app+'/lab/');
 await page.goForward();await page.waitForURL(app+'/admin/');
 await end.waitFor();
 assert.equal(documents,1,'internal links reloaded the document');
 assert.equal(await page.evaluate(()=>window.__workspaceFlash),false,'navigation reset loaded views');
 // Hold an authorized admin response across session expiry; it must not repopulate private data.
 let releaseRead,readHeldResolve;
 const readHeld=new Promise(resolve=>readHeldResolve=resolve);
 await page.route(app+'/api/admin',async route=>{const response=await route.fetch();await new Promise(resolve=>{releaseRead=resolve;readHeldResolve()});await route.fulfill({response})},{times:1});
 await readHeld;
 await page.route(app+'/api/me',route=>route.fulfill({status:401,contentType:'application/json',body:'{"error":"Signed out"}'}));
 await page.getByRole('heading',{name:'Sign in to continue',exact:true}).waitFor();
 releaseRead();
 await page.getByRole('link',{name:'Continue with CDC ID',exact:false}).waitFor();
 await page.unroute(app+'/api/me');
 // A new student session must receive neither the old private view nor late results.
 await page.route(app+'/api/me',async route=>{const response=await route.fetch();const overview=await response.json();overview.user.admin=false;overview.csrf='different-session';await route.fulfill({response,json:overview})});
 await page.getByRole('heading',{name:'For meeting administrators',exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:'End meeting',exact:true}).count(),0);
 assert.equal(await page.getByRole('table').count(),0);
 // Login is still an HTTP navigation to the server-owned OIDC endpoint.
 await page.unroute(app+'/api/me');
 await page.route(app+'/api/me',route=>route.fulfill({status:401,contentType:'application/json',body:'{"error":"Signed out"}'}));
 await page.getByRole('link',{name:'Continue with CDC ID',exact:false}).waitFor();
 const idp=process.argv[3];assert(idp,'Local identity redirect fixture required');
 await Promise.all([page.waitForURL(idp+'/authorize',{waitUntil:'domcontentloaded'}),page.getByRole('link',{name:'Continue with CDC ID',exact:false}).click()]);
 assert.equal(await page.title(),'OIDC redirect fixture');
 assert(documents>1);
 console.log('Expanded meeting settings, exact eight-hour extension, numeric team order, destructive confirmation, same-document navigation/history, session expiry with a late response, student privacy, and server-owned login passed at desktop/mobile widths. OIDC provider is a fixture.');
} finally {await browser.close()}
