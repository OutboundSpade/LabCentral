<script lang="ts">
 import {onMount} from 'svelte';
 import {api,mutationHeaders,type AccessLaunch} from './api';
 import {presentAccess} from './access-navigation';
 import ExtensionPanels from './ExtensionPanels.svelte';
 import type {components} from './api.generated';
 let {instanceId,resourceId,busy=false}:{instanceId:string;resourceId:string;busy?:boolean}=$props();
 type Action=NonNullable<NonNullable<components['schemas']['Topology']['resources']>[number]['actions']>[number];
 let actions=$state<Action[]>([]),pending=$state(false),error=$state('');
 let signal:AbortSignal;
 onMount(()=>{const abort=new AbortController();signal=abort.signal;let loading=false;
 const refresh=async()=>{if(loading)return;loading=true;try{const topology=await api<components['schemas']['Topology']>(`/lab-instances/${encodeURIComponent(instanceId)}/topology`,{signal});if(!signal.aborted)actions=topology.resources?.find(r=>r.id===resourceId)?.actions??[];}catch{if(!signal.aborted)actions=[];}finally{loading=false;}};
 void refresh();const timer=setInterval(()=>void refresh(),5000);return()=>{abort.abort();clearInterval(timer);};});
 async function open(action:Action){pending=true;error='';try{const launch=await api<AccessLaunch>('/access-sessions',{method:'POST',signal,headers:mutationHeaders(),body:JSON.stringify({instanceId,resourceId,method:action.method,durationSeconds:900})});await presentAccess(launch,signal);}catch(e){if(!signal.aborted)error=e instanceof Error?e.message:'Access unavailable';}finally{pending=false;}}
</script>
<div class="flex flex-wrap gap-2">{#each actions as action}<button type="button" class="btn btn-primary" disabled={busy||pending} onclick={()=>void open(action)}>{action.method==='vnc'?'Open console':action.title}</button>{/each}</div>
{#if error}<p role="alert" class="text-error">{error}</p>{/if}
<ExtensionPanels slot="resource-action" {instanceId} {resourceId} />
