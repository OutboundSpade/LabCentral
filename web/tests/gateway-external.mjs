import assert from 'node:assert/strict';
import {mkdtemp,readFile,writeFile,rm,chmod} from 'node:fs/promises';
import {tmpdir} from 'node:os';import {join,resolve} from 'node:path';import {fileURLToPath,pathToFileURL} from 'node:url';
import {spawn,execFileSync} from 'node:child_process';import {createServer} from 'node:net';import {randomBytes,createHash} from 'node:crypto';import {setTimeout as delay} from 'node:timers/promises';
import {chromium,firefox,webkit} from 'playwright';
const root=fileURLToPath(new URL('../../',import.meta.url)),binary=resolve(process.env.LABCENTRAL_EXTENSION_TEST_BINARY??join(root,'bin/extension-foundations/labd'));
const {GatewayClient}=await import(pathToFileURL(join(process.env.LABCENTRAL_EXTENSION_SDK_ROOT??root,'examples/external-gateway/client.mjs')).href);
const digest=bytes=>createHash('sha256').update(bytes).digest('hex');const before=digest(await readFile(binary));const directory=await mkdtemp(join(tmpdir(),'labcentral-gateway-proof-'));await chmod(directory,0o700);
const port=async()=>{const s=createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const p=s.address().port;await new Promise(r=>s.close(r));return p};
const p=await port(),operations=await port(),origin=`https://127.0.0.1:${p}`;let child,logs='';
try{
 const cert=join(directory,'certificate.pem'),key=join(directory,'key.pem'),keyring=join(directory,'session.json'),passwordFile=join(directory,'password');const password=randomBytes(24).toString('base64url');
 execFileSync('openssl',['req','-x509','-newkey','rsa:2048','-nodes','-keyout',key,'-out',cert,'-days','1','-subj','/CN=127.0.0.1','-addext','subjectAltName=IP:127.0.0.1'],{stdio:'ignore'});await chmod(cert,0o600);await chmod(key,0o600);
 await writeFile(keyring,JSON.stringify({schemaVersion:'labcentral.session-keys/v1',currentKeyId:'fixture',keys:[{id:'fixture',material:randomBytes(32).toString('base64url')}]}),{mode:0o600});await writeFile(passwordFile,password,{mode:0o600});
 child=spawn(binary,['--auth-mode=local','--docker-plugin=','--listen',`127.0.0.1:${p}`,'--observability-listen',`127.0.0.1:${operations}`,'--public-origin',origin,'--tls-certificate-file',cert,'--tls-key-file',key,'--session-keyring-file',keyring,'--bootstrap-password-file',passwordFile,'--data-dir',join(directory,'data')],{cwd:root,stdio:['ignore','pipe','pipe']});child.stdout.on('data',b=>logs+=b);child.stderr.on('data',b=>logs+=b);
 const ca=await readFile(cert);await delay(1000);if(child.exitCode!==null)throw new Error(logs);
 for(const [name,engine] of Object.entries({chromium,firefox,webkit})){
  const browser=await engine.launch();try{
   const page=await browser.newPage({ignoreHTTPSErrors:true});await page.goto(origin+'/device');await page.getByLabel('Username',{exact:true}).fill('bootstrap-admin');await page.getByLabel('Password',{exact:true}).fill(password);await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.getByLabel('Device code',{exact:true}).waitFor();
   const product=async(path,body)=>page.evaluate(async({path,body})=>{const csrf=document.cookie.split(';').map(s=>s.trim()).find(s=>s.startsWith('__Host-labcentral-csrf='))?.split('=')[1];const res=await fetch('/api/v1'+path,{method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':decodeURIComponent(csrf??''),'Idempotency-Key':crypto.randomUUID()},body:body===undefined?undefined:JSON.stringify(body)});const text=await res.text();if(!res.ok)throw new Error(`${res.status} ${text}`);return text?JSON.parse(text):null},{path,body});
   if(name==='chromium'){await product('/organizations',{id:'organization.gateway',name:'Gateway proof'});await product('/projects',{id:'project.gateway',organizationId:'organization.gateway',name:'Gateway proof'});}
   const template=await product('/lab-templates',{projectId:'project.gateway',name:'gateway-'+name,description:'Independent client proof'});const registered=await product('/admin/gateway-clients',{name:'External '+name});
   const client=new GatewayClient(origin,registered.client.id,registered.clientSecret,{ca});const requested=await client.begin({projectId:'project.gateway',actions:['templates.read'],templateIds:[template.id],instanceIds:[],targetIds:[]});assert.equal(requested.status,200);assert.equal(requested.body.expires_in,300);assert.equal(requested.body.interval,5);
   await page.getByLabel('Device code',{exact:true}).fill(requested.body.user_code);await page.getByRole('button',{name:'Review request'}).click();await page.getByRole('heading',{name:'External '+name,exact:true}).waitFor();await page.getByRole('button',{name:'Approve access',exact:true}).click();await page.getByRole('status').filter({hasText:'Approved.'}).waitFor();
   await delay(5100);const issued=await client.poll(requested.body.device_code);assert.equal(issued.status,200,JSON.stringify(issued.body));assert.equal(issued.body.expires_in,900);assert(!issued.body.refresh_token);const token=issued.body.access_token;
   const visible=await client.templates(token);assert.equal(visible.status,200);assert(visible.body.items.some(t=>t.id===template.id));assert.equal((await client.poll(requested.body.device_code)).status,400);
   await page.goto(origin+'/account');await page.getByRole('button',{name:'Revoke access',exact:true}).click();await page.getByText('Revoked',{exact:true}).first().waitFor();assert.equal((await client.templates(token)).status,401);
   console.log(`PASS: ${name} browser-approved external gateway, scoped template read, single-use code and grant revocation`);
  }finally{await browser.close()}
 }
 assert.equal(digest(await readFile(binary)),before);
}finally{if(child&&child.exitCode===null){child.kill('SIGTERM');await Promise.race([new Promise(r=>child.on('exit',r)),delay(10000)]);if(child.exitCode===null)child.kill('SIGKILL')}await writeFile(join(root,'bin/extension-foundations/gateway-proof.log'),logs);await rm(directory,{recursive:true,force:true})}
