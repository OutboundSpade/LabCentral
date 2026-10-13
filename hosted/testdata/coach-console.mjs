import assert from 'node:assert/strict';
import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox'],ignoreDefaultArgs:['--disable-popup-blocking']});
try {
 const app=process.argv[2];
 const evidence=process.env.LABCENTRAL_BROWSER_EVIDENCE;
 if(evidence)fs.mkdirSync(evidence,{recursive:true});
 const context=await browser.newContext({ignoreHTTPSErrors:true,reducedMotion:'reduce',viewport:{width:1365,height:1000}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();
 await page.goto(app+'/admin/');
 await page.getByRole('heading',{name:'Coach machine access'}).waitFor();
 await page.getByLabel('Team',{exact:true}).selectOption({label:'Team 2'});
 const open=page.getByRole('link',{name:'Open SSH',exact:true});
 await open.waitFor();
 await page.getByLabel('Machine',{exact:true}).selectOption('support');
 const team=await page.getByLabel('Team',{exact:true}).inputValue();
 const href=new URL(await open.getAttribute('href'),app);
 assert.equal(href.pathname,'/admin/terminal/');
 assert.equal(href.searchParams.get('team'),team);assert.equal(href.searchParams.get('machine'),'support');
 const inventory=await (await context.request.get(app+'/api/admin/teams/'+team+'/machines')).json();
 assert.equal(href.searchParams.get('meeting'),inventory.meetingID);assert.equal(href.searchParams.get('template'),inventory.template);
 assert(inventory.instanceID&&href.searchParams.get('instance')===inventory.instanceID,'console URL pins the library allocation');
 assert(!/csrf|token|cookie/i.test(href.search),'no secrets in console URL');
 assert.equal(await open.getAttribute('target'),'_blank');assert.match(await open.getAttribute('rel'),/noopener/);
 // The console opens in its own tab; the fixture engine fails SSH, so the server closes with 1011.
 const next=page.waitForEvent('popup');await open.click();const consoleTab=await next;
 assert.equal(await consoleTab.evaluate(()=>window.opener),null);
 const sockets=[];consoleTab.on('websocket',ws=>sockets.push(ws));
 await consoleTab.getByText('Guest console unavailable. Reconnect starts a fresh shell.',{exact:false}).waitFor();
 await consoleTab.locator('[data-terminal-status="retrying"]').waitFor();
 // Manual disconnect cancels the pending bounded retry.
 await consoleTab.getByRole('button',{name:'Disconnect',exact:true}).click();
 await consoleTab.locator('[data-terminal-status="disconnected"]').waitFor();
 const stopped=sockets.length;await consoleTab.waitForTimeout(3500);
 assert.equal(sockets.length,stopped,'no retry after Disconnect');
 if(evidence)await consoleTab.screenshot({path:evidence+'/coach-console-desktop.png'});
 // An explicit Reconnect authorizes and opens a fresh socket, which fails the same way.
 await consoleTab.getByRole('button',{name:'Reconnect',exact:true}).click();
 await consoleTab.locator('[data-terminal-status="retrying"]').waitFor();
 assert(sockets.length>stopped,'Reconnect opens a fresh socket');
 await consoleTab.getByRole('button',{name:'Disconnect',exact:true}).click();
 await consoleTab.locator('[data-terminal-status="disconnected"]').waitFor();
 // Rendering check under the Go-served prerendered HTML and its strict CSP. From
 // here only the terminal WebSocket is a Playwright mock (no guest, no SSH);
 // /api/me and the machine inventory remain the real authenticated HTTP API.
 const mock={hello:null,resize:[],input:[]};
 await context.routeWebSocket(/\/terminal$/,ws=>{ws.onMessage(m=>{
  if(typeof m!=='string'){mock.input.push(Buffer.from(m));return}
  const frame=JSON.parse(m);if(mock.hello){mock.resize.push(frame);return}
  mock.hello=frame;ws.send(JSON.stringify({type:'ready',expires:new Date(Date.now()+600000).toISOString()}));
  ws.send(Buffer.from('\x1b[1;32mcoach@support\x1b[0m:~$ ls\r\n\x1b[34mbin\x1b[0m  etc  var\r\n\x1b[6;20HXY\x1b[8;1Hcoach@support:~$ '));
 })});
 await consoleTab.getByRole('button',{name:'Reconnect',exact:true}).click();
 await consoleTab.locator('[data-terminal-status="connected"]').waitFor();
 assert.equal(mock.hello.instanceID,inventory.instanceID);assert.equal(mock.hello.meetingID,inventory.meetingID);assert.equal(mock.hello.template,inventory.template);
 assert(mock.hello.cols>=2&&mock.hello.cols<=500&&mock.hello.rows>=2&&mock.hello.rows<=200,'hello dimensions bounded');
 await consoleTab.waitForFunction(()=>[...document.querySelectorAll('.xterm-rows > div')].some(r=>r.textContent.includes('coach@support:~$ ')&&!r.textContent.includes('ls')));
 const rendered=await consoleTab.evaluate(()=>{
  const rows=[...document.querySelectorAll('.xterm-rows > div')].map(r=>r.textContent.replace(/\u00a0/g,' '));
  const spans=[...document.querySelectorAll('.xterm-rows span')];const bold=spans.find(e=>e.textContent.includes('coach@support'));const plain=spans.find(e=>e.textContent.includes('etc'));
  return {rows,mirrored:document.adoptedStyleSheets.length,weight:getComputedStyle(bold).fontWeight,color:getComputedStyle(bold).color,plain:getComputedStyle(plain).color,font:getComputedStyle(document.querySelector('.xterm-rows')).fontFamily};
 });
 assert.equal(rendered.rows[5].indexOf('XY'),19,'cursor addressing');
 assert(rendered.mirrored>=1&&Number(rendered.weight)>=600&&rendered.color!==rendered.plain,'xterm vendor styles apply under the static CSP: '+JSON.stringify(rendered));
 assert.match(rendered.font,/monospace/);
 await consoleTab.evaluate(()=>{window.__copied=[];Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async t=>void window.__copied.push(t)}})});
 const box=await consoleTab.locator('.xterm-screen').boundingBox();
 await consoleTab.mouse.move(box.x+4,box.y+4);await consoleTab.mouse.down();await consoleTab.mouse.move(box.x+150,box.y+6,{steps:8});await consoleTab.mouse.up();
 assert(await consoleTab.evaluate(()=>document.querySelectorAll('.xterm-selection div').length>0),'selection is drawn');
 await consoleTab.getByRole('button',{name:'Copy selection'}).click();
 await consoleTab.getByText(/^Copied \d+ characters\.$/).waitFor();
 assert((await consoleTab.evaluate(()=>window.__copied[0])).startsWith('coach@support'));
 await consoleTab.locator('.xterm-helper-textarea').focus();await consoleTab.keyboard.type('pwd');await consoleTab.keyboard.press('Enter');
 for(let i=0;i<50&&Buffer.concat(mock.input).toString()!=='pwd\r';i++)await consoleTab.waitForTimeout(50);
 assert.equal(Buffer.concat(mock.input).toString(),'pwd\r');
 if(evidence)await consoleTab.screenshot({path:evidence+'/coach-terminal-mock-desktop.png'});
 const resized=mock.resize.length;await consoleTab.setViewportSize({width:390,height:844});
 for(let i=0;i<60&&mock.resize.length===resized;i++)await consoleTab.waitForTimeout(50);
 const last=mock.resize.at(-1);
 assert(mock.resize.length>resized&&last.type==='resize'&&last.cols<mock.hello.cols&&last.cols>=2&&last.rows>=2,'live resize after the phone viewport: '+JSON.stringify({hello:mock.hello.cols,resize:mock.resize}));
 assert(await consoleTab.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'console mobile horizontal overflow');
 if(evidence)await consoleTab.screenshot({path:evidence+'/coach-terminal-mock-mobile.png'});
 await consoleTab.getByRole('button',{name:'Disconnect',exact:true}).click();
 await consoleTab.locator('[data-terminal-status="disconnected"]').waitFor();
 if(evidence)await consoleTab.screenshot({path:evidence+'/coach-console-mobile.png'});
 await consoleTab.close();
 await page.setViewportSize({width:390,height:844});
 await page.getByRole('heading',{name:'Coach machine access'}).scrollIntoViewIfNeeded();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile horizontal overflow');
 await page.getByLabel('Machine',{exact:true}).selectOption('workstation');
 assert.equal(new URL(await open.getAttribute('href'),app).searchParams.get('machine'),'workstation');
 if(evidence)await page.screenshot({path:evidence+'/coach-picker-mobile.png',fullPage:true});
 const machines=await context.request.get(app+'/api/admin/teams/'+team+'/machines');
 assert.equal(machines.status(),200);
 assert((await machines.json()).machines.some(m=>m.name==='support'));
 // Admins can preview the complete revision, while a separate student session is denied.
 const catalog=await context.request.get(app+'/api/templates');
 const teacherTemplates=(await catalog.json()).templates;assert.equal(teacherTemplates.length,1);assert(teacherTemplates[0].coachOnly && teacherTemplates[0].machines.some(m=>m.name==='support'));
 await page.goto(app+'/labs/?template=pinned');await page.getByText('Coach-only',{exact:true}).waitFor();assert.equal(await page.getByText('support',{exact:true}).count(),1);assert.equal(await page.getByRole('link',{name:'Publish practice',exact:true}).count(),0);assert(await page.getByRole('button',{name:/Start practice:/}).isDisabled(),'coach-only revisions cannot start student practice');await page.getByText('Coach-only revisions are previews for instructors.',{exact:true}).first().waitFor();
 if(evidence){await page.screenshot({path:evidence+'/coach-preview-mobile.png',fullPage:true});await page.setViewportSize({width:1365,height:1000});await page.screenshot({path:evidence+'/coach-preview-desktop.png',fullPage:true})}
 const studentContext=await browser.newContext({ignoreHTTPSErrors:true});await studentContext.addCookies([{name:'__Host-labcentral',value:'blue-cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const studentCatalog=await studentContext.request.get(app+'/api/templates');assert.equal(studentCatalog.status(),200);assert.equal((await studentCatalog.json()).templates.length,0);
 const studentMachines=await studentContext.request.get(app+'/api/admin/teams/'+team+'/machines');assert([401,403].includes(studentMachines.status()),'students cannot list coach machines');
 const studentPage=await studentContext.newPage();await studentPage.goto(app+'/labs/?template=pinned');await studentPage.getByText('This template revision is unavailable.',{exact:true}).waitFor();assert.equal(await studentPage.getByText('support',{exact:true}).count(),0);
 const studentSockets=[];studentPage.on('websocket',ws=>studentSockets.push(ws));
 await studentPage.goto(app+href.pathname+href.search);await studentPage.getByRole('heading',{name:'For meeting administrators'}).waitFor();await studentPage.waitForTimeout(500);assert.equal(studentSockets.length,0,'students never open a console socket');
 await studentContext.close();
 console.log('Coach console new-tab launch, authenticated WebSocket failure with manual disconnect/reconnect, xterm rendering under the static CSP via one mocked terminal WebSocket, full admin inventory, student denial and desktop/mobile layout passed. SSH failure and the rendered shell output are fixtures; no live SSH or platform acceptance claim.');
} finally {await browser.close()}
