import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createServer} from 'node:http';
import {WebSocketServer} from 'ws';
import {chromium,firefox,webkit} from 'playwright';
const build=new URL('../build/',import.meta.url);
let sessions=new Map(),driver='',outage=false,sequence=0,inputs=[];
const user=req=>/test-user=([ab])/.exec(req.headers.cookie??'')?.[1];
const session=(id)=>({id,subjectId:'user.'+sessions.get(id)?.user,instanceId:'instance.shared',resourceId:'vm.desktop',method:'vnc',state:'active',sharedConsole:true,resourceVersion:1});
const server=createServer(async(req,res)=>{
 const path=new URL(req.url,'http://localhost').pathname;const json=(value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
 if(path.startsWith('/api/v1/')){
  const p=path.slice(7),actor=user(req);let raw='';for await(const c of req)raw+=c;
  if(!actor)return json({detail:'Authentication required'},401);
  if(p==='/server-info')return json({authMode:'local',uiFeatures:['browser-vnc']});
  if(p==='/session')return json({subjectId:'user.'+actor,displayName:actor==='a'?'Alice':'Bob',roles:['participant']});
  if(p==='/projects')return json({items:[]});
  if(p==='/lab-instances/instance.shared')return json({id:'instance.shared',name:'Linux team triage',instructions:'Run hostname and compare with your teammate.',ownerKind:'team',ownerId:'team.shared',phase:'ready',resources:[{id:'vm.desktop',kind:'VM',phase:'ready'}]});
  if(p==='/access-sessions'&&req.method==='POST'){const id='access.'+actor+'-'+(++sequence);sessions.set(id,{user:actor,connected:false});return json({session:session(id),oneTimeToken:'token-'+id,launchUrl:'/access/'+id},201);}
  if(p.startsWith('/access-sessions/')){
   const id=p.split('/')[2],record=sessions.get(id);if(!record||record.user!==actor)return json({detail:'Not found'},404);
   if(p.endsWith('/presentation'))return json({streamPath:'/access-proxy/v1/access/'+id+'/websocket'});
   if(p.endsWith('/actions/revoke')){record.connected=false;record.ws?.close();if(driver===id)driver='';return json(session(id));}
   if(p.includes('/console')){
    if(outage)return json({detail:'Unavailable'},503);
    if(p.endsWith('/take-control')){assert(req.headers['idempotency-key']);driver=id;}
    if(p.endsWith('/release-control')&&driver===id)driver='';
    return json({apiVersion:'labcentral.console-presence/v1',sessionId:id,instanceId:'instance.shared',resourceId:'vm.desktop',generation:1,fence:sequence+1,driverSessionId:driver,requestedSessionId:driver,participants:[...sessions].filter(([,s])=>s.connected).map(([id,s])=>({sessionId:id,userId:'user.'+s.user,displayName:s.user==='a'?'Alice':'Bob'}))});
   }
   return json(session(id));
  }
  return json({detail:'Not found'},404);
 }
 const name=path.startsWith('/access/')?'200.html':path==='/'?'index.html':path.slice(1);const file=new URL(name,build);if(!file.href.startsWith(build.href))return res.writeHead(404).end();
 try{res.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(await readFile(file));}catch{res.writeHead(404).end();}
});
const wss=new WebSocketServer({server});
wss.on('connection',(ws,req)=>{
 let stage=0,id='',buffer=Buffer.alloc(0);
 ws.on('close',()=>{const s=sessions.get(id);if(s)s.connected=false;if(driver===id)driver='';});
 ws.on('message',raw=>{
  buffer=Buffer.concat([buffer,Buffer.from(raw)]);
  if(stage===0){const auth=JSON.parse(buffer);id=auth.accessSessionId;const s=sessions.get(id);assert.equal(s?.user,user(req));assert.equal(auth.oneTimeToken,'token-'+id);s.connected=true;s.ws=ws;ws.send('{"authorized":true}');setTimeout(()=>ws.send(Buffer.from('RFB 003.008\n')),10);buffer=Buffer.alloc(0);stage=1;return;}
  if(stage===1){if(buffer.length<12)return;buffer=buffer.subarray(12);ws.send(Buffer.from([1,1]));stage=2;}
  if(stage===2){if(buffer.length<1)return;buffer=buffer.subarray(1);ws.send(Buffer.alloc(4));stage=3;}
  if(stage===3){if(buffer.length<1)return;assert.equal(buffer[0],1,'browser must request shared upstream');buffer=buffer.subarray(1);const name=Buffer.from('Shared desktop'),init=Buffer.alloc(24+name.length);init.writeUInt16BE(64,0);init.writeUInt16BE(48,2);init[4]=32;init[5]=24;init[7]=1;init.writeUInt16BE(255,8);init.writeUInt16BE(255,10);init.writeUInt16BE(255,12);init[14]=16;init[15]=8;init.writeUInt32BE(name.length,20);name.copy(init,24);ws.send(init);stage=4;}
  while(buffer.length){const type=buffer[0];let length=0;if(type===0)length=20;else if(type===2){if(buffer.length<4)return;length=4+4*buffer.readUInt16BE(2);}else if(type===3)length=10;else if(type===4)length=8;else if(type===5)length=6;else if(type===150)length=10;else throw new Error('Unsupported browser message '+type);if(buffer.length<length)return;
   const frame=buffer.subarray(0,length);buffer=buffer.subarray(length);
   if(type===4)inputs.push({id,key:frame.readUInt32BE(4),down:frame[1]});
   if(type===3&&frame[1]===0){const update=Buffer.alloc(16+64*48*4);update.writeUInt16BE(1,2);update.writeUInt16BE(64,8);update.writeUInt16BE(48,10);for(let i=16;i<update.length;i+=4){update[i]=31;update[i+1]=63;update[i+2]=127;}ws.send(update);}
  }
 });
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
try{for(const[name,engine]of Object.entries({chromium,firefox,webkit})){
 if(process.env.LABCENTRAL_BROWSER&&process.env.LABCENTRAL_BROWSER!==name)continue;
 sessions=new Map();driver='';outage=false;inputs=[];sequence=0;
 const browser=await engine.launch();try{
  const pages=[];const errors=[];
  for(const actor of ['a','b']){const context=await browser.newContext();await context.addCookies([{name:'test-user',value:actor,url:origin}]);const page=await context.newPage();page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(e.message));const id='access.'+actor;sessions.set(id,{user:actor,connected:false});await page.goto(origin);await page.waitForLoadState("networkidle");await page.evaluate(({id})=>sessionStorage.setItem('labcentral.access-token.v1.'+id,'token-'+id),{id});await page.goto(origin+'/access/'+id);await page.getByText('Connected',{exact:true}).waitFor();await page.getByRole('button',{name:'Take control',exact:true}).waitFor();pages.push(page);}
  const[a,b]=pages;await a.getByRole('button',{name:'Instructions',exact:true}).click();await a.getByRole('complementary',{name:'Exercise instructions'}).getByText('Run hostname and compare with your teammate.',{exact:true}).waitFor();assert(await b.getByRole('checkbox',{name:'View only',exact:true}).isChecked());await a.getByText(/Connected:.*Alice.*Bob/).waitFor();for(const p of pages){assert(await p.getByRole('button',{name:'Send key'}).isDisabled());await p.waitForFunction(()=>{const c=document.querySelector('canvas');return c&&Array.from(c.getContext('2d').getImageData(5,5,1,1).data).slice(0,3).some(v=>v>0);});}
  const pixels=await Promise.all(pages.map(p=>p.evaluate(()=>Array.from(document.querySelector('canvas').getContext('2d').getImageData(5,5,1,1).data))));assert.deepEqual(pixels[0],pixels[1],'same desktop pixels');
  await a.getByRole('button',{name:'Take control',exact:true}).click();await a.getByText('You have control',{exact:true}).waitFor();await a.getByRole('button',{name:'Send key'}).click();await a.waitForTimeout(100);assert.equal(inputs.at(-1)?.id,'access.a');
  await b.getByRole('button',{name:'Take control',exact:true}).click();await b.getByText('You have control',{exact:true}).waitFor();await a.getByText('Watching',{exact:true}).waitFor();assert(await a.getByRole('button',{name:'Send key'}).isDisabled());await b.getByRole('button',{name:'Send key'}).click();await b.waitForTimeout(100);assert.equal(inputs.at(-1)?.id,'access.b');
  await b.getByRole('button',{name:'Release control',exact:true}).click();await b.getByText('Watching',{exact:true}).waitFor();
  await a.getByRole('button',{name:'Take control',exact:true}).click();await a.getByText('You have control',{exact:true}).waitFor();await a.waitForLoadState("networkidle");await a.reload();await a.getByText('Connected',{exact:true}).waitFor();await a.getByText('Watching',{exact:true}).waitFor();assert.notEqual(new URL(a.url()).pathname,'/access/access.a');assert.equal(driver,'');
  await b.getByRole('button',{name:'Take control',exact:true}).click();await b.getByText('You have control',{exact:true}).waitFor();outage=true;driver='';await b.getByText('Watching',{exact:true}).waitFor();assert(await b.getByRole('button',{name:'Send key'}).isDisabled());outage=false;
  await b.setViewportSize({width:390,height:844});assert(await b.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));assert.deepEqual(errors,[]);console.log(`PASS ${name}: independent members, shared desktop, take/release, takeover, reconnect, outage and mobile`);
 }finally{await browser.close();}
}}finally{wss.close();server.closeAllConnections();await new Promise(r=>server.close(r));}
