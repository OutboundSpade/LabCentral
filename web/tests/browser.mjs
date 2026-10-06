const {chromium,firefox} = await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || 'playwright');
import fs from 'node:fs';import assert from 'node:assert/strict';
const evidence=process.env.LABCENTRAL_BROWSER_EVIDENCE;
if(!evidence)throw new Error('Set LABCENTRAL_BROWSER_EVIDENCE outside source');fs.mkdirSync(evidence,{recursive:true});
const browser=await (process.env.LABCENTRAL_FIREFOX?firefox.launch({headless:true,executablePath:process.env.LABCENTRAL_FIREFOX}):chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox'],ignoreDefaultArgs:['--disable-popup-blocking']}));
try{
 for(const width of [1365,390]){
  const context=await browser.newContext({viewport:{width,height:900}});const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
  let authenticated=false;let currentTeam='t1';let switched=false;
  const teams=Array.from({length:5},(_,i)=>({id:'t'+(i+1),meetingID:'m',name:'Team '+(i+1),capacity:4,members:i===4?4:2,status:'ready'}));
  const meeting={id:'m',name:'Example Branch practice',template:'template',status:'ready',created:new Date().toISOString(),expires:new Date(Date.now()+3600000).toISOString()};
  const template={latest:true,removing:false,imagesReady:4,imagesTotal:4,buildsPending:0,commitTitle:'Modernize Example Branch as portable Ubuntu lab and Ansible playbooks',committedAt:'2026-09-28T12:00:00Z',removalBlocked:'Used by the current meeting. End it before removing images.',id:'template',name:'branch-services',commit:'ce17e48902cd7bb844c19c80e76b0168b935be00',built:true,handover:'# Example Branch\n\nKeep customer banking, ledger processing, and document publishing available during your shift.\n\nVisit **http://web** from your desktop.\n\n## The branch\n\nYour team shares four machines. Use the handover in Documents to find the exercise accounts.\n\n<script>alert("unsafe")</script>'};
  await context.route('**/desktop-test-launch',async route=>{assert.equal(route.request().method(),'POST');assert.equal(route.request().postData(),'ticket=test-ticket');await route.fulfill({contentType:'text/html',body:'<h1>Desktop launch accepted</h1>'})});
  const overview=()=>({user:{id:'u',name:'CDC Test Student',email:'test@example.invalid',admin:true},csrf:'test-csrf',meeting,membership:{userID:'u',teamID:currentTeam,pendingTeam:'',switching:false},teams,lab:{teammates:[{name:"CDC Test Student"},{name:"Jordan"}],team:teams.find(t=>t.id===currentTeam),machines:['web','database','ftp','workstation'].map(name=>({name,status:'ready',desktop:name==='workstation'})),handover:template.handover}});
  await page.route('**/api/**',async route=>{const req=route.request(),path=new URL(req.url()).pathname;
   if(!authenticated){await route.fulfill({status:401,json:{error:'Sign in'}});return}
   if(path==='/api/me'){await route.fulfill({json:overview()});return}
   if(path==='/api/admin'){await route.fulfill({json:{meeting,teams,templates:[template],jobs:[{id:'j',kind:'prepare',target:'m',status:'complete',created:meeting.created}]}});return}
   assert.equal(req.headers()['x-csrf-token'],'test-csrf');
   if(path==='/api/access'){await new Promise(resolve=>setTimeout(resolve,6000));await route.fulfill({json:{url:'http://127.0.0.1:5173/desktop-test-launch',ticket:'test-ticket'}});return}
   if(path==='/api/team'){currentTeam=JSON.parse(req.postData()).teamID;switched=true}
   await route.fulfill({json:{ok:true}});
  });
  await page.goto('http://127.0.0.1:5173/');await page.getByRole('heading',{name:'Your lab awaits.'}).waitFor();await page.screenshot({path:`${evidence}/home-${width}.png`,fullPage:true});authenticated=true;
  for(const [route,heading] of [['teams/','Choose your team'],['lab/','Your lab'],['admin/','Manage meeting'],['admin/templates/','Lab templates']]){
   if(route==='teams/')await page.goto('http://127.0.0.1:5173/'+route);
   else {await page.getByRole('navigation').getByRole('link',{name:route==='lab/'?'Your lab':route==='admin/'?'Manage meeting':'Templates',exact:true}).click();await page.waitForURL('**/'+route);await page.waitForFunction(()=>document.querySelector('#svelte-announcer')?.textContent===document.title);assert(await page.locator('#svelte-announcer').evaluate(e=>{const s=getComputedStyle(e),r=e.getBoundingClientRect();return e.getAttribute('aria-live')==='assertive'&&s.position==='absolute'&&s.clipPath==='inset(50%)'&&r.width<=1&&r.height<=1}),'navigation title is visually exposed on '+route)}
   await page.getByRole('heading',{name:heading,exact:true}).waitFor();await page.getByRole('button',{name:route==='teams/'?'Team full':route==='lab/'?'Open desktop':route==='admin/'?'End meeting':'Refresh from GitHub',exact:true}).waitFor();
   assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'horizontal overflow on '+route);
   await page.screenshot({path:`${evidence}/${route.replaceAll('/','-')}${width}.png`,fullPage:true});
   if(route==='teams/'){await page.getByRole('button',{name:'Team full'}).waitFor();assert(await page.getByRole('button',{name:'Team full'}).isDisabled())}
   if(route==='lab/'){assert.equal(await page.locator('article script').count(),0);await page.getByRole('button',{name:'Open desktop'}).waitFor();const next=page.waitForEvent('popup');await page.getByRole('button',{name:'Open desktop'}).click();const desktop=await next;await desktop.getByRole('heading',{name:'Desktop launch accepted'}).waitFor();assert.equal(await desktop.evaluate(()=>window.opener),null);await desktop.close()}
   if(route==='admin/'){await page.getByRole('button',{name:'End meeting',exact:true}).click();await page.getByRole('dialog').waitFor();await page.getByRole('button',{name:'Cancel',exact:true}).click();await page.getByRole('dialog').waitFor({state:'hidden'})}
  }
  await page.goto('http://127.0.0.1:5173/teams/');await page.getByRole('button',{name:'Switch team'}).first().click();await page.waitForURL('**/lab/');assert(switched);assert.equal(currentTeam,'t2');assert.deepEqual(errors,[]);await context.close();console.log('PASS routes, responsive layout, full team, safe handover, confirmation, team switch:',width);
 }
}finally{await browser.close()}
