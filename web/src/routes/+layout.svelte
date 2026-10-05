<script lang="ts">
 import '../app.css';import {onMount} from 'svelte';import {page} from '$app/state';import {Monitor,Users,SlidersHorizontal,LogOut,ArrowUpRight,GitBranch} from '@lucide/svelte';import {session,loading,refresh,api} from '$lib/api';import Notice from '$lib/Notice.svelte';let {children}=$props();let error=$state('');
 onMount(()=>{void refresh();const t=setInterval(refresh,10000);return()=>clearInterval(t)});
 async function logout(){try{await api('/auth/logout',{});location.assign('/')}catch(e){error=(e as Error).message;await refresh()}}
</script>
<svelte:head><title>LabCentral</title><link rel="icon" type="image/svg+xml" href="/brand.svg"/><meta name="description" content="Your team, your workspace, and today's CDC competition lab."/></svelte:head>
<a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:z-10 focus:bg-base-100 focus:p-4">Skip to content</a>
<div class="min-h-screen bg-base-100 text-base-content">
 <header class="border-b border-base-300"><div class="mx-auto max-w-6xl px-5 sm:px-8"><div class="navbar min-h-20 flex-wrap gap-3 px-0">
  <a href="/" aria-label="LabCentral home" class="mr-auto flex items-center gap-3 text-xl font-medium tracking-tight"><img src="/brand.svg" width="32" height="32" class="object-contain" alt=""/> <span>Lab<span class="font-bold">Central</span></span></a>
  {#if $session}<span class="hidden max-w-44 truncate text-sm text-base-content/75 sm:inline">{$session.user.name}</span><button onclick={logout} class="btn btn-sm min-h-11 btn-ghost min-w-11" aria-label="Sign out" title="Sign out"><LogOut size={17}/></button>{:else if !$loading}<a class="btn btn-sm min-h-11 btn-ghost" href="https://auth.example.org">CDC ID <ArrowUpRight size={16}/></a>{/if}
 </div>
 {#if $session}<nav aria-label="Main navigation" class="grid grid-cols-2 gap-1 pb-3 sm:flex sm:flex-wrap">
  <a href="/teams/" aria-current={page.url.pathname==='/teams/'?'page':undefined} class="btn btn-sm min-h-11 btn-ghost" class:bg-base-200={page.url.pathname==='/teams/'}><Users size={16}/>Teams</a>
  <a href="/lab/" aria-current={page.url.pathname==='/lab/'?'page':undefined} class="btn btn-sm min-h-11 btn-ghost" class:bg-base-200={page.url.pathname==='/lab/'}><Monitor size={16}/>Your lab</a>
  {#if $session.user.admin}<a href="/admin/" aria-current={page.url.pathname==='/admin/'?'page':undefined} class="btn btn-sm min-h-11 btn-ghost" class:bg-base-200={page.url.pathname==='/admin/'}><SlidersHorizontal size={16}/>Manage meeting</a><a href="/admin/templates/" aria-current={page.url.pathname.startsWith('/admin/templates/')?'page':undefined} class="btn btn-sm min-h-11 btn-ghost" class:bg-base-200={page.url.pathname.startsWith('/admin/templates/')}><GitBranch size={16}/>Templates</a>{/if}
 </nav>{/if}</div></header>
 <main id="main" class="mx-auto max-w-6xl px-5 py-10 sm:px-8 sm:py-14"><Notice message={error}/>{@render children()}</main>
</div>
