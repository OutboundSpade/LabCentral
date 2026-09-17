<script lang="ts">
  import { onMount } from "svelte";
  import { api, mutationHeaders } from "./api";
  let { projectId, templateId, onpublished }: { projectId: string; templateId: string; onpublished: () => void } = $props();
  type Work = {id:string;state:string;failureCode?:string;request:{kind?:string;reference?:string;templateId?:string}};
  let bindings = $state<{binding:string;available:boolean}[]>([]);
  let binding = $state("");
  let reference = $state("");
  let work = $state<Work[]>([]);
  let busy = $state(false), error = $state("");
  const scope = () => `projectId=${encodeURIComponent(projectId)}`;
  let refresh = async () => {};
  onMount(() => {
    const abort = new AbortController(); let loading=false;
    const completed = new Set<string>(); let initialized=false;
    refresh=async()=>{if(loading||abort.signal.aborted)return;loading=true;try{
      const result=await api<{items:Work[]}>(`/extension-operations?${scope()}`,{signal:abort.signal});
      if(abort.signal.aborted)return;
      work=result.items.filter(w=>w.request.templateId===templateId&&w.request.kind==='source');
      for(const w of work)if(w.state==='succeeded'&&!completed.has(w.id)){completed.add(w.id);if(initialized)onpublished();}
      initialized=true;
    }catch(e){if(!abort.signal.aborted)error=e instanceof Error?e.message:"Could not read source operations";}finally{loading=false;}};
    void api<{items:{binding:string;available:boolean}[]}>(`/source-bindings?${scope()}`,{signal:abort.signal}).then(r=>{if(!abort.signal.aborted){bindings=r.items;binding=bindings.find(b=>b.available)?.binding??"";}}).catch(()=>{});
    void refresh();const timer=setInterval(()=>void refresh(),3000);
    return()=>{abort.abort();clearInterval(timer);};
  });
  async function resolve(ref=reference){busy=true;error="";try{await api("/source-resolutions",{method:"POST",headers:mutationHeaders(),body:JSON.stringify({projectId,templateId,binding,reference:ref})});await refresh();}catch(e){error=e instanceof Error?e.message:"Source resolution failed";}finally{busy=false;}}
  async function cancel(id:string){try{await api(`/extension-operations/${encodeURIComponent(id)}/cancel?${scope()}`,{method:"POST",headers:mutationHeaders()});await refresh();}catch(e){error=e instanceof Error?e.message:"Cancellation failed";}}
</script>
{#if bindings.length}
<section class="mb-6 grid gap-3 rounded-box border border-base-300 p-5">
  <h2 class="font-semibold">Publish from a source</h2>
  <p class="text-sm text-base-content/70">Resolve a reference through an installed source. Successful resolution publishes a new immutable revision.</p>
  <label class="grid gap-1">Source<select class="select" bind:value={binding}>{#each bindings as b}<option value={b.binding} disabled={!b.available}>{b.binding}{b.available?"":" (unavailable)"}</option>{/each}</select></label>
  <label class="grid gap-1">Reference<input class="input" maxlength="1024" bind:value={reference} /></label>
  <button type="button" class="btn justify-self-start" disabled={busy||!binding||!reference||!templateId} onclick={()=>void resolve()}>Resolve and publish</button>
  {#if !templateId}<p class="text-sm">Select or create a template first.</p>{/if}
  {#if error}<p role="alert" class="text-error">{error}</p>{/if}
  <ul>{#each work as w}<li class="flex flex-wrap items-center gap-3 py-2"><span>{w.request.reference} · {w.state}</span>{#if w.failureCode}<span>{w.failureCode}</span>{/if}{#if w.state==='queued'||w.state==='running'}<button type="button" class="btn btn-sm" onclick={()=>void cancel(w.id)}>Cancel</button>{:else if w.state==='failed'||w.state==='canceled'}<button type="button" class="btn btn-sm" disabled={busy} onclick={()=>void resolve(w.request.reference)}>Retry</button>{/if}</li>{/each}</ul>
</section>
{/if}
