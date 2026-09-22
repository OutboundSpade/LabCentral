// Real browser workload. Configuration and credentials stay in the private operator directory.
const fs=require('fs'),path=require('path'),crypto=require('crypto');
const {chromium}=require('../../web/node_modules/playwright');
const origin=process.env.LC_LOAD_ORIGIN;if(!origin)throw Error('LC_LOAD_ORIGIN required');
const root=process.env.LC_LOAD_DIRECTORY;if(!root)throw Error('LC_LOAD_DIRECTORY required');
const name=process.env.LC_LOAD_RUN||'baseline',duration=Number(process.env.LC_LOAD_SECONDS||600);
const participants=JSON.parse(fs.readFileSync(path.join(root,'participants.json'))).sort((a,b)=>a.group-b.group||a.number-b.number);
const labs=JSON.parse(fs.readFileSync(path.join(root,'labs.json')));
if(participants.length!==15||labs.length!==5||labs.some(l=>l.phase!=='ready'))throw Error('Need 15 distinct participants and five ready labs');
const output=path.join(root,name);fs.mkdirSync(output,{recursive:true,mode:0o700});
const events=fs.createWriteStream(path.join(output,'events.jsonl'),{mode:0o600});
const started=Date.now(),users=[];let finishing=false,stopRequested=false;
process.once('SIGTERM',()=>{stopRequested=true;emit('operator-stop');});
function emit(event,data={}){events.write(JSON.stringify({at:Date.now(),elapsedMs:Date.now()-started,event,...data})+'\n');}
function delay(ms){return new Promise(r=>setTimeout(r,ms));}
function route(url){try{return new URL(url,origin).pathname.replace(/access_[a-z0-9]+/g,':session').replace(/inst_[a-z0-9]+/g,':instance');}catch{return 'unknown';}}
async function api(c,p,method='GET',data){const csrf=(await c.cookies()).find(x=>x.name==='__Host-labcentral-csrf');const r=await c.request.fetch(origin+'/api/v1'+p,{method,data,timeout:60000,headers:{Origin:origin,'X-CSRF-Token':csrf?.value||'','Idempotency-Key':'class-load-'+crypto.randomUUID()}});if(!r.ok())throw Error(`${method} ${route(p)} HTTP ${r.status()}`);return r.status()===204?null:await r.json();}
function track(page,user){
 page.on('requestfinished',async request=>{if(!request.url().includes('/api/v1/'))return;const response=await request.response();const t=request.timing();emit('http',{user,route:route(request.url()),method:request.method(),status:response?.status(),ms:t.responseEnd});});
 page.on('requestfailed',r=>emit('http-failed',{user,route:route(r.url()),error:r.failure()?.errorText}));
 page.on('pageerror',e=>emit('page-error',{user,error:e.message}));
 page.on('websocket',socket=>{let bytes=0,frames=0,sent=0;const opened=Date.now();emit('socket-open',{user,route:route(socket.url())});socket.on('framereceived',({payload})=>{bytes+=Buffer.byteLength(payload);frames++;});socket.on('framesent',()=>sent++);socket.on('socketerror',error=>emit('socket-error',{user,error}));socket.on('close',()=>emit('socket-close',{user,ms:Date.now()-opened,bytes,frames,sent,finishing}));});
}
async function launch(user){
 const p=user.page;
 await p.goto(origin+'/lab?id='+user.lab.instanceId,{waitUntil:'domcontentloaded',timeout:60000});
 const t=Date.now();const result=await api(user.context,'/access-sessions','POST',{instanceId:user.lab.instanceId,resourceId:'vm.desktop',method:'vnc',durationSeconds:1800});
 user.sessions.push(result.session.id);emit('launch',{user:user.id,ms:Date.now()-t,session:result.session.id});
 await p.evaluate(({id,token})=>sessionStorage.setItem('labcentral.access-token.v1.'+id,token),{id:result.session.id,token:result.oneTimeToken});
 await p.goto(origin+'/access/'+result.session.id,{waitUntil:'domcontentloaded',timeout:60000});
 await p.getByText('Connected',{exact:true}).waitFor({timeout:60000});
 emit('connected',{user:user.id,ms:Date.now()-t});
}
async function control(user,round){
 if(!user.lab.testOwned)return;
 try{
  const p=user.page,t=Date.now();if(await p.getByRole('button',{name:'Take control',exact:true}).isVisible())await p.getByRole('button',{name:'Take control',exact:true}).click();
  await p.getByText('You have control',{exact:true}).waitFor({timeout:20000});
  emit('control',{user:user.id,round,ms:Date.now()-t});
  await p.locator('canvas').click();
  if(round===0){await p.keyboard.press('Control+Alt+t');await delay(2000);}else{await p.keyboard.press('Control+c');await delay(250);}
  const marker=name+'-g'+user.info.group+'-u'+user.info.number+'-r'+round;
  for(const key of ` echo ${marker} >> /tmp/lc-classroom-load-input; watch -n 1 date -u`){await p.keyboard.press(key==='>'?'Shift+Period':key===' '?'Space':key);await delay(12);}await p.keyboard.press('Enter');emit('input',{user:user.id,marker});if(round===0)await p.screenshot({path:path.join(output,user.id+'-initial-input.png')});
 }catch(e){emit('control-failed',{user:user.id,round,error:e.message.slice(0,300)});}
}
(async()=>{
 const browser=await chromium.launch({headless:true,handleSIGTERM:false,args:['--disable-background-timer-throttling','--disable-renderer-backgrounding']});
 try{
  for(const info of participants){const context=await browser.newContext({storageState:info.state,viewport:{width:1100,height:750}});const page=await context.newPage();page.setDefaultTimeout(15000);const u={id:`g${info.group}u${info.number}`,info,context,page,lab:labs.find(l=>l.group===info.group),sessions:[],lastStatus:''};track(page,u.id);users.push(u);}
  await Promise.all(users.map(async(u,i)=>{await delay(i*250);for(let retry=0;retry<3&&!stopRequested;retry++){try{await launch(u);u.launched=true;break;}catch(e){emit('launch-failed',{user:u.id,retry,error:e.message.slice(0,300)});if(retry<2)await delay(2000);}}}));
  if(users.some(u=>!u.launched))throw Error('Could not establish all fifteen participant consoles');
  const holdStart=Date.now();emit('hold-start',{users:15,durationSeconds:duration});console.log('15-user hold started',name);
  let round=0,nextControl=0,nextNavigation=15000,nextSample=0;
  while(!stopRequested&&Date.now()-holdStart<duration*1000){const elapsed=Date.now()-holdStart;
   if(elapsed>=nextSample){nextSample+=2000;await Promise.all(users.map(async u=>{try{const status=await u.page.locator('header [aria-live="polite"]').textContent({timeout:3000}).catch(()=> 'no-console');const clean=status?.trim();if(clean!==u.lastStatus){emit('status',{user:u.id,status});u.lastStatus=clean;}if(clean==='Connected')u.connectedSamples=(u.connectedSamples||0)+1;u.samples=(u.samples||0)+1;}catch(e){emit('sample-error',{user:u.id,error:e.message.slice(0,100)});}}));}
   if(elapsed>=nextControl){nextControl+=90000;const current=round++;await Promise.all(users.filter(u=>u.info.number===current%3+1).map(u=>control(u,current)));}
   if(elapsed>=nextNavigation){nextNavigation+=45000;await Promise.all(users.filter(u=>u.info.number===1).map(async u=>{try{if(!u.metadata){u.metadata=await u.context.newPage();track(u.metadata,u.id+'-metadata');}const t=Date.now();await u.metadata.goto(origin+'/lab?id='+u.lab.instanceId,{waitUntil:'domcontentloaded',timeout:30000});await u.metadata.getByRole('heading',{name:'Lab details',exact:true}).waitFor({timeout:30000});emit('navigation',{user:u.id,ms:Date.now()-t});}catch(e){emit('navigation-failed',{user:u.id,error:e.message.slice(0,200)});}}));}
   await delay(500);
  }
  emit('hold-end');
  for(const u of users){await u.page.screenshot({path:path.join(output,u.id+'.png')}).catch(()=>{});await u.context.storageState({path:u.info.state});}
 }finally{
  finishing=true;
  for(const u of users){await u.context.storageState({path:u.info.state}).catch(()=>{});}
  fs.writeFileSync(path.join(output,'summary.json'),JSON.stringify({started,ended:Date.now(),durationSeconds:duration,aborted:stopRequested,users:users.map(u=>({id:u.id,subjectId:u.info.subjectId,sessions:u.sessions,connectedSamples:u.connectedSamples||0,samples:u.samples||0,lastStatus:u.lastStatus}))},null,2),{mode:0o600});
  await browser.close();events.end();
 }
})().catch(e=>{console.error(e.message);process.exit(1)});
