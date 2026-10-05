<script lang="ts">
 import {Activity,Check,Clock,Monitor,RotateCw,TriangleAlert} from '@lucide/svelte';
 import type {AdminView,Job} from '$lib/types';
 import {api,date} from '$lib/api';
 import Status from './Status.svelte';
 let {data,onchange}:{data:AdminView,onchange:()=>Promise<void>}=$props();
 let retrying=$state('');
 let feedback=$state<{error:boolean,message:string}|null>(null);
 async function retry(job:Job){
  if(retrying||!job.retryable)return;
  retrying=job.id;feedback=null;
  try{const next=await api<Job>('/api/admin/jobs/retry',{id:job.id});feedback={error:false,message:`Recovery ${next.status} for ${target(job)}. Follow its progress under Active.`};filter='active';try{await onchange()}catch{feedback.message+=' Progress refresh is unavailable; the recovery was accepted.'}}
  catch(e){feedback={error:true,message:(e as Error).message};try{await onchange()}catch{/* Keep the action error visible. */}}
  finally{retrying=''}
 }
 let filter=$state('active');
 const active=$derived(data.jobs.filter(j=>['queued','running'].includes(j.status)));
 const failed=$derived(data.jobs.filter(j=>j.status==='failed'&&j.needsAttention));
 const visible=$derived(data.jobs.filter(j=>(j.kind!=='access-cleanup'||j.status==='failed')&&(filter==='all'||filter==='failed'&&j.status==='failed'&&j.needsAttention||filter==='active'&&['queued','running'].includes(j.status))).slice(0,12));
 function target(job:Job){if(job.targetLabel)return job.targetLabel;const t=data.templates.find(t=>t.id===job.target);if(t)return `${t.name} · ${t.commit.slice(0,7)}`;const r=data.exerciseRuns?.find(r=>r.id===job.target)??data.checkRuns?.find(r=>r.id===job.target);return data.teams.find(t=>t.id===r?.teamID)?.name??(job.target===data.meeting?.id?data.meeting.name:job.kind==='refresh'?'Template repository':job.kind==='access-cleanup'?'Desktop sessions':'Lab operation')}
 function waiting(job:Job){return job.waitingOn?.map(id=>{const dependency=data.jobs.find(j=>j.id===id);return dependency?`${dependency.kind} · ${target(dependency)}`:'overlapping resource operation'}).join(', ')}
</script>
<section class="mt-8 border-y border-base-300 py-6" aria-labelledby="activity-title">
 <div class="flex flex-wrap items-center justify-between gap-4">
  <div><h2 id="activity-title" class="flex items-center gap-2 text-xl font-semibold"><Activity size={20}/>Live activity</h2><p class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-base-content/75"><span>{active.filter(j=>j.status==='running').length} running · {active.filter(j=>j.status==='queued').length} queued</span><span class="inline-flex items-center gap-1.5"><Monitor size={15}/>{data.activeDesktops??0} active desktops</span><span>Updates every 2 seconds</span></p></div>
  <fieldset class="flex flex-wrap gap-2"><legend class="sr-only">Activity filter</legend>
   <button class="btn btn-sm min-h-11" class:btn-primary={filter==='active'} aria-pressed={filter==='active'} onclick={()=>filter='active'}>{#if filter==='active'}<Check size={15} aria-hidden="true"/>{/if}Active ({active.length})</button>
   <button class="btn btn-sm min-h-11" class:btn-primary={filter==='failed'} aria-pressed={filter==='failed'} onclick={()=>filter='failed'}>{#if filter==='failed'}<Check size={15} aria-hidden="true"/>{/if}Needs attention <span class="badge badge-sm" class:badge-error={failed.length>0}>({failed.length})</span></button>
   <button class="btn btn-sm min-h-11" class:btn-primary={filter==='all'} aria-pressed={filter==='all'} onclick={()=>filter='all'}>{#if filter==='all'}<Check size={15} aria-hidden="true"/>{/if}Recent</button>
  </fieldset>
 </div>
 {#if failed.length&&filter!=='failed'}<div class="mt-4 flex flex-wrap items-center gap-3 rounded-box border border-error/40 bg-base-200 p-4"><TriangleAlert size={18} class="text-error" aria-hidden="true"/><p class="flex-1 text-sm">{failed.length} {failed.length===1?'operation needs':'operations need'} attention.</p><button class="btn btn-sm min-h-11" onclick={()=>filter='failed'}>View failures</button></div>{/if}
 {#if feedback}<p class="mt-4 break-words text-sm" class:text-error={feedback.error} role={feedback.error?'alert':'status'}>{feedback.message}</p>{/if}
 <ul class="mt-4 divide-y divide-base-300">
 {#each visible as job (job.id)}<li class="flex flex-wrap items-start gap-3 py-4">
  {#if job.status==='running'}<span class="loading loading-spinner loading-xs mt-1" aria-hidden="true"></span>{:else if job.status==='failed'}<TriangleAlert size={17} class="mt-1 text-error"/>{:else}<Clock size={17} class="mt-1"/>{/if}
  <div class="min-w-0 flex-1"><div class="flex flex-wrap items-center gap-2"><span class="font-medium capitalize">{job.kind==='check'?'Service checks':job.kind==='access-cleanup'?'Desktop cleanup':job.kind}</span><Status status={job.status}/></div><p class="mt-1 break-words text-sm">{target(job)}</p>{#if job.kind==='build'}{@const template=data.templates.find(t=>t.id===job.target)}{#if template}<p class="mt-1 text-sm text-base-content/75">{template.imagesReady} / {template.imagesTotal} images ready</p>{/if}{:else if job.kind==='prepare'&&job.target===data.meeting?.id}<p class="mt-1 text-sm text-base-content/75">{data.teams.filter(t=>t.status==='ready').length} / {data.teams.length} team labs ready</p>{/if}<p class="mt-1 text-xs text-base-content/75">{job.started?`Started ${date(job.started)}`:`Queued ${date(job.created)}`}</p>{#if job.status==='queued'}<p class="mt-2 break-words text-sm text-base-content/75">{waiting(job)?`Waiting for ${waiting(job)}`:'Waiting for an available worker'}</p>{/if}{#if job.error}<p class={["mt-2 break-words text-sm",job.needsAttention?"text-error":"text-base-content/75"]}>{job.error==='context canceled'?'Operation interrupted (context canceled).':job.error}</p>{/if}</div>
  {#if job.status==='failed'&&job.retryable}<button class="btn btn-sm min-h-11" disabled={!!retrying} onclick={()=>retry(job)}>{#if retrying===job.id}<span class="loading loading-spinner loading-xs" aria-hidden="true"></span>Queuing recovery…{:else}<RotateCw size={15}/>Retry recovery{/if}</button>{:else if job.retryBlocked}<p class="w-full pl-7 text-sm text-base-content/75">{job.retryBlocked}</p>{/if}
 </li>{:else}<li class="py-5 text-sm text-base-content/75">{filter==='active'?'No operations running or queued.':filter==='failed'?'No failed operations.':'No operations yet.'}</li>{/each}
 </ul>
</section>
