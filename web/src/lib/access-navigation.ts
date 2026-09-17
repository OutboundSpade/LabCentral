import { goto } from '$app/navigation';
import { api, downloadAccessConfiguration, type AccessLaunch } from './api';
export async function presentAccess(launch:AccessLaunch,signal:AbortSignal){
 if(!launch.oneTimeToken)throw new Error('The console launch was incomplete. Please try again.');
 if(launch.launchUrl){const u=new URL(launch.launchUrl,location.origin);if(u.origin!==location.origin||u.username||u.password||u.search||u.hash||u.pathname!==`/access/${encodeURIComponent(launch.session.id)}`)throw new Error('The console launch was incomplete. Please try again.');}
 const p=await api<{method:string;presentation:string;renderer?:string}>(`/access-sessions/${encodeURIComponent(launch.session.id)}/presentation`,{signal});
 if(p.presentation==='credential-download'){
  const blob=await downloadAccessConfiguration(launch.session.id,launch.oneTimeToken,signal);
  if(signal?.aborted)return;
  const url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download='lab-access.conf';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);return;
 }
 if(signal?.aborted)return;
 if(p.renderer){
  const catalog=await api<{items:{pluginInstance:string;id:string;slot:string}[]}>('/extensions/browser',{signal});
  if(!catalog.items.some(v=>`${v.pluginInstance}/${v.id}`===p.renderer&&v.slot==='access-view'))throw new Error('The access renderer is unavailable.');
  sessionStorage.setItem(`labcentral.access-token.v1.${launch.session.id}`,launch.oneTimeToken);
  await goto(`/extension?session=${encodeURIComponent(launch.session.id)}`);return;
 }
 if(p.method!=='vnc'||p.presentation!=='graphical')throw new Error('No compatible access renderer is installed.');
 sessionStorage.setItem(`labcentral.access-token.v1.${launch.session.id}`,launch.oneTimeToken);
 await goto(`/access/${encodeURIComponent(launch.session.id)}`);
}
