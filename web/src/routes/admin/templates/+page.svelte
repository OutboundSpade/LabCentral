<script lang="ts">
 import Activity from '$lib/Activity.svelte';
 import {onMount} from 'svelte';
 import {GitCommitHorizontal,RefreshCw,Hammer,History,Check,Trash2,Clock,LockKeyhole} from '@lucide/svelte';
 import Guard from '$lib/Guard.svelte';import PageHeader from '$lib/PageHeader.svelte';import Notice from '$lib/Notice.svelte';
 import {api,session,adminView,refreshAdmin} from '$lib/api';import type {Template} from '$lib/types';
 let data=$derived($adminView);let error=$state('');let busy=$state(false);
 let selected=$state<Template|null>(null);let action=$state<'build'|'remove-images'>('build');let confirmation:HTMLDialogElement;
 const refreshing=$derived(data?.jobs.some(j=>j.kind==='refresh'&&(j.status==='queued'||j.status==='running'))??false);
 function date(value:string){return new Date(value).toLocaleString([],{dateStyle:'medium',timeStyle:'short'})}
 function jobFor(id:string){return data?.templates.find(t=>t.id===id)?.operation??data?.jobs.find(j=>j.target===id&&(j.kind==='build'||j.kind==='remove-images'))}
 function active(id:string){const j=jobFor(id);return j?.status==='queued'||j?.status==='running'}
 async function load(){try{await refreshAdmin()}catch(e){error=(e as Error).message}}
 onMount(()=>{void load();const timer=setInterval(load,2000);return()=>clearInterval(timer)});
 async function run(path:string,body:unknown={}){busy=true;error='';try{await api(path,body);confirmation?.close();await load()}catch(e){error=(e as Error).message}finally{busy=false}}
 function confirm(template:Template,operation:'build'|'remove-images'){selected=template;action=operation;error='';confirmation.showModal()}
 let history=$state(false);
 function featured(template:Template,index:number){return index<3||template.latest||data?.meeting?.template===template.id||active(template.id)}
 const earlier=$derived(data?.templates.filter((t,index)=>!featured(t,index)).length??0);
