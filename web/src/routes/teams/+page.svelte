<script lang="ts">
 import {goto} from '$app/navigation';import {Users,ArrowRight,Check,LogOut,Clock} from '@lucide/svelte';import Guard from '$lib/Guard.svelte';import PageHeader from '$lib/PageHeader.svelte';import Notice from '$lib/Notice.svelte';import Status from '$lib/Status.svelte';import {session,loadError,api,refresh,date} from '$lib/api';let error=$state('');let pending=$state('');let checked=$state('');
 async function checkAgain(){await refresh();if($loadError){error=$loadError;checked='';return}error='';checked=new Date().toLocaleTimeString([],{hour:'numeric',minute:'2-digit',second:'2-digit'})}
 async function join(teamID:string){pending=teamID||'leave';error='';try{await api('/api/team',{teamID});await refresh();if(teamID)await goto('/lab/')}catch(e){error=(e as Error).message;await refresh()}finally{pending=''}}
</script>
<svelte:head><title>Choose your team · LabCentral</title></svelte:head>
<div class="max-w-3xl">
<PageHeader title="Choose your team"/>
<Guard>{#if $session?.meeting && $session.meeting.status!=='ended' && $session.meeting.status!=='ending'}
 <div class="mb-5 flex flex-wrap items-center justify-between gap-3 text-sm"><h2 class="font-medium">{$session.meeting.name}</h2><span class="flex items-center gap-2 text-xs text-base-content/75"><Clock size={14}/>Ends at {date($session.meeting.expires)}</span></div>
 {#if $session.membership.teamID}<p class="mb-5 text-sm leading-relaxed text-base-content/75">Switching closes your desktop. Files stay with the previous team until the meeting ends.</p>{/if}
 <Notice message={error}/>{#if $session.membership.switching}<div class="alert my-4 text-sm" role="status"><span class="loading loading-spinner loading-sm"></span>Your previous desktop is closing. Retry the same team to finish your change.</div>{/if}
 <ul class="divide-y divide-base-content/10 border-y border-base-content/10">
  {#each $session.teams as team}<li class="flex flex-wrap items-center gap-3 py-4" class:bg-base-200={$session.membership.teamID===team.id}>
   <Users size={17} class="ml-3 shrink-0 text-base-content/75" aria-hidden="true"/><div class="min-w-28 flex-1"><div class="flex flex-wrap items-center gap-2"><h3 class="text-sm font-medium">{team.name}</h3>{#if team.id===$session.membership.teamID}<span class="inline-flex items-center gap-1 text-xs text-primary"><Check size={12}/>Your team</span>{/if}</div><p class="mt-1 text-xs tabular-nums text-base-content/75">{team.members} of {team.capacity} seats filled</p></div>
   <div class="mr-3 flex flex-wrap items-center gap-3"><Status status={team.status}/>{#if team.id===$session.membership.teamID&&!$session.membership.switching}<a href="/lab/" class="btn btn-sm min-h-11 btn-primary">Your lab<ArrowRight size={15}/></a>{:else}<button class="btn btn-sm min-h-11" disabled={!!pending||team.members>=team.capacity} onclick={()=>join(team.id)}>{#if pending===team.id}<span class="loading loading-spinner loading-xs"></span>{/if}{team.members>=team.capacity?'Team full':$session.membership.teamID?'Switch team':'Join team'}<ArrowRight size={15}/></button>{/if}</div>
  </li>{/each}
 </ul>
 {#if $session.membership.teamID}<div class="mt-4 flex justify-end"><button class="btn btn-sm min-h-11 btn-ghost" disabled={!!pending} onclick={()=>join('')}><LogOut size={15}/>Leave team</button></div>{/if}
{:else}
 <section class="border-t border-base-content/10 py-8"><Users size={24} aria-hidden="true"/><h2 class="mt-4 text-lg font-semibold">No meeting is open yet</h2><p class="mt-2 text-sm text-base-content/75">Teams appear when a meeting opens.</p><Notice message={error}/><button class="btn btn-sm min-h-11 mt-5" onclick={checkAgain}>Check again</button>{#if checked}<p class="mt-3 text-xs text-base-content/75" role="status">Checked at {checked}</p>{/if}</section>
{/if}</Guard>
</div>
