<script lang="ts">
 import {onMount} from 'svelte';
 import {api,mutationHeaders} from './api';
 type Client={id:string;name:string;disabled:boolean};
 let clients=$state<Client[]>([]),name=$state(''),secret=$state(''),error=$state(''),busy=$state(false);
 const refresh=async()=>{const r=await api<{items:Client[]}>('/admin/gateway-clients');clients=r.items;};
 onMount(()=>{void refresh().catch(()=>{});});
 async function create(){busy=true;error='';secret='';try{const r=await api<{client:Client;clientSecret:string}>('/admin/gateway-clients',{method:'POST',headers:mutationHeaders(),body:JSON.stringify({name})});secret=r.clientSecret;name='';await refresh();}catch(e){error=e instanceof Error?e.message:'Registration failed';}finally{busy=false;}}
 async function disable(c:Client){try{await api(`/admin/gateway-clients/${encodeURIComponent(c.id)}/status`,{method:'POST',headers:mutationHeaders(),body:JSON.stringify({disabled:!c.disabled})});await refresh();}catch(e){error=e instanceof Error?e.message:'Update failed';}}
</script>
<section class="mt-6 rounded-box border border-base-300 p-5"><h2 class="font-semibold">Registered gateways</h2><form class="mt-3 flex flex-wrap gap-3" onsubmit={e=>{e.preventDefault();void create();}}><label>Client name<input class="input ml-3" bind:value={name} required maxlength="128" /></label><button class="btn" disabled={busy}>Register gateway</button></form>
{#if secret}<div class="my-3"><p>Save this client secret in the gateway's protected configuration. It is shown once.</p><input class="input mt-2 w-full font-mono" readonly value={secret} aria-label="New gateway client secret" /><button class="btn btn-sm mt-2" onclick={()=>secret=''}>Dismiss secret</button></div>{/if}
{#if error}<p role="alert" class="text-error">{error}</p>{/if}<ul class="mt-3 space-y-3">{#each clients as c}<li>{c.name} · {c.id} · {c.disabled?'Disabled':'Enabled'} <button class="btn btn-sm" onclick={()=>void disable(c)}>{c.disabled?'Enable':'Disable'}</button></li>{/each}</ul></section>
