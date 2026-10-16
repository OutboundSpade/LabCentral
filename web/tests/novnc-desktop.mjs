// Browser layout/interaction evidence with a mocked noVNC transport and lesson API.
// No platform or identity acceptance is claimed by this test.
import fs from 'node:fs';
import assert from 'node:assert/strict';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE||'playwright');
const source=fs.readFileSync('hosted/desktop.go','utf8');
const constant=(body,name)=>body.match(new RegExp('const '+name+' = `([^`]*)`'))[1];
const html=constant(source,'desktopHTML').replaceAll('{{origin}}','http://app.test').replaceAll('{{practiceRun}}','run-1').replace('<link rel="stylesheet" href="/desktop.css">','<link rel="stylesheet" href="/lesson-ui.css"><link rel="stylesheet" href="/desktop.css">');
const css=constant(source,'desktopCSS');
const client=constant(fs.readFileSync('hosted/novnc.go','utf8'),'novncJS');
const build='web/build';
const appCSS=fs.readdirSync(build+'/_app/immutable/assets').filter(f=>f.endsWith('.css')).map(f=>fs.readFileSync(build+'/_app/immutable/assets/'+f,'utf8')).join('\n');
const fake=`export default class RFB extends EventTarget {
 constructor(display,url){super();window.__rfb=this;this.display=display;const canvas=document.createElement('canvas');canvas.width=1280;canvas.height=800;canvas.style='max-width:100%;max-height:100%';display.append(canvas);setTimeout(()=>this.dispatchEvent(new Event('connect')),0)}
 clipboardPasteFrom(text){window.__pasted=text}
}`;
const evidence=process.env.LABCENTRAL_BROWSER_EVIDENCE;
assert(evidence,'Set LABCENTRAL_BROWSER_EVIDENCE outside source');fs.mkdirSync(evidence,{recursive:true});
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try{
 for(const width of [1366,390]){
  const context=await browser.newContext({viewport:{width,height:900}});const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
  let displaced=false;
  await context.route('http://desktop.test/**',async route=>{
   const path=new URL(route.request().url()).pathname;
   const send=(body,contentType)=>route.fulfill({body,contentType});
   if(path==='/')return send(html,'text/html');if(path==='/desktop.css')return send(css,'text/css');if(path==='/client.js')return send(client,'text/javascript');if(path==='/vendor/core/rfb.js')return send(fake,'text/javascript');
   if(path==='/credentials')return route.fulfill({json:{username:'lcstudent',password:'guest-password'}});
   if(path==='/status')return route.fulfill({json:displaced?{code:'desktop_taken_over',takenOverBy:'Coach'}:{code:'owned'}});
   if(path==='/lesson-ui.css')return send(appCSS,'text/css');
   if(path==='/lesson-client.js')return send(fs.readFileSync(build+'/lesson.js','utf8'),'text/javascript');
   if(path.startsWith('/_app/'))return route.fulfill({path:build+path});
   if(path==='/lesson')return route.fulfill({json:{lab:{id:'lesson',title:'Service recovery',mode:'self-paced',stepCount:1,completedSteps:0,template:'revision',description:'Repair the service',estimatedMinutes:30,publishedAt:new Date().toISOString()},steps:[{id:'port',title:'Find the port',kind:'answer',instructions:'Inspect the listener',prompt:'Which port?'}],releasedSteps:1,progress:{labID:'lesson',completedSteps:0,updated:new Date().toISOString()},run:{id:'run-1',status:'ready',labID:'lesson'},quota:{remainingSeconds:3600,limitMinutes:180,usedSeconds:0},cloudReady:true,localAvailable:true}});
   return route.fulfill({status:404,body:'not found'});
  });
  await page.goto('http://desktop.test/');await page.getByRole('button',{name:'Console login',exact:true}).waitFor();
  await page.getByRole('button',{name:'Console login',exact:true}).click();await page.getByRole('heading',{name:'Guest console login'}).waitFor();assert(await page.getByText('Password: guest-password').isVisible());await page.getByRole('button',{name:'Close',exact:true}).click();
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  assert.equal(await page.evaluate(()=>document.fullscreenElement?.id),'desktop-viewport');
  await page.evaluate(()=>document.exitFullscreen());
  await page.evaluate(()=>{Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.__copied=text}}});window.__rfb.dispatchEvent(new CustomEvent('clipboard',{detail:{text:'guest clipboard text'}}))});
  await page.getByRole('button',{name:'Copy from desktop',exact:true}).click();
  assert.equal(await page.evaluate(()=>window.__copied),'guest clipboard text');
  await page.locator('#lesson-panel').waitFor();
  const answer=page.getByRole('textbox',{name:/answer/i});await answer.fill('8080');assert.equal(await answer.inputValue(),'8080');
  await page.getByRole('button',{name:'Paste to desktop',exact:true}).click();
  await page.locator('#paste-text').dispatchEvent('paste',{clipboardData:undefined}).catch(()=>{});
  await page.evaluate(()=>{const transfer=new DataTransfer();transfer.setData('text/plain','test clipboard');document.querySelector('#paste-text').dispatchEvent(new ClipboardEvent('paste',{clipboardData:transfer,bubbles:true}))});
  assert.equal(await page.evaluate(()=>window.__pasted),'test clipboard');
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
  await page.waitForTimeout(500);
  await page.screenshot({path:evidence+'/novnc-'+width+'.png',fullPage:true});
  displaced=true;await page.evaluate(()=>window.__rfb.dispatchEvent(new Event('disconnect')));await page.getByRole('status').filter({hasText:'Coach took over'}).waitFor();
  assert.deepEqual(errors,[]);await context.close();console.log('PASS mocked noVNC desktop and lesson',width);
 }
}finally{await browser.close()}
