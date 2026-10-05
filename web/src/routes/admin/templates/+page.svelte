<script lang="ts">
 import Activity from '$lib/Activity.svelte';
 import {onMount} from 'svelte';
 import {GitCommitHorizontal,RefreshCw,Hammer,ArrowLeft,History,Check,Trash2,Clock,LockKeyhole} from '@lucide/svelte';
 import Guard from '$lib/Guard.svelte';import Notice from '$lib/Notice.svelte';
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
</script>
<svelte:head><title>Lab templates · LabCentral</title></svelte:head>
<a href="/admin/" class="link mb-7 inline-flex items-center gap-2 text-sm"><ArrowLeft size={16}/>Manage meeting</a>
<div class="flex flex-wrap items-start justify-between gap-5">
 <div><h1 class="text-3xl font-semibold tracking-tight sm:text-4xl">Lab templates</h1><p class="mt-3 text-sm text-base-content/75">Revision history · newest first</p></div>
 {#if $session?.user.admin}<button class="btn btn-sm min-h-11" disabled={busy||refreshing} onclick={()=>run('/api/admin/templates/refresh')}>{#if refreshing}<span class="loading loading-spinner loading-xs"></span>{:else}<RefreshCw size={16}/>{/if}{refreshing?'Refreshing…':'Refresh from GitHub'}</button>{/if}
</div>
<Guard admin>
 <Notice message={error}/>
 {#if data}
  {#if data.jobs.find(j=>j.kind==='refresh')?.status==='failed'}<Notice message={data.jobs.find(j=>j.kind==='refresh')?.error??'GitHub refresh failed. Please retry.'}/>{/if}
  {#if data.jobs.some(j=>j.status==='failed')}<p class="mt-6 text-sm text-error">Some operations need attention. <a class="link inline-flex min-h-11 items-center" href="#activity-title">View failures in live activity</a>.</p>{/if}
  <ul class="timeline timeline-vertical timeline-compact timeline-snap-icon mt-10 max-w-4xl" aria-label="Template revision history">
   {#each data.templates as template,index (template.id)}
    {@const job=jobFor(template.id)}{@const working=active(template.id)}
    <li class="min-w-0 grid-cols-[0_auto_minmax(0,1fr)]">
     {#if index>0}<hr/>{/if}
     <div class="timeline-middle rounded-full bg-base-100 p-1" class:text-primary={template.latest}><GitCommitHorizontal size={20}/></div>
     <div class="timeline-end mb-10 w-full min-w-0 pl-3 sm:pl-6">
      <article aria-label={`${template.name} revision ${template.commit.slice(0,7)}`}>
       <div class="flex flex-wrap items-center gap-3 text-sm"><span class="font-medium">{template.name}</span>{#if template.latest}<span class="badge badge-sm badge-primary">Latest</span>{/if}{#if data.meeting?.template===template.id&&data.meeting.status!=='ended'}<span class="badge badge-sm"><LockKeyhole size={12}/>In use</span>{/if}</div>
       <h2 class="mt-3 text-xl font-semibold leading-snug">{template.commitTitle||'Imported revision'}</h2>
       <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-base-content/75">
        <a class="link inline-flex items-center gap-1.5" href={`https://github.com/example/lab-templates/commit/${template.commit}`} target="_blank" rel="noreferrer"><GitCommitHorizontal size={15}/><code>{template.commit.slice(0,7)}</code></a>
        <span class="inline-flex items-center gap-1.5"><Clock size={14}/>{#if template.committedAt}<time datetime={template.committedAt}>{date(template.committedAt)}</time>{:else}Date unavailable — refresh from GitHub{/if}</span>
       </div>
       <div class="mt-5 flex flex-wrap items-center gap-3 text-sm" aria-live="polite">
        {#if working}<span class="loading loading-spinner loading-xs"></span><span>{job?.status==='queued'?'Queued':job?.kind==='build'?'Building images…':'Removing images…'}</span>
        {:else if job?.status==='failed'}<span class="badge badge-sm badge-error">{job.kind==='build'?'Build failed':'Cleanup failed'}</span>
        {:else if template.removing}<span class="badge badge-sm">Cleanup pending</span>
        {:else if template.removedAt}<span class="badge badge-sm">Images removed</span>
        {:else if template.built}<span class="inline-flex items-center gap-1.5"><Check size={16} class="text-success"/>Ready to use</span>
        {:else}<span class="badge badge-sm">Not built</span>{/if}
        {#if !template.removedAt}<span class="text-base-content/75">{#if !template.built&&template.imagesReady===template.imagesTotal&&template.imagesTotal>0}Images cached · build confirmation required{:else}{template.imagesReady} / {template.imagesTotal} images ready{/if}</span>{/if}
       </div>
       {#if job?.waitingOn?.length}<p class="mt-2 text-sm text-base-content/75">Waiting for {job.waitingOn.map(id=>data?.jobs.find(j=>j.id===id)?.kind??'overlapping work').join(', ')} on shared resources.</p>{/if}
       {#if template.instructor}<details class="collapse collapse-arrow mt-5 border border-base-300 bg-base-200"><summary class="collapse-title flex items-center gap-2 font-medium"><LockKeyhole size={16}/>Instructor only · {template.commit.slice(0,7)}</summary><div class="collapse-content text-sm"><p class="whitespace-pre-line leading-relaxed">{template.instructor.notes}</p><ul class="mt-4 space-y-3">{#each template.instructor.exercises as exercise}<li><p class="font-medium">{exercise.name}</p><p class="mt-1 whitespace-pre-line text-base-content/75">{exercise.walkthrough}</p></li>{/each}</ul><p class="mt-4 text-base-content/75">{template.instructor.checks.length} scheduled service checks available.</p></div></details>{/if}
       {#if template.removedAt}<p class="mt-2 text-sm text-base-content/75">Removed {date(template.removedAt)}. Images shared by other revisions are retained.</p>{:else if template.builtAt}<p class="mt-2 text-sm text-base-content/75">Built {date(template.builtAt)}</p>{/if}
       {#if job?.status==='failed'}<p class="mt-3 break-words text-sm text-error">{job.error}</p>{/if}
       <div class="mt-5 flex flex-wrap gap-3">
        {#if !template.built&&!template.removing}<button class="btn btn-sm min-h-11" class:btn-primary={template.latest} disabled={busy||working} onclick={()=>confirm(template,'build')}><Hammer size={16}/>{job?.kind==='build'&&job.status==='failed'?'Retry build':template.removedAt?'Rebuild images':'Build images'}</button>{/if}
        {#if template.imagesReady>0||template.buildsPending>0||template.removing||job?.kind==='build'&&job.status==='failed'}<button class="btn btn-sm min-h-11 btn-ghost" disabled={busy||working||!!template.removalBlocked} onclick={()=>confirm(template,'remove-images')}><Trash2 size={16}/>{template.removing?'Retry cleanup':'Remove images'}</button>{/if}
       </div>
       {#if template.removalBlocked}<p class="mt-3 flex items-start gap-2 text-sm text-base-content/75"><LockKeyhole size={14} class="mt-0.5 shrink-0"/>{template.removalBlocked}</p>{/if}
      </article>
     </div>
     {#if index<data.templates.length-1}<hr/>{/if}
    </li>
   {:else}<li><div class="timeline-middle"><History size={22}/></div><div class="timeline-end py-1 pl-4"><h2 class="text-lg font-semibold">No revisions yet</h2><p class="mt-2 text-sm text-base-content/75">Refresh from GitHub to import your templates.</p></div></li>{/each}
  </ul>
  <Activity {data} onretry={id=>run('/api/admin/jobs/retry',{id})}/>
 {:else}<p class="mt-10 flex items-center gap-3 text-sm" role="status"><span class="loading loading-spinner loading-sm"></span>Loading revisions…</p>{/if}
</Guard>
<dialog bind:this={confirmation} class="modal" aria-labelledby="revision-action-title">
 <div class="modal-box">
  <h2 id="revision-action-title" class="text-xl font-semibold">{action==='build'?'Build images?':'Remove images?'}</h2>
  <p class="mt-3 font-medium">{selected?.name} · <code>{selected?.commit.slice(0,7)}</code></p>
  <p class="mt-4 leading-relaxed">{action==='build'?`Build ${selected?.imagesTotal??0} reusable images from this revision. Temporary build and validation machines incur charges; matching images are reused.`:'Remove this revision’s unused native images, snapshots and unfinished builds. Shared images are retained. The revision stays in this timeline and can be rebuilt.'}</p>
  <Notice message={error}/>
  <div class="modal-action"><form method="dialog"><button class="btn btn-sm min-h-11">Cancel</button></form><button class="btn btn-sm min-h-11" class:btn-primary={action==='build'} class:btn-error={action==='remove-images'} disabled={busy} onclick={()=>run('/api/admin/templates/'+action,{id:selected?.id})}>{#if busy}<span class="loading loading-spinner loading-xs"></span>{/if}{action==='build'?'Start build':'Remove unused images'}</button></div>
 </div>
</dialog>
