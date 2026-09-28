<script lang="ts">import {ArrowRight,LogIn} from '@lucide/svelte';import {session} from '$lib/api';</script>
<svelte:head><title>LabCentral</title></svelte:head>
<section class="max-w-xl py-12 sm:py-20">
 <h1 class="text-4xl font-semibold tracking-tight sm:text-5xl">Your lab awaits.</h1>
 <p class="mt-5 text-lg text-base-content/75">{$session?'Choose your team or return to your lab.':'Sign in to join your team and open your desktop.'}</p>
 {#if $session}<a href={$session.membership.teamID?'/lab/':'/teams/'} class="btn btn-primary mt-8">{$session.membership.teamID?'Open your lab':'Choose your team'}<ArrowRight size={18}/></a>{:else}<a href="/auth/login" class="btn btn-primary mt-8">Sign in with CDC ID<LogIn size={18}/></a>{/if}
</section>
