package hosted

// noVNC is served by the authenticated provider relay from access/web/vendor.
// VNC authentication happens inside that relay; these credentials are only for
// the guest's own greeter and can never authenticate to the Proxmox API.
const novncJS = `import RFB from '/vendor/core/rfb.js';
const status=document.getElementById('status');
const viewport=document.getElementById('desktop-viewport');
const display=document.getElementById('dcv-display');
const copy=document.getElementById('clipboard');
const paste=document.getElementById('paste');
const dialog=document.getElementById('paste-dialog');
const text=document.getElementById('paste-text');
let rfb,login,clipboard='';
const ended=async()=>{login?.remove();copy.disabled=true;paste.disabled=true;dialog.close();text.value='';clipboard='';for(let i=0;i<24;i++){try{const response=await fetch('/status',{cache:'no-store'});if(!response.ok)break;const state=await response.json();if(state.takenOverBy){status.textContent=state.takenOverBy+' took over this desktop. Return to your lab to take it back.';return}if(state.code==='available')break}catch{break}await new Promise(done=>setTimeout(done,5000))}};
document.getElementById('fullscreen').onclick=()=>viewport.requestFullscreen?.();
paste.onclick=()=>{text.value='';dialog.showModal();text.focus()};
// noVNC's clipboard channel is text-only and depends on the guest clipboard agent.
text.addEventListener('paste',event=>{const value=event.clipboardData?.getData('text/plain');if(value!==undefined){event.preventDefault();rfb?.clipboardPasteFrom(value);dialog.close();status.textContent='Text sent to desktop clipboard, if supported by the guest.'}});
for(const event of ['keydown','keyup'])dialog.addEventListener(event,e=>e.stopPropagation());
dialog.addEventListener('close',()=>{text.value=''});
copy.onclick=async()=>{if(!clipboard){status.textContent='Copy text inside the desktop first. Clipboard requires guest support.';return}try{await navigator.clipboard.writeText(clipboard);status.textContent='Desktop text copied.'}catch{status.textContent='Clipboard permission is required. Copy text inside the desktop, then retry.'}};
if(document.body.dataset.practiceRun)import('/lesson-client.js').catch(()=>{});
try{
 const response=await fetch('/credentials',{cache:'no-store'});
 if(!response.ok)throw Error();const credentials=await response.json();
 rfb=new RFB(display,location.origin.replace('https:','wss:').replace('http:','ws:')+'/websocket');
 rfb.scaleViewport=true;rfb.resizeSession=true;
 rfb.addEventListener('connect',()=>{status.textContent='Sign in as '+credentials.username+' using the Console login button.';copy.disabled=false;paste.disabled=false});
 rfb.addEventListener('disconnect',()=>{status.textContent='Disconnected. Return to your lab to reconnect.';ended()});
 rfb.addEventListener('securityfailure',()=>{status.textContent='Console connection failed. Return to your lab to reconnect.'});
 rfb.addEventListener('clipboard',event=>{clipboard=event.detail.text});
 login=document.createElement('button');login.textContent='Console login';login.type='button';
 const info=document.createElement('dialog');info.className='modal';
 const box=document.createElement('div');box.className='modal-box space-y-3';
 const title=document.createElement('h2');title.id='console-login-title';info.setAttribute('aria-labelledby',title.id);title.textContent='Guest console login';title.className='font-semibold';
 const user=document.createElement('p');user.textContent='Username: '+credentials.username;
 const password=document.createElement('p');password.textContent='Password: '+credentials.password;password.className='break-all select-all font-mono';
 const hint=document.createElement('p');hint.textContent='Choose Student or Other at the guest login screen and enter these credentials. This signs in to the shared VM screen.';
 const close=document.createElement('button');close.className='btn btn-sm min-h-11';close.textContent='Close';close.onclick=()=>info.close();
 box.append(title,user,password,hint,close);info.append(box);document.body.append(info);login.onclick=()=>info.showModal();document.querySelector('header').append(login);
}catch{status.textContent='Access ended. Return to your lab to reconnect.';ended()}`
