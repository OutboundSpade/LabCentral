<script lang="ts">
 import {onMount} from 'svelte';
 import {api,type AccessSession} from './api';
 import ExtensionFrame from './ExtensionFrame.svelte';
 import type {components} from './api.generated';
 let {sessionId}:{sessionId:string}=$props();
 let session=$state<AccessSession>();let extension=$state<components['schemas']['BrowserExtensionDTO']>();let error=$state('');
 onMount(()=>{const abort=new AbortController();let loading=false;
 const refresh=async()=>{if(loading)return;loading=true;try{
  const [record,presentation,catalog]=await Promise.all([api<AccessSession>(`/access-sessions/${encodeURIComponent(sessionId)}`,{signal:abort.signal}),api<{renderer?:string}>(`/access-sessions/${encodeURIComponent(sessionId)}/presentation`,{signal:abort.signal}),api<{items:components['schemas']['BrowserExtensionDTO'][]}>('/extensions/browser',{signal:abort.signal})]);
  if(abort.signal.aborted)return;
  if(!['ready','active'].includes(record.state))throw new Error('Access has ended.');
  const selected=catalog.items.find(v=>v.slot==='access-view'&&`${v.pluginInstance}/${v.id}`===presentation.renderer);if(!selected)throw new Error('Access renderer unavailable.');session=record;extension=selected;
 }catch(e){if(!abort.signal.aborted){extension=undefined;error=e instanceof Error?e.message:'Access unavailable';}}finally{loading=false;}};
 void refresh();const timer=setInterval(()=>void refresh(),5000);return()=>{abort.abort();clearInterval(timer);};});
</script>
{#if error}<p role="alert" class="text-error">{error}</p>{/if}
{#if session}<a class="link" href={`/lab?id=${encodeURIComponent(session.instanceId)}`}>Back to lab</a>{/if}
{#if extension&&session}{#key `${extension.pluginInstance}/${extension.id}/${extension.url}/${sessionId}`}<ExtensionFrame {extension} instanceId={session.instanceId} resourceId={session.resourceId} initialSessionId={sessionId} />{/key}{/if}
