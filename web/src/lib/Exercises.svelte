<script lang="ts">
 import {Play,RotateCcw,Check,Shield,Users} from '@lucide/svelte';
 import {api,date} from '$lib/api';
 import Notice from '$lib/Notice.svelte';
 import type {AdminView} from '$lib/types';
 let {data,onchange}:{data:AdminView,onchange:()=>Promise<void>}=$props();
 let selected=$state<string[]>([]);let action=$state('');let scenario=$state('');let error=$state('');let busy=$state(false);let dialog:HTMLDialogElement;
 async function retry(id:string){const job=data.jobs.find(j=>j.kind==='exercise'&&j.target===id&&j.status==='failed');if(!job)return;busy=true;error='';try{await api('/api/admin/jobs/retry',{id:job.id});await onchange()}catch(e){error=(e as Error).message}finally{busy=false}}
 const available=$derived(data.teams.filter(t=>t.status==='ready'&&!data.exerciseRuns?.some(r=>r.teamID===t.id&&(['queued','resetting','applying','checking'].includes(r.status)||data.jobs.some(j=>j.target===r.id&&['queued','running'].includes(j.status))))));
 const chosen=$derived(available.filter(t=>selected.includes(t.id)));
 const definition=$derived(data.exercises?.find(d=>d.id===scenario));
 const open=$derived(!!data.meeting&& !['ending','ended'].includes(data.meeting.status)&&Date.parse(data.meeting.expires)>Date.now());
 function confirm(next:string){action=next;error='';dialog.showModal()}
 async function run(next:string){busy=true;error='';try{await api('/api/admin/exercises',{action:next,teamIDs:chosen.map(t=>t.id)});dialog?.close();await onchange()}catch(e){error=(e as Error).message}finally{busy=false}}
</script>
{#if data.exercises?.length && open}
<section class="mt-10 border-t border-base-300 pt-7" aria-labelledby="exercises-title">
 <div class="flex flex-wrap items-center justify-between gap-3"><h2 id="exercises-title" class="flex items-center gap-2 text-xl font-semibold"><Shield size={20}/>Exercises</h2><button class="btn btn-sm" disabled={busy||!available.length} onclick={()=>selected=available.map(t=>t.id)}><Users size={16}/>Select ready teams</button></div>
 <p class="mt-2 text-sm text-base-content/70">Each break resets the web server first. Bank data and desktop files stay.</p>
 <Notice message={error}/>
 <fieldset class="mt-5"><legend class="text-sm font-medium">Teams</legend><div class="mt-2 divide-y divide-base-300">
 {#each data.teams as team}
 {@const record=data.exerciseRuns?.find(r=>r.teamID===team.id)}
 <div class="py-3"><div class="flex flex-wrap items-center justify-between gap-3"><label class="flex items-center gap-3"><input class="checkbox checkbox-sm" type="checkbox" value={team.id} bind:group={selected} disabled={busy||!available.some(t=>t.id===team.id)}/><span class="font-medium">{team.name}</span></label><span class="badge badge-sm">{record?.status??(team.status==='ready'?'No exercise':team.status)}</span></div>
 {#if record}<div class="mt-2 ml-7 text-sm"><p>{data.exercises.find(d=>d.id===record.action)?.name??(record.action==='reset'?'Reset web server':'Check recovery')} · {date(record.started)}</p><p class="mt-1 break-words text-base-content/70" class:text-error={record.status==='failed'}>{record.report||'In progress'}</p>{#if record.status==='failed'&&data.jobs.some(j=>j.target===record.id&&j.status==='failed')}<button class="btn btn-sm mt-2" disabled={busy||!available.some(t=>t.id===team.id)} onclick={()=>retry(record.id)}><RotateCcw size={14}/>Retry recovery</button>{/if}</div>{/if}
 </div>{/each}
 </div></fieldset>
 <div class="mt-5 flex flex-wrap items-end gap-3"><div class="w-full sm:w-72"><label class="label mb-2" for="exercise-scenario">Exercise</label><select class="select select-sm w-full" id="exercise-scenario" bind:value={scenario}><option value="" disabled>Choose an exercise</option>{#each data.exercises as d}<option value={d.id}>{d.name}</option>{/each}</select></div><button class="btn btn-sm btn-primary" disabled={busy||!scenario||!chosen.length} onclick={()=>confirm(scenario)}><Play size={16}/>Run exercise</button><button class="btn btn-sm" disabled={busy||!chosen.length} onclick={()=>confirm('reset')}><RotateCcw size={16}/>Reset web server</button><button class="btn btn-sm btn-ghost" disabled={busy||!chosen.length} onclick={()=>run('check')}><Check size={16}/>Check recovery</button></div>
 {#if definition}<details class="collapse collapse-arrow mt-5 border border-base-300"><summary class="collapse-title font-medium">Instructor walkthrough</summary><div class="collapse-content text-sm"><p>{definition.description}</p><p class="mt-3 whitespace-pre-line leading-relaxed">{definition.walkthrough}</p></div></details>{/if}
</section>
{/if}
<dialog class="modal" bind:this={dialog} aria-labelledby="exercise-confirm-title"><div class="modal-box"><h2 class="text-xl font-semibold" id="exercise-confirm-title">{action==='reset'?'Reset the web server?':'Run this exercise?'}</h2><p class="mt-4">{chosen.map(t=>t.name).join(', ')}</p><p class="mt-3 text-sm leading-relaxed">This restores Apache configuration, the homepage, and the exercise support account, replacing changes to those items. {action==='reset'?'The web server will return to its working baseline.':'The selected break will then be applied.'} Bank data, published documents, desktop files, and historical logs are preserved.</p><Notice message={error}/><div class="modal-action"><form method="dialog"><button class="btn btn-sm" disabled={busy}>Cancel</button></form><button class="btn btn-sm btn-primary" disabled={busy||!chosen.length||!open} onclick={()=>run(action)}>{busy?'Queuing…':action==='reset'?'Reset selected teams':'Run selected exercise'}</button></div></div></dialog>