</script>
<svelte:head><title>Lab templates · LabCentral</title></svelte:head>
<PageHeader title="Lab templates" description="Revisions stay pinned. Existing labs keep their current revision.">
 {#if $session?.user.admin}<button class="btn btn-sm min-h-11 lg:min-h-8" disabled={busy||refreshing} onclick={()=>run('/api/admin/templates/refresh')}>{#if refreshing}<span class="loading loading-spinner loading-xs"></span>{:else}<RefreshCw size={15}/>{/if}{refreshing?'Refreshing…':'Refresh from GitHub'}</button>{/if}
</PageHeader>
<Guard admin><Notice message={error}/>
{#if data}
 {#if data.jobs.find(j=>j.kind==='refresh')?.status==='failed'}<Notice message={data.jobs.find(j=>j.kind==='refresh')?.error??'GitHub refresh failed. Please retry.'}/>{/if}
 <div class="grid items-start gap-8 lg:grid-cols-[minmax(0,1.6fr)_minmax(18rem,1fr)]">
  <section class="min-w-0">
   <ul class="divide-y divide-base-content/10 border-y border-base-content/10" aria-label="Template revision history">
    {#each data.templates as template,index (template.id)}
     {@const job=jobFor(template.id)}{@const working=active(template.id)}
     <li class="py-5" hidden={!history&&!featured(template,index)}>
      <article aria-label={`${template.name} revision ${template.commit.slice(0,7)}`}>
       <div class="mb-2 flex flex-wrap items-center gap-3 text-xs"><span class="font-medium">{template.name}</span>{#if template.latest}<span class="text-primary">Latest</span>{/if}{#if data.meeting?.template===template.id&&data.meeting.status!=='ended'}<span class="inline-flex items-center gap-1 text-base-content/75"><LockKeyhole size={12}/>In use</span>{/if}</div>
       <h2 class="text-base font-semibold leading-snug">{template.commitTitle||'Imported revision'}</h2>
       <div class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-base-content/75">
        <a class="link link-hover inline-flex items-center gap-1.5" href={`https://github.com/example/lab-templates/commit/${template.commit}`} target="_blank" rel="noreferrer"><GitCommitHorizontal size={14}/><code>{template.commit.slice(0,7)}</code></a>
        <span class="inline-flex items-center gap-1.5"><Clock size={13}/>{#if template.committedAt}<time datetime={template.committedAt}>{date(template.committedAt)}</time>{:else}Date unavailable — refresh from GitHub{/if}</span>
       </div>
       <div class="mt-3 flex flex-wrap items-center gap-3 text-xs" aria-live="polite">
        {#if working}<span class="loading loading-spinner loading-xs"></span><span>{job?.status==='queued'?'Queued':job?.kind==='build'?'Building images…':'Removing images…'}</span>
        {:else if job?.status==='failed'}<span class="text-error">{job.kind==='build'?'Build failed':'Cleanup failed'}</span>
        {:else if template.removing}<span>Cleanup pending</span>
        {:else if template.removedAt}<span>Images removed</span>
        {:else if template.built}<span class="inline-flex items-center gap-1.5"><Check size={13} class="text-success"/>Ready to use</span>
        {:else}<span class="text-base-content/75">Not built</span>{/if}
        {#if !template.removedAt}<span class="tabular-nums text-base-content/75">{#if !template.built&&template.imagesReady===template.imagesTotal&&template.imagesTotal>0}Images cached · build confirmation required{:else}{template.imagesReady} / {template.imagesTotal} images ready{/if}</span>{/if}
       </div>
       {#if job?.waitingOn?.length}<p class="mt-2 text-xs leading-relaxed text-base-content/75">Waiting for {job.waitingOn.map(id=>data?.jobs.find(j=>j.id===id)?.kind??'overlapping work').join(', ')} on shared resources.</p>{/if}
       {#if template.instructor}<details class="collapse collapse-arrow mt-4 border border-base-content/10"><summary class="collapse-title flex items-center gap-2 text-sm font-medium"><LockKeyhole size={14}/>Instructor only · {template.commit.slice(0,7)}</summary><div class="collapse-content text-sm"><p class="whitespace-pre-line leading-relaxed">{template.instructor.notes}</p><ul class="mt-4 space-y-3">{#each template.instructor.exercises as exercise}<li><p class="font-medium">{exercise.name}</p><p class="mt-1 whitespace-pre-line text-base-content/75">{exercise.walkthrough}</p></li>{/each}</ul><p class="mt-4 text-xs text-base-content/75">{template.instructor.checks.length} scheduled service checks available.</p></div></details>{/if}
       {#if template.removedAt}<p class="mt-2 text-xs text-base-content/75">Removed {date(template.removedAt)}. Images shared by other revisions are retained.</p>{:else if template.builtAt}<p class="mt-2 text-xs text-base-content/75">Built {date(template.builtAt)}</p>{/if}
       {#if job?.status==='failed'}<p class="mt-3 break-words text-sm text-error">{job.error}</p>{/if}
       <div class="mt-3 flex flex-wrap gap-2">
        {#if !template.built&&!template.removing}<button class="btn btn-sm min-h-11 lg:min-h-8" class:btn-primary={template.latest} disabled={busy||working} onclick={()=>confirm(template,'build')}><Hammer size={15}/>{job?.kind==='build'&&job.status==='failed'?'Retry build':template.removedAt?'Rebuild images':'Build images'}</button>{/if}
        {#if template.imagesReady>0||template.buildsPending>0||template.removing||job?.kind==='build'&&job.status==='failed'}<button class="btn btn-sm min-h-11 btn-ghost lg:min-h-8" disabled={busy||working||!!template.removalBlocked} onclick={()=>confirm(template,'remove-images')}><Trash2 size={15}/>{template.removing?'Retry cleanup':'Remove images'}</button>{/if}
       </div>
       {#if template.removalBlocked}<p class="mt-2 flex items-start gap-2 text-xs leading-relaxed text-base-content/75"><LockKeyhole size={13} class="mt-0.5 shrink-0"/>{template.removalBlocked}</p>{/if}
      </article>
     </li>
    {:else}<li class="py-8"><History size={22}/><h2 class="mt-3 text-base font-semibold">No revisions yet</h2><p class="mt-2 text-sm text-base-content/75">Refresh from GitHub to import your templates.</p></li>{/each}
   </ul>
   {#if earlier}<button type="button" class="btn btn-sm min-h-11 btn-ghost mt-3" aria-expanded={history} onclick={()=>history=!history}><History size={15}/>{history?'Show fewer revisions':`Show ${earlier} earlier ${earlier===1?'revision':'revisions'}`}</button>{/if}
  </section>
  <aside class="min-w-0 lg:sticky lg:top-6 lg:border-l lg:border-base-content/10 lg:pl-6"><Activity {data} onchange={refreshAdmin}/></aside>
 </div>
{:else}<p class="flex items-center gap-2 text-sm text-base-content/75" role="status"><span class="loading loading-spinner loading-sm"></span>Loading revisions…</p>{/if}
</Guard>
<dialog bind:this={confirmation} class="modal" aria-labelledby="revision-action-title"><div class="modal-box">
 <h2 id="revision-action-title" class="text-lg font-semibold">{action==='build'?'Build images?':'Remove images?'}</h2><p class="mt-3 text-sm font-medium">{selected?.name} · <code>{selected?.commit.slice(0,7)}</code></p><p class="mt-3 text-sm leading-relaxed">{action==='build'?`Build ${selected?.imagesTotal??0} reusable images from this revision. Temporary build and validation machines incur charges; matching images are reused.`:'Remove this revision’s unused native images, snapshots and unfinished builds. Shared images are retained. The revision stays in this history and can be rebuilt.'}</p><Notice message={error}/>
 <div class="modal-action"><form method="dialog"><button class="btn btn-sm min-h-11">Cancel</button></form><button class="btn btn-sm min-h-11" class:btn-primary={action==='build'} class:btn-error={action==='remove-images'} disabled={busy} onclick={()=>run('/api/admin/templates/'+action,{id:selected?.id})}>{#if busy}<span class="loading loading-spinner loading-xs"></span>{/if}{action==='build'?'Start build':'Remove unused images'}</button></div>
</div></dialog>
