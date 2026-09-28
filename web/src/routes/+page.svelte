<script lang="ts">import {ArrowRight,LogIn,Users,Monitor,BookOpen,ShieldCheck} from '@lucide/svelte';import {session} from '$lib/api';</script>
<svelte:head><title>Welcome · LabCentral</title></svelte:head>
<section class="grid gap-14 py-6 md:grid-cols-[1.3fr_1fr] md:items-center md:py-16">
 <div><h1 class="max-w-xl text-4xl font-semibold leading-tight tracking-tight sm:text-6xl">Your team.<br/>Your next challenge.</h1><p class="mt-6 max-w-md text-lg leading-relaxed text-base-content/75">A workspace for CDC competition practice. Join your team, pick up the handover, and get to work.</p>
 {#if $session}<a href={$session.membership.teamID?'/lab/':'/teams/'} class="btn btn-primary mt-8">{$session.membership.teamID?'Open your lab':'Choose your team'}<ArrowRight size={18}/></a>{:else}<a href="/auth/login" class="btn btn-primary mt-8">Continue with CDC ID<LogIn size={18}/></a>{/if}
 <p class="mt-5 flex items-center gap-2 text-sm text-base-content/65"><ShieldCheck size={16} aria-hidden="true"/>For CDC competition members.</p></div>
 <div class="border-y border-base-300 py-3"><h2 class="py-5 text-lg font-semibold">From sign-in to your lab</h2><ol class="divide-y divide-base-300">
 <li class="flex gap-5 py-6"><span class="font-mono text-sm text-base-content/50">01</span><div><h3 class="flex items-center gap-2 font-medium"><Users size={18}/>Find your team</h3><p class="mt-2 text-sm leading-relaxed text-base-content/70">Choose an open seat alongside your teammates.</p></div></li>
 <li class="flex gap-5 py-6"><span class="font-mono text-sm text-base-content/50">02</span><div><h3 class="flex items-center gap-2 font-medium"><BookOpen size={18}/>Read the handover</h3><p class="mt-2 text-sm leading-relaxed text-base-content/70">Get the brief and the information you need to begin.</p></div></li>
 <li class="flex gap-5 py-6"><span class="font-mono text-sm text-base-content/50">03</span><div><h3 class="flex items-center gap-2 font-medium"><Monitor size={18}/>Open your workspace</h3><p class="mt-2 text-sm leading-relaxed text-base-content/70">Your own desktop, connected to your team's lab.</p></div></li>
 </ol></div>
</section>
