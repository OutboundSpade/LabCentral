<script lang="ts">
 import {Activity,Clock,Monitor,RotateCw,TriangleAlert} from '@lucide/svelte';
 import type {AdminView,Job} from '$lib/types';
 import {date} from '$lib/api';
 let {data,onretry}:{data:AdminView,onretry:(id:string)=>void}=$props();
 let filter=$state('active');
 const active=$derived(data.jobs.filter(j=>['queued','running'].includes(j.status)));
 const failed=$derived(data.jobs.filter(j=>j.status==='failed'));
 const visible=$derived(data.jobs.filter(j=>j.kind!=='access-cleanup'&&(filter==='all'||filter==='failed'&&j.status==='failed'||filter==='active'&&['queued','running'].includes(j.status))).slice(0,12));
 function target(job:Job){const t=data.templates.find(t=>t.id===job.target);if(t)return `${t.name} · ${t.commit.slice(0,7)}`;const r=data.exerciseRuns?.find(r=>r.id===job.target)??data.checkRuns?.find(r=>r.id===job.target);return data.teams.find(t=>t.id===r?.teamID)?.name??(job.target===data.meeting?.id?data.meeting.name:job.kind==='refresh'?'Template repository':'Lab operation')}
 function waiting(job:Job){return job.waitingOn?.map(id=>{const dependency=data.jobs.find(j=>j.id===id);return dependency?`${dependency.kind} · ${target(dependency)}`:'overlapping resource operation'}).join(', ')}
</script>
<section class="mt-8 border-y border-base-300 py-6" aria-labelledby="activity-title">
 <div class="flex flex-wrap items-center justify-between gap-4">
  <div><h2 id="activity-title" class="flex items-center gap-2 text-xl font-semibold"><Activity size={20}/>Live activity</h2><p class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-base-content/70"><span>{active.filter(j=>j.status==='running').length} running · {active.filter(j=>j.status==='queued').length} queued</span><span class="inline-flex items-center gap-1.5"><Monitor size={15}/>{data.activeDesktops??0} active desktops</span><span>Updates every 2 seconds</span></p></div>
  <div><label for="activity-filter" class="sr-only">Activity filter</label><select id="activity-filter" class="select select-sm" bind:value={filter}><option value="active">Active operations</option><option value="failed">Needs attention ({failed.length})</option><option value="all">Recent operations</option></select></div>
 </div>
 <ul class="mt-4 divide-y divide-base-300">
 {#each visible as job (job.id)}<li class="flex flex-wrap items-start gap-3 py-4">
  {#if job.status==='running'}<span class="loading loading-spinner loading-xs mt-1" aria-label="Running"></span>{:else if job.status==='failed'}<TriangleAlert size={17} class="mt-1 text-error"/>{:else}<Clock size={17} class="mt-1"/>{/if}
  <div class="min-w-0 flex-1"><div class="flex flex-wrap items-center gap-2"><span class="font-medium capitalize">{job.kind==='check'?'Service checks':job.kind}</span><span class="badge badge-sm" class:badge-error={job.status==='failed'}>{job.status}</span></div><p class="mt-1 break-words text-sm">{target(job)}</p>{#if job.kind==='build'}{@const template=data.templates.find(t=>t.id===job.target)}{#if template}<p class="mt-1 text-sm text-base-content/70">{template.imagesReady} / {template.imagesTotal} images ready</p>{/if}{:else if job.kind==='prepare'&&job.target===data.meeting?.id}<p class="mt-1 text-sm text-base-content/70">{data.teams.filter(t=>t.status==='ready').length} / {data.teams.length} team labs ready</p>{/if}<p class="mt-1 text-xs text-base-content/65">{job.started?`Started ${date(job.started)}`:`Queued ${date(job.created)}`}</p>{#if job.status==='queued'}<p class="mt-2 break-words text-sm text-base-content/70">{waiting(job)?`Waiting for ${waiting(job)}`:'Waiting for an available worker'}</p>{/if}{#if job.error}<p class="mt-2 break-words text-sm text-error">{job.error}</p>{/if}</div>
  {#if job.status==='failed'}<button class="btn btn-sm" onclick={()=>onretry(job.id)}><RotateCw size={15}/>Retry recovery</button>{/if}
 </li>{:else}<li class="py-5 text-sm text-base-content/70">{filter==='active'?'No active operations. Independent builds and deployments can run together.':filter==='failed'?'No failed operations.':'No operations yet.'}</li>{/each}
 </ul>
</section>
