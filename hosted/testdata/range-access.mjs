import assert from 'node:assert/strict';
import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const [app,desktop]=process.argv.slice(2);
const evidence=process.env.LABCENTRAL_BROWSER_EVIDENCE;
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox'],ignoreDefaultArgs:['--disable-popup-blocking']});
try {
 for(const width of [1366,390]) {
  const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width,height:950}});
  const errors=[];
  context.on('page',p=>p.on('pageerror',e=>errors.push(e.message)));
  await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Strict'}]);
  await context.request.get(app+'/_fixture/terminal-policy?enabled=true');
  await context.route('**/favicon.ico',r=>r.fulfill({status:204}));
  let shells=0;
  await context.routeWebSocket('**/api/machines/*/terminal',ws=>{
   ws.onMessage(data=>{
    if(typeof data!=='string')return;
    const hello=JSON.parse(data);
    if(!hello.csrf)return;
    assert.equal(hello.template,'pinned');
    const name=new URL(ws.url()).pathname.split('/')[3];
    shells++;
    ws.send(JSON.stringify({type:'ready',expires:new Date(Date.now()+600000).toISOString()}));
    ws.send(Buffer.from('Fixture shell on '+name+'\r\n'));
   });
  });
  const page=await context.newPage(); await page.goto(app+'/lab/');
  await page.getByRole('heading',{name:'Team machines',exact:true}).waitFor();
  const inventory=await page.evaluate(async()=>{const r=await fetch('/api/machines');if(!r.ok)throw Error('Inventory denied');return r.json()});
  assert.equal(inventory.machines.length,10);
  await page.getByText('VyOS router',{exact:true}).waitFor();
  await page.getByText('Palo Alto firewall',{exact:true}).waitFor();
  await page.getByText('https://172.30.0.200/',{exact:true}).waitFor();
  assert.equal(await page.getByRole('button',{name:'Open panos desktop',exact:true}).count(),0);
  const desktops=[];
  for(const machine of inventory.machines.filter(m=>m.desktop)){
   const popup=context.waitForEvent('page');
   await page.getByRole('button',{name:`Open ${machine.name} desktop`,exact:true}).click();
   const tab=await popup;
   await tab.waitForURL(url=>url.origin===desktop&&/^\/sessions\/[a-f0-9]{24}\/$/.test(url.pathname));
   await tab.waitForFunction(()=>typeof window.fixtureFirstFrame==='function');
   assert.equal(await tab.evaluate(()=>window.opener),null);
   const credentials=await tab.evaluate(async()=>{const r=await fetch('credentials');return r.json()});
   assert.equal(credentials.username,machine.name);
   const accessID=new URL(tab.url()).pathname.split('/')[2];
   assert.equal(await tab.evaluate(()=>window.fixtureDcvRoute.labcentralDesktop),accessID);
   assert.equal(await tab.evaluate(()=>window.fixtureDcvChannelRoute.labcentralDesktop),accessID);
   const routed=await tab.evaluate(async id=>{const r=await fetch('/credentials?labcentralDesktop='+id);return r.json()},accessID);
   assert.equal(routed.username,machine.name);
   desktops.push({name:machine.name,tab});
  }
  assert.equal(new Set(desktops.map(d=>d.tab.url())).size,8);
  assert.equal((await context.cookies(desktop)).filter(c=>c.name.startsWith('__Host-labcentral_desktop_')).length,8);
  for(const d of desktops)assert.equal((await d.tab.evaluate(async()=>{const r=await fetch('credentials');return r.json()})).username,d.name);
  const first=desktops[0];
  await page.getByRole('button',{name:`Disconnect ${first.name} desktop`,exact:true}).click();
  for(const d of desktops.slice(1))assert.equal((await d.tab.evaluate(async()=>{const r=await fetch('credentials');return r.json()})).username,d.name);
  const terminals=[];
  for(const machine of inventory.machines){
   const popup=context.waitForEvent('page');
   await page.getByRole('link',{name:`Open ${machine.name} terminal`,exact:true}).click();
   const tab=await popup; await tab.waitForURL(url=>url.pathname==='/terminal/');
   await tab.getByRole('button',{name:'Disconnect',exact:true}).waitFor();
   await tab.waitForFunction(()=>document.querySelector('.xterm-screen')?.textContent?.includes('Fixture shell on'));
   assert.equal(new URL(tab.url()).searchParams.get('machine'),machine.name);
   terminals.push(tab);
  }
  assert.equal(shells,10);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,'Lab overflows horizontally');
  if(evidence){fs.mkdirSync(evidence,{recursive:true});await page.screenshot({path:`${evidence}/range-${width}.png`,fullPage:true});await terminals[0].screenshot({path:`${evidence}/range-terminal-${width}.png`});}
  assert.deepEqual(errors,[]);
  await context.request.get(app+'/_fixture/terminal-policy?enabled=false');
  await page.reload();
  await page.getByRole('heading',{name:'Team machines',exact:true}).waitFor();
  assert.equal(await page.getByRole('link',{name:/^Open .* terminal$/}).count(),0,'Legacy templates exposed participant terminals');
  assert.equal(await page.getByRole('button',{name:/^Open .* desktop$/}).count(),8,'Terminal opt-in changed desktops');
  await context.close();
 }
 console.log('Eight independent desktop launches through real hosted HTTP, per-machine credentials/cookies, sibling-preserving disconnect, ten student terminal tabs including VyOS/PAN-OS, private firewall management hints and desktop/mobile layout passed. DCV and SSH transports are fixtures; no live platform or OIDC acceptance claim.');
} finally {await browser.close()}
