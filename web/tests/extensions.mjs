import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { createHash } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import { chromium, firefox, webkit } from 'playwright';

const root=fileURLToPath(new URL('../../',import.meta.url));
const sdk=resolve(process.env.LABCENTRAL_EXTENSION_SDK_ROOT ?? root);
const binary=resolve(process.env.LABCENTRAL_EXTENSION_TEST_BINARY ?? join(root,'bin/extension-foundations/labd'));
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const before=digest(await readFile(binary));
const directory=await mkdtemp(join(tmpdir(),'labcentral-extension-test-'));
const port=async()=>{const s=createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const p=s.address().port;await new Promise(r=>s.close(r));return p;};
const mainPort=await port(),assetPort=await port(),operationsPort=await port();
const origin=`http://127.0.0.1:${mainPort}`,assets=`http://127.0.0.2:${assetPort}`;
const run=(cmd,args)=>new Promise((resolve,reject)=>{const c=spawn(cmd,args,{cwd:root,stdio:'pipe'});let output='';c.stdout.on('data',b=>output+=b);c.stderr.on('data',b=>output+=b);c.on('exit',code=>code===0?resolve(output):reject(new Error(output)));c.on('error',reject);});
let child,logs='';
try {
 // This package is produced only after the exact core with embedded UI exists.
 await run(process.execPath,[join(sdk,'examples/external-browser/package.mjs'),join(directory,'package')]);
 const args=['--auth-mode=none','--docker-plugin=','--listen',`127.0.0.1:${mainPort}`,'--observability-listen',`127.0.0.1:${operationsPort}`,'--data-dir',join(directory,'data'),'--installation',join(directory,'package/installation.json'),'--extension-asset-origin',assets,'--extension-asset-listen',`127.0.0.2:${assetPort}`];
 child=spawn(binary,args,{cwd:root,stdio:['ignore','pipe','pipe']});
 child.stdout.on('data',b=>logs+=b);child.stderr.on('data',b=>logs+=b);
 let ready=false;
 for(let n=0;n<100;n++){if(child.exitCode!==null)throw new Error(logs);try{ready=(await fetch(origin+'/api/v1/server-info')).ok;}catch{}if(ready)break;await delay(100);}
 assert(ready,logs);
 for(const [name,engine] of Object.entries({chromium,firefox,webkit})){
  const browser=await engine.launch();
  try{
   const page=await browser.newPage();
   await page.goto(origin+'/plugins');
   const frame=page.frameLocator('iframe[title="Independent extension fixture"]');
   await frame.locator('#status').filter({hasText:'Connected: fixture.browser'}).waitFor({timeout:15000});
   assert.deepEqual(JSON.parse(await frame.locator('#isolation').innerText()),{cookieDenied:true,storageDenied:true,parentDenied:true});
   await frame.getByRole('button',{name:'Probe denied method'}).click();
   await frame.locator('#denial').filter({hasText:'Denied'}).waitFor();
   const contribution=page.frames().find(f=>f.url().startsWith(assets));
   assert(contribution);
   assert.equal(await contribution.evaluate(async()=>{try{await fetch('/not-an-api');return false;}catch{return true;}}),true,'CSP must block direct network fetch');
   const result=await contribution.evaluate(async()=>{window.parent.postMessage({version:1,id:'forged',method:'context.read'},'*');return document.querySelector('#status').textContent;});
   assert.equal(result,'Connected: fixture.browser');
   // A new document in the same frame must not inherit an authorized channel.
   await contribution.evaluate(()=>location.reload());
   await page.getByRole('alert').filter({hasText:'Extension navigation ended its connection.'}).waitFor();
   console.log(`PASS: ${name} independently installed browser package, opaque origin, denied bridge/network and navigation invalidation`);
  }finally{await browser.close();}
 }
 assert.equal(digest(await readFile(binary)),before,'core/UI artifact changed during installation proof');
}finally{
 if(child && child.exitCode===null){child.kill('SIGTERM');await Promise.race([new Promise(r=>child.on('exit',r)),delay(10000)]);if(child.exitCode===null)child.kill('SIGKILL');}
 await writeFile(join(root,'bin/extension-foundations/browser-proof.log'),logs);
 await rm(directory,{recursive:true,force:true});
}
