<script lang="ts">
 import '../app.css';import {onMount} from 'svelte';import {page} from '$app/state';import {Monitor,Users,SlidersHorizontal,LogOut,ArrowUpRight} from '@lucide/svelte';import {session,refresh,api} from '$lib/api';import Notice from '$lib/Notice.svelte';let {children}=$props();let error=$state('');
 onMount(()=>{void refresh();const t=setInterval(refresh,10000);return()=>clearInterval(t)});
 async function logout(){try{await api('/auth/logout',{});location.assign('/')}catch(e){error=(e as Error).message;await refresh()}}
</script>
<svelte:head><title>LabCentral · example Cyber Defense Club</title><meta name="description" content="Your team, your workspace, and today's CDC competition lab."/></svelte:head>
<a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:z-10 focus:bg-base-100 focus:p-4">Skip to content</a>
<div class="min-h-screen bg-base-100 text-base-content">
 <header class="border-b border-base-300"><div class="mx-auto max-w-6xl px-5 sm:px-8"><div class="navbar min-h-20 flex-wrap gap-3 px-0">
  <a href="/" class="mr-auto flex items-center gap-3 text-xl font-semibold tracking-tight"><Monitor size={24} class="text-primary" aria-hidden="true"/>LabCentral<span class="hidden border-l border-base-300 pl-3 text-xs font-medium tracking-normal text-base-content/65 sm:block">example CYBER DEFENSE CLUB</span></a>
  {#if $session}<span class="hidden max-w-44 truncate text-sm text-base-content/70 sm:inline">{$session.user.name}</span><button onclick={logout} class="btn btn-sm btn-ghost" aria-label="Sign out"><LogOut size={17}/></button>{:else}<a class="btn btn-sm btn-ghost" href="https://auth.example.org">CDC ID <ArrowUpRight size={16}/></a>{/if}
 </div>
 {#if $session}<nav aria-label="Main navigation" class="flex flex-wrap gap-1 pb-3">
  <a href="/teams/" aria-current={page.url.pathname==='/teams/'?'page':undefined} class="btn btn-sm btn-ghost" class:bg-base-200={page.url.pathname==='/teams/'}><Users size={16}/>Teams</a>
  <a href="/lab/" aria-current={page.url.pathname==='/lab/'?'page':undefined} class="btn btn-sm btn-ghost" class:bg-base-200={page.url.pathname==='/lab/'}><Monitor size={16}/>Your lab</a>
  {#if $session.user.admin}<a href="/admin/" class="btn btn-sm btn-ghost" class:bg-base-200={page.url.pathname.startsWith('/admin/')}><SlidersHorizontal size={16}/>Manage meeting</a>{/if}
 </nav>{/if}</div></header>
 <main id="main" class="mx-auto max-w-6xl px-5 py-10 sm:px-8 sm:py-14"><Notice message={error}/>{@render children()}</main>
 <footer class="mx-auto mt-12 flex max-w-6xl flex-wrap justify-between gap-3 border-t border-base-300 px-5 py-7 text-xs text-base-content/65 sm:px-8"><span>example Cyber Defense Club</span><span>Shared practice. Independent workspaces.</span></footer>
</div>
