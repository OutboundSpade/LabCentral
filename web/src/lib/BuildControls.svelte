<script lang="ts">
 import {onMount} from 'svelte';
 import {api,mutationHeaders} from './api';
 let {projectId,revisionId}:{projectId:string;revisionId:string}=$props();
 let recipe=$state('');let target=$state('');let message=$state('');let busy=$state(false);let operations=$state<any[]>([]);let targets=$state<any[]>([]);
 const controller=new AbortController();
 async function refresh(){try{const v=await api<any>(`/extension-operations?projectId=${encodeURIComponent(projectId)}`,{signal:controller.signal});operations=(v.items??[]).filter((x:any)=>x.request?.kind!=='source');}catch{}}
 onMount(()=>{void refresh();void api<any>('/provider-targets',{signal:controller.signal}).then(v=>{targets=v.items??[];target=targets[0]?.id??''}).catch(()=>{});const t=setInterval(refresh,3000);return()=>{clearInterval(t);controller.abort()}});
 async function run(path:string,body:any){busy=true;message='';try{await api(path,{method:'POST',body:JSON.stringify(body),headers:mutationHeaders(),signal:controller.signal});await refresh()}catch(e){message=e instanceof Error?e.message:'Request failed'}finally{busy=false}}
</script>
<details class="card border border-base-300 bg-base-100 p-4"><summary class="cursor-pointer font-medium">Builds and deployment preparation</summary>
 <p class="mt-3 text-sm text-base-content/70">Recipes come from the published bundle. Imported images need administrator approval and a qualified target before deployment.</p>
 <div class="mt-3 flex flex-wrap items-end gap-2"><label class="fieldset"><span class="fieldset-legend">Recipe ID</span><input class="input input-bordered" bind:value={recipe} placeholder="base-image" /></label><button class="btn btn-sm" disabled={busy||!revisionId||!recipe} onclick={()=>run('/builds',{projectId,revisionId,recipeId:recipe})}>Build image</button>
 <label class="fieldset"><span class="fieldset-legend">Target</span><select class="select select-bordered" bind:value={target}>{#each targets as t}<option value={t.id}>{t.name??t.id}</option>{/each}</select></label><button class="btn btn-sm" disabled={busy||!revisionId||!target} onclick={()=>run('/deployment-preparations',{projectId,preparation:{version:2,revisionId,targetId:target,inputs:[],acknowledged:true,lifetime:{ttlSeconds:3600,persistent:false}}})}>Prepare and deploy</button></div>
 {#if message}<p class="mt-2 text-error" role="alert">{message}</p>{/if}
 <ul class="mt-4 space-y-3">{#each operations as op}<li class="rounded border border-base-300 p-3"><p><strong>{op.request.kind}</strong> · {op.state} {op.failureCode??''}</p><code class="text-xs">{op.id}</code>
 <div class="mt-2 flex gap-2">{#if op.state==='queued'||op.state==='running'}<button class="btn btn-xs" disabled={busy} onclick={()=>run(`/extension-operations/${op.id}/cancel?projectId=${encodeURIComponent(projectId)}`,{})}>Cancel</button>{/if}
 {#if op.request.kind==='build'&&op.state==='succeeded'}<button class="btn btn-xs" disabled={!target||busy} onclick={()=>run('/image-imports',{projectId,buildId:op.id,targetId:target})}>Import to target</button>{/if}
 {#if op.request.kind==='image-import'&&op.state==='succeeded'}<button class="btn btn-xs" disabled={busy} onclick={()=>run(`/image-bindings/${op.id}/policy`,{projectId,approved:true,retired:false})}>Approve image</button><button class="btn btn-xs" disabled={busy} onclick={()=>run(`/image-bindings/${op.id}/policy`,{projectId,approved:false,retired:true})}>Retire image</button>{/if}
 {#if (op.state==='failed'||op.state==='canceled')&&op.request.build}<button class="btn btn-xs" disabled={busy} onclick={()=>run('/builds',{projectId,revisionId:op.request.build.revisionId,recipeId:op.request.build.recipe.id})}>Retry build</button>{/if}
 </div>{#if op.result}<details class="mt-2"><summary class="cursor-pointer text-sm">Inspect result</summary><pre class="overflow-auto text-xs">{JSON.stringify(op.result,null,2)}</pre></details>{/if}</li>{/each}</ul>
</details>
