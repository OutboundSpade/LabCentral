<script lang="ts">
  import { onMount } from "svelte";
  import { api, mutationHeaders } from "./api";
  type Grant={id:string;clientName:string;clientId:string;expiresAt:string;revoked:boolean;scope:{projectId:string;actions:string[]}};
  let items=$state<Grant[]>([]),error=$state("");
  let load=async()=>{};
  onMount(()=>{const abort=new AbortController();let pending=false;load=async()=>{if(pending)return;pending=true;try{const r=await api<{items:Grant[]}>("/session/gateway-grants",{signal:abort.signal});if(!abort.signal.aborted)items=r.items;}catch(e){if(!abort.signal.aborted)error=e instanceof Error?e.message:"Could not load grants";}finally{pending=false;}};void load();const timer=setInterval(()=>void load(),10000);return()=>{abort.abort();clearInterval(timer);};});
  async function revoke(id:string){try{await api(`/session/gateway-grants/${encodeURIComponent(id)}/revoke`,{method:"POST",headers:mutationHeaders()});await load();}catch(e){error=e instanceof Error?e.message:"Revocation failed";}}
</script>
<section class="rounded-box border border-base-300 p-6"><h2 class="mb-3 text-lg font-semibold">Gateway access</h2><a href="/device" class="link">Authorize a gateway</a>
{#if error}<p role="alert" class="text-error">{error}</p>{/if}
<ul class="mt-4 space-y-4">{#each items as g}<li><p class="font-medium">{g.clientName}</p><p class="text-sm">{g.clientId} · {g.scope.projectId} · {g.scope.actions.join(", ")}</p><p class="text-sm">{g.revoked?"Revoked":`Expires ${new Date(g.expiresAt).toLocaleString()}`}</p>{#if !g.revoked&&Date.parse(g.expiresAt)>Date.now()}<button class="btn btn-sm mt-2" onclick={()=>void revoke(g.id)}>Revoke access</button>{/if}</li>{/each}</ul>{#if !items.length}<p class="mt-4">No gateway grants.</p>{/if}</section>
