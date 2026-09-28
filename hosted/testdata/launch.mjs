const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
import assert from 'node:assert/strict';
const [app,desktop]=process.argv.slice(2);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox'],ignoreDefaultArgs:['--disable-popup-blocking']});
try {
 const context=await browser.newContext({ignoreHTTPSErrors:true}); // Local TLS fixtures only.
 await context.route('**/favicon.ico',route=>route.fulfill({status:204}));
 const page=await context.newPage();
 const team={id:'team',name:'Team 1',capacity:4,members:1,status:'ready'};
 await page.route('**/api/me',route=>route.fulfill({json:{user:{id:'student',name:'Student',admin:false},csrf:'csrf',meeting:{name:'Practice',status:'ready',expires:new Date(Date.now()+3600000).toISOString()},membership:{teamID:'team'},teams:[team],lab:{team,teammates:[],machines:[],handover:'# Handover'}}}));
 await page.route('**/api/access',route=>route.fulfill({json:{url:desktop+'/launch',ticket:'browser-ticket'}}));
 await page.goto(app+'/lab/');
 const popup=context.waitForEvent('page');
 const request=context.waitForEvent('request',r=>r.url()===desktop+'/launch');
 await page.getByRole('button',{name:'Open desktop',exact:true}).click();
 const launch=await request;
 assert.equal(launch.method(),'POST');
 assert.equal((await launch.allHeaders()).origin,app);
 assert([undefined,app+'/'].includes((await launch.allHeaders()).referer));
 const tab=await popup;
 await tab.waitForURL(desktop+'/');
 assert.equal(await tab.evaluate(()=>window.opener),null);
 assert((await context.cookies(desktop)).some(c=>c.name==='__Host-labcentral_desktop'&&c.httpOnly&&c.secure));
 await tab.waitForFunction(()=>typeof window.fixtureFirstFrame==='function');
 assert((await tab.locator('#status').innerText()).startsWith('Connecting'));
 await tab.evaluate(()=>window.fixtureFirstFrame());
 assert((await tab.locator('#status').innerText()).startsWith('Connected'));
 assert(await tab.getByRole('button',{name:'Copy from desktop'}).isDisabled());
 assert(!await tab.evaluate(()=>window.fixtureClipboardCapture));
 await tab.evaluate(()=>window.fixtureClipboardEstablished());
 assert(await tab.evaluate(()=>window.fixtureClipboardCapture&&window.fixtureClipboardAuto));
 await tab.getByRole('button',{name:'Copy from desktop'}).click();
 assert(await tab.evaluate(()=>window.fixtureClipboardCopy));
 for(const width of [1365,390]){
  await tab.setViewportSize({width,height:900});
  await tab.waitForFunction(width=>window.fixtureResolution?.[0]===width,width);
  await tab.waitForFunction(()=>{const d=document.querySelector('#dcv-display').getBoundingClientRect(),v=document.querySelector('#desktop-viewport').getBoundingClientRect();return d.width<=v.width+.1&&d.height<=v.height+.1});
  assert(await tab.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  if(process.env.LABCENTRAL_BROWSER_EVIDENCE) await tab.screenshot({path:process.env.LABCENTRAL_BROWSER_EVIDENCE+`/desktop-fixture-${width}.png`});
 }
 await tab.evaluate(()=>window.fixtureDisconnect());
 assert(await tab.getByRole('button',{name:'Copy from desktop'}).isDisabled());
 const replay=await context.request.post(desktop+'/launch',{headers:{Origin:app},form:{ticket:'browser-ticket'},maxRedirects:0});
 assert.equal(replay.status(),403);
 console.log('PASS real browser cross-origin launch, origin-only referrer, cookie exchange and replay denial');
} finally {await browser.close()}
