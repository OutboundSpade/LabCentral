<script lang="ts">
 import {Monitor,Server,Clock,ExternalLink,Users,BookOpen,Unplug} from '@lucide/svelte';import MarkdownIt from 'markdown-it';import Guard from '$lib/Guard.svelte';import Notice from '$lib/Notice.svelte';import Status from '$lib/Status.svelte';import {session,api,refresh,date} from '$lib/api';import type {Launch} from '$lib/types';let error=$state('');let busy=$state(false);const markdown=new MarkdownIt({html:false,linkify:false});let handover=$derived(markdown.render($session?.lab.handover??''));
 async function open(){
  // Reserve the tab during the click; provisioning can outlast browser user activation.
  const tab=window.open('about:blank','_blank');
  if(!tab){error='Allow a new tab for LabCentral, then open your desktop again.';return}
  tab.opener=null;tab.document.title='Starting your desktop · LabCentral';tab.document.body.textContent='Starting your desktop. This can take a minute…';
  busy=true;error='';
  try{const result=await api<Launch>('/api/access',{});if(tab.closed)throw new Error('Desktop tab closed. Open it again when you are ready.');const form=tab.document.createElement('form');form.method='POST';form.action=result.url;const input=tab.document.createElement('input');input.type='hidden';input.name='ticket';input.value=result.ticket;form.append(input);tab.document.body.replaceChildren(form);form.submit()}
  catch(e){tab.close();error=(e as Error).message}finally{busy=false}
 }
 async function close(){busy=true;error='';try{await api('/api/access/close',{})}catch(e){error=(e as Error).message}finally{busy=false}}
</script>
<svelte:head><title>Your lab · LabCentral</title></svelte:head>
<h1 class="text-3xl font-semibold tracking-tight sm:text-4xl">Your lab</h1>
<Guard><Notice message={error}/>
{#if $session?.meeting?.status==='ended'||$session?.meeting?.status==='ending'}<section class="py-12"><Clock size={28}/><h2 class="mt-4 text-xl font-semibold">This meeting has ended</h2><p class="mt-3 text-base-content/75">The team's machines and workspaces are being removed. Join the next meeting for a fresh lab.</p><a href="/teams/" class="btn btn-sm min-h-11 mt-6">Back to teams</a></section>
{:else if !$session?.lab.team}<section class="py-12"><Users size={28}/><h2 class="mt-4 text-xl font-semibold">Choose a team</h2><p class="mt-3 text-base-content/75">Join a team to access its lab.</p><a href="/teams/" class="btn btn-sm min-h-11 btn-primary mt-6">Choose a team</a></section>
{:else}<div class="mt-9 grid gap-10 lg:grid-cols-[1fr_20rem]">
 <aside class="min-w-0 lg:order-2 lg:border-l lg:border-base-300 lg:pl-8"><div class="flex items-center justify-between"><h2 class="text-xl font-semibold">{$session.lab.team.name}</h2><a href="/teams/" class="link inline-flex min-h-11 items-center text-sm">Change team</a></div><p class="mt-2 flex items-center gap-2 text-sm text-base-content/75"><Clock size={16}/>Ends at {$session.meeting?date($session.meeting.expires):'—'}</p>
 <div class="card card-border mt-5 bg-base-200"><div class="card-body p-5"><div class="flex items-center justify-between gap-3"><h3 class="card-title text-base"><Monitor size={20}/>Your desktop</h3><Status status={$session.lab.team.status}/></div><p class="text-sm leading-relaxed text-base-content/75">Your personal desktop opens in a new tab. Files stay until the meeting ends.</p><button class="btn btn-sm min-h-11 btn-primary mt-3 w-full" disabled={busy||$session.lab.team.status!=='ready'} onclick={open}>{#if busy}<span class="loading loading-spinner loading-xs"></span>{:else}<ExternalLink size={16}/>{/if}{busy?'Starting…':$session.lab.team.status==='ready'?'Open desktop':$session.lab.team.status==='failed'?'Lab needs attention':'Preparing your lab'}</button><button class="btn btn-sm min-h-11 btn-ghost w-full" disabled={busy||$session.lab.team.status!=='ready'} onclick={close}><Unplug size={16}/>Disconnect desktop</button></div></div>
 <h3 class="mt-8 text-sm font-semibold">Your teammates</h3><ul class="mt-3 space-y-2 text-sm text-base-content/75">{#each $session.lab.teammates ?? [] as member}<li>{member.name}</li>{:else}<li>Your team roster will appear here.</li>{/each}</ul>
 <h3 class="mt-8 text-sm font-semibold">Your team's machines</h3><ul class="mt-3 divide-y divide-base-300">{#each $session.lab.machines as machine}<li class="flex items-center gap-3 py-3"><Server size={16} aria-hidden="true"/><span class="flex-1 text-sm">{machine.name}</span><Status status={machine.status}/></li>{:else}<li class="py-4 text-sm text-base-content/75">Preparing machines.</li>{/each}</ul>{#if $session.lab.team.status==='failed'}<Notice message="Your lab needs administrator attention. Your team membership is saved."/>{/if}
 </aside>
 <section class="min-w-0 lg:order-1"><div class="mb-7 flex items-center gap-2 border-b border-base-300 pb-4"><BookOpen size={20}/><h2 class="text-xl font-semibold">Student handover</h2></div><article class="prose prose-invert prose-sm max-w-none prose-headings:font-semibold prose-a:text-primary">{@html handover}</article></section>
 </div>{/if}</Guard>
